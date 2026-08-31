//go:build darwin

// Package kinds' Apple verb dispatch — the Go port of the
// dev/build/deploy/clean half of admin_lib/apple.py's "High-level archetype
// wrappers" section (its own comment: "The apple archetype's kind:"python"
// commands call these with _APPLE_CONFIG"). Darwin-only for the same reason
// internal/luahost's Apple surface is: xcodebuild, simctl, devicectl and
// codesign are macOS-specific.
//
// What this file deliberately does NOT port, and why — every one of these
// is a documented gap in docs/architecture/lua-host-api.md's "What is
// intentionally not here yet", not a silent omission:
//
//   - The mac dev-rename/re-sign postprocessing (DevMacOSPostprocess is
//     bound in internal/luahost/apple_darwin.go and callable, but nothing
//     here calls it) — a dev mac build is not renamed/re-signed to a
//     distinct Finder/Dock entry. The dev/prod bundle-suffix convention
//     layer itself (_compose_dev_settings, _entitlements_paths,
//     _target_needs_provisioning_profile) IS ported — see
//     applesign_darwin.go — and its ADMIN_DEV_BUNDLE_SUFFIX override is
//     wired into every dev-loop build in appledevloop_darwin.go.
//   - Dev-icon-swap hooks (build_icon_hooks: backup/swap-banner/restore
//     around each build). apple.backup_icons/apple.swap_dev_icons_banner/
//     apple.restore_icons are bound and callable from a future pass; wiring
//     them into pre_build/post_build here is left undone this round.
//   - App Store Connect device registration (ensure_device_registered) —
//     lives in admin_lib/appstoreconnect.py, a separate module this ticket
//     does not port. A device deploy that hits Apple's "team has no devices"
//     error still reports that error; it just isn't pre-empted.
//   - is_running_fn/kill_fn overrides (_make_mac_dev_running_check/
//     _make_mac_dev_kill) — the mac dev loop always uses the default
//     pgrep/pkill -x process check.
package kinds

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
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
func runAppleVerb(verb, action string, cfg map[string]any, args []string) error {
	h := newHost()
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
		return appleDevDevice(cfg, h, f)
	case "deploy_mac":
		return appleDeployMac(cfg, h, f)
	case "deploy_ios":
		return appleDeployDevice(cfg, h, "ios", f)
	case "deploy_tv":
		return appleDeployDevice(cfg, h, "tv", f)
	case "clean_ios":
		return appleClean(cfg, h)
	default:
		return fmt.Errorf("action %q: kind=\"apple\" has no verb %q", action, verb)
	}
}

// --- config resolution -----------------------------------------------------
// Ports the plain (non-dev/prod-suffix) half of admin_lib/apple.py's
// _resolve_* helpers: the Xcode project is the source of truth for anything
// not overridden in [apple], via apple.xcode_setting.

func appleProject(cfg map[string]any) string {
	p, _ := luahost.DetectProjectPath(cfgString(cfg, "project", ""))
	return p
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
	project := appleProject(cfg)
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
	project := appleProject(cfg)
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
	project := appleProject(cfg)
	config := cfgString(cfg, "mac_deploy_config", "Release")
	process := appleProcess(project, cfg, "mac", scheme)
	installPath := cfgString(cfg, "mac_install_path_prod", "")
	if installPath == "" {
		installPath = "/Applications/" + process + ".app"
	}
	derivedName := appleDerivedDataName(cfg, project)

	h.Info("Killing any running " + process + "...")
	_ = exec.Command("pkill", "-x", process).Run()

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

	appPath, _ := luahost.FindApp(appleDerivedGlob(derivedName, config, "", process))
	if appPath == "" {
		return fmt.Errorf("could not find built app")
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

func appleDeployDevice(cfg map[string]any, h *luahost.Host, platform string, f appleFlags) error {
	scheme := cfgString(cfg, platform+"_scheme", "")
	if scheme == "" {
		return fmt.Errorf("apple.%s_scheme required", platform)
	}
	project := appleProject(cfg)
	config := cfgString(cfg, platform+"_deploy_config", "Release")
	process := appleProcess(project, cfg, platform, scheme)
	bundleID := appleBundle(project, cfg, platform, false)
	derivedName := appleDerivedDataName(cfg, project)
	sdk := appleDeviceSDK[platform]

	deviceType := cfgString(cfg, platform+"_device_type", "")
	deviceID, deviceName, _, warnLines, err := h.FindConnectedDevice(platform, deviceType)
	for _, w := range warnLines {
		h.Warn(w)
	}
	if err != nil {
		return err
	}
	h.Info("Deploying to " + deviceName)

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
	if err := luahost.InstallOnDevice(deviceID, appPath); err != nil {
		h.Warn("Install failed — retrying once...")
		if err := luahost.InstallOnDevice(deviceID, appPath); err != nil {
			if platform == "tv" {
				h.Err("Keep the Apple TV awake and on the same network as this Mac.")
			} else {
				h.Err("Try plugging in via USB cable for a more reliable connection.")
			}
			return fmt.Errorf("install failed after retry: %w", err)
		}
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
