// nuke.go implements bulletproof dev-process cleanup: port + pidfile + argv
// sweep, plus the interactive single-port kill and orphaned-supervisor
// reaping it builds on top of process.go.
package ports

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
)

// devCmdlineMarkers and devRuntimes are heuristics for "this looks like a
// dev process" used when lsof can't map a
// port to a pid (macOS + a stale mount) and a holder must be identified by
// what it is instead of what socket it holds.
var devCmdlineMarkers = []string{
	"cargo watch", "cargo-watch", "watchexec", "nodemon",
	"vite", "esbuild", "npm run dev", "bun run dev", "tsc -w", "tsc --watch",
}

var devRuntimes = map[string]bool{"node": true, "bun": true, "deno": true, "python": true, "python3": true}

// isProjectDevProc reports whether p is one of this project's dev
// processes. Deliberately narrow: a bare language runtime only counts when the script
// it runs lives inside projectRoot, so unrelated tooling merely running with
// a cwd inside the project (MCP servers, npx packages, editors) is never
// swept up.
func isProjectDevProc(p Proc, projectRoot string) bool {
	for _, m := range devCmdlineMarkers {
		if strings.Contains(p.Cmdline, m) {
			return true
		}
	}
	if strings.Contains(p.Cmdline, "target/debug/") || strings.Contains(p.Cmdline, "target/release/") {
		return true
	}
	base := baseName(p.Argv0)
	if devRuntimes[base] {
		prefix := projectRoot + string(os.PathSeparator)
		for _, tok := range argvTail(p.Argv) {
			if strings.HasPrefix(tok, prefix) {
				return true
			}
		}
	}
	return false
}

func baseName(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return p
	}
	return p[i+1:]
}

func argvTail(argv []string) []string {
	if len(argv) <= 1 {
		return nil
	}
	return argv[1:]
}

// ProjectDevHolders returns holder-shaped entries for this project's dev
// processes: the fallback for naming a port's holder when lsof can't map
// port→pid.
// Every dev process rooted in projectRoot is treated as a holder regardless
// of which port is asked about, since the degraded path can't tell which
// port each process listens on; scoping is by per-pid cwd, so unrelated
// processes are never touched.
func ProjectDevHolders(projectRoot string, timeout time.Duration) []Holder {
	root := absPath(projectRoot)
	var holders []Holder
	for _, p := range ListProcesses() {
		if !isProjectDevProc(p, root) {
			continue
		}
		cwd := p.Cwd
		if cwd == "" {
			cwd = PidCwd(p.PID, timeout)
		}
		if !cwdInRoot(cwd, root) {
			continue
		}
		ppid, pcmd := psParent(p.PID)
		cmd := p.Cmdline
		if cmd == "" {
			cmd = "?"
		}
		holders = append(holders, Holder{PID: p.PID, Command: cmd, ParentPID: ppid, ParentCommand: pcmd})
	}
	return holders
}

// defaultSupervisorMarkers is the default `markers` set for
// KillStaleSupervisors — both the shell-wrapper form ("cargo watch", with a space) and the bare
// binary form ("cargo-watch", with a hyphen), since /proc reports whichever
// one the shell exec'd into or died leaving behind.
var defaultSupervisorMarkers = []string{
	"cargo watch", "cargo-watch",
	"watchexec",
	"nodemon",
	"tsc -w", "tsc --watch",
}

