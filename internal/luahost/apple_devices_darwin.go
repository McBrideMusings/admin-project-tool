//go:build darwin

package luahost

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// --- Simulators --------------------------------------------------------

var simDeviceLine = regexp.MustCompile(`^\s+(.+?)\s+\(([A-F0-9-]{36})\)(?:\s+\((\w+)\))?`)
var simRuntimeHeader = regexp.MustCompile(`^-- (.+) --$`)

// FindSimulator finds a simulator device ID by exact name, preferring a
// booted instance. Returns "" if none match.
func FindSimulator(deviceName string) (string, error) {
	out, err := execOutput("xcrun", "simctl", "list", "devices", "available")
	if err != nil {
		return "", err
	}
	var first string
	for _, line := range strings.Split(out, "\n") {
		m := simDeviceLine.FindStringSubmatch(line)
		if m == nil || m[1] != deviceName {
			continue
		}
		if m[3] == "Booted" {
			return m[2], nil
		}
		if first == "" {
			first = m[2]
		}
	}
	return first, nil
}

// ResolveSimulator picks the best simulator for platform ("ios" or "tv"):
// an already-booted device of that platform first, else the newest
// available device, grouping by runtime header to tell an Apple TV apart
// from an iPhone simulator. infoLines carries the "Using booted/available
// simulator: ..." message; the Lua binding relays it via info(). Returns
// ("", "", nil, nil) with a non-nil error message when nothing matched.
func ResolveSimulator(platform string) (deviceID, deviceName string, infoLine string, err error) {
	plat, ok := simPlatforms[platform]
	if !ok {
		return "", "", "", fmt.Errorf("unknown simulator platform %q — expected one of %s", platform, strings.Join(simPlatformKeys(), ", "))
	}

	// 1. Already-booted device of this platform.
	if out, e := execOutput("xcrun", "simctl", "list", "devices", "booted"); e == nil {
		var currentRuntime string
		for _, line := range strings.Split(out, "\n") {
			if rm := simRuntimeHeader.FindStringSubmatch(line); rm != nil {
				currentRuntime = rm[1]
				continue
			}
			m := simDeviceLine.FindStringSubmatch(line)
			if m != nil && m[3] == "Booted" && strings.HasPrefix(currentRuntime, plat.RuntimeMatch) {
				return m[2], m[1], fmt.Sprintf("Using booted simulator: %s", m[1]), nil
			}
		}
	}

	// 2. None booted — newest available device of this platform.
	out, e := execOutput("xcrun", "simctl", "list", "devices", "available")
	if e != nil {
		return "", "", "", e
	}
	type candidate struct{ runtime, name, id string }
	var candidates []candidate
	var currentRuntime string
	for _, line := range strings.Split(out, "\n") {
		if rm := simRuntimeHeader.FindStringSubmatch(line); rm != nil {
			currentRuntime = rm[1]
			continue
		}
		m := simDeviceLine.FindStringSubmatch(line)
		if m != nil && m[3] == "Shutdown" && strings.HasPrefix(currentRuntime, plat.RuntimeMatch) && strings.Contains(m[1], plat.DeviceMatch) {
			candidates = append(candidates, candidate{currentRuntime, m[1], m[2]})
		}
	}
	if len(candidates) == 0 {
		msg := fmt.Sprintf("No %s simulators found.", plat.DeviceMatch)
		if platform != "ios" {
			msg += fmt.Sprintf(" Install the runtime with: xcodebuild -downloadPlatform %s", plat.Label)
		}
		return "", "", "", fmt.Errorf("%s", msg)
	}
	best := candidates[len(candidates)-1]
	return best.id, best.name, fmt.Sprintf("Using simulator: %s (%s)", best.name, best.runtime), nil
}

// BootSimulator issues `simctl boot` and opens Simulator.app. Returns once
// boot has been *requested* — the device may still be booting; use
// EnsureBootedSimulator when the caller needs a ready device.
func BootSimulator(deviceID string) {
	_ = execRun("xcrun", "simctl", "boot", deviceID)
	_ = execRun("open", "-a", "Simulator")
}

