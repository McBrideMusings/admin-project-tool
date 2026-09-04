//go:build darwin

package kinds

// Continuous Apple dev-loop supervision — the Go port of
// admin_lib/apple.py's dev_loop_macos / dev_loop_simulator / dev_loop_device
// (called through dev_mac/dev_ios/dev_tv/dev_device), composing:
//
//   - internal/reload's CALLER-OWNED shape (SetupReload/PollForReload/
//     IsRebuildRequested/ClearRebuildRequest) for the "rebuild the whole
//     build-install-launch cycle on SIGUSR1 or 'r'" signal — not
//     RunWithReload, which owns restarting a single child process and is
//     the wrong shape for "build, then install, then launch, then attach".
//   - internal/streams.OutputPump (via pumpAttached, apple_darwin.go) for
//     the attached log/console stream, run in its own goroutine.
//   - internal/luahost's already-bound Apple primitives (XcodebuildFiltered,
//     EnsureBootedSimulator, InstallAndLaunchSim, FindConnectedDevice, ...)
//     for every OS-facing call — this file does no xcodebuild/simctl/
//     devicectl invocation of its own beyond the new console/log-stream
//     starters in apple_console_darwin.go.
//
// See apple_darwin.go's package doc comment for what is deliberately not
// ported (App Store Connect device registration). The dev/prod bundle-suffix
// convention layer (applesign_darwin.go's appleComposeDevSettings) IS wired
// into every build call below, and so are the dev-icon-swap hooks and the
// iOS/tvOS display-name patch (appleiconhooks_darwin.go's
// appleBuildIconHooks/applePatchDisplayName), around appleDevSimulator's
// and appleDevDevice's build/install steps. appleDevMac has no icon-swap
// story of its own (it was never in scope for that porting pass) but does
// get its own dev-rename/re-sign postprocessing and dev-aware process
// checks — see the mac_dev_rename handling and appleMacDevIsRunning/
// appleMacDevKill calls below.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/reload"
)

// appleDevPIDFile is the single dev-loop PID file every Apple dev verb
// shares — matching admin_lib/apple.py's shared default
// pid_file="/tmp/admin-run.pid" (one dev loop at a time; server-reload's
// SignalAll and `admin kill` both look for exactly this path).
var appleDevPIDFile = reload.PIDPrefix + ".pid"

const reloadPollInterval = 300 * time.Millisecond

