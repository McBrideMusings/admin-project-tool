//go:build darwin

// Package kinds' Apple verb dispatch — the Go port of the
// dev/build/deploy/clean half of admin_lib/apple.py's "High-level archetype
// wrappers" section (its own comment: "The apple archetype's kind:"python"
// commands call these with _APPLE_CONFIG"). Darwin-only for the same reason
// internal/luahost's Apple surface is: xcodebuild, simctl, devicectl and
// codesign are macOS-specific.
//
// What this file deliberately does NOT port, and why — a documented gap in
// docs/architecture/lua-host-api.md's "What is intentionally not here yet",
// not a silent omission:
//
//   - App Store Connect device registration (ensure_device_registered) —
//     lives in admin_lib/appstoreconnect.py, a separate module not ported
//     here; a device deploy that would need it still surfaces Apple's own
//     "team has no devices" error, just not pre-empted.
//
// Everything else the package doc used to list here as deferred is now
// ported. The dev/prod bundle-suffix convention layer (_compose_dev_settings,
// _entitlements_paths, _target_needs_provisioning_profile) is ported — see
// applesign_darwin.go — and its ADMIN_DEV_BUNDLE_SUFFIX override is wired
// into every dev-loop build in appledevloop_darwin.go. Dev-icon-swap hooks
// (build_icon_hooks: backup/swap-banner/restore around each build) and the
// iOS/tvOS display-name patch (patch_ios_display_name) are ported and wired
// — see appleiconhooks_darwin.go — into appledevloop_darwin.go's
// appleDevSimulator and appleDevDevice (admin-7ig8.1). The mac dev-rename/
// re-sign postprocessing (DevMacOSPostprocess, bound in
// internal/luahost/apple_darwin.go) and the is_running_fn/kill_fn overrides
// (_make_mac_dev_running_check/_make_mac_dev_kill) are ported and wired into
// appleDevMac (admin-3rgp.75) — see this file's "process helpers" section
// (appleResolveMacDevProductName/appleResolveMacDevProcessPattern/
// appleMacDevIsRunning/appleMacDevKill) and appledevloop_darwin.go's
// appleDevMac.
package kinds

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/streams"
)

// appleSimSDK carries the two things that differ between an iOS and a tvOS
// simulator build target beyond what internal/luahost's own (unexported)
// simPlatforms table tracks — the xcodebuild destination label and the
// DerivedData product-directory suffix. Kept local to this file: both are
// dev-loop/build concerns, not primitives another Apple binding needs.
var appleSimSDK = map[string]struct {
	destinationLabel string
	sdkSuffix        string
	deviceEnv        string
}{
	"ios": {"iOS Simulator", "-iphonesimulator", "IOS_DEVICE"},
	"tv":  {"tvOS Simulator", "-appletvsimulator", "TV_DEVICE"},
}

var appleDeviceSDK = map[string]struct {
	destination string
	sdkSuffix   string
}{
	"ios": {"generic/platform=iOS", "-iphoneos"},
	"tv":  {"generic/platform=tvOS", "-appletvos"},
}

// runAppleVerb dispatches a kind="apple" action's `run` verb to its
// implementation, mirroring admin_lib/interp.py::_make_apple's
// `getattr(apple_mod, func_name)(cfg)`.
func runAppleVerb(verb, action string, cfg map[string]any, args []string, m *manifest.Manifest) error {
	h := newHost(m)
	f := parseAppleFlags(args)
	switch verb {
	case "build_mac":
		return appleBuildFlat(cfg, h, "mac", "platform=macOS", f)
	case "build_ios":
		return appleBuildSim(cfg, h, "ios", f)
	case "build_tv":
		return appleBuildSim(cfg, h, "tv", f)
	case "dev_mac":
		return appleDevMac(cfg, h, f)
	case "dev_ios":
		return appleDevSimulator(cfg, h, "ios", f)
	case "dev_tv":
		return appleDevSimulator(cfg, h, "tv", f)
	case "dev_device":
		return appleDevDevice(cfg, h, "ios", f)
	case "dev_device_tv":
		return appleDevDevice(cfg, h, "tv", f)
	case "deploy_mac":
		return appleDeployMac(cfg, h, f)
	case "deploy_ios":
		return appleDeployDevice(cfg, h, "ios", f)
	case "deploy_tv":
		return appleDeployDevice(cfg, h, "tv", f)
	case "clean_ios":
		return appleClean(cfg, h)
	case "appid":
		return appleAppID(cfg, h, args)
	default:
		return fmt.Errorf("action %q: kind=\"apple\" has no verb %q", action, verb)
	}
}

// --- config resolution -----------------------------------------------------
// Ports the plain (non-dev/prod-suffix) half of admin_lib/apple.py's
// _resolve_* helpers: the Xcode project is the source of truth for anything
// not overridden in [apple], via apple.xcode_setting.