// EnsureBootedSimulator resolves a simulator for platform, opens
// Simulator.app, and blocks on `simctl bootstatus -b` until it finishes
// booting (which itself boots the device if it wasn't already — no race
// between "boot" and "install").
func EnsureBootedSimulator(platform string) (deviceID, deviceName, infoLine string, err error) {
	deviceID, deviceName, infoLine, err = ResolveSimulator(platform)
	if err != nil {
		return "", "", "", err
	}
	_ = execRun("open", "-a", "Simulator")
	_ = execRun("xcrun", "simctl", "bootstatus", deviceID, "-b")
	return deviceID, deviceName, infoLine, nil
}

// InstallAndLaunchSim installs appPath on deviceID and launches bundleID,
// passing launchArgs as UIApplication launch arguments and env as environment
// variables delivered to the launched process. `simctl launch` reads any
// SIMCTL_CHILD_<NAME> var off its own environment and hands <NAME> to the
// child — there is no direct `-e` flag the way `devicectl` has, so that's the
// prefix this applies.
func InstallAndLaunchSim(deviceID, appPath, bundleID string, launchArgs []string, env map[string]string) {
	_ = execRun("xcrun", "simctl", "install", deviceID, appPath)
	args := append([]string{"simctl", "launch", deviceID, bundleID}, launchArgs...)
	if len(env) == 0 {
		_ = execRun("xcrun", args...)
		return
	}
	cmd := exec.Command("xcrun", args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, "SIMCTL_CHILD_"+k+"="+v)
	}
	_ = cmd.Run()
}

func TerminateSimApp(deviceID, bundleID string) {
	_ = execRun("xcrun", "simctl", "terminate", deviceID, bundleID)
}

// IsSimAppRunning checks launchctl inside the simulator for bundleID.
func IsSimAppRunning(deviceID, bundleID string) bool {
	out, err := execOutput("xcrun", "simctl", "spawn", deviceID, "launchctl", "list")
	if err != nil {
		return false
	}
	return strings.Contains(out, bundleID)
}

// AppBundleIdentifier reads CFBundleIdentifier from a built .app via
// PlistBuddy (which, unlike the plist.read capability, handles both binary
// and XML Info.plist — a built app's is typically binary). Returns "" if
// the app or key is missing.
func AppBundleIdentifier(appPath string) string {
	infoPlist := filepath.Join(appPath, "Info.plist")
	if _, err := os.Stat(infoPlist); err != nil {
		return ""
	}
	out, err := execOutput("/usr/libexec/PlistBuddy", "-c", "Print :CFBundleIdentifier", infoPlist)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// --- Physical devices ----------------------------------------------------

type deviceCtlList struct {
	Result struct {
		Devices []deviceCtlDevice `json:"devices"`
	} `json:"result"`
}

type deviceCtlDevice struct {
	Identifier       string `json:"identifier"`
	DeviceProperties struct {
		Name string `json:"name"`
	} `json:"deviceProperties"`
	HardwareProperties struct {
		UDID       string `json:"udid"`
		Platform   string `json:"platform"`
		DeviceType string `json:"deviceType"`
	} `json:"hardwareProperties"`
	ConnectionProperties struct {
		TransportType string `json:"transportType"`
	} `json:"connectionProperties"`
}

// listDevices queries `xcrun devicectl list devices` and returns every
// wired or local-network device. Uses h.LogDir for the required
// --json-output path, keying it per-checkout rather than one shared /tmp
// path (admin-project-tool#447).
func (h *Host) listDevices() ([]deviceCtlDevice, error) {
	if err := os.MkdirAll(h.LogDir, 0o755); err != nil {
		return nil, err
	}
	jsonPath := filepath.Join(h.LogDir, "devices.json")
	if err := execRun("xcrun", "devicectl", "list", "devices", "--json-output", jsonPath); err != nil {
		return nil, nil // non-zero exit -> no devices, not an error
	}
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, nil
	}
	var parsed deviceCtlList
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, nil
	}
	var out []deviceCtlDevice
	for _, d := range parsed.Result.Devices {
		if d.ConnectionProperties.TransportType == "wired" || d.ConnectionProperties.TransportType == "localNetwork" {
			out = append(out, d)
		}
	}
	return out, nil
}

