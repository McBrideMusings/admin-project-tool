//go:build darwin

package luahost

// Long-running Apple log/console primitives, covering the --console mode of
// device launch that LaunchOnDevice (apple_devices_darwin.go) explicitly
// does not cover. These start a child process and hand back its combined
// stdout/stderr as an io.ReadCloser plus a "still running" channel; feeding
// that stream to a terminal/log is the caller's job via
// internal/streams.OutputPump (admin-3rgp.12 composes it in
// internal/kinds), per this repo's "the output pump lives in exactly one
// place" rule. Nothing here duplicates that loop.

import (
	"encoding/json"
	"io"
	"os/exec"
	"regexp"
)

// StreamMacLog starts `log stream --predicate <predicate> --level debug
// --color always` (or, when simID is non-empty, the same command run inside
// the simulator via `xcrun simctl spawn <simID> log stream ...`) and
// returns it already started. done closes once the child exits (from
// either end — Kill() or a natural exit), so a caller can poll "is it still
// running" without racing exec.Cmd's internal state.
func StreamMacLog(predicate, simID string) (cmd *exec.Cmd, out io.ReadCloser, done <-chan struct{}, err error) {
	if simID != "" {
		cmd = exec.Command("xcrun", "simctl", "spawn", simID, "log", "stream",
			"--predicate", predicate, "--level", "debug", "--color", "always")
	} else {
		cmd = exec.Command("log", "stream",
			"--predicate", predicate, "--level", "debug", "--color", "always")
	}
	return startAttached(cmd)
}

// SweepOrphanLogStreams kills any `log stream` process whose argv mentions
// subsystem, guarding against a stream orphaned by a prior hard-killed dev
// session (terminal closed, SIGKILL, OOM — issue #353). Best-effort: a
// failing pkill (nothing matched) is not an error.
func SweepOrphanLogStreams(subsystem string) {
	if subsystem == "" {
		return
	}
	_ = exec.Command("pkill", "-f", "log stream.*"+regexp.QuoteMeta(subsystem)).Run()
}

// StartDeviceConsole launches bundleID on deviceID with --console attached,
// terminating any existing instance first, forwarding env as launch
// environment variables (OS_ACTIVITY_DT_MODE=YES un-redacts the app's own
// os_log output). This is the mode
// LaunchOnDevice's doc comment names as deliberately not covered there: it
// blocks on the child's stdout until the app exits, which belongs to an
// output pump, not a one-shot host function.
func StartDeviceConsole(deviceID, bundleID string, env map[string]string) (cmd *exec.Cmd, out io.ReadCloser, done <-chan struct{}, err error) {
	args := []string{"devicectl", "device", "process", "launch", "--console",
		"--terminate-existing", "--device", deviceID}
	if len(env) > 0 {
		b, jerr := json.Marshal(env)
		if jerr != nil {
			return nil, nil, nil, jerr
		}
		args = append(args, "-e", string(b))
	}
	args = append(args, bundleID)
	cmd = exec.Command("xcrun", args...)
	return startAttached(cmd)
}

// startAttached starts cmd with its stdout+stderr merged into the returned
// pipe, and a done channel that closes once Wait() returns (which also
// closes the write end of the pipe, so a caller reading with bufio.Scanner
// sees a clean EOF rather than a blocked read).
func startAttached(cmd *exec.Cmd) (*exec.Cmd, io.ReadCloser, <-chan struct{}, error) {
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return nil, nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = pw.Close()
		close(done)
	}()
	return cmd, pr, done, nil
}