// applePrebuildRan memoizes [apple] prebuild_cmd per process — a single
// `admin` invocation resolves the project path many times (build, dev loop
// iterations, deploy), and re-running the prereq each time would be wasted
// work. Mirrors admin_lib/apple.py::_run_prebuild_cmd's _PREBUILD_RAN set.
var applePrebuildRan = map[string]bool{}

// runApplePrebuild runs [apple] prebuild_cmd once per process, at the moment
// the project path is resolved. Every apple verb funnels through
// appleProject, so a generated .xcodeproj (XcodeGen, Tuist) kept out of
// version control — absent on a fresh clone, stale after a project.yml edit —
// gets regenerated before anything reads it. Ports
// admin_lib/apple.py::_run_prebuild_cmd.
func runApplePrebuild(cfg map[string]any, h *luahost.Host) error {
	rv := &envResolver{}
	cmd := strings.TrimSpace(rv.s(cfgString(cfg, "prebuild_cmd", "")))
	if rv.firstErr != nil {
		return rv.firstErr
	}
	if cmd == "" || applePrebuildRan[cmd] {
		return nil
	}
	applePrebuildRan[cmd] = true
	h.Info("prereqs: " + cmd)
	res, err := h.RunCmd(cmd, luahost.RunCmdOptions{CaptureLog: true})
	if err != nil {
		return err
	}
	if !res.Ok {
		return fmt.Errorf("prereq command failed: %s", cmd)
	}
	return nil
}

func appleProject(cfg map[string]any, h *luahost.Host) (string, error) {
	if err := runApplePrebuild(cfg, h); err != nil {
		return "", err
	}
	p, _ := luahost.DetectProjectPath(cfgString(cfg, "project_path", ""))
	return p, nil
}

func appleProcess(project string, cfg map[string]any, platform, scheme string) string {
	if v := cfgString(cfg, platform+"_process", ""); v != "" {
		return v
	}
	if v, ok := luahost.XcodeSetting(project, scheme, "PRODUCT_NAME"); ok && v != "" {
		return v
	}
	return scheme
}

func appleBundle(project string, cfg map[string]any, platform string, dev bool) string {
	prod := cfgString(cfg, platform+"_bundle", "")
	if prod == "" {
		scheme := cfgString(cfg, platform+"_scheme", "")
		if v, ok := luahost.XcodeSetting(project, scheme, "PRODUCT_BUNDLE_IDENTIFIER"); ok {
			prod = v
		}
	}
	if !dev {
		return prod
	}
	if v := cfgString(cfg, platform+"_dev_bundle", ""); v != "" {
		return v
	}
	if prod != "" {
		return prod + ".dev"
	}
	return ""
}

func appleSubsystem(project string, cfg map[string]any, platform string, dev bool) string {
	if v := cfgString(cfg, "subsystem", ""); v != "" {
		return v
	}
	return appleBundle(project, cfg, platform, dev)
}

func appleDerivedDataName(cfg map[string]any, project string) string {
	if v := cfgString(cfg, "derived_data_name", ""); v != "" {
		return v
	}
	base := filepath.Base(project)
	base = strings.TrimSuffix(base, ".xcworkspace")
	base = strings.TrimSuffix(base, ".xcodeproj")
	return base
}

// appleDerivedGlob mirrors admin_lib/apple.py's _derived_glob. FindApp
// expands the leading "~" itself.
func appleDerivedGlob(derivedName, config, sdkSuffix, productName string) string {
	return fmt.Sprintf("~/Library/Developer/Xcode/DerivedData/%s-*/Build/Products/%s%s/%s.app",
		derivedName, config, sdkSuffix, productName)
}

// appleDevEnv reads [apple] dev_env — environment variables delivered to the
// dev-launched process (simulator or device), resolved through ${VAR}. This
// is the debug-session-seeding hook a project reaches for when its app reads
// a launch-time env var (a cookie jar path, a feature flag, a fixture) that
// launch_args can't express, since launch_args are UIApplication arguments,
// not environment. See InstallAndLaunchSim (SIMCTL_CHILD_ prefix) and
// StartDeviceConsole (devicectl -e) for how each platform actually delivers
// it.
func appleDevEnv(cfg map[string]any) (map[string]string, error) {
	raw := cfgMap(cfg, "dev_env")
	if len(raw) == 0 {
		return nil, nil
	}
	rv := &envResolver{}
	out := map[string]string{}
	for k, v := range cfgStringMap(raw) {
		out[k] = rv.s(v)
	}
	if rv.firstErr != nil {
		return nil, rv.firstErr
	}
	return out, nil
}

func appleBuildAction(clean bool) string {
	if clean {
		return "clean build"
	}
	return "build"
}

