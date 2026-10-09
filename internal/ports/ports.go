// Package ports holds port-probing and preflight helpers a dev-loop kind
// (vite-dev today; docker-dev and the kill-port/nuke-dev kinds later) needs
// before it can bind a port.
package ports

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/sys/unix"
)

// psParent returns pid's parent pid and the parent's command line, via `ps`
// — portable across macOS and Linux, unlike reading /proc/<pid>/stat for the
// ppid field (which this package doesn't otherwise need, since holder
// detection already has the pid from /proc/net/tcp, fuser, netstat or lsof).
// Returns (0, "") when ps is unavailable, times out, or pid has no parent.
func psParent(pid int) (int, string) {
	psPath, err := exec.LookPath("ps")
	if err != nil {
		return 0, ""
	}
	out, err := exec.Command(psPath, "-p", strconv.Itoa(pid), "-o", "ppid=").Output()
	if err != nil {
		return 0, ""
	}
	ppidStr := strings.TrimSpace(string(out))
	if ppidStr == "" {
		return 0, ""
	}
	ppid, err := strconv.Atoi(ppidStr)
	if err != nil {
		return 0, ""
	}
	return ppid, psCommand(ppid)
}

// psCommand returns pid's full command line via `ps`, or "" when ps is
// unavailable, times out, or pid is gone.
func psCommand(pid int) string {
	psPath, err := exec.LookPath("ps")
	if err != nil {
		return ""
	}
	out, err := exec.Command(psPath, "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// probe is one bind attempt PortFree makes.
type probe struct{ network, host string }

// probes lists the addresses PortFree binds against: wildcard
// and loopback on both IP stacks. A listener bound to a specific address
// (Vite on 127.0.0.1) only collides with a bind to that same address, so the
// wildcard probe alone would miss it.
var probes = []probe{
	{"tcp4", "0.0.0.0"},
	{"tcp4", "127.0.0.1"},
	{"tcp6", "::"},
	{"tcp6", "::1"},
}

// reuseAddrControl sets SO_REUSEADDR on the probe socket before bind: a
// connection socket left in TIME_WAIT by a just-exited server must read as
// free, or a back-to-back `admin dev` restart would be refused for 30-60s
// even though nothing is listening.
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

// PortFree does an authoritative
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
	PID           int
	Command       string
	ParentPID     int    // 0 means unknown
	ParentCommand string // "" means unknown
}

// PortHolders names the process(es) listening on port, via a detection
// chain: /proc/net/tcp{,6} + /proc/<pid>/fd (Linux, no external deps), then
// `fuser`, then `lsof` (macOS/BSD). Every pid found is enriched with its
// parent's pid/command via `ps`. Returns nil, never an
// error, when every detection method is unavailable or times out — a caller
// must treat "no holders named" as "could not identify", not "port is free"
// (PortFree is the only source of truth for that).
func PortHolders(port int) []Holder {
	pids := portHolderPIDs(port, 2*time.Second)
	var holders []Holder
	seen := map[int]bool{}
	for _, pid := range pids {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		ppid, pcmd := psParent(pid)
		cmd := psCommand(pid)
		if cmd == "" {
			cmd = "?"
		}
		holders = append(holders, Holder{PID: pid, Command: cmd, ParentPID: ppid, ParentCommand: pcmd})
	}
	return holders
}

// portHolderPIDs finds listener pids via /proc/net/tcp{,6}, then `fuser`,
// then `netstat -anv` (macOS), then `lsof`.
func portHolderPIDs(port int, timeout time.Duration) []int {
	if pids := pidsForSocketInodes(procNetListeners(port)); len(pids) > 0 {
		return pids
	}

	if fuserPath, err := exec.LookPath("fuser"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		out, _ := exec.CommandContext(ctx, fuserPath, "-n", "tcp", strconv.Itoa(port)).Output()
		cancel()
		var pids []int
		for _, tok := range strings.Fields(string(out)) {
			if pid, err := strconv.Atoi(tok); err == nil {
				pids = append(pids, pid)
			}
		}
		if len(pids) > 0 {
			return pids
		}
	}

	if netstatPath, err := exec.LookPath("netstat"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		out, _ := exec.CommandContext(ctx, netstatPath, "-anv", "-p", "tcp").Output()
		cancel()
		if pids, ok := netstatListenerPIDs(string(out), port); ok {
			return pids
		}
	}

	if lsofPath, err := exec.LookPath("lsof"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		out, _ := exec.CommandContext(ctx, lsofPath, "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN").Output()
		cancel()
		var pids []int
		lines := strings.Split(string(out), "\n")
		for _, line := range lines[1:] { // skip header
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			if pid, err := strconv.Atoi(fields[1]); err == nil {
				pids = append(pids, pid)
			}
		}
		return pids
	}

	return nil
}

// netstatListenerPIDs parses macOS `netstat -anv -p tcp` output for the pids
// listening on port. macOS netstat reads the kernel's socket table and names
// each socket's owner in a `process:pid` column, so it answers in
// milliseconds; lsof instead walks every process's file table and blocks
// for minutes behind any process wedged in the kernel (a hung SMB mount is
// enough). The process name can contain spaces ("Code Helper:812"), so the
// pid is the first field after the state that ends in `:<digits>`, not a
// fixed column. ok reports whether the output carried the `process:pid`
// header: when it did, an empty pid list is the complete answer (nothing
// listens) and lsof is not consulted. Linux netstat has no such column, so
// ok is false there and the caller falls through.
func netstatListenerPIDs(out string, port int) (pids []int, ok bool) {
	suffix := "." + strconv.Itoa(port)
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "process:pid") {
			ok = true
		}
		fields := strings.Fields(line)
		if len(fields) < 7 || !strings.HasPrefix(fields[0], "tcp") ||
			fields[5] != "LISTEN" || !strings.HasSuffix(fields[3], suffix) {
			continue
		}
		for _, f := range fields[6:] {
			i := strings.LastIndexByte(f, ':')
			if i < 0 {
				continue
			}
			if pid, err := strconv.Atoi(f[i+1:]); err == nil && pid > 0 {
				if !slices.Contains(pids, pid) {
					pids = append(pids, pid)
				}
				break
			}
		}
	}
	return pids, ok
}

