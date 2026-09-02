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
// ported (mac dev-rename/re-sign postprocessing, icon-swap hooks, App Store
// Connect device registration, is_running/kill overrides). The dev/prod
// bundle-suffix convention layer (applesign_darwin.go's
// appleComposeDevSettings) IS wired into every build call below.

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
	project := appleProject(cfg)
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

	cleanup, err := reload.SetupReload(appleDevPIDFile)
	if err != nil {
		return err
	}
	defer cleanup()
	luahost.SweepOrphanLogStreams(subsystem)

	for {
		reload.ClearRebuildRequest()
		appleKillProcess(h, process)

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

		appeared := waitForTrue(33, reloadPollInterval, func() bool { return appleProcessRunning(process) })
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
		isRunning := func() bool { return appleProcessRunning(process) }
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
		appleKillProcess(h, process)

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
	project := appleProject(cfg)
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
	extraSettings, err := appleComposeDevSettings(cfg, h, project, platform)
	if err != nil {
		return err
	}

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

		h.Info("Building for simulator...")
		res, err := h.XcodebuildFiltered(luahost.XcodebuildOptions{
			Project: project, Scheme: scheme, Config: config,
			Destination: destination, Action: appleBuildAction(f.clean),
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

		h.Info("Installing on simulator...")
		luahost.InstallAndLaunchSim(simID, appPath, launchBundle, launchArgs)

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
	project := appleProject(cfg)
	config := cfgString(cfg, platform+"_build_config", "Debug")
	process := appleProcess(project, cfg, platform, scheme)
	bundleID := appleBundle(project, cfg, platform, true)
	derivedName := appleDerivedDataName(cfg, project)
	appPattern := appleDerivedGlob(derivedName, config, sdk.sdkSuffix, process)

	deviceType := cfgString(cfg, platform+"_device_type", "")
	deviceID, deviceName, _, warnLines, err := h.FindConnectedDevice(platform, deviceType)
	for _, w := range warnLines {
		h.Warn(w)
	}
	if err != nil {
		return err
	}
	extraSettings, err := appleComposeDevSettings(cfg, h, project, platform)
	if err != nil {
		return err
	}

	cleanup, err := reload.SetupReload(appleDevPIDFile)
	if err != nil {
		return err
	}
	defer cleanup()

	launchBundle := bundleID

	for {
		reload.ClearRebuildRequest()
		h.TerminateDeviceApp(deviceID, launchBundle)

		h.Info(fmt.Sprintf("Building for device (%s)...", deviceName))
		res, err := h.XcodebuildFiltered(luahost.XcodebuildOptions{
			Project: project, Scheme: scheme, Config: config,
			Destination: sdk.destination, Action: appleBuildAction(f.clean),
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

		h.Info("Installing on " + deviceName + "...")
		if err := luahost.InstallOnDevice(deviceID, appPath); err != nil {
			h.Err("Install failed: " + err.Error())
			return err
		}

		h.Info("Launching on " + deviceName + "...")
		consoleCmd, consoleOut, consoleDone, cerr := luahost.StartDeviceConsole(deviceID, launchBundle,
			map[string]string{"OS_ACTIVITY_DT_MODE": "YES"})
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