// --- build (no run, no install) --------------------------------------------

func appleBuildFlat(cfg map[string]any, h *luahost.Host, platform, destination string, f appleFlags) error {
	scheme := cfgString(cfg, platform+"_scheme", "")
	if scheme == "" {
		return fmt.Errorf("apple.%s_scheme required", platform)
	}
	project, err := appleProject(cfg, h)
	if err != nil {
		return err
	}
	config := cfgString(cfg, platform+"_build_config", "Debug")
	h.Info(fmt.Sprintf("Building %s (%s) for %s...", scheme, config, platform))
	res, err := h.XcodebuildFiltered(luahost.XcodebuildOptions{
		Project: project, Scheme: scheme, Config: config,
		Destination: destination, Action: appleBuildAction(f.clean),
		ExtraSettings: appleComposeSignSettings(cfg, h, project, platform),
	})
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		h.Warn(w)
	}
	if !res.Ok {
		for _, w := range luahost.DiagnoseBuildFailure(res.RawLogPath) {
			h.Warn(w)
		}
		return fmt.Errorf("build failed")
	}
	h.Ok(platform + " build succeeded")
	return nil
}

func appleBuildSim(cfg map[string]any, h *luahost.Host, platform string, f appleFlags) error {
	scheme := cfgString(cfg, platform+"_scheme", "")
	if scheme == "" {
		return fmt.Errorf("apple.%s_scheme required", platform)
	}
	sdk := appleSimSDK[platform]
	deviceName := os.Getenv(sdk.deviceEnv)
	if deviceName == "" {
		deviceName = cfgString(cfg, platform+"_device", "")
	}
	var destination string
	if deviceName != "" {
		destination = fmt.Sprintf("platform=%s,name=%s", sdk.destinationLabel, deviceName)
	} else {
		simID, _, _, err := luahost.ResolveSimulator(platform)
		if err != nil {
			return err
		}
		destination = fmt.Sprintf("platform=%s,id=%s", sdk.destinationLabel, simID)
	}
	return appleBuildFlat(cfg, h, platform, destination, f)
}

// --- clean -------------------------------------------------------------

func appleClean(cfg map[string]any, h *luahost.Host) error {
	scheme := cfgString(cfg, "ios_scheme", "")
	if scheme == "" {
		scheme = cfgString(cfg, "mac_scheme", "")
	}
	if scheme == "" {
		scheme = cfgString(cfg, "tv_scheme", "")
	}
	if scheme == "" {
		return fmt.Errorf("apple.ios_scheme, apple.mac_scheme or apple.tv_scheme required for clean")
	}
	project, err := appleProject(cfg, h)
	if err != nil {
		return err
	}
	name := appleDerivedDataName(cfg, project)
	if name == "" {
		name = scheme
	}
	h.Info("Removing DerivedData for " + name + "...")
	if err := luahost.CleanDerivedData(name); err != nil {
		return err
	}
	h.Ok("DerivedData cleaned")
	return nil
}

// --- deploy (build + install, no dev loop) ---------------------------------

func appleDeployMac(cfg map[string]any, h *luahost.Host, f appleFlags) error {
	scheme := cfgString(cfg, "mac_scheme", "")
	if scheme == "" {
		return fmt.Errorf("apple.mac_scheme required")
	}
	project, err := appleProject(cfg, h)
	if err != nil {
		return err
	}
	config := cfgString(cfg, "mac_deploy_config", "Release")
	process := appleProcess(project, cfg, "mac", scheme)
	installPath := cfgString(cfg, "mac_install_path_prod", "")
	if installPath == "" {
		installPath = "/Applications/" + process + ".app"
	}
	derivedName := appleDerivedDataName(cfg, project)

	h.Info("Killing any running " + process + "...")
	_ = exec.Command("pkill", "-x", process).Run()

	// Developer ID cannot be reached by passing CODE_SIGN_IDENTITY to a plain
	// build: an automatically-signed target refuses it outright with
	// "conflicting provisioning settings". Distribution signing is an EXPORT
	// step — archive the app under its normal development signing, then
	// -exportArchive re-signs it with the Developer ID identity, applies the
	// hardened runtime (which notarization requires) and drops get-task-allow.
	var appPath string
	if appleWantsDeveloperID(cfg) {
		exported, cleanup, err := appleArchiveAndExportDeveloperID(cfg, h, project, scheme, config, f.clean)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return err
		}
		appPath = exported
	} else {
		h.Info(fmt.Sprintf("Building %s (%s)...", scheme, config))
		res, err := h.XcodebuildFiltered(luahost.XcodebuildOptions{
			Project: project, Scheme: scheme, Config: config,
			Destination: "platform=macOS", Action: appleBuildAction(f.clean),
			ExtraSettings: appleComposeSignSettings(cfg, h, project, "mac"),
		})
		if err != nil {
			return err
		}
		if !res.Ok {
			return fmt.Errorf("build failed")
		}
		appPath, _ = luahost.FindApp(appleDerivedGlob(derivedName, config, "", process))
	}
	if appPath == "" {
		return fmt.Errorf("could not find built app")
	}

	// Notarize BEFORE copying into place, so what lands in installPath is the
	// stapled bundle. Stapling the installed copy instead leaves a window where
	// the deployed app is the un-stapled one, and a failed staple leaves it
	// there permanently.
	if b, _ := cfg["notarize"].(bool); b {
		if err := appleNotarizeAndStaple(h, appPath); err != nil {
			return fmt.Errorf("refusing to install an app that did not notarize: %w", err)
		}
	}

	// Runs after build and notarize/staple, before anything touches
	// installPath, so a rejecting verify command never lets a bad app
	// replace the installed one.
	if err := appleRunMacDeployVerify(cfg, h, appPath); err != nil {
		return err
	}

	h.Info("Installing to " + installPath)
	_ = os.RemoveAll(installPath)
	if err := appleCopyTree(appPath, installPath); err != nil {
		return err
	}

	if cfgBool(cfg, "mac_deploy_launch", true) {
		h.Info("Launching " + installPath)
		if err := exec.Command("open", installPath).Run(); err != nil {
			return err
		}
	}
	h.Ok("deploy mac -> " + installPath)
	return nil
}