// procNetListeners parses /proc/net/tcp{,6} for sockets in LISTEN state on
// port, returning their inode strings. Absent on any non-Linux host (and
// most containers without /proc/net/tcp), in which case it returns nil and
// portHolderPIDs falls through to fuser/lsof.
func procNetListeners(port int) []string {
	portHex := fmt.Sprintf("%04X", port)
	var inodes []string
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Scan() // skip header
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 10 {
				continue
			}
			if fields[3] != "0A" { // 0A == TCP_LISTEN
				continue
			}
			if !strings.HasSuffix(fields[1], ":"+portHex) {
				continue
			}
			inodes = append(inodes, fields[9])
		}
		f.Close()
	}
	return inodes
}

// pidsForSocketInodes walks /proc/<pid>/fd once looking for any socket fd
// matching inodes.
func pidsForSocketInodes(inodes []string) []int {
	if len(inodes) == 0 {
		return nil
	}
	targets := map[string]bool{}
	for _, ino := range inodes {
		targets["socket:["+ino+"]"] = true
	}
	selfPID := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == selfPID {
			continue
		}
		fdDir := fmt.Sprintf("/proc/%d/fd", pid)
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(fdDir + "/" + fd.Name())
			if err != nil {
				continue
			}
			if targets[target] {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}

// PreflightPort ensures port is free, prompting (on a TTY) or force-killing
// (non-TTY) any holder found. Returns true if
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
		if h.ParentPID != 0 {
			parentCmd := h.ParentCommand
			if parentCmd == "" {
				parentCmd = "?"
			}
			fmt.Fprintf(os.Stderr, "          parent pid %d: %s\n", h.ParentPID, parentCmd)
		}
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

	pids := make([]int, len(holders))
	for i, h := range holders {
		pids[i] = h.PID
	}
	KillPIDs(pids, 3*time.Second)
	// Re-probe with bind, not pid-liveness: a child can respawn, or the
	// socket can linger after the pid dies — the bind probe is the real
	// answer.
	return PortFree(port)
}

// WaitForPort polls until port accepts a TCP connection or timeout elapses.
// Returns false (and leaves it to the caller to warn) on timeout rather
// than erroring, so the caller can continue anyway.
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