// appleDevMac is dev_mac: macOS dev loop — build, launch via `open -n`,
// attach `log stream`, rebuild on signal.
func appleDevMac(cfg map[string]any, h *luahost.Host, f appleFlags) error {
	scheme := cfgString(cfg, "mac_scheme", "")
	if scheme == "" {
		return fmt.Errorf("apple.mac_scheme required")
	}
	project, err := appleProject(cfg, h)
	if err != nil {
		return err
	}
	config := cfgString(cfg, "mac_build_config", "Debug")
	process := appleProcess(project, cfg, "mac", scheme)
	subsystem := appleSubsystem(project, cfg, "mac", true)
	predicate, err := luahost.LogPredicate(process, subsystem, f.logLevel)
	if err != nil {
		return err
	}
	launchArgs := f.launchArgs
	if len(launchArgs) == 0 {
		launchArgs = cfgStrings(cfg, "launch_args")
	}
	extraSettings, err := appleComposeDevSettings(cfg, h, project, "mac")
	if err != nil {
		return err
	}

	// Dev-aware process checks (ported from Python's
	// _make_mac_dev_running_check/_make_mac_dev_kill): once mac_dev_rename
	// renames the built app to "<process>Dev.app", the generic
	// pgrep/pkill -x process check can no longer tell dev and prod apart.
	// Built once, outside the loop — the dev pattern depends only on cfg
	// and f.install, not on loop state, matching how Python builds
	// is_running_fn/kill_fn once outside dev_loop_macos's own while loop.
	isDevRunning := appleMacDevIsRunning(cfg, process, f.install)
	killDev := appleMacDevKill(h, cfg, process, f.install)

	cleanup, err := reload.SetupReload(appleDevPIDFile)
	if err != nil {
		return err
	}
	defer cleanup()
	luahost.SweepOrphanLogStreams(subsystem)

	for {
		reload.ClearRebuildRequest()
		killDev()

		h.Info("Building...")
		res, err := h.XcodebuildFiltered(luahost.XcodebuildOptions{
			Project: project, Scheme: scheme, Config: config,
			Destination: "platform=macOS", Action: appleBuildAction(f.clean),
			ExtraSettings: extraSettings,
		})
		if err != nil {
			return err
		}
		if !res.Ok {
			h.Err("Build failed")
			if reload.IsRebuildRequested() {
				h.Warn("Rebuild requested during failed build, retrying...")
				continue
			}
			return fmt.Errorf("build failed")
		}

		buildDir, _ := luahost.TargetBuildDir(luahost.XcodebuildOptions{
			Project: project, Scheme: scheme, Config: config, Destination: "platform=macOS",
			ExtraSettings: extraSettings,
		})
		if buildDir == "" {
			h.Err("BUILD SUCCEEDED but xcodebuild reported no TARGET_BUILD_DIR")
			return fmt.Errorf("no TARGET_BUILD_DIR for %s/%s", scheme, config)
		}
		appPath := filepath.Join(buildDir, process+".app")
		if fi, err := os.Stat(appPath); err != nil || !fi.IsDir() {
			h.Err("BUILD SUCCEEDED but no app at " + appPath)
			return fmt.Errorf("app not found at %s", appPath)
		}

		// Two independent things can happen to the built app before launch:
		// it can be renamed to the dev name (mac_dev_rename, default true),
		// and it can be installed to a fixed path (--install). Rename FIRST,
		// then install what came out — matching Python's dev_mac() composing
		// pre_launch as `_installer(_rename(built_app))`. Renaming after the
		// install copy would rename the wrong (already-copied) path.
		if cfgBool(cfg, "mac_dev_rename", true) {
			product := appleResolveMacDevProductName(cfg, process)
			display := appleResolveDevDisplayName(cfg, "mac", process)
			if product != "" || display != "" {
				signIdentity := appleResolveDevSignIdentity(cfg, h)
				dest, perr := luahost.DevMacOSPostprocess(appPath, product, display, signIdentity)
				// DevMacOSPostprocess returns "" only on a real failure
				// (not an .app bundle, missing Info.plist, rename failed);
				// a non-empty dest with a non-nil error is a cosmetic-only
				// plutil/codesign warning — the rename itself succeeded.
				if dest == "" {
					return perr
				}
				if perr != nil {
					h.Warn(perr.Error())
				}
				appPath = dest
			}
		}

		if f.install {
			dest := cfgString(cfg, "mac_install_path_dev", "")
			if dest == "" {
				return fmt.Errorf("--install requires apple.mac_install_path_dev in admin.toml")
			}
			h.Info("Installing to " + dest)
			_ = os.RemoveAll(dest)
			if err := appleCopyTree(appPath, dest); err != nil {
				return err
			}
			appPath = dest
		}

		h.Info("Launching " + appPath)
		openArgs := []string{"-n", appPath}
		if len(launchArgs) > 0 {
			openArgs = append(openArgs, "--args")
			openArgs = append(openArgs, launchArgs...)
		}
		if err := runOpen(openArgs); err != nil {
			return err
		}

		appeared := waitForTrue(33, reloadPollInterval, isDevRunning)
		if !appeared {
			h.Warn("App did not appear in process list within 10s")
			h.Warn("It may have crashed on launch, or detection failed.")
		}

		streamCmd, streamOut, streamDone, serr := luahost.StreamMacLog(predicate, "")
		if serr != nil {
			h.Warn("could not attach log stream: " + serr.Error())
		} else {
			go pumpAttached(h, streamOut)
		}

		h.Info("Press r to rebuild, Ctrl+C to quit")
		isRunning := isDevRunning
		if !appeared {
			isRunning = func() bool { return true }
		}
		gotReload := reload.PollForReload(isRunning, reloadPollInterval)

		if streamCmd != nil {
			_ = streamCmd.Process.Kill()
			<-streamDone
		}

		if gotReload {
			h.Info("Reload signal received — rebuilding...")
		} else if appeared {
			h.Info("Stopping " + process + "...")
		} else {
			h.Info("Exiting (app never appeared).")
		}
		killDev()

		if !gotReload {
			return nil
		}
	}
}