// KillStaleSupervisors kills orphaned file-watcher / dev-supervisor
// processes inside projectRoot and returns how many pids were signalled.
// markers defaults to defaultSupervisorMarkers when nil. Match is project-scoped:
// cmdline must contain a marker AND cwd must be inside projectRoot.
func KillStaleSupervisors(projectRoot string, markers []string) int {
	if markers == nil {
		markers = defaultSupervisorMarkers
	}
	root := absPath(projectRoot)
	selfPID := os.Getpid()
	var stale []int
	for _, p := range ListProcesses() {
		if p.PID == selfPID {
			continue
		}
		matched := false
		for _, m := range markers {
			if strings.Contains(p.Cmdline, m) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		cwd := p.Cwd
		if cwd == "" {
			cwd = PidCwd(p.PID, 2*time.Second)
		}
		if !cwdInRoot(cwd, root) {
			continue
		}
		stale = append(stale, p.PID)
	}
	if len(stale) == 0 {
		return 0
	}
	KillPIDs(stale, 3*time.Second)
	return len(stale)
}

// KillPortInteractive shows port's holders, prompts (on a TTY; force-kills
// otherwise), then kills. Returns true if the port
// ended up cleared (including "nothing was listening").
func KillPortInteractive(port int) bool {
	holders := PortHolders(port)
	if len(holders) == 0 {
		fmt.Printf("No process on port %d — nothing to kill.\n", port)
		return true
	}

	fmt.Printf("\nPort %d is in use:\n", port)
	for _, h := range holders {
		fmt.Printf("  pid %6d: %s\n", h.PID, h.Command)
		if h.ParentPID != 0 {
			parentCmd := h.ParentCommand
			if parentCmd == "" {
				parentCmd = "?"
			}
			fmt.Printf("          parent pid %d: %s\n", h.ParentPID, parentCmd)
		}
	}
	fmt.Println()

	pids := make([]int, len(holders))
	for i, h := range holders {
		pids[i] = h.PID
	}

	if !isatty.IsTerminal(os.Stdin.Fd()) {
		fmt.Fprintf(os.Stderr, "Stdin is not a TTY — force-killing port %d holders.\n", port)
	} else {
		fmt.Print("Kill it? [Enter to kill, Ctrl+C to abort] ")
		reader := bufio.NewReader(os.Stdin)
		if _, err := reader.ReadString('\n'); err != nil {
			fmt.Println()
			return false
		}
	}

	results := KillPIDs(pids, 3*time.Second)
	if !AnyAlive(results) {
		fmt.Printf("Port %d cleared.\n", port)
		return true
	}
	fmt.Fprintf(os.Stderr, "Failed to clear port %d.\n", port)
	return false
}

// PidfileSpec is one `[dev_kill].pidfiles[]` entry.
type PidfileSpec struct {
	Path   string
	Format string // "int" (default) or "json"
	Key    string // JSON key, default "pid"
}

// PatternSpec is one `[dev_kill].patterns[]` entry.
type PatternSpec struct {
	Match  string
	Regex  bool
	Global bool // skip the project-root cwd scope
}

// PortSpec is one `[dev_kill].ports[]` entry — a port plus an optional label
// (used only for log messages).
type PortSpec struct {
	Port  int
	Label string
}

// NukeDevOptions is NukeDevProcesses' parameter set.
type NukeDevOptions struct {
	Ports             []PortSpec
	Pidfiles          []PidfileSpec
	JSFiles           []string
	WebSubdirs        []string
	Runtimes          []string
	WebArgvMarkers    []string
	SupervisorMarkers []string // nil means defaultSupervisorMarkers
	Patterns          []PatternSpec
}

// NukeReport is what NukeDevProcesses actually did — the acceptance
// criterion's "reports what it killed" made concrete.
type NukeReport struct {
	Killed    []KillResult // every pid signalled, across every pass
	StillHeld []int        // ports still occupied after the verify pass
}

func defaultRuntimes() []string { return []string{"bun", "node", "python", "python3"} }
func defaultWebMarkers() []string {
	return []string{"vite", "esbuild", "npm run dev"}
}

// NukeDevProcesses runs four kill passes plus a verify pass — see the inline
// comment above each pass below for why it exists.
func NukeDevProcesses(projectRoot string, opts NukeDevOptions) NukeReport {
	root := absPath(projectRoot)
	runtimes := opts.Runtimes
	if runtimes == nil {
		runtimes = defaultRuntimes()
	}
	webMarkers := opts.WebArgvMarkers
	if webMarkers == nil {
		webMarkers = defaultWebMarkers()
	}

	jsPaths := map[string]bool{}
	for _, name := range opts.JSFiles {
		jsPaths[joinPath(root, name)] = true
	}
	var webDirs []string
	for _, sub := range opts.WebSubdirs {
		webDirs = append(webDirs, joinPath(root, sub))
	}

	killed := map[int]bool{}
	var report NukeReport
	selfPID := os.Getpid()

	killSet := func(pids []int) {
		var toKill []int
		seen := map[int]bool{}
		for _, p := range pids {
			if p == 0 || p == selfPID || killed[p] || seen[p] {
				continue
			}
			seen[p] = true
			toKill = append(toKill, p)
		}
		if len(toKill) == 0 {
			return
		}
		results := KillPIDs(toKill, 3*time.Second)
		report.Killed = append(report.Killed, results...)
		for _, p := range toKill {
			killed[p] = true
		}
	}

	// 1. Ports — plus the bun/node/npm/python supervisor parent of each
	// holder.
	for _, ps := range opts.Ports {
		holders := portHoldersWithRetry(ps.Port, root, portHolderRetryBudget)
		var pids []int
		for _, h := range holders {
			pids = append(pids, h.PID)
			if h.ParentPID != 0 {
				pc := strings.ToLower(h.ParentCommand)
				if strings.Contains(pc, "bun") || strings.Contains(pc, "node") ||
					strings.Contains(pc, "npm") || strings.Contains(pc, "python") {
					pids = append(pids, h.ParentPID)
				}
			}
		}
		killSet(pids)
	}

	// 2. Pidfiles.
	for _, spec := range opts.Pidfiles {
		if spec.Path == "" {
			continue
		}
		pid := readPidfile(spec)
		if pid != 0 && pid != selfPID && !killed[pid] {
			if pidAlive(pid) {
				killSet(ChildPIDs(pid))
				killSet([]int{pid})
			}
		}
		_ = os.Remove(spec.Path)
	}

	// 3. argv/cwd sweep.
	runtimeSet := map[string]bool{}
	for _, r := range runtimes {
		runtimeSet[r] = true
	}
	var stragglers []int
	for _, p := range ListProcesses() {
		if killed[p.PID] {
			continue
		}
		base := baseName(p.Argv0)
		if runtimeSet[base] {
			matched := false
			for _, tok := range p.Argv {
				if jsPaths[tok] {
					matched = true
					break
				}
			}
			if matched {
				stragglers = append(stragglers, p.PID)
				continue
			}
		}
		// argv0 prefix catches a binary invoked by its own path. It misses the
		// common shape, though: `node <dir>/node_modules/.bin/vite` has argv0
		// "node" and the interesting path in argv[1], so check every argument.
		hasArgvPrefix := false
		for _, d := range webDirs {
			prefix := d + string(os.PathSeparator)
			if strings.HasPrefix(p.Argv0, prefix) {
				hasArgvPrefix = true
				break
			}
			for _, tok := range p.Argv {
				if strings.HasPrefix(tok, prefix) {
					hasArgvPrefix = true
					break
				}
			}
			if hasArgvPrefix {
				break
			}
		}
		if hasArgvPrefix {
			stragglers = append(stragglers, p.PID)
			continue
		}
		// The cwd branch needs PidCwd, exactly as the port and pidfile passes
		// above do: `ps` cannot report a working directory, so Process.Cwd is
		// always "" on macOS (process.go's ps fallback sets it empty), and
		// comparing "" against a web dir made this whole branch dead there —
		// a `npm run dev` vite survived every teardown.
		//
		// The marker test comes FIRST even though it used to be innermost:
		// PidCwd shells out per process, so asking it about every process on
		// the machine turned a sub-second sweep into minutes. Almost nothing
		// matches a web marker, so this ordering pays for lsof only on the
		// handful of candidates that could possibly qualify.
		if len(webDirs) > 0 && matchesAny(p.Cmdline, webMarkers) {
			cwd := p.Cwd
			if cwd == "" {
				cwd = PidCwd(p.PID, 2*time.Second)
			}
			for _, d := range webDirs {
				if cwd == d || strings.HasPrefix(cwd, d+string(os.PathSeparator)) {
					stragglers = append(stragglers, p.PID)
					break
				}
			}
		}
	}
	killSet(stragglers)

	// 3b. Declared argv patterns.
	for _, spec := range opts.Patterns {
		if spec.Match == "" {
			continue
		}
		var rx *regexp.Regexp
		if spec.Regex {
			var err error
			rx, err = regexp.Compile(spec.Match)
			if err != nil {
				continue // manifest validation's job to reject; skip at runtime
			}
		}
		var hits []int
		for _, p := range ListProcesses() {
			if killed[p.PID] {
				continue
			}
			var match bool
			if rx != nil {
				match = rx.MatchString(p.Cmdline)
			} else {
				match = strings.Contains(p.Cmdline, spec.Match)
			}
			if !match {
				continue
			}
			if !spec.Global {
				cwd := p.Cwd
				if cwd == "" {
					cwd = PidCwd(p.PID, 2*time.Second)
				}
				if !cwdInRoot(cwd, root) {
					continue
				}
			}
			hits = append(hits, p.PID)
		}
		killSet(hits)
	}

	// 4. File-watcher / supervisor orphans.
	KillStaleSupervisors(root, opts.SupervisorMarkers)

	// 5. Verify every declared port is actually free; kill whatever still
	// holds one. A respawn loop can rebind a port within a second of pass 1
	// clearing it, under a pid no earlier pass saw.
	for _, ps := range opts.Ports {
		for i := 0; i < 2; i++ {
			if PortFree(ps.Port) {
				break
			}
			holders := portHoldersWithRetry(ps.Port, root, portHolderRetryBudget)
			if len(holders) == 0 {
				break
			}
			var pids []int
			for _, h := range holders {
				pids = append(pids, h.PID)
			}
			killSet(pids)
		}
		if !PortFree(ps.Port) {
			report.StillHeld = append(report.StillHeld, ps.Port)
		}
	}

	return report
}

func readPidfile(spec PidfileSpec) int {
	data, err := os.ReadFile(spec.Path)
	if err != nil {
		return 0
	}
	if spec.Format == "json" {
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			return 0
		}
		key := spec.Key
		if key == "" {
			key = "pid"
		}
		switch v := obj[key].(type) {
		case float64:
			return int(v)
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return 0
			}
			return n
		default:
			return 0
		}
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return n
}