// appleRunMacDeployVerify runs [apple] mac_deploy_verify — a guard command
// that gets the built app's path as its one argument and must exit zero
// before deploy_mac is allowed to install it. Unset or empty: a no-op.
func appleRunMacDeployVerify(cfg map[string]any, h *luahost.Host, appPath string) error {
	verifyCmd := strings.TrimSpace(cfgString(cfg, "mac_deploy_verify", ""))
	if verifyCmd == "" {
		return nil
	}
	fullCmd := verifyCmd + " " + shellQuote(appPath)
	h.Info("Running mac_deploy_verify: " + fullCmd)
	res, err := h.RunCmd(fullCmd, luahost.RunCmdOptions{CaptureLog: true})
	if err != nil {
		return fmt.Errorf("mac_deploy_verify failed (%q): %w", fullCmd, err)
	}
	if !res.Ok {
		return fmt.Errorf("mac_deploy_verify failed (%q exited %d) — refusing to install", fullCmd, res.ExitCode)
	}
	return nil
}

func appleDeployDevice(cfg map[string]any, h *luahost.Host, platform string, f appleFlags) error {
	scheme := cfgString(cfg, platform+"_scheme", "")
	if scheme == "" {
		return fmt.Errorf("apple.%s_scheme required", platform)
	}
	project, err := appleProject(cfg, h)
	if err != nil {
		return err
	}
	config := cfgString(cfg, platform+"_deploy_config", "Release")
	process := appleProcess(project, cfg, platform, scheme)
	bundleID := appleBundle(project, cfg, platform, false)
	derivedName := appleDerivedDataName(cfg, project)
	sdk := appleDeviceSDK[platform]

	deviceType := cfgString(cfg, platform+"_device_type", "")
	deviceID, deviceName, udid, warnLines, err := h.FindConnectedDevice(platform, deviceType)
	for _, w := range warnLines {
		h.Warn(w)
	}
	if err != nil {
		return err
	}
	h.Info("Deploying to " + deviceName)
	appleEnsureDeviceRegistered(h, udid, deviceName)

	h.Info(fmt.Sprintf("Building %s for device (%s)...", scheme, config))
	res, err := h.XcodebuildFiltered(luahost.XcodebuildOptions{
		Project: project, Scheme: scheme, Config: config,
		Destination: sdk.destination, Action: appleBuildAction(f.clean),
		ExtraSettings: appleComposeSignSettings(cfg, h, project, platform),
	})
	if err != nil {
		return err
	}
	if !res.Ok {
		for _, w := range luahost.DiagnoseBuildFailure(res.RawLogPath) {
			h.Warn(w)
		}
		return fmt.Errorf("build failed")
	}

	appPath, _ := luahost.FindApp(appleDerivedGlob(derivedName, config, sdk.sdkSuffix, process))
	if appPath == "" {
		return fmt.Errorf("could not find built app")
	}

	h.Info("Installing to device " + deviceName + "...")
	// Three attempts, backing off and RE-RESOLVING the device between them.
	//
	// A wireless CoreDevice link drops mid-install often enough to matter
	// (NWError 54, "Connection was invalidated"), and the device then vanishes
	// from CoreDevice's table for a second or two. The previous retry fired
	// immediately and reused the device id that had just gone stale, so it
	// failed *differently* — "unable to locate a device matching the requested
	// device identifier" — rather than succeeding, and the advice it printed
	// ("plug in via USB") named a cause that was not the real one: the same
	// device installed fine by hand seconds later, untouched.
	installErr := luahost.InstallOnDevice(deviceID, appPath)
	for attempt := 0; attempt < 2 && installErr != nil; attempt++ {
		delay := time.Duration(3*(attempt+1)) * time.Second
		h.Warn(fmt.Sprintf("Install failed — waiting %s and re-resolving the device (attempt %d of 3)...", delay, attempt+2))
		time.Sleep(delay)

		// A lookup failure here is the device still being absent from the
		// table, which is the condition we are waiting out — not a reason to
		// stop.
		newID, newName, _, _, findErr := h.FindConnectedDevice(platform, deviceType)
		if findErr != nil || newID == "" {
			h.Warn("Device not back in CoreDevice's table yet.")
			continue
		}
		deviceID, deviceName = newID, newName
		installErr = luahost.InstallOnDevice(deviceID, appPath)
	}
	if installErr != nil {
		if platform == "tv" {
			h.Err("Keep the Apple TV awake and on the same network as this Mac.")
		} else {
			h.Err("The device stayed unreachable across all three attempts, so this is " +
				"not the transient control-channel drop the retries cover — check that " +
				"it is awake, unlocked and on this network, or connect USB.")
		}
		return fmt.Errorf("install failed after 3 attempts: %w", installErr)
	}

	if bundleID != "" {
		h.Info("Launching " + bundleID)
		if err := luahost.LaunchOnDevice(deviceID, bundleID, nil); err != nil {
			h.Warn("launch failed: " + err.Error())
		}
	}
	h.Ok("deploy " + platform + " -> " + deviceName)
	return nil
}