// FindConnectedDevice finds a connected device of platform ("ios" or "tv"),
// filtered by deviceType (e.g. "iPhone") when there is more than one
// candidate and deviceType is non-empty. An archetype's Lua body reads its
// own config/env and passes the resolved device-type selector in.
func (h *Host) FindConnectedDevice(platform, deviceType string) (deviceID, deviceName, udid string, warnLines []string, err error) {
	plat, ok := devicePlatforms[platform]
	if !ok {
		return "", "", "", nil, fmt.Errorf("unknown device platform %q — expected one of %s", platform, strings.Join(devicePlatformKeys(), ", "))
	}
	all, err := h.listDevices()
	if err != nil {
		return "", "", "", nil, err
	}
	var devices []deviceCtlDevice
	for _, d := range all {
		if d.HardwareProperties.Platform == plat.Platform {
			devices = append(devices, d)
		}
	}

	if deviceType != "" && len(devices) > 0 {
		var matching []deviceCtlDevice
		for _, d := range devices {
			if strings.EqualFold(d.HardwareProperties.DeviceType, deviceType) {
				matching = append(matching, d)
			}
		}
		if len(matching) > 0 {
			devices = matching
		} else {
			warnLines = append(warnLines, fmt.Sprintf("no connected %s — falling back to any %s device", deviceType, plat.Label))
		}
	}

	if len(devices) == 0 {
		return "", "", "", warnLines, fmt.Errorf("no connected %s device found", plat.Label)
	}
	if len(devices) > 1 {
		names := make([]string, len(devices))
		for i, d := range devices {
			names[i] = fmt.Sprintf("%s (%s)", d.DeviceProperties.Name, d.HardwareProperties.DeviceType)
		}
		warnLines = append(warnLines,
			fmt.Sprintf("%d %s devices connected: %s", len(devices), plat.Label, strings.Join(names, ", ")),
			fmt.Sprintf("Using the first. Pin one with %s_device_type in [apple] or %s_DEVICE_TYPE.", platform, strings.ToUpper(platform)))
	}
	d := devices[0]
	return d.Identifier, d.DeviceProperties.Name, d.HardwareProperties.UDID, warnLines, nil
}

// InstallOnDevice installs appPath on the physical device deviceID.
func InstallOnDevice(deviceID, appPath string) error {
	return execRun("xcrun", "devicectl", "device", "install", "app", "--device", deviceID, appPath)
}

// LaunchOnDevice launches bundleID on deviceID, terminating any existing
// instance first, passing env as launch environment variables. The
// --console/attached-stdout mode is explicitly not covered here: it blocks
// on the child's stdout until exit, which is the output-pump's job
// (internal/streams.OutputPump), not a one-shot host function. A caller
// needing attached device console output is admin-3rgp.11/.17's concern
// once that lands in Go.
func LaunchOnDevice(deviceID, bundleID string, env map[string]string) error {
	args := []string{"devicectl", "device", "process", "launch", "--terminate-existing", "--device", deviceID}
	if len(env) > 0 {
		b, err := json.Marshal(env)
		if err != nil {
			return err
		}
		args = append(args, "-e", string(b))
	}
	args = append(args, bundleID)
	return execRun("xcrun", args...)
}

func (h *Host) findDeviceProcess(deviceID, bundleID string) (pid int, ok bool) {
	if err := os.MkdirAll(h.LogDir, 0o755); err != nil {
		return 0, false
	}
	jsonPath := filepath.Join(h.LogDir, "processes.json")
	if err := execRun("xcrun", "devicectl", "device", "info", "processes", "--device", deviceID, "--json-output", jsonPath); err != nil {
		return 0, false
	}
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return 0, false
	}
	var parsed struct {
		Result struct {
			RunningProcesses []struct {
				BundleIdentifier  string `json:"bundleIdentifier"`
				ProcessIdentifier int    `json:"processIdentifier"`
			} `json:"runningProcesses"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return 0, false
	}
	for _, p := range parsed.Result.RunningProcesses {
		if p.BundleIdentifier == bundleID {
			return p.ProcessIdentifier, true
		}
	}
	return 0, false
}

// TerminateDeviceApp terminates bundleID on the physical device deviceID.
// Silently succeeds if the app isn't running.
func (h *Host) TerminateDeviceApp(deviceID, bundleID string) {
	pid, ok := h.findDeviceProcess(deviceID, bundleID)
	if !ok {
		return
	}
	_ = execRun("xcrun", "devicectl", "device", "process", "terminate", "--device", deviceID, "--pid", fmt.Sprintf("%d", pid))
}

// IsDeviceAppRunning reports whether bundleID has a running process on the
// physical device deviceID.
func (h *Host) IsDeviceAppRunning(deviceID, bundleID string) bool {
	_, ok := h.findDeviceProcess(deviceID, bundleID)
	return ok
}