// appleDevSimulator is dev_ios/dev_tv: build for an iOS or tvOS simulator,
// install+launch, attach `simctl spawn log stream`, rebuild on signal.
// Everything below is identical between the two platforms except the
// xcodebuild destination and DerivedData suffix (appleSimSDK).
func appleDevSimulator(cfg map[string]any, h *luahost.Host, platform string, f appleFlags) error {
	scheme := cfgString(cfg, platform+"_scheme", "")
	if scheme == "" {
		return fmt.Errorf("apple.%s_scheme required", platform)
	}
	project, err := appleProject(cfg, h)
	if err != nil {
		return err
	}
	config := cfgString(cfg, platform+"_build_config", "Debug")
	process := appleProcess(project, cfg, platform, scheme)
	bundleID := appleBundle(project, cfg, platform, true)
	subsystem := appleSubsystem(project, cfg, platform, true)
	predicate, err := luahost.LogPredicate(process, subsystem, f.logLevel)
	if err != nil {
		return err
	}
	sdk := appleSimSDK[platform]
	derivedName := appleDerivedDataName(cfg, project)
	appPattern := appleDerivedGlob(derivedName, config, sdk.sdkSuffix, process)
	launchArgs := f.launchArgs
	if len(launchArgs) == 0 {
		launchArgs = cfgStrings(cfg, "launch_args")
	}
	devEnv, err := appleDevEnv(cfg)
	if err != nil {
		return err
	}
	extraSettings, err := appleComposeDevSettings(cfg, h, project, platform)
	if err != nil {
		return err
	}
	iconPre, iconPost := appleBuildIconHooks(cfg, h)
	devDisplayName := appleResolveDevDisplayName(cfg, platform, process)

	cleanup, err := reload.SetupReload(appleDevPIDFile)
	if err != nil {
		return err
	}
	defer cleanup()
	luahost.SweepOrphanLogStreams(subsystem)

	deviceName := os.Getenv(sdk.deviceEnv)
	if deviceName == "" {
		deviceName = cfgString(cfg, platform+"_device", "")
	}
	var simID string
	if deviceName != "" {
		if id, ferr := luahost.FindSimulator(deviceName); ferr == nil && id != "" {
			simID = id
		} else {
			h.Warn(fmt.Sprintf("simulator '%s' not found, falling back to auto-resolve", deviceName))
		}
	} else {
		h.Info("No simulator pinned — auto-resolving.")
	}
	var destination string
	if simID != "" {
		luahost.BootSimulator(simID)
		_ = runQuiet("xcrun", "simctl", "bootstatus", simID, "-b")
		destination = fmt.Sprintf("platform=%s,id=%s", sdk.destinationLabel, simID)
	} else {
		id, name, info, rerr := luahost.EnsureBootedSimulator(platform)
		if rerr != nil {
			return rerr
		}
		simID = id
		if info != "" {
			h.Info(info)
		}
		_ = name
		destination = fmt.Sprintf("platform=%s,id=%s", sdk.destinationLabel, simID)
	}

	for {
		reload.ClearRebuildRequest()
		existingApp, _ := luahost.FindApp(appPattern)
		launchBundle := bundleID
		if existingApp != "" {
			if b := luahost.AppBundleIdentifier(existingApp); b != "" {
				launchBundle = b
			}
		}
		if launchBundle == "" {
			launchBundle = subsystem
		}
		luahost.TerminateSimApp(simID, launchBundle)

		if err := iconPre(); err != nil {
			return err
		}
		h.Info("Building for simulator...")
		res, err := h.XcodebuildFiltered(luahost.XcodebuildOptions{
			Project: project, Scheme: scheme, Config: config,
			Destination: destination, Action: appleBuildAction(f.clean),
			ExtraSettings: extraSettings,
		})
		if perr := iconPost(); perr != nil {
			h.Warn("could not restore icons: " + perr.Error())
		}
		if err != nil {
			return err
		}
		if !res.Ok {
			h.Err("Build failed")
			if reload.IsRebuildRequested() {
				h.Warn("Rebuild requested during failed build, retrying...")
				continue
			}
			return fmt.Errorf("build failed")
		}

		appPath, _ := luahost.FindApp(appPattern)
		if appPath == "" {
			h.Err("Could not find built app")
			return fmt.Errorf("no app matching %s", appPattern)
		}

		launchBundle = luahost.AppBundleIdentifier(appPath)
		if launchBundle == "" {
			launchBundle = bundleID
		}
		if launchBundle == "" {
			launchBundle = subsystem
		}
		expected := bundleID
		if expected == "" {
			expected = subsystem
		}
		if expected != "" && launchBundle != expected {
			h.Warn(fmt.Sprintf("Built app has bundle ID %q, expected %q", launchBundle, expected))
			h.Warn("Path: " + appPath)
			h.Warn("Stale DerivedData from another project may be interfering.")
		}

		if err := applePatchDisplayName(appPath, devDisplayName); err != nil {
			h.Err(err.Error())
			return err
		}

		h.Info("Installing on simulator...")
		luahost.InstallAndLaunchSim(simID, appPath, launchBundle, launchArgs, devEnv)

		isRunning := func() bool { return luahost.IsSimAppRunning(simID, launchBundle) }
		waitForTrue(10, reloadPollInterval, isRunning)

		streamCmd, streamOut, streamDone, serr := luahost.StreamMacLog(predicate, simID)
		if serr != nil {
			h.Warn("could not attach log stream: " + serr.Error())
		} else {
			go pumpAttached(h, streamOut)
		}

		h.Info("Press r to rebuild, Ctrl+C to quit")
		gotReload := reload.PollForReload(isRunning, reloadPollInterval)

		if streamCmd != nil {
			_ = streamCmd.Process.Kill()
			<-streamDone
		}

		if gotReload {
			h.Info("Reload signal received — rebuilding...")
		} else {
			h.Info("Stopping " + process + "...")
		}
		luahost.TerminateSimApp(simID, launchBundle)

		if !gotReload {
			return nil
		}
	}
}