// --- process helpers (macOS dev loop's default is_running/kill) ------------

func appleProcessRunning(process string) bool {
	return exec.Command("pgrep", "-x", process).Run() == nil
}

func appleKillProcess(h *luahost.Host, process string) {
	if !appleProcessRunning(process) {
		return
	}
	_ = exec.Command("pkill", "-x", process).Run()
	for i := 0; i < 15; i++ {
		if !appleProcessRunning(process) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.Warn(process + " ignored SIGTERM, sending SIGKILL")
	_ = exec.Command("pkill", "-9", "-x", process).Run()
}

// appleResolveMacDevProductName mirrors admin_lib/apple.py's
// _resolve_mac_dev_product_name: the renamed dev .app's filename (no
// space — must be a valid filename and bundle path component). Default:
// "<process>Dev". Returns "" when process is also empty.
func appleResolveMacDevProductName(cfg map[string]any, process string) string {
	if v := cfgString(cfg, "mac_dev_product_name", ""); v != "" {
		return v
	}
	if process == "" {
		return ""
	}
	return process + "Dev"
}

// appleResolveMacDevProcessPattern mirrors admin_lib/apple.py's
// _resolve_mac_dev_process: a `pgrep -f` pattern that uniquely identifies
// the running dev instance so it can't be confused with a prod copy.
//
// The pattern has to name the bundle the dev loop actually launches, or it
// matches nothing: the loop never finds its own app to kill and reports
// that it never launched, while `open -n` stacks a new window every
// rebuild. Three cases, in Python's own priority order:
//
//   - install=true and apple.mac_install_path_dev is set: the app is
//     copied there before launch (dev --install), so the pattern names
//     that fixed path.
//   - mac_dev_rename=false: no "<name>Dev.app" is ever produced, so the
//     app runs straight out of the build-products directory — the pattern
//     names that directory instead of a bundle name.
//   - otherwise: the renamed "<product>.app" bundle.
//
// Returns "" when process is empty (nothing to build a pattern from) or,
// in the mac_dev_rename branch, when the resolved product name is empty.
func appleResolveMacDevProcessPattern(cfg map[string]any, process string, install bool) string {
	if v := cfgString(cfg, "mac_dev_process", ""); v != "" {
		return v
	}
	if process == "" {
		return ""
	}
	if install {
		if dest := cfgString(cfg, "mac_install_path_dev", ""); dest != "" {
			return strings.TrimRight(dest, "/") + "/Contents/MacOS/" + process
		}
	}
	if !cfgBool(cfg, "mac_dev_rename", true) {
		config := cfgString(cfg, "mac_build_config", "Debug")
		return "/Build/Products/" + config + "/" + process + ".app/Contents/MacOS/" + process
	}
	product := appleResolveMacDevProductName(cfg, process)
	if product == "" {
		return ""
	}
	return product + ".app/Contents/MacOS/" + process
}

// appleMacDevIsRunning mirrors admin_lib/apple.py's
// _make_mac_dev_running_check: `pgrep -f <dev pattern>` where the pattern
// (e.g. "CCToolDev.app/Contents/MacOS/CCTool") is itself the disambiguator
// — a prod build under /Applications/CCTool.app/... doesn't match it.
// Falls back to the generic `pgrep -x process` check when no dev pattern
// resolves, and to an always-false check when process is also empty.
func appleMacDevIsRunning(cfg map[string]any, process string, install bool) func() bool {
	devPattern := appleResolveMacDevProcessPattern(cfg, process, install)
	if devPattern != "" {
		return func() bool { return exec.Command("pgrep", "-f", devPattern).Run() == nil }
	}
	if process != "" {
		return func() bool { return appleProcessRunning(process) }
	}
	return func() bool { return false }
}

// appleMacDevKill mirrors admin_lib/apple.py's _make_mac_dev_kill: kills
// only the dev instance (matched by the same `pgrep -f` pattern
// appleMacDevIsRunning uses), never a prod copy, then waits for it to
// actually exit — returning before the process is gone lets `open -n`
// race with LaunchServices and silently activate the dying instance.
// SIGTERM first, escalating to SIGKILL after 1.5s; when SIGKILL still
// hasn't cleared it after another 1s, warns and gives up rather than
// blocking forever (the extra post-SIGKILL wait+warn Python's dev-pattern
// branch has that appleKillProcess's generic prod-process fallback below
// does not).
//
// Falls back to appleKillProcess(h, process) when no dev pattern resolves
// — the same generic pgrep/pkill -x check dev loops used before this dev-
// aware split existed.
func appleMacDevKill(h *luahost.Host, cfg map[string]any, process string, install bool) func() {
	devPattern := appleResolveMacDevProcessPattern(cfg, process, install)
	if devPattern == "" {
		return func() { appleKillProcess(h, process) }
	}
	isRunning := appleMacDevIsRunning(cfg, process, install)
	send := func(sig syscall.Signal) {
		out, err := exec.Command("pgrep", "-f", devPattern).Output()
		if err != nil {
			return
		}
		for _, field := range strings.Fields(string(out)) {
			pid, perr := strconv.Atoi(field)
			if perr != nil {
				continue
			}
			_ = syscall.Kill(pid, sig)
		}
	}
	return func() {
		if !isRunning() {
			return
		}
		send(syscall.SIGTERM)
		for i := 0; i < 15; i++ {
			if !isRunning() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		h.Warn(devPattern + " ignored SIGTERM, sending SIGKILL")
		send(syscall.SIGKILL)
		for i := 0; i < 10; i++ {
			if !isRunning() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		h.Warn(devPattern + " still alive after SIGKILL — proceeding anyway")
	}
}

// runQuiet runs name(args...), discarding output and reporting only whether
// it exited zero — used for the small fire-and-forget shell-outs (`open`,
// `simctl bootstatus`) that don't need streamed output of their own.
func runQuiet(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func waitForTrue(attempts int, interval time.Duration, cond func() bool) bool {
	for i := 0; i < attempts; i++ {
		if cond() {
			return true
		}
		time.Sleep(interval)
	}
	return false
}

// pumpAttached reads r line-by-line and feeds it through an OutputPump that
// writes to h.Stdout (through internal/frame, so a pinned footer repaints)
// and appends to the active log. Blocks until r hits EOF (the child exited
// and startAttached's goroutine closed the write end) — callers run it in
// its own goroutine.
func pumpAttached(h *luahost.Host, r io.Reader) {
	pump := &streams.OutputPump{
		Out:   func(s string) { frame.Write(h.Stdout, s) },
		LogFn: h.AppendLog,
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		pump.Write(scanner.Text() + "\n")
	}
	pump.Flush()
}

// appleCopyTree recursively copies src to dst, matching Python's
// shutil.copytree — used by the macOS --install path and deploy_mac's
// install-to-/Applications step, both of which shutil.copytree an .app
// bundle wholesale in the original. Named distinctly from sync.go's own
// copyTree (a different signature — excludes-aware, dest already exists)
// rather than overloading that name.
func appleCopyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// appleNotarizeAndStaple submits a Developer ID-signed .app to Apple's notary
// service, waits for the verdict, and staples the ticket to the bundle.
//
// Stapling is what makes the result work OFFLINE. Without it Gatekeeper has to
// reach Apple to learn the app is notarized, so a first launch on a machine
// with no network — or behind a captive portal — still shows the unidentified
// developer dialog. The ticket travels with the bundle once stapled.
//
// Authenticates with the App Store Connect API key already in the environment
// (APP_STORE_CONNECT_KEY_PATH / _KEY_ID / _ISSUER_ID), the same three values
// xcodebuild signs with, rather than a `notarytool store-credentials` keychain
// profile: a profile would be a second copy of the same secret that has to be
// created by hand on every machine before a deploy could work at all.
func appleNotarizeAndStaple(h *luahost.Host, appPath string) error {
	keyPath := os.Getenv("APP_STORE_CONNECT_KEY_PATH")
	keyID := os.Getenv("APP_STORE_CONNECT_KEY_ID")
	issuer := os.Getenv("APP_STORE_CONNECT_ISSUER_ID")
	var missing []string
	for _, pair := range []struct{ name, value string }{
		{"APP_STORE_CONNECT_KEY_PATH", keyPath},
		{"APP_STORE_CONNECT_KEY_ID", keyID},
		{"APP_STORE_CONNECT_ISSUER_ID", issuer},
	} {
		if pair.value == "" {
			missing = append(missing, pair.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("cannot notarize: %s not set (the same App Store Connect credentials xcodebuild signs with)", strings.Join(missing, ", "))
	}

	tmp, err := os.MkdirTemp("", "admin-notarize-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	// notarytool takes an archive, never a bundle directory. `ditto -c -k
	// --keepParent` is Apple's own prescription: plain zip does not preserve the
	// symlinks and extended attributes inside a .app, and a bundle that arrives
	// mangled is rejected for reasons that describe the archive rather than the
	// app.
	archive := filepath.Join(tmp, filepath.Base(appPath)+".zip")
	if out, err := exec.Command("ditto", "-c", "-k", "--keepParent", appPath, archive).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to archive the app for notarization: %v: %s", err, strings.TrimSpace(string(out)))
	}

	h.Info("Submitting to Apple's notary service (this waits for the verdict)...")
	cmd := exec.Command("xcrun", "notarytool", "submit", archive,
		"--key", keyPath, "--key-id", keyID, "--issuer", issuer,
		"--wait", "--timeout", "30m")
	out, err := cmd.CombinedOutput()
	text := string(out)
	h.Info(strings.TrimSpace(text))
	if err != nil || !strings.Contains(text, "status: Accepted") {
		// The submission log names the offending binary and the reason, and is
		// the only place that detail exists — the summary above never has it.
		if id := appleNotarySubmissionID(text); id != "" {
			h.Err("Full reason: xcrun notarytool log " + id +
				" --key \"$APP_STORE_CONNECT_KEY_PATH\"" +
				" --key-id \"$APP_STORE_CONNECT_KEY_ID\"" +
				" --issuer \"$APP_STORE_CONNECT_ISSUER_ID\"")
		}
		return fmt.Errorf("notarization did not succeed")
	}

	h.Info("Stapling the notarization ticket...")
	if out, err := exec.Command("xcrun", "stapler", "staple", appPath).CombinedOutput(); err != nil {
		return fmt.Errorf("stapling failed (the app is notarized but the ticket is not attached, so a first launch with no network still warns): %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// appleNotarySubmissionID pulls the submission UUID out of notarytool's output.
func appleNotarySubmissionID(out string) string {
	return appleNotaryIDRe.FindString(out)
}

var appleNotaryIDRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// appleWantsDeveloperID reports whether this mac deploy should go through the
// archive/export path. Either key alone implies it: notarization is only
// accepted for Developer ID-signed apps, and a Developer ID identity is only
// reachable by exporting.
func appleWantsDeveloperID(cfg map[string]any) bool {
	if b, _ := cfg["notarize"].(bool); b {
		return true
	}
	return strings.Contains(strings.ToLower(cfgString(cfg, "mac_sign_identity", "")), "developer id")
}

// appleArchiveAndExportDeveloperID archives the scheme and exports it with
// method=developer-id, returning the exported .app and a cleanup for the temp
// tree it lives in.
//
// The archive itself is built under the project's ordinary signing — export is
// where the identity changes, which is why nothing here passes
// CODE_SIGN_IDENTITY. -allowProvisioningUpdates plus the App Store Connect key
// lets the export create the Developer ID profile it needs rather than
// requiring one to be made in the portal first.
func appleArchiveAndExportDeveloperID(cfg map[string]any, h *luahost.Host, project, scheme, config string, clean bool) (string, func(), error) {
	signingCert := cfgString(cfg, "mac_sign_identity", "Developer ID Application")
	bundleID := appleBundle(project, cfg, "mac", false)
	profileName := cfgString(cfg, "mac_provisioning_profile", "")
	if profileName == "" {
		return "", nil, fmt.Errorf(
			"apple.mac_provisioning_profile is required for a Developer ID export: "+
				"name the MAC_APP_DIRECT profile for %s (manual signing cannot guess "+
				"it, and automatic signing needs a cloud-signing permission the API "+
				"key may not have)", bundleID)
	}

	tmp, err := os.MkdirTemp("", "admin-devid-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }

	team := ""
	if raw, ok := cfg["development_team"]; ok && raw != nil {
		if v, err := manifest.ResolveEnv(fmt.Sprintf("%v", raw)); err == nil {
			team = v
		}
	}
	if team == "" {
		team = os.Getenv("IOS_DEVELOPMENT_TEAM")
	}

	archivePath := filepath.Join(tmp, scheme+".xcarchive")
	// The action leads, and a macOS archive takes generic/platform=macOS — a
	// bare `platform=macOS` with the action in the middle makes xcodebuild dump
	// its usage instead of building anything.
	args := []string{"archive",
		"-project", project, "-scheme", scheme, "-configuration", config,
		"-destination", "generic/platform=macOS", "-archivePath", archivePath,
		"-allowProvisioningUpdates"}
	args = append(args, appleAuthArgv()...)

	// The archive is built under the project's ORDINARY signing, team included:
	// without DEVELOPMENT_TEAM, a target whose entitlements need a profile fails
	// with "has entitlements that require signing with a development
	// certificate". The Developer ID identity is deliberately not passed here —
	// applying it at archive time is the "conflicting provisioning settings"
	// failure this whole path exists to route around. Export re-signs.
	if team != "" {
		args = append(args, "DEVELOPMENT_TEAM="+team)
	}

	h.Info(fmt.Sprintf("Archiving %s (%s)...", scheme, config))
	if out, err := exec.Command("xcodebuild", args...).CombinedOutput(); err != nil {
		return "", cleanup, fmt.Errorf("archive failed: %v\n%s", err, appleTailLines(string(out), 40))
	}

	// signingStyle MANUAL, naming the profile and the identity outright.
	//
	// `automatic` routes the export through Apple's CLOUD SIGNING, which asks
	// the App Store Connect key to mint a Developer ID profile on the fly. When
	// the key lacks that permission the export reports "Cloud signing permission
	// error" AND "No profiles for '<bundle id>' were found" together — and the
	// second line is misleading, because an ACTIVE profile can exist in the
	// account and cloud signing still never looks at it. Manual signing uses the
	// profile installed under ~/Library/MobileDevice/Provisioning Profiles and
	// the Developer ID certificate already in the keychain, so no permission to
	// create anything is needed.
	options := filepath.Join(tmp, "ExportOptions.plist")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>method</key><string>developer-id</string>
<key>signingStyle</key><string>manual</string>
<key>destination</key><string>export</string>
<key>teamID</key><string>` + team + `</string>
<key>signingCertificate</key><string>` + signingCert + `</string>
<key>provisioningProfiles</key><dict>
<key>` + bundleID + `</key><string>` + profileName + `</string>
</dict>
</dict></plist>
`
	if err := os.WriteFile(options, []byte(plist), 0o644); err != nil {
		return "", cleanup, err
	}

	exportDir := filepath.Join(tmp, "export")
	exportArgs := []string{"-exportArchive", "-archivePath", archivePath,
		"-exportOptionsPlist", options, "-exportPath", exportDir,
		"-allowProvisioningUpdates"}
	exportArgs = append(exportArgs, appleAuthArgv()...)

	h.Info("Exporting Developer ID-signed app...")
	if out, err := exec.Command("xcodebuild", exportArgs...).CombinedOutput(); err != nil {
		return "", cleanup, fmt.Errorf("developer-id export failed: %v\n%s", err, appleTailLines(string(out), 40))
	}

	entries, err := os.ReadDir(exportDir)
	if err != nil {
		return "", cleanup, err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".app") {
			return filepath.Join(exportDir, e.Name()), cleanup, nil
		}
	}
	return "", cleanup, fmt.Errorf("export produced no .app in %s", exportDir)
}

// appleTailLines keeps the last n lines, so a failure reports the part of
// xcodebuild's output that names the reason rather than thousands of lines that
// do not.
func appleTailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// appleAuthArgv returns the App Store Connect authentication flags as separate
// argv elements.
//
// luahost.AppStoreConnectAuthFlags returns them SHELL-QUOTED and joined —
// `-authenticationKeyPath "/path"` as a single string — which is right for
// building a command line to hand to a shell and wrong for exec.Command, where
// it arrives as one unrecognised argument and xcodebuild answers by dumping its
// entire usage. Nothing in that dump says which argument was bad.
func appleAuthArgv() []string {
	path := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_KEY_PATH"))
	keyID := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_KEY_ID"))
	issuer := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_ISSUER_ID"))
	if path == "" || keyID == "" || issuer == "" {
		return nil
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return nil
	}
	return []string{
		"-authenticationKeyPath", path,
		"-authenticationKeyID", keyID,
		"-authenticationKeyIssuerID", issuer,
	}
}
