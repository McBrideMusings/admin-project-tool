// Package simpool is admin's one door to the machine-wide simulator pool
// (the `sim` tool, acquire/release). The pool caps how many simulators boot
// at once across every session on the machine; admin's own simctl calls run
// inside the binary, where no outside guard can see them, so every path that
// picks a simulator takes its device from here.
//
// The pool tool is an optional host dependency. Its path comes from
// ADMIN_SIM_TOOL; unset, Acquire returns a nil lease and callers behave as
// they did before the pool existed. Status says which, for `admin check`.
package simpool

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// EnvTool names the environment variable holding the absolute path of the
// pool tool.
const EnvTool = "ADMIN_SIM_TOOL"

var udidRe = regexp.MustCompile(`^[A-F0-9]{8}-[A-F0-9]{4}-[A-F0-9]{4}-[A-F0-9]{4}-[A-F0-9]{12}$`)

// ErrBusy is returned when the pool had no free slot within its wait window
// (the tool's exit 75).
var ErrBusy = errors.New("simulator pool busy")

// Tool reports the configured pool tool: its path, and whether it is set and
// executable.
func Tool() (path string, usable bool) {
	path = os.Getenv(EnvTool)
	if path == "" {
		return "", false
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		return path, false
	}
	return path, true
}

// Status is the one line `admin check` prints about the pool dependency.
func Status() string {
	path, ok := Tool()
	switch {
	case path == "":
		return "sim pool: not configured (set " + EnvTool + " to the pool tool's path) — simulators are picked without a lease"
	case !ok:
		return "sim pool: missing — " + EnvTool + "=" + path + " is not an executable file — simulators are picked without a lease"
	default:
		return "sim pool: found at " + path
	}
}

// Lease is a simulator device held from the pool.
type Lease struct {
	// UDID is the leased device's identifier.
	UDID string

	tool, device, runtime string
	once                  sync.Once
	err                   error
}

var (
	heldMu sync.Mutex
	held   *Lease
)

// Acquire takes a device from the pool, waiting for a free slot. device and
// runtime are `simctl` identifiers for a non-default device (an iPad, an
// Apple TV); both empty takes the pool's default. It returns (nil, nil) when
// no usable pool tool is configured, so callers fall back to their own pick.
// A second Acquire in one process returns the lease already held, or an error
// when it asks for a different device or runtime.
func Acquire(device, runtime string) (*Lease, error) {
	tool, ok := Tool()
	if !ok {
		return nil, nil
	}
	heldMu.Lock()
	defer heldMu.Unlock()
	if held != nil {
		if held.device != device || held.runtime != runtime {
			return nil, fmt.Errorf("this process already holds simulator %s (device %q, runtime %q); release it before leasing another kind", held.UDID, held.device, held.runtime)
		}
		return held, nil
	}
	args := []string{"acquire"}
	if device != "" {
		args = append(args, "--device", device)
	}
	if runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	cmd := exec.Command(tool, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	msg := strings.TrimSpace(stderr.String())
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 75 {
			return nil, fmt.Errorf("%w: %s", ErrBusy, msg)
		}
		return nil, fmt.Errorf("sim acquire: %v: %s", err, msg)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	udid := strings.TrimSpace(lines[len(lines)-1])
	if !udidRe.MatchString(udid) {
		return nil, fmt.Errorf("sim acquire printed %q, not a device UDID", stdout.String())
	}
	held = &Lease{UDID: udid, tool: tool, device: device, runtime: runtime}
	return held, nil
}

// Release gives the device back; the tool shuts it down. Safe to call more
// than once and on a nil lease.
func (l *Lease) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		out, err := exec.Command(l.tool, "release").CombinedOutput()
		if err != nil {
			l.err = fmt.Errorf("sim release: %v: %s", err, strings.TrimSpace(string(out)))
		}
		heldMu.Lock()
		if held == l {
			held = nil
		}
		heldMu.Unlock()
	})
	return l.err
}

// ReleaseHeld releases the lease this process holds, if any.
func ReleaseHeld() error {
	heldMu.Lock()
	l := held
	heldMu.Unlock()
	return l.Release()
}
