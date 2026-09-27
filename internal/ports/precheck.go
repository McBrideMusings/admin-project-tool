// precheck.go implements a live-vs-orphan dev
// precheck: nuke.go kills whatever matches; a precheck must not. The point
// is to reap the corpse of a previous dev run (its terminal is gone) while
// never killing the session a human is sitting in right now. The two are
// told apart by walking a process's ancestry looking for a real controlling
// terminal: a chain that crosses a TTY belongs to a live interactive
// session (abort — tell the user to Ctrl-C it); a chain that reaches init
// with no TTY is an orphan (reap it).
package ports

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ttyEntry is one process's {ppid, hasTTY} pair.
type ttyEntry struct {
	ppid   int
	hasTTY bool
}

// procTTYMap returns {pid: (ppid, hasTTY)} for every visible process, via
// /proc/<pid>/stat on Linux, or `ps -Ao pid=,ppid=,tty=` otherwise (macOS/BSD).
func procTTYMap() map[int]ttyEntry {
	if info, err := os.Stat("/proc"); err == nil && info.IsDir() {
		if m := procTTYMapProc(); m != nil {
			return m
		}
	}
	return procTTYMapPS()
}

func procTTYMapProc() map[int]ttyEntry {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	out := map[int]ttyEntry{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		s := string(data)
		// comm (field 2) is wrapped in parens and may itself contain spaces
		// or parens; split on the LAST ')' so the numeric fields after it
		// line up regardless. After it: [0]=state [1]=ppid [2]=pgrp
		// [3]=session [4]=tty_nr.
		rparen := strings.LastIndexByte(s, ')')
		if rparen < 0 {
			continue
		}
		fields := strings.Fields(s[rparen+1:])
		if len(fields) < 5 {
			continue
		}
		ppid, err1 := strconv.Atoi(fields[1])
		ttyNr, err2 := strconv.Atoi(fields[4])
		if err1 != nil || err2 != nil {
			continue
		}
		out[pid] = ttyEntry{ppid: ppid, hasTTY: ttyNr != 0}
	}
	return out
}

func procTTYMapPS() map[int]ttyEntry {
	psPath, err := exec.LookPath("ps")
	if err != nil {
		return map[int]ttyEntry{}
	}
	out, err := exec.Command(psPath, "-Ao", "pid=,ppid=,tty=").Output()
	if err != nil {
		return map[int]ttyEntry{}
	}
	result := map[int]ttyEntry{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		tty := ""
		if len(fields) > 2 {
			tty = fields[2]
		}
		hasTTY := tty != "" && tty != "??" && tty != "-"
		result[pid] = ttyEntry{ppid: ppid, hasTTY: hasTTY}
	}
	return result
}

// HasTTYAncestor reports whether pid or any ancestor up to init owns a
// controlling terminal — the live-vs-orphan test. tmap is a snapshot from
// procTTYMap; nil triggers building one on demand. A chain that hits a pid
// absent from the snapshot (a race, or a permission gap) is treated as
// orphaned: better to reap a corpse than refuse to start dev.
func HasTTYAncestor(pid int, tmap map[int]ttyEntry) bool {
	if tmap == nil {
		tmap = procTTYMap()
	}
	p := pid
	seen := map[int]bool{}
	for depth := 0; p > 1 && depth < 32 && !seen[p]; depth++ {
		seen[p] = true
		entry, ok := tmap[p]
		if !ok {
			return false
		}
		if entry.hasTTY {
			return true
		}
		p = entry.ppid
	}
	return false
}

// argvInProject reports whether any argv token of p is a path at or under
// projectRoot. Narrows a bare-runtime match to this project's own scripts.
func argvInProject(p Proc, projectRoot string) bool {
	root := absPath(projectRoot)
	prefix := root + string(os.PathSeparator)
	for _, tok := range p.Argv {
		if tok == root || strings.HasPrefix(tok, prefix) {
			return true
		}
	}
	return false
}

// DevPrecheckOptions is DevPrecheck's parameter set.
type DevPrecheckOptions struct {
	Runtimes       []string // default: bun, node, vite, esbuild
	AcceptRuntimes []string
	Containers     []string
	ReapPidfiles   []string
	Label          string // default "dev"
	Timeout        time.Duration
}

func defaultPrecheckRuntimes() []string { return []string{"bun", "node", "vite", "esbuild"} }

// DevPrecheckResult reports what DevPrecheck found and did.
type DevPrecheckResult struct {
	Reaped            []int
	Live              []int
	RunningContainers []string
	Aborted           bool
}

// DevPrecheck refuses to start a dev loop when a live one already owns this
// project. Every process rooted in projectRoot is classified as live (its ancestry crosses
// a real TTY) or orphan (its chain reaches init with no TTY); orphans are
// reaped silently, a live session or a running container aborts.
func DevPrecheck(projectRoot string, opts DevPrecheckOptions) DevPrecheckResult {
	root := absPath(projectRoot)
	var result DevPrecheckResult

	for _, name := range opts.Containers {
		if containerRunning(name) {
			result.RunningContainers = append(result.RunningContainers, name)
			result.Aborted = true
		}
	}

	runtimes := opts.Runtimes
	if runtimes == nil {
		runtimes = defaultPrecheckRuntimes()
	}
	runtimeSet := map[string]bool{}
	for _, r := range runtimes {
		runtimeSet[r] = true
	}
	acceptSet := map[string]bool{}
	for _, r := range opts.AcceptRuntimes {
		acceptSet[r] = true
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	selfPID := os.Getpid()
	var tmap map[int]ttyEntry
	tmapBuilt := false

	for _, p := range ListProcesses() {
		if p.PID == selfPID {
			continue
		}
		base := baseName(p.Argv0)
		if !runtimeSet[base] && !acceptSet[base] {
			continue
		}
		cwd := p.Cwd
		if cwd == "" {
			cwd = PidCwd(p.PID, timeout)
		}
		if !cwdInRoot(cwd, root) {
			continue
		}
		if runtimeSet[base] && !argvInProject(p, root) {
			continue
		}
		if !tmapBuilt {
			tmap = procTTYMap()
			tmapBuilt = true
		}
		if HasTTYAncestor(p.PID, tmap) {
			result.Live = append(result.Live, p.PID)
		} else {
			result.Reaped = append(result.Reaped, p.PID)
		}
	}

	if len(result.Reaped) > 0 {
		KillPIDs(result.Reaped, 3*time.Second)
		for _, path := range opts.ReapPidfiles {
			_ = os.Remove(path)
		}
	}
	if len(result.Live) > 0 {
		result.Aborted = true
	}
	return result
}