// appleDevDevice is dev_device (iOS) and dev_device_tv (tvOS): build a Debug
// configuration for a physical device, install via devicectl, launch with
// --console attached (non-redacted os_log — the only way to get device log
// output at all, per LaunchOnDevice's own doc comment), rebuild on signal.
// Unlike the simulator loop, a rebuild has to tear the launched app down and
// relaunch it — there is no "attach to an already-running device process"
// primitive.
//
// The tvOS half exists because `deploy tv` builds Release, and a project may
// gate its debug surface on `#if DEBUG` — leaving a physical Apple TV with no
// way to be driven or inspected at all. Everything that differs between the
// two platforms is already keyed by platform (appleDeviceSDK,
// luahost.devicePlatforms), so this loop only had to stop naming iOS.
func appleDevDevice(cfg map[string]any, h *luahost.Host, platform string, f appleFlags) error {
	sdk, ok := appleDeviceSDK[platform]
	if !ok {
		return fmt.Errorf("dev device: unknown platform %q", platform)
	}
	scheme := cfgString(cfg, platform+"_scheme", "")
	if scheme == "" {
		return fmt.Errorf("apple.%s_scheme required", platform)
	}
	project, err := appleProject(cfg, h)
	if err != nil {
		return err
	}
	config := cfgString(cfg, platform+"_build_config", "Debug")
	process := appleProcess(project, cfg, platform, scheme)
	bundleID := appleBundle(project, cfg, platform, true)
	derivedName := appleDerivedDataName(cfg, project)
	appPattern := appleDerivedGlob(derivedName, config, sdk.sdkSuffix, process)

	deviceType := cfgString(cfg, platform+"_device_type", "")
	deviceID, deviceName, udid, warnLines, err := h.FindConnectedDevice(platform, deviceType)
	for _, w := range warnLines {
		h.Warn(w)
	}
	if err != nil {
		return err
	}
	appleEnsureDeviceRegistered(h, udid, deviceName)
	devEnv, err := appleDevEnv(cfg)
	if err != nil {
		return err
	}
	launchEnv := map[string]string{"OS_ACTIVITY_DT_MODE": "YES"}
	for k, v := range devEnv {
		launchEnv[k] = v
	}
	extraSettings, err := appleComposeDevSettings(cfg, h, project, platform)
	if err != nil {
		return err
	}
	iconPre, iconPost := appleBuildIconHooks(cfg, h)
	devDisplayName := appleResolveDevDisplayName(cfg, platform, process)

	cleanup, err := reload.SetupReload(appleDevPIDFile)
	if err != nil {
		return err
	}
	defer cleanup()

	launchBundle := bundleID

	for {
		reload.ClearRebuildRequest()
		h.TerminateDeviceApp(deviceID, launchBundle)

		if err := iconPre(); err != nil {
			return err
		}
		h.Info(fmt.Sprintf("Building for device (%s)...", deviceName))
		res, err := h.XcodebuildFiltered(luahost.XcodebuildOptions{
			Project: project, Scheme: scheme, Config: config,
			Destination: sdk.destination, Action: appleBuildAction(f.clean),
			ExtraSettings: extraSettings,
		})
		if perr := iconPost(); perr != nil {
			h.Warn("could not restore icons: " + perr.Error())
		}
		if err != nil {
			return err
		}
		if !res.Ok {
			h.Err("Build failed")
			if reload.IsRebuildRequested() {
				h.Warn("Rebuild requested during failed build, retrying...")
				continue
			}
			return fmt.Errorf("build failed")
		}

		appPath, _ := luahost.FindApp(appPattern)
		if appPath == "" {
			h.Err("Could not find built app")
			return fmt.Errorf("no app matching %s", appPattern)
		}
		if b := luahost.AppBundleIdentifier(appPath); b != "" {
			launchBundle = b
		} else if launchBundle == "" {
			launchBundle = bundleID
		}

		if err := applePatchDisplayName(appPath, devDisplayName); err != nil {
			h.Err(err.Error())
			return err
		}

		h.Info("Installing on " + deviceName + "...")
		if err := luahost.InstallOnDevice(deviceID, appPath); err != nil {
			h.Err("Install failed: " + err.Error())
			return err
		}

		h.Info("Launching on " + deviceName + "...")
		consoleCmd, consoleOut, consoleDone, cerr := luahost.StartDeviceConsole(deviceID, launchBundle, launchEnv)
		if cerr != nil {
			return cerr
		}
		go pumpAttached(h, consoleOut)

		h.Info("Press r to rebuild, Ctrl+C to quit")
		isRunning := func() bool {
			select {
			case <-consoleDone:
				return false
			default:
				return true
			}
		}
		gotReload := reload.PollForReload(isRunning, reloadPollInterval)

		_ = consoleCmd.Process.Kill()
		<-consoleDone

		if gotReload {
			h.Info("Reload signal received — rebuilding...")
		} else {
			h.Info("Stopping " + process + "...")
		}
		h.TerminateDeviceApp(deviceID, launchBundle)

		if !gotReload {
			h.Info("Done.")
			return nil
		}
	}
}

func runOpen(args []string) error {
	return runQuiet("open", args...)
}

// appleResolveDevSignIdentity resolves the identity DevMacOSPostprocess
// re-signs the renamed dev .app with, mirroring Python's dev_mac():
// `(resolve_signing_identity(cfg) if "sign_identity" in cfg else None) or
// "-"`. Re-signing with the same stable identity the build used (rather
// than falling through to an ad-hoc re-sign) is what lets TCC grants
// survive the rename; "-" (ad-hoc) is the unchanged default when no
// sign_identity is configured. Follows the same
// present-key-then-luahost.ResolveSigningIdentity shape
// applesign_darwin.go's appleComposeSignSettings uses for build-time
// signing.
func appleResolveDevSignIdentity(cfg map[string]any, h *luahost.Host) string {
	if _, ok := cfg["sign_identity"]; !ok {
		return "-"
	}
	selector := cfgString(cfg, "sign_identity", "")
	ident, warnLines := luahost.ResolveSigningIdentity(selector)
	for _, w := range warnLines {
		h.Warn(w)
	}
	if ident == "" {
		return "-"
	}
	return ident
}
