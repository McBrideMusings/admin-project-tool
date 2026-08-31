// process.go is the Go port of admin_lib/ports_nuke.py's process-listing and
// scoping primitives: enumerate every visible process, find one's cwd and
// controlling-terminal ancestry, and decide whether a process belongs to a
// given project. nuke.go and precheck.go build the actual cleanup/precheck
// behaviour on top of these.
package ports

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Proc is one visible process — the Go port of the dict
// admin_lib/ports_nuke.py::list_processes yields.
type Proc struct {
	PID     int
	Argv    []string
	Cmdline string
	Argv0   string
	Cwd     string // "" means unknown; the ps fallback can't report it.
}

// ListProcesses returns every visible process except this one — the Go port
// of admin_lib/ports_nuke.py::list_processes: a /proc walk when /proc exists
// (Linux), a `ps` fallback otherwise (macOS/BSD). Order is not significant
// to any caller.
func ListProcesses() []Proc {
	if info, err := os.Stat("/proc"); err == nil && info.IsDir() {
		return listProcessesProc()
	}
	return listProcessesPS()
}

func listProcessesProc() []Proc {
	selfPID := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var procs []Proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == selfPID {
			continue
		}
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil || len(raw) == 0 {
			continue
		}
		var argv []string
		for _, tok := range strings.Split(string(raw), "\x00") {
			if tok != "" {
				argv = append(argv, tok)
			}
		}
		if len(argv) == 0 {
			continue
		}
		cwd, _ := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
		procs = append(procs, Proc{
			PID: pid, Argv: argv, Cmdline: strings.Join(argv, " "),
			Argv0: argv[0], Cwd: cwd,
		})
	}
	return procs
}

func listProcessesPS() []Proc {
	psPath, err := exec.LookPath("ps")
	if err != nil {
		return nil
	}
	out, err := exec.Command(psPath, "-Ao", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	selfPID := os.Getpid()
	var procs []Proc
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		if pid == selfPID {
			continue
		}
		cmdline := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		if cmdline == "" {
			continue
		}
		argv := strings.Fields(cmdline)
		procs = append(procs, Proc{
			PID: pid, Argv: argv, Cmdline: cmdline, Argv0: argv[0], Cwd: "",
		})
	}
	return procs
}

// PidCwd returns pid's working directory, or "" when it can't be determined
// — the Go port of admin_lib/ports_nuke.py::_pid_cwd. /proc first (Linux);
// per-pid `lsof -d cwd` otherwise (macOS/BSD), scoped to one fd so it can't
// hang the way a full lsof scan can on a wedged network mount.
func PidCwd(pid int, timeout time.Duration) string {
	if cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil {
		return cwd
	}
	lsofPath, err := exec.LookPath("lsof")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, lsofPath, "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(ln, "n") {
			return ln[1:]
		}
	}
	return ""
}

// cwdInRoot reports whether cwd is projectRoot or a directory beneath it —
// the Go port of admin_lib/ports_nuke.py::_cwd_in_root. An empty cwd (the
// caller couldn't resolve it) is never inside the root.
func cwdInRoot(cwd, projectRoot string) bool {
	if cwd == "" {
		return false
	}
	return cwd == projectRoot || strings.HasPrefix(cwd, projectRoot+string(os.PathSeparator))
}

// pidPpidMap returns {pid: ppid} for every visible process via one
// `ps -Ao pid=,ppid=` call — the Go port of
// admin_lib/ports_nuke.py::_pid_ppid_map.
func pidPpidMap() map[int]int {
	psPath, err := exec.LookPath("ps")
	if err != nil {
		return nil
	}
	out, err := exec.Command(psPath, "-Ao", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	m := map[int]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		m[pid] = ppid
	}
	return m
}

// ChildPIDs returns every descendant of pid, deepest first — the Go port of
// admin_lib/ports_nuke.py::child_pids. Deepest-first so a caller killing the
// list in order reaps grandchildren before the parent that would otherwise
// reparent them to init. Children of one parent are visited in sorted-pid
// order, for deterministic output; a real process tree can't cycle, so a
// depth guard (matching Python's `depth > 20`) is the only defense needed
// against a bogus `ps` snapshot.
func ChildPIDs(pid int) []int {
	ppidMap := pidPpidMap()
	tree := map[int][]int{}
	for child, parent := range ppidMap {
		tree[parent] = append(tree[parent], child)
	}
	for p := range tree {
		sort.Ints(tree[p])
	}

	var out []int
	var walk func(p, depth int)
	walk = func(p, depth int) {
		if depth > 20 {
			return
		}
		for _, c := range tree[p] {
			if c == p {
				continue
			}
			walk(c, depth+1)
			out = append(out, c)
		}
	}
	walk(pid, 0)
	return out
}

// absPath is filepath.Abs with the error swallowed — every caller here
// already has a best-effort fallback (use the input unchanged), matching
// Python's bare os.path.abspath (which never raises for a string input).
func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
