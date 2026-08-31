// Package ports holds port-probing and preflight helpers — the Go port of
// the subset of admin_lib/ports.py a dev-loop kind (vite-dev today; docker-dev
// and the kill-port/nuke-dev kinds later) needs before it can bind a port.
//
// Full parity with admin_lib/ports.py's holder-naming chain (the /proc walk,
// fuser fallback) and admin_lib/ports_nuke.py's project-wide orphan sweep
// (kill_stale_supervisors, the four-pass escalating nuke) is explicitly out
// of scope here — that is admin-3rgp.15's job ("Port ports, process cleanup
// and the remaining kinds": kill-port, nuke-dev, dev-precheck). This package
// is the minimal, real (not stubbed) primitive vite-dev's own preflight
// check needs: is the port free, and if not, who's on it and can it be
// cleared. .15 is expected to extend this package (PortHolders in
// particular) rather than re-derive it.
package ports

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/sys/unix"
)

// probe is one bind attempt PortFree makes.
type probe struct{ network, host string }

// probes mirrors admin_lib/ports.py::port_free's _PORT_PROBE_ADDRS: wildcard
// and loopback on both IP stacks. A listener bound to a specific address
// (Vite on 127.0.0.1) only collides with a bind to that same address, so the
// wildcard probe alone would miss it.
var probes = []probe{
	{"tcp4", "0.0.0.0"},
	{"tcp4", "127.0.0.1"},
	{"tcp6", "::"},
	{"tcp6", "::1"},
}

// reuseAddrControl sets SO_REUSEADDR on the probe socket before bind — the
// same reason admin_lib/ports.py::port_free sets it: a connection socket
// left in TIME_WAIT by a just-exited server must read as free, or a
// back-to-back `admin dev` restart would be refused for 30-60s even though
// nothing is listening.
func reuseAddrControl(_, _ string, c syscall.RawConn) error {
	var sockErr error
	err := c.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	})
	if err != nil {
		return err
	}
	return sockErr
}

// PortFree is the Go port of admin_lib/ports.py::port_free: an authoritative
// bind() probe, not a holder lookup — a bind touches no process file tables,
// so it can't hang the way `lsof` can on a stale/disconnected network mount.
// Only EADDRINUSE counts as busy; a probe that fails for any other reason
// (an unsupported family, IPv6 disabled) is skipped, not read as busy.
func PortFree(port int) bool {
	lc := net.ListenConfig{Control: reuseAddrControl}
	portStr := strconv.Itoa(port)
	for _, p := range probes {
		addr := net.JoinHostPort(p.host, portStr)
		ln, err := lc.Listen(context.Background(), p.network, addr)
		if err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				return false
			}
			continue
		}
		ln.Close()
	}
	return true
}

// Holder is one process found listening on a probed port.
type Holder struct {
	PID     int
	Command string
}

// PortHolders names the process(es) listening on port via the `lsof` CLI —
// the macOS-first half of admin_lib/ports.py::port_holders (the /proc walk
// and `fuser` fallback are Linux-only paths this package does not carry;
// admin-3rgp.15 owns filling those in for parity on Linux). Returns nil,
// never an error, when lsof is absent or times out — a caller must treat
// "no holders named" as "could not identify", not "port is free" (PortFree
// is the only source of truth for that).
func PortHolders(port int) []Holder {
	lsofPath, err := exec.LookPath("lsof")
	if err != nil {
		return nil
	}
	out, err := exec.Command(lsofPath, "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return nil
	}
	var holders []Holder
	lines := strings.Split(string(out), "\n")
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		holders = append(holders, Holder{PID: pid, Command: fields[0]})
	}
	return holders
}

// PreflightPort is the Go port of admin_lib/ports.py::preflight_port: ensure
// port is free, prompting (on a TTY) or force-killing (non-TTY, matching
// Python's `sys.stdin.isatty()` branch) any holder found. Returns true if
// the port is free or was cleared, false if the user declined, no holder
// could be named for a busy port, or the port is still busy after the kill
// attempt.
func PreflightPort(port int, label string) bool {
	if PortFree(port) {
		return true
	}

	tag := ""
	if label != "" {
		tag = " (" + label + ")"
	}
	holders := PortHolders(port)
	fmt.Fprintf(os.Stderr, "\nPort %d%s is already in use:\n", port, tag)
	if len(holders) == 0 {
		fmt.Fprintln(os.Stderr, "  (could not identify the holder — `lsof` is missing, timed out,")
		fmt.Fprintln(os.Stderr, "   or found nothing; refusing to proceed)")
		fmt.Fprintf(os.Stderr, "Free port %d%s manually and retry.\n", port, tag)
		return false
	}
	for _, h := range holders {
		fmt.Fprintf(os.Stderr, "  pid %6d: %s\n", h.PID, h.Command)
	}

	proceed := true
	if isatty.IsTerminal(os.Stdin.Fd()) {
		fmt.Fprintf(os.Stderr, "Kill and continue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		proceed = strings.EqualFold(strings.TrimSpace(line), "y")
		if !proceed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return false
		}
	} else {
		fmt.Fprintf(os.Stderr, "Stdin is not a TTY — force-killing port %d%s holders.\n", port, tag)
	}

	for _, h := range holders {
		_ = syscall.Kill(h.PID, syscall.SIGTERM)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if PortFree(port) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, h := range holders {
		_ = syscall.Kill(h.PID, syscall.SIGKILL)
	}
	time.Sleep(200 * time.Millisecond)
	return PortFree(port)
}

// WaitForPort is the Go port of admin_lib/web_dev.py::_wait_for_port: poll
// until port accepts a TCP connection or timeout elapses. Returns false (and
// leaves it to the caller to warn) on timeout rather than erroring — matches
// Python's "continuing anyway" behaviour.
func WaitForPort(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}