func joinPath(root, name string) string {
	if name == "" {
		return root
	}
	if strings.HasPrefix(name, "/") {
		return name
	}
	return root + string(os.PathSeparator) + name
}

// containerRunning reports whether a docker container named exactly name is
// up. A missing docker binary or any error is treated as not-running: a
// broken/absent docker must never block starting dev.
func containerRunning(name string) bool {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return false
	}
	out, err := exec.Command(dockerPath, "ps", "--filter", "name=^"+name+"$", "--format", "{{.Names}}").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// portHolderRetryBudget bounds portHoldersWithRetry's polling. 1s is enough
// slack for lsof to catch up with a socket the kernel already reports bound
// (see portHoldersWithRetry's doc comment) without meaningfully slowing a
// NukeDevProcesses call on a port that turns out to have no holder at all —
// that path never enters the retry loop, since it never has PortFree(port)
// == false with zero holders found.
const portHolderRetryBudget = 1 * time.Second

// portHoldersWithRetry discovers the holder(s) of a port the caller already
// knows (or suspects) is busy, retrying with a short poll-and-backoff
// instead of a single lookup.
//
// The single-shot version of this call is where
// TestNukeDevProcessesReportsPortHolderKills' flake lived: PortFree does a
// raw bind()-probe, which sees a freshly-listen()ing socket the instant the
// kernel accepts it, while PortHolders/ProjectDevHolders both shell out to
// lsof, which walks its own kernel/proc-table snapshot on its own schedule
// and can momentarily still report zero holders for a socket a bind probe
// already sees as busy — worse under load or parallel test execution, which
// matches the observed "flaky solo, passes inside the full suite" shape.
// Pass 1 calling PortHolders exactly once meant that race silently skipped
// the port's kill for the rest of NukeDevProcesses, surfacing only in pass
// 5's own two-attempt, no-delay verify loop — not enough of a gap for lsof
// to catch up either.
//
// portHoldersWithRetry polls every 50ms, up to budget, stopping as soon as
// a holder turns up, PortFree(port) reports the port is actually free after
// all, or the budget is spent — never a blind sleep, and never entered at
// all for a port PortFree already sees as free.
func portHoldersWithRetry(port int, projectRoot string, budget time.Duration) []Holder {
	deadline := time.Now().Add(budget)
	for {
		holders := PortHolders(port)
		if len(holders) == 0 && !PortFree(port) {
			holders = ProjectDevHolders(projectRoot, 2*time.Second)
		}
		if len(holders) > 0 || PortFree(port) || time.Now().After(deadline) {
			return holders
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// matchesAny reports whether s contains any of the substrings.
func matchesAny(s string, substrings []string) bool {
	for _, sub := range substrings {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
