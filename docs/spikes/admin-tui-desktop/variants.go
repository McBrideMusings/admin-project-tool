// Admin Dashboard — Detail-right (admin-3rgp.19).
//
// harness.go supplies the picker, the tweaks, the footer bar, the geometry
// assertion and the dump. This file is the design.
//
//	admin prototype admin-tui-desktop     ⌥s screen · ⌥e data · ⌥r tracker
//	go run . -dump -dir .                 write the ten reference frames
//
// The Work tab is the only thing this prototype designs. Repo, the tracker tab
// and Config are NOT redrawn here — they are rendered by calling
// internal/tui.Frame, repo-dash's own exported headless seam, so the prototype
// shows what those tabs actually look like rather than a reconstruction that
// drifts. That headless seam is the one everything visual renders through.
//
// Which tracker tabs exist is a property of the repo, not a setting: repo-dash
// derives it from the manifest's declared `tracker` key and `uses_pull_requests`
// (internal/tui/model.go's trackers/hasTracker/wantsPRs, read by visibleTabs). A
// repo declaring tracker = ["beads"] gets a Beads tab; one declaring ["github"]
// gets Issues instead; a repo declaring uses_pull_requests = true gets PRs
// regardless of tracker. So the tracker tweak swaps which repo the embedded
// frame is rendered against — each of the three built by fixtures.go to
// declare exactly one of those shapes, because that is the only honest way to
// see all three.
package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/tui"
)

const (
	DumpWidth  = 96
	DumpHeight = 30
)

var Axes = []Axis{
	{Key: "screen", Label: "Screen", Hotkey: "s", Values: []string{
		// "commands" is FIRST on purpose: the first Values entry is what
		// launching the prototype lands on, and the Commands list is the
		// Work tab's default surface. Reordering renames no file —
		// stateName() keys dumps by value, not position — it only changes
		// the launch default and the ⌥s cycle order.
		"commands", "commands-running", "commands-empty",
		// The Work tab's live states, then the rest of the reference frames.
		"commands-workflow-selected", "repo", "issues", "issues-selected",
		"dispatch-sheet", "swarm", "config", "running", "no-manifest",
		// Every other surface repo-dash draws — see INVENTORY.md. Each is the real
		// frame, reached by driving the same keys a person would press.
		//
		// "repo", "no-manifest", "repo-commits", "repo-filter" and
		// "repo-new-worktree" still have to be dumped — set() (harness.go)
		// indexes into this list to reach them at all, interactively or from a
		// test — but their .txt output is gitignored and not part of the
		// reference set: see docs/spikes/admin-tui-desktop/live-frames/README.md.
		"repo-commits", "repo-filter", "repo-new-worktree",
		"tracker-detail", "tracker-graph", "tracker-blocking", "tracker-tree", "tracker-filter",
		"config-local",
		// Hosted-process surfaces. commands-targets-open (the multi-target
		// submenu open) was dropped: every submenu is always open now, so it
		// was a duplicate of "commands" the moment collapsing went away.
		"hosted", "hosted-empty", "hosted-quit",
	}},
	// Both live-only. Dumping either would multiply every frame name and break
	// the paths .20 and .28 cite by.
	{Key: "data", Label: "Data", Values: []string{"populated", "empty"}, Hotkey: "e", NoDump: true},
	{Key: "tracker", Label: "Tracker", Values: []string{"beads", "github", "prs"}, Hotkey: "r", NoDump: true},
}

var (
	dim  = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))
	cy   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(6))
	gr   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2))
	yl   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(3))
	rd   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(1))
	mg   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(5))
	bo   = lipgloss.NewStyle().Bold(true)
	pill = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(0)).Background(lipgloss.ANSIColor(6)).Bold(true)
)

// Settled: a right-aligned word per row — "hosted" for a child process this
// dashboard owns, the pane id for a detached session it merely watches.
//
// Two directions were tried and rejected. "Glyph" (⇱ ours / ⇲ elsewhere) spent
// one column instead of six but made the reader learn two symbols for a
// distinction that changes what a key does. "Sections" grouped the list
// instead, and could not work: a hosted process is folded INTO its command's
// row, so the commands group cannot be titled by running-ness without lying
// about the idle rows in it, and un-folding live work back out is the
// arrangement admin-3rgp.20 deliberately replaced.
var Variants = []Variant{{
	Name:    "Detail-right",
	Caption: "interleaved rows, a third of the screen on whatever the cursor is on",
	Render:  render,
	Key:     onKey,
	Reset: func() {
		selDelta = 0
		peeked = map[string]bool{}
		resetCursor()
		resetLiveRows()
	},
	OnDump: func() { Blocking = true },
}}

// ---- the real tabs, rendered by repo-dash itself ----

type repoSrc struct{ root, nwo, branch string }

// Digit keys index the drawn tabs (internal/tui/model.go's switchTabAt). Every
// screen below that doesn't override the `tracker` axis renders against
// trackerRepo("beads") (fixtures.go), a temp-dir repo this prototype builds
// itself rather than one of three live checkouts on the developer's machine
// (admin-p55x) — so its five real drawn tabs are 1 Commands, 2 Workers, 3 Repo,
// 4 the tracker (Beads here), 5 Config. Each of the three fixtures is built to
// draw exactly that five-tab shape (fixtures.go's axisManifest), which is what
// keeps these constants safe to hardcode: they index a tab SET the fixture
// guarantees, not one inferred from a repo that could change shape under the
// prototype. Everything past the tab is the same key a person presses — see
// INVENTORY.md for the full map.
const (
	keyRepo    = "3"
	keyTracker = "4"
)

// Config is reached by stepping right from the tracker tab rather than by its
// digit, so this path does not depend on how many digits the footer offers.
var keyConfig = []string{keyTracker, "right"}

// embedded maps a screen to the keys that reach it in repo-dash. A screen absent
// here is one the prototype draws itself.
var embedded = map[string][]string{
	"repo":              {keyRepo},
	"no-manifest":       {keyRepo},
	"repo-commits":      {keyRepo, "tab"},
	"repo-filter":       {keyRepo, "/"},
	"repo-new-worktree": {keyRepo, "w"},

	"issues":           {keyTracker},
	"issues-selected":  {keyTracker, "down"},
	"tracker-detail":   {keyTracker, "d"},
	"tracker-graph":    {keyTracker, "d", "right"},
	"tracker-blocking": {keyTracker, "d", "right", "right"},
	"tracker-tree":     {keyTracker, "g"},
	"tracker-filter":   {keyTracker, "/"},

	"config":       keyConfig,
	"config-local": append(append([]string{}, keyConfig...), "tab"),
}

var (
	frameMu    sync.Mutex
	frameCache = map[string][]string{}
	warming    = map[string]bool{}
	warmWG     sync.WaitGroup
	// Blocking is set by the dump, which must not capture a placeholder: a
	// reference frame that says "loading…" is worse than no frame at all.
	Blocking bool
)

// warm loads one repo ONCE and renders every embedded screen from it. A Frame
// call per screen re-runs Init and pays its own settle wait every time —
// fourteen screens over three repos was ~40 seconds of waiting spread across
// the session, and it landed on whichever tab you pressed next. Frames
// snapshots them all from one load instead.
//
// Runs off the render path so the first paint is instant; screens fill in as
// their repo lands, and the harness's tick repaints when they do.
func warm(src repoSrc, w, h int) {
	wk := fmt.Sprintf("%s|%d|%d", src.root, w, h)
	frameMu.Lock()
	if warming[wk] {
		frameMu.Unlock()
		return
	}
	warming[wk] = true
	frameMu.Unlock()

	warmWG.Add(1)
	run := func() {
		defer warmWG.Done()
		names := make([]string, 0, len(embedded))
		for n := range embedded {
			names = append(names, n)
		}
		sort.Strings(names) // deterministic, so the dump is too
		seqs := make([][]string, len(names))
		for i, n := range names {
			seqs[i] = embedded[n]
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		m := tui.New(ctx, src.root, src.nwo, src.branch, "main")
		// Settle is a backstop now (frame.go), not the normal wait — Frames
		// returns as soon as the load is quiescent, so a generous ceiling
		// costs nothing on the common path and only matters for a genuinely
		// slow git call under real contention (admin-3rgp.44).
		outs := tui.Frames(ctx, m, tui.FrameOptions{
			Width: w, Height: h + 3, Settle: 8 * time.Second,
		}, seqs)

		frameMu.Lock()
		for i, n := range names {
			lines := strings.Split(outs[i], "\n")
			body := []string{dim.Render("   (no frame)")}
			if len(lines) > 3 {
				body = lines[3:]
			}
			rows := make([]string, h)
			for j := range rows {
				rows[j] = at(body, j)
			}
			frameCache[key(src, n, w, h)] = rows
		}
		frameMu.Unlock()
	}
	if Blocking {
		run()
		return
	}
	go run()
}

func key(src repoSrc, screen string, w, h int) string {
	return fmt.Sprintf("%s|%s|%d|%d", src.root, screen, w, h)
}

// realTab returns a warmed frame, or a placeholder while its repo is still
// loading. It never blocks the render: a prototype that stalls a second on every
// tab press cannot be judged, because what you are judging is how it feels.
//
// The frame includes repo-dash's own rule AND footer — in a modal state the
// footer IS the mode (view.go:500): the new-worktree prompt, the filter query
// and its match count, the delete confirm all live there.
func realTab(src repoSrc, screen string, w, h int) []string {
	frameMu.Lock()
	rows, ok := frameCache[key(src, screen, w, h)]
	frameMu.Unlock()
	if ok {
		return rows
	}
	warm(src, w, h)
	if Blocking {
		warmWG.Wait()
		frameMu.Lock()
		rows, ok = frameCache[key(src, screen, w, h)]
		frameMu.Unlock()
		if ok {
			return rows
		}
	}
	out := make([]string, h)
	for i := range out {
		out[i] = ""
	}
	out[0] = dim.Render("   loading " + src.nwo + "…")
	return out
}

// ---- work tab: the design ----

type screenSpec struct {
	tab int
	// cmdSel seeds the combined cursor onto a Commands row by its index in
	// selectableCommands(); -1 leaves liveSel to place it.
	cmdSel  int
	noMan   bool
	sheet   bool
	liveSel int
	running bool
	// hosted is the terminal view over a Command's hosted pty — the dashboard
	// runs it on a pty it owns rather than in a herdr pane, and this is where
	// that output is read. Full width: a third of the screen truncates every
	// log line to "14:22:09 [INFO] test: ======…", which is unreadable.
	hosted      bool
	hostedEmpty bool
}

// The tabs, in the order the real dashboard draws them (internal/tui/model.go's
// tabNames): Commands and Workers first, then the three embedded tabs.
const (
	tabCommands = iota
	tabWorkers
	tabRepo
	tabTracker
	tabConfig
)

// cmdSel seeds the Commands tab's cursor by its index in selectableCommands(), stored +1
// so the zero value means "unset". liveSel is a Workers-tab row by its index in
// liveSectionRows(); -1 leaves the cursor on the Commands tab's first row.
var screens = map[string]screenSpec{
	// 34 is retire-merged's first target in selectableCommands() order, +1.
	"commands-workflow-selected": {tab: tabCommands, liveSel: -1, cmdSel: 34},
	"running":                    {tab: tabWorkers, liveSel: 1, running: true}, // admin-3rgp.7 — worker, waiting
	"repo":                       {tab: tabRepo},
	"issues":                     {tab: tabTracker},
	"issues-selected":            {tab: tabTracker},
	"dispatch-sheet":             {tab: tabTracker, sheet: true},
	"swarm":                      {tab: tabWorkers, liveSel: 2}, // admin-3rgp.33 — worker, failed
	"config":                     {tab: tabConfig},
	"no-manifest":                {tab: tabRepo, noMan: true},

	// Hosted-process surfaces (admin-3rgp.61/.63). liveSel here indexes
	// liveRows, the row the attached view shows (attachedFor).
	"hosted":       {tab: tabWorkers, liveSel: 1, hosted: true},
	"hosted-empty": {tab: tabWorkers, liveSel: 1, hosted: true, hostedEmpty: true},
	"hosted-quit":  {tab: tabCommands, liveSel: -1, sheet: true, running: true},

	// The Commands surface — see the block above render() for the fixture and
	// the interaction. Both render through renderWorkBody, never hostedBody.
	"commands": {tab: tabCommands, liveSel: -1},
	// The cursor on a command that IS running: the row carries its own state
	// word and the band previews the running process, not a description of it.
	// 9 is "dev cf" in selectableCommands() order, stored +1.
	"commands-running": {tab: tabCommands, liveSel: -1, cmdSel: 9},
	"commands-empty":   {tab: tabCommands, liveSel: -1},

	// The embedded modes. tab drives only the prototype's own tab bar and
	// footer; the body comes from repo-dash via the key sequence in `embedded`.
	"repo-commits":      {tab: tabRepo},
	"repo-filter":       {tab: tabRepo},
	"repo-new-worktree": {tab: tabRepo},
	"tracker-detail":    {tab: tabTracker},
	"tracker-graph":     {tab: tabTracker},
	"tracker-blocking":  {tab: tabTracker},
	"tracker-tree":      {tab: tabTracker},
	"tracker-filter":    {tab: tabTracker},
	"config-local":      {tab: tabConfig},
}

var selDelta int

var workTabs = []string{"commands", "swarm", "repo", "issues", "config"}

func onKey(key string, s State, set func(axis, value string)) bool {
	cur := s.Get("screen")
	if cur == "" {
		cur = "commands"
	}
	// Tab switching is resolved BEFORE the Work tab's own keys. It used to be
	// after, so ⇥ / ←→ were swallowed by the tab's own handler (which binds
	// neither) and the Work tab became a screen you could not leave.
	switch key {
	case "tab", "right", "l", "shift+tab", "left", "h":
	default:
		if isWorkTab(cur) {
			if onWorkTabKey(key, cur, set) {
				return true
			}
		}
	}
	// "p" returns to the Commands surface from any other Work-tab screen. Never
	// on the hosted pty view (its footer promises typing goes to the process)
	// or under an overlay.
	if key == "p" {
		if sp := screens[cur]; sp.tab <= tabWorkers && !sp.hosted && !sp.sheet {
			set("screen", "commands")
			return true
		}
	}
	idx := 0
	for i, t := range workTabs {
		if screens[t].tab == screens[cur].tab {
			idx = i
		}
	}
	switch key {
	// A multi-target command's submenu is always open (no collapse state left
	// to toggle), so → and ← now mean only one thing everywhere: switch tabs.
	case "tab", "right", "l":
		set("screen", workTabs[(idx+1)%len(workTabs)])
		selDelta = 0
		enterTab(workTabs[(idx+1)%len(workTabs)])
		return true
	case "shift+tab", "left", "h":
		set("screen", workTabs[(idx-1+len(workTabs))%len(workTabs)])
		selDelta = 0
		enterTab(workTabs[(idx-1+len(workTabs))%len(workTabs)])
		return true
	case "down", "j":
		selDelta++
		return true
	case "up", "k":
		selDelta--
		return true
	case "enter":
		// The dispatch sheet is the prototype's own overlay: repo-dash has no
		// such thing yet, which is why it is drawn here and not by Frame.
		switch cur {
		case "issues", "issues-selected":
			set("screen", "dispatch-sheet")
		case "dispatch-sheet":
			set("screen", "issues")
		case "hosted-quit":
			set("screen", "commands")
		}
		return true

	// q and esc are the same key here, and what they mean depends on depth:
	// attached, they detach; in the list, they quit. So leaving a process you
	// are watching is two presses of whichever one your hands reach for, and
	// neither can kill a dev loop by reflex from inside its own output.
	case "q", "esc":
		switch cur {
		case "dispatch-sheet":
			set("screen", "issues")
		case "hosted", "hosted-empty":
			// Detach returns to the tab the attached row lives on.
			back := "commands"
			if attachedRow != nil && isWorkerRow(attachedRow) {
				back = "swarm"
			}
			attachedRow = nil
			set("screen", back)
		case "hosted-quit":
			set("screen", "commands")
		default:
			set("screen", "hosted-quit")
		}
		return true
	case "m":
		if cur == "no-manifest" {
			set("screen", "repo")
		} else {
			set("screen", "no-manifest")
		}
		selDelta = 0
		return true
	}
	return false
}

func wrap(base, n int) int {
	if n <= 0 {
		return 0
	}
	return ((base+selDelta)%n + n) % n
}

// liveRow is one live thing. `hosted` is the distinction that matters and the
// one the old "pane N" column got wrong: a hosted row is a CHILD PROCESS on a
// pty this dashboard owns — it dies when the dashboard quits, and its output
// is readable in-app. A detached row is a herdr pane running its own agent
// session: not a child, not ours to kill on quit, reachable only by focusing
// that pane. The two need different verbs and different lifetimes, so the list
// has to say which it is at a glance rather than implying "pane" for both.
type liveRow struct {
	// cmd/target are the resolved invocation this row IS, so a running
	// command is the same thing in the Commands list and in Running rather
	// than two rows that happen to share a name.
	state, id, doing, cmd, target string
	hosted                        bool
	pane                          string // herdr pane id — empty for a hosted child

	// lastCmd is the hover-preview spike's addition: the last thing actually
	// sent to this row, shown as subtext beneath it. For a worker that's the
	// command that dispatched it; for a hosted command, the resolved shell
	// line.
	lastCmd string
	// hasRing mirrors livework.Entry.Output being non-nil: only a hosted
	// child (Via == "") drains its own bytes into a local ring buffer this
	// process can read TODAY. A detached Worker's output lives in its own
	// herdr pane — this process never sees it, only the one-line `doing` it
	// self-reports.
	//
	// The three workers below are drawn with hasRing: true anyway, at
	// pierce's request ("seeing live auto-tail output on workers too seems
	// like a good idea") — this is the spike asking a question the real
	// registry can't yet answer, not a claim that it already can. Making
	// that real means herdr streaming a pane's bytes back to admin (a
	// second drain path alongside the hosted-pty Ring, or the pane pushing
	// into one directly) — a capability gap worth its own ticket if this
	// direction is picked, not something this prototype should paper over.
	hasRing bool
	tail    []string
	// logPath and urls are what auto-tail's header shows INSTEAD of the raw
	// child footer text once a real one exists — the same fields
	// internal/frame's pinned footer already resolves per command
	// (commandLogPath, kinds.FrameURLs), not new information invented here.
	logPath string
	urls    []string
	// elapsed, branch and worktree are what the live band shows for a row.
	// branch/worktree are a worker's alone — a Claude session runs in its own
	// checkout, and where that is is the first thing you want when it stalls.
	elapsed  string
	branch   string
	worktree string
}

// liveRowsSeed / liveRows: liveRows now grows at runtime — dispatching a
// palette entry materializes it here (dispatchPalette), and `k` removes it
// (killLiveRow) — so, same as paletteMRU/paletteMRUSeed, a dump has to
// restore the seed every frame or an earlier interactive dispatch would
// leak into it.
var liveRowsSeed = []liveRow{
	{state: "running", id: "admin-3rgp.36", doing: "writing internal/lua/host.go", pane: "pane 3",
		elapsed: "18m", branch: "feat/admin-3rgp.36-lua-host",
		worktree: "/Users/dev/.worktrees/admin-project-tool/admin-3rgp.36",
		logPath:  root + "/.admin/logs/worker-admin-3rgp.36.log",
		lastCmd:  "review admin-3rgp.36 (workflow: review-changes)",
		hasRing:  true, tail: []string{
			"  reading internal/tui/work.go",
			"  reading internal/tui/workview.go",
			"  reading internal/livework/livework.go",
			"",
			"  Adding hover-preview support to the detail pane —",
			"  Entry.Output already drains a hosted pty's bytes",
			"  into a Ring, so the preview reuses that rather",
			"  than inventing a second capture path.",
			"",
			"  writing internal/lua/host.go",
		}},
	{state: "waiting", id: "admin-3rgp.7", doing: "needs input: root command name", pane: "pane 4",
		elapsed: "6m", branch: "feat/admin-3rgp.7-root-command",
		worktree: "/Users/dev/.worktrees/admin-project-tool/admin-3rgp.7",
		logPath:  root + "/.admin/logs/worker-admin-3rgp.7.log",
		lastCmd:  "implement admin-3rgp.7", hasRing: true, tail: []string{
			"  the manifest declares no top-level command name —",
			"  every existing project sets one (admin.toml's",
			"  [identity] or the repo directory name), and this",
			"  fixture repo has neither.",
			"",
			"  what should the root command be called? this",
			"  decides the CLI verb (\"admin <name>\") and the",
			"  dashboard's header line.",
			"",
			"  needs input: root command name",
		}},
	{state: "failed", id: "admin-3rgp.33", doing: "go test ./internal/detect FAIL", pane: "pane 5",
		elapsed: "2m", branch: "fix/admin-3rgp.33-detect",
		worktree: "/Users/dev/.worktrees/admin-project-tool/admin-3rgp.33",
		logPath:  root + "/.admin/logs/worker-admin-3rgp.33.log",
		lastCmd:  "implement admin-3rgp.33", hasRing: true, tail: []string{
			"=== RUN   TestDetectHugo",
			"--- PASS: TestDetectHugo (0.00s)",
			"=== RUN   TestDetectReactNative",
			"--- FAIL: TestDetectReactNative (0.01s)",
			"    detect_test.go:88: expected archetype",
			"    \"react-native\", got \"\"",
			"FAIL",
			"FAIL	github.com/.../internal/detect	0.014s",
			"",
			"go test ./internal/detect FAIL",
		}},
	// A Command's live work is hosted by the dashboard on a pty it owns, not
	// dispatched to a pane — so `where` says "hosted", and quitting the
	// dashboard kills it. A Worker above still runs in its own pane and
	// outlives the dashboard on purpose; the column is what tells them apart.
	{state: "running", id: "dev cf", doing: "vitepress dev · localhost:5173", cmd: "dev", target: "cf", hosted: true, elapsed: "4m12s",
		lastCmd: "cd apps && bunx wrangler dev --target cf", hasRing: true,
		logPath: root + "/.admin/logs/dev-cf.log", urls: []string{"http://localhost:5173"},
		tail: []string{
			"",
			"  vitepress v1.3.4",
			"",
			"  ➜  Local:   http://localhost:5173/",
			"  ➜  Network: use --host to expose",
			"",
			"14:22:14 [vite] hmr update /guide/index.md",
			"14:22:41 [vite] hmr update /reference/api.md",
			"14:23:02 [vite] page reload /architecture/design.md",
			"14:23:19 [vite] hmr update /guide/index.md",
		}},
	{state: "waiting", id: "test gate", doing: "Enter a number, or q to cancel:", cmd: "test", target: "gate", hosted: true, elapsed: "12s",
		lastCmd: "cd apps && bunx wrangler test --target gate", hasRing: true,
		logPath: root + "/.admin/logs/test-gate.log",
		tail: []string{
			"tests/test_frame.py ..........................      [ 47%]",
			"tests/test_guards.py .................              [ 61%]",
			"tests/test_interp.py ......................         [ 78%]",
			"14:22:31|DEBUG|test|cache hit: archetypes/apple.toml",
			"14:22:33|WARN|test|tests/test_apple.py::TestIconRender slow (4.1s)",
			"tests/test_manifest.py ...........                  [ 88%]",
			"tests/test_validate.py .....",
			"",
			"multi-target: pick a target",
			"  1) all   2) py   3) shell",
		}},
}

var liveRows = append([]liveRow{}, liveRowsSeed...)

func resetLiveRows() {
	liveRows = append([]liveRow{}, liveRowsSeed...)
}

var liveStyle = map[string]lipgloss.Style{
	"running": gr, "waiting": yl, "done": dim, "failed": rd,
}

func mark(sel bool, s string) string {
	if sel {
		return cy.Render("◆ ") + s
	}
	return dim.Render("◇ ") + s
}

// peeked tracks which hosted rows (by command name) are expanded inline
// under "inline-peek" — several at once, since each is independent. Live
// state outside State, so Reset must clear it or a dump inherits whatever
// the last keypress left open (in practice never dumped: hover is NoDump).
var peeked = map[string]bool{}

// liveWhere renders the activity plus whatever the current direction uses to
// say hosted-child vs detached-pane.
func liveWhere(lr *liveRow, dw int) string {
	w, st := whereCol(lr)
	return dim.Render(Pad(trunc(lr.doing, dw), dw)+" ") + st.Render(w)
}

// whereLabel is the detail pane's long form, which never abbreviates.
func whereLabel(lr *liveRow) string {
	if lr.hosted {
		return cy.Render("hosted here") + dim.Render(" · dies when admin quits")
	}
	return mg.Render(lr.pane) + dim.Render(" · detached, outlives admin")
}

// ---- how a row says hosted-child vs detached-pane ----
//
// It matters because the two are not the same kind of thing — a hosted
// child dies with the dashboard and its output opens in-app; a detached pane
// outlives it and can only be focused — so the verbs, the quit confirm and the
// footer all branch on it. A list that shows the wrong one gets a dev loop
// killed by reflex, or an agent session abandoned.

// whereCol is the settled treatment: a right-aligned word, the incumbent's
// shape but honest — "hosted" for a child, the pane id for a detached one, never
// "pane N" for something that is not a pane.
func whereCol(lr *liveRow) (string, lipgloss.Style) {
	if lr.hosted {
		return "hosted", cy
	}
	return lr.pane, mg
}

// ---- hosted process: the terminal view over the dashboard's own pty ----

var hostedOut = []string{
	"14:22:07|INFO|test|.venv/bin/pytest -q",
	"14:22:09|INFO|test|======================= test session starts =======================",
	"14:22:09|INFO|test|platform darwin -- Python 3.13.1, pytest-8.3.4",
	"14:22:09|INFO|test|rootdir: .",
	"14:22:09|INFO|test|collected 221 items",
	"14:22:11|INFO|test|tests/test_archetypes.py ....................       [ 12%]",
	"14:22:14|INFO|test|tests/test_apple.py .............                   [ 28%]",
	"14:22:19|INFO|test|tests/test_frame.py ..........................      [ 47%]",
	"14:22:24|INFO|test|tests/test_guards.py .................              [ 61%]",
	"14:22:28|INFO|test|tests/test_interp.py ......................         [ 78%]",
	"14:22:31|DEBUG|test|cache hit: archetypes/apple.toml",
	"14:22:33|WARN|test|tests/test_apple.py::TestIconRender slow (4.1s)",
	"14:22:35|INFO|test|tests/test_manifest.py ...........                  [ 88%]",
	"14:22:37|INFO|test|tests/test_validate.py .....",
	"14:22:39|INFO|test|multi-target: pick a target",
	"14:22:39|INFO|test|  1) all   2) py   3) shell",
}

var lvlStyle = map[string]lipgloss.Style{"INFO": cy, "DEBUG": dim, "WARN": yl, "ERROR": rd}

// outLine colors one log line the way internal/tui/log_format.go does: dim
// clock, level word in its own color, scope tag its own hue, message plain.
func outLine(l string, w int) string {
	p := strings.SplitN(l, "|", 4)
	if len(p) < 4 {
		return trunc(l, w)
	}
	st, ok := lvlStyle[p[1]]
	if !ok {
		st = cy
	}
	head := dim.Render(p[0]) + " " + st.Render("["+p[1]+"]") + " " + mg.Render(p[2]) + " "
	return head + trunc(p[3], maxInt(1, w-VW(p[0])-len(p[1])-VW(p[2])-5))
}

// hostedBody is the full-width terminal view. Full width is the decision, not a
// default: at a third of the screen every line dies at "[INFO] test: ======…".
// The child's own pinned footer renders inside it, because from the child's
// point of view the pty IS its terminal — that is what keeps one footer
// implementation instead of two.
// attachCrumb is the row the tab bar gives up while attached: what you are
// attached to, and nothing else. The log path does not share this row — at 80
// columns the identity leaves it about 21 cells, which cuts a log file's name
// to "command-test:counter…" with no directory, date or extension. It gets the
// "log" row under the crumb instead (hostedBody), with the full width.
func attachCrumb(lr *liveRow, w int) string {
	if lr == nil {
		return Pad(" "+dim.Render("attached"), w)
	}
	return Pad(" "+cy.Render("attached")+dim.Render("  ·  ")+
		liveStyle[lr.state].Render(liveGlyph(lr))+" "+bo.Render(lr.id)+
		dim.Render("  ·  "+lr.state+"  ·  "+lr.elapsed), w)
}

func hostedBody(w, h int, empty bool, lr *liveRow) []string {
	// The header states what the process is actually doing, and the output below
	// has to agree — so it is read off the row actually attached to, not written
	// out here. Attaching to a different row has to show that row or the view is
	// a picture of one process wearing another's name.
	// Identity is on the breadcrumb row above (attachCrumb). Under it go the
	// same field rows the docked band shows for this row (fieldRows) — log and
	// urls for a command, the five worker rows for a worker — at the top rather
	// than under the output: a child's output is what scrolls, so anything
	// pinned beneath it either floats in dead space when the output is short or
	// gets pushed off when it is long. Then the rule and the child's own screen.
	var out []string
	if lr != nil {
		out = append(out, fieldRows(lr, w)...)
	}
	out = append(out, dim.Render(strings.Repeat("╌", w)))
	if empty {
		return append(out, "", " "+dim.Render("waiting for output…"))
	}
	body := hostedOut
	if lr != nil && len(lr.tail) > 0 {
		body = lr.tail
	}
	// The child owns every row from the rule to the floor. Its newest line sits
	// at the bottom, where a terminal puts it, so the output does not float in
	// the middle of a tall screen with dead space beneath it.
	avail := maxInt(h-len(out), 1)
	if len(body) > avail {
		body = body[len(body)-avail:]
	}
	for i := 0; i < avail-len(body); i++ {
		out = append(out, Pad("", w))
	}
	for _, l := range body {
		out = append(out, " "+outLine(l, w-2))
	}
	return out
}

// tailLines returns the last n lines of tail, padded to exactly n lines so
// the pane never jitters as output scrolls in — the same "fixed height" rule
// the split-pane box below relies on for the opposite reason (an empty box
// that doesn't move once something starts running).
// tailLines returns the last n lines of tail, padded to exactly n lines so the
// band never jitters as output scrolls in. Lines in the pipe-delimited fixture
// form ("14:22:33|WARN|test|…") go through outLine, the same colorizer the
// hosted terminal view uses — a raw "|"-separated line in the band was the
// fixture leaking its own storage format onto the screen.
func tailLines(tail []string, n, w int) []string {
	if n < 1 {
		n = 1
	}
	t := tail
	if len(t) > n {
		t = t[len(t)-n:]
	}
	out := make([]string, n)
	pad := n - len(t)
	for i := range out {
		if i < pad {
			continue
		}
		if strings.Count(t[i-pad], "|") >= 3 {
			out[i] = outLine(t[i-pad], w)
			continue
		}
		out[i] = dim.Render(trunc(t[i-pad], w))
	}
	return out
}

// ---- command palette (spike/work-tab-redesign, round 3) ----
//
// The question: at social-poker's real scale (~94 commands, several
// multi-target parents with 4-8 targets each), a flat categorized list is
// the wrong default — you cannot scan to what you want, you have to
// remember which parent it hangs under. The fixture below is scaled down
// (~40 resolved entries, not 94) for spike tractability, but keeps the same
// SHAPE: several deep multi-target parents plus a handful of single
// commands, which is the density that actually stresses the design.
//
// commandEntries is deliberately separate from `cmds`/liveRows above — the
// original ten reference frames (commands.txt, swarm.txt, …) render from
// that smaller admin-project-tool-shaped fixture and must not shift under
// this change. The palette screens are new frames, appended to the screen
// axis, with their own fixture.

// commandEntry is one FULLY RESOLVED invocation — what actually gets
// dispatched. "deploy grafana" is an entry; "deploy" alone (the parent,
// ambiguous among 8 targets) never is, which is why a parent row carries
// group=true and no marker. A spacer is the blank row between two command
// groups: no name, no marker, never under the cursor.
type commandEntry struct {
	parent, target, run string
	group               bool
	spacer              bool
	last                bool
}

func (e commandEntry) resolved() string {
	if e.target == "" {
		return e.parent
	}
	return e.parent + " " + e.target
}

// label is what the row prints in the name column: a target prints its own key
// under its parent's heading, a flat command prints its name.
func (e commandEntry) label() string {
	if e.target == "" {
		return e.parent
	}
	return e.target
}

// connector is the tree glyph a target row draws under its parent. A flat
// command has no parent heading above it, so it draws none.
func (e commandEntry) connector() string {
	if e.target == "" {
		return ""
	}
	if e.last {
		return dim.Render("└ ")
	}
	return dim.Render("├ ")
}

func parentOf(name string) commandParent {
	for _, p := range commandParents {
		if p.name == name {
			return p
		}
	}
	return commandParent{name: name}
}

// commandRows is the Commands section in draw order: each parent heading (a
// group row, unselectable) followed by its targets, and a flat command as a
// single selectable row of its own, with a spacer wherever the group changes.
func commandRows() []commandEntry {
	var out []commandEntry
	for i, p := range commandParents {
		if i > 0 && p.group != commandParents[i-1].group {
			out = append(out, commandEntry{spacer: true})
		}
		if len(p.targets) == 0 {
			out = append(out, commandEntry{parent: p.name, run: "bash " + p.name + ".sh"})
			continue
		}
		out = append(out, commandEntry{parent: p.name, group: true})
		for i, t := range p.targets {
			for _, e := range commandEntries {
				if e.parent == p.name && e.target == t {
					e.last = i == len(p.targets)-1
					out = append(out, e)
				}
			}
		}
	}
	return out
}

// commandParent is one declared command: a flat one (no targets) or a
// multi-target group whose targets are the things that actually run. The
// fixture is deliberately dense — social-poker's real scale — so the list is
// judged at the size that made it hard to scan, not at a toy one.
type commandParent struct {
	name, kind, desc string
	targets          []string // resolved suffixes, "" entry means no targets
	// query is the raw `over = { query = "..." }` string a kind="workflow"
	// entry resolves against the tracker — set only on the one workflow
	// parent the fixture carries, since a selection-sourced workflow never
	// reaches the Commands tab at all (WorkflowTarget.AppearsInCommandList).
	query string
	// group is the manifest's command group. The real list draws one blank
	// row wherever the group changes (internal/tui/work.go's workRows); the
	// fixture's second group (lint, fmt, clean) puts that spacer at the foot
	// of the Commands list, below every dumped frame's window.
	group int
}

var commandParents = []commandParent{
	{name: "deploy", kind: "multi-target", desc: "Deploy a service to production",
		targets: []string{"analytics-eng", "chat-moderation", "hand-reports-drain", "ios", "ios-device", "mainframe", "name-check", "tournament-processor"}},
	{name: "dev", kind: "multi-target", desc: "Run a local dev server",
		targets: []string{"cf", "local"}},
	{name: "logs", kind: "multi-target", desc: "Tail logs (prod webview client console via Tampermonkey bridge)",
		targets: []string{"branch-client", "local-client", "preview-cf", "prod-client", "staging-server", "workstream-server"}},
	{name: "kill", kind: "multi-target", desc: "Kill local dev server processes (local zombies only — never remote)",
		targets: []string{"all", "cf", "local", "mainframe", "supabase"}},
	{name: "migrate", kind: "multi-target", desc: "Run D1 migrations (shows picker)",
		targets: []string{"local", "preview", "prod", "staging"}},
	{name: "test", kind: "multi-target", desc: "Run tests — `check` (typecheck+lint+unit) or `gate` (CF harness suite)",
		targets: []string{"check", "ci", "convergence", "gate", "job", "rehearse", "shards", "workflows"}},
	// A workflow command: its targets are tracker items, not manifest targets,
	// which is why commands-workflow-selected is its own frame — the entry
	// dispatches an agent over a bead rather than running a shell line.
	{name: "retire-merged", kind: "workflow", desc: "Remove worktrees whose branch is merged",
		targets: []string{"fix-detector-parity-skip", "go-transient-kind-factories", "docs-adr-lua", "launchd-env-file"},
		query:   "status:closed label:worktree"},
	{name: "grafana", kind: "multi-target", desc: "Grafana alert rules (export / diff / deploy)",
		targets: []string{"deploy", "diff", "export"}},
	{name: "xplay-trail", kind: "multi-target", desc: "Trail xplay transfers live (preview|staging|prod, or raw <flags>)",
		targets: []string{"preview", "prod", "raw", "staging"}},
	{name: "lint", kind: "shell", desc: "Lint and auto-fix all apps and packages", group: 1},
	{name: "fmt", kind: "shell", desc: "Format all files", group: 1},
	{name: "clean", kind: "shell", desc: "Remove build artifacts", group: 1},
}

// commandEntries is commandParents flattened to the resolved shape fuzzy
// search actually matches against — the whole point being that "cf" has to
// find "dev cf" and "kill cf" directly, never just "dev" or "kill".
var commandEntries = func() []commandEntry {
	var out []commandEntry
	for _, p := range commandParents {
		if len(p.targets) == 0 {
			out = append(out, commandEntry{parent: p.name, run: "bash " + p.name + ".sh"})
			continue
		}
		for _, t := range p.targets {
			run := "cd apps && bunx wrangler " + p.name + " --target " + t
			if p.kind == "workflow" {
				// A workflow entry dispatches an agent over a tracker item; there
				// is no shell line to show, and printing a fake one would make the
				// frame lie about what ↵ does.
				run = "dispatch claude over " + t
			}
			out = append(out, commandEntry{parent: p.name, target: t, run: run})
		}
	}
	return out
}()

var (
	// cursorAt is each list tab's own cursor, indexed by tabCommands and
	// tabWorkers, as the real Model.cursor is per tab: leaving a tab and coming
	// back finds the cursor where it was left.
	cursorAt [tabWorkers + 1]int
	// cursorMoved lets a screen's declared cmdSel/liveSel seed its tab's cursor
	// without freezing it: until the cursor is moved or the tab is entered by
	// ←→, the seed wins, after that the cursor is the user's. Reset clears it,
	// so every dumped frame renders its own screen's seed rather than the
	// previous frame's.
	cursorMoved [tabWorkers + 1]bool
	cmdStatus   string
	// attachedRow is which live row the hosted view is showing. Set by ↵/a,
	// cleared on detach, so attaching to a different row actually changes what
	// that view renders.
	attachedRow *liveRow
)

// isWorkTab is every Commands- or Workers-tab list screen, whichever row the
// cursor sits on. Keying off the name prefix missed running/swarm, so ↵ on a
// live row fell through to onKey's own switch and the two disagreed about what
// attach meant.
func isWorkTab(screen string) bool {
	sp, ok := screens[screen]
	return ok && sp.tab <= tabWorkers && !sp.hosted && !sp.sheet
}

func onWorkersTab(screen string) bool { return screens[screen].tab == tabWorkers }

func resetCursor() {
	cursorAt, cursorMoved = [tabWorkers + 1]int{}, [tabWorkers + 1]bool{}
	cmdStatus, attachedRow = "", nil
}

// enterTab is arriving on a screen by ←→. A screen's own seed is for its
// dumped frame; a person arriving on a list tab for the first time starts on
// its top row, and on any later visit finds the row they left.
func enterTab(screen string) {
	if t := screens[screen].tab; t <= tabWorkers && !cursorMoved[t] {
		cursorAt[t], cursorMoved[t] = 0, true
	}
}

// isLive is entryIsLive (internal/tui/work.go): a failed or exited row is
// still listed, but ↵ no longer attaches to it and x has nothing to kill.
func isLive(lr *liveRow) bool {
	return lr != nil && lr.state != "failed" && lr.state != "exited"
}

// dispatchCommand is Enter on an idle Commands row: materialize it as a live
// row so there is something to attach to or kill. It runs on its own row in
// the Commands list, the same as dev cf and test gate in the seed.
func dispatchCommand(e commandEntry) {
	cmdStatus = "dispatched: " + e.resolved()
	if liveForEntry(e, false) != nil {
		return // already running — ↵ attaches instead, see activate
	}
	liveRows = append(liveRows, liveRow{
		state: "running", id: e.resolved(), doing: "starting…", cmd: e.parent, target: e.target,
		hosted: true, elapsed: "0s", lastCmd: e.run, hasRing: true,
		logPath: root + "/.admin/logs/" + strings.ReplaceAll(e.resolved(), " ", "-") + ".log",
		tail: []string{
			"$ " + e.run,
			"",
			"14:22:58 starting…",
		},
	})
}

func killLiveRow(id string) {
	out := liveRows[:0]
	for _, lr := range liveRows {
		if lr.id == id {
			continue
		}
		out = append(out, lr)
	}
	liveRows = out
}

// liveForEntry is the live row this Commands entry currently IS, if it is
// running. One lookup, so the Commands row's state word, the band's preview
// and the cursor can never disagree about whether a command is live.
func liveForEntry(e commandEntry, empty bool) *liveRow {
	if empty {
		return nil
	}
	for i := range liveRows {
		if liveRows[i].cmd == e.parent && liveRows[i].target == e.target {
			return &liveRows[i]
		}
	}
	return nil
}

// selectableCommands is every Commands row the cursor can land on, in draw
// order. A group row holds targets and runs nothing itself, and a spacer is
// not a row at all, so neither is here.
func selectableCommands() []commandEntry {
	var out []commandEntry
	for _, e := range commandRows() {
		if !e.group && !e.spacer {
			out = append(out, e)
		}
	}
	return out
}

// tabRowCount is how many rows the cursor can land on in this tab's list.
func tabRowCount(cur string) int {
	if onWorkersTab(cur) {
		return len(liveSectionRows(false))
	}
	return len(selectableCommands())
}

// moveCursor walks the active tab's own list. Each tab has its own cursor, as
// in the real dashboard (Model.cursor is per tab).
func moveCursor(cur string, delta int) {
	if total := tabRowCount(cur); total > 0 {
		t := screens[cur].tab
		cursorAt[t], cursorMoved[t] = (tabCursor(cur, false)+delta+total)%total, true
	}
}

// tabCursor is the cursor on this screen's tab. A screen's cmdSel or liveSel
// seeds it until ↑↓ moves it.
func tabCursor(cur string, empty bool) int {
	sp, ok := screens[cur]
	if !ok || sp.tab > tabWorkers {
		return 0
	}
	if cursorMoved[sp.tab] {
		return cursorAt[sp.tab]
	}
	if onWorkersTab(cur) {
		if n := len(liveSectionRows(empty)); n > 0 && sp.liveSel >= 0 {
			return sp.liveSel % n
		}
		return 0
	}
	if sp.cmdSel > 0 {
		return sp.cmdSel - 1 // stored +1 so the zero value means "unset"
	}
	return 0
}

// attachedFor is which row the hosted view renders: whatever ↵ attached to
// while driving, or — for a dumped frame, where nothing has been pressed — the
// screen's own declared liveSel, so hosted.txt stays a picture of one specific
// row rather than of whichever row happened to be first.
func attachedFor(screen string) *liveRow {
	if attachedRow != nil {
		return attachedRow
	}
	if sp, ok := screens[screen]; ok && sp.liveSel >= 0 && sp.liveSel < len(liveRows) {
		return &liveRows[sp.liveSel]
	}
	return nil
}

// cursorLive is the live row the cursor is on: a running command on the
// Commands tab, or a worker on the Workers tab. nil means the cursor is on
// something idle.
func cursorLive(cur string) *liveRow {
	i := tabCursor(cur, false)
	if onWorkersTab(cur) {
		if rows := liveSectionRows(false); i < len(rows) {
			return rows[i]
		}
		return nil
	}
	if cmds := selectableCommands(); i < len(cmds) {
		return liveForEntry(cmds[i], false)
	}
	return nil
}

// rowAction is what ↵ does on the cursor row, resolveRowAction's answer
// (internal/tui/work.go): attach while the row's process is live, dispatch an
// idle or finished command, open the transcript of a finished worker.
func rowAction(lr *liveRow, onWorkers bool) string {
	switch {
	case isLive(lr):
		return "attach"
	case onWorkers && lr != nil:
		return "transcript"
	}
	return "dispatch"
}

// activate is ↵, and what it does follows the row's state (rowAction).
// Attaching sets attachedRow and switches to the hosted view — the prototype
// has to actually go there, or ↵ on a running row is a footer promising
// something the prototype does not do. The prototype has no transcript view,
// so that action only says what it would open.
func activate(cur string, set func(axis, value string)) {
	lr := cursorLive(cur)
	switch rowAction(lr, onWorkersTab(cur)) {
	case "attach":
		attachedRow = lr
		set("screen", "hosted")
	case "transcript":
		cmdStatus = "transcript: " + lr.id
	default:
		if cmds := selectableCommands(); !onWorkersTab(cur) {
			if i := tabCursor(cur, false); i < len(cmds) {
				dispatchCommand(cmds[i])
			}
		}
	}
}

// killSelected is `x` on a live row. Kill is x, never k — k is cursor-up
// everywhere in this dashboard. A failed or exited row has nothing to kill.
func killSelected(cur string) {
	lr := cursorLive(cur)
	if !isLive(lr) {
		return
	}
	i := tabCursor(cur, false)
	killLiveRow(lr.id)
	if onWorkersTab(cur) && i > 0 {
		i--
	}
	t := screens[cur].tab
	cursorAt[t], cursorMoved[t] = i, true
}

func onWorkTabKey(key string, cur string, set func(axis, value string)) bool {
	switch key {
	case "down", "j":
		moveCursor(cur, 1)
		return true
	case "up", "k":
		moveCursor(cur, -1)
		return true
	case "enter":
		activate(cur, set)
		return true
	case "a":
		// `a` is attach and nothing else, so it is a no-op on an idle row
		// rather than a second key that starts things.
		if isLive(cursorLive(cur)) {
			activate(cur, set)
		}
		return true
	case "x":
		killSelected(cur)
		return true
	}
	return false
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// commandRowLine is one row of the Commands section, on the same grid
// internal/tui/workview.go's renderWorkRow draws. lw is the row's width after
// its one leading column. A multi-target parent renders with NO marker — it is
// a heading over its targets and runs nothing itself, so the cursor cannot land
// on it and nothing here promises ↵ will run it. The names still start in the
// same column, so the tree stays aligned.
func commandRowLine(e commandEntry, sel bool, lw int, empty bool) string {
	// A running row spends the columns after the name on state, elapsed and
	// activity. An idle row has none of those to show, so its description (a
	// target's run line) starts right after the name and takes their cells.
	rest := maxInt(lw-wMarker-wName-wState-wElapsed, 6)
	idle := maxInt(lw-wMarker-wName, 6)

	if e.spacer {
		return Pad("", lw+1)
	}
	if e.group {
		p := parentOf(e.parent)
		// Two blank cells where a runnable row has "◇ ": the marker column is
		// what says the cursor can be here, and a group runs nothing itself.
		// The name truncates one cell short of wName so a full-width name never
		// runs straight into its description (admin-3rgp.72.19).
		return Pad("   "+bo.Render(Pad(trunc(p.name, wName-1), wName))+dim.Render(trunc(p.desc, idle)), lw+1)
	}

	marker := dim.Render("◇ ")
	if sel {
		marker = cy.Render("◆ ")
	}
	var name string
	if e.target != "" {
		key := Pad(trunc(e.target, wName-3), wName-2)
		if sel {
			key = cy.Render(key)
		}
		name = e.connector() + key
	} else {
		name = Pad(trunc(e.parent, wName-1), wName)
	}
	head := " " + marker + name

	// A running command says so HERE, on its own row, in the columns the grid
	// already reserves. There is no separate Running section: a command that is
	// running is the same row it always was, wearing its state.
	if lr := liveForEntry(e, empty); lr != nil {
		return Pad(head+
			liveStyle[lr.state].Render(Pad(liveGlyph(lr)+" "+lr.state, wState))+
			dim.Render(Pad(lr.elapsed, wElapsed))+
			dim.Render(trunc(lr.doing, rest)), lw+1)
	}
	// A target says what it will run; a flat command says what it is for.
	if e.target != "" {
		return Pad(head+dim.Render(trunc(e.run, idle)), lw+1)
	}
	return Pad(head+dim.Render(trunc(parentOf(e.parent).desc, idle)), lw+1)
}

// kindLabel abbreviates the one kind whose name does not fit the band's kind
// field.
func kindLabel(k string) string {
	if k == "multi-target" {
		return "multi-tgt"
	}
	return k
}

// commandSections is the Commands tab's list: every declared command, groups
// and their targets. selIdx is the cursor's own 0-based position among the
// SELECTABLE rows.
func commandSections(w, selIdx int, empty bool) []string {
	out := []string{Pad(" "+bo.Render("Commands")+dim.Render("  "+itoa(len(commandEntries))), w)}
	sel := 0
	for _, e := range commandRows() {
		if e.group || e.spacer {
			out = append(out, commandRowLine(e, false, w, empty))
			continue
		}
		out = append(out, commandRowLine(e, sel == selIdx, w, empty))
		sel++
	}
	return out
}

// ---- frame ----
// ---- the Work tab: command palette above, one live band below ----
//
// This is the settled layout, and it replaced four things that used to be
// open questions in this file:
//
//   - The flat Commands list is gone. At social-poker's real scale (~94
//     resolved invocations) it could not be scanned, only remembered; the
//     palette is the default and `b` is the browse fallback. The `commands`
//     screen and its frame went with it.
//   - The `hover` axis is gone. It offered off / auto-tail / split-pane /
//     inline-peek as competing answers to "where does live output go"; the
//     answer is this band, always on, full width, for whatever the cursor is
//     on. Nothing is gated on a mode any more.
//   - The left/right versus stacked orientation switch is gone. The band is
//     full width at every geometry, so the list gives up height and never
//     width — the failure the two-thirds split had was that output got a
//     ~30-column column on exactly the terminals where log lines are longest.
//   - A multi-target parent is a group, not a command. It holds targets and
//     runs nothing itself, so the cursor cannot land on one and it carries no
//     marker: the marker column is what promises ↵ will run the row.
//
// Two rules the band obeys everywhere:
//
//   - The field block is a FIXED slot (fieldRowsH), padded. The eye reads
//     motion as meaning, so a block that grew a row for a command that had
//     actually been run once slid everything below it down and claimed a state
//     change that had not happened.
//   - Nothing is said twice. The previewed thing is named once, on the band's
//     own title rule, with the count of everything else running on the right
//     of that same rule. There is no chip row and no tab strip: the cursor is
//     the selector, so a second row of names only reprinted the list.

// root is a fixed, illustrative checkout path for the mock log/worktree
// paths below. These rows are fiction — no dispatched command, log file or
// worktree in liveRowsSeed was ever actually run — so nothing reads them off
// disk, and "the checkout this happens to run from" was never the right
// answer for what they should say. It used to be resolved at runtime from
// this source file's own location on the theory that a fixed constant would
// only ever be right in the one checkout it was written for; in practice
// that made every re-shoot from a linked worktree grow the worktree's own
// segment into eleven mock paths long enough to trip fitPath's elision,
// corrupting them (admin-3rgp.72.20). A fixed constant is right precisely
// because these paths are fiction: the dump must produce the same fiction
// regardless of which checkout or worktree it runs from. Deliberately not
// any real developer's home directory — a generic stand-in, same as the
// fixture repo names below (fixtures.go's "fixture/beads-repo" etc).
const root = "/Users/dev/admin-project-tool"

// The live sections are a fixed column grid. The boundaries are named because
// the band's title rule and the row list have to agree on them.
//
//	 ◆ admin-3rgp.36   ◈ running 18m    writing internal/lua/host.go   pane 3
//	└┬──────────────┘ └─────────┘└────┘└───────────────────────────┘ └──────┘
//	 1  marker+name     state     elapsed        activity              where
const (
	wMarker  = 2  // "◆ " / "◇ "
	wName    = 16 // the name column
	wState   = 10 // "◈ running"
	wElapsed = 7  // "18m", "4m12s"
)

// fieldRowsH is the fixed height of the band's field block, for every row kind:
// a live command's log+url, a live worker's log+branch+worktree+session+now, or
// an idle entry's kind+desc+last-run+last-log. Five is the tallest of those.
const fieldRowsH = 5

// bandH is how many rows the live band takes out of the body. Fixed, so the
// list above it never reflows as the cursor moves.
// bandH is fixed per terminal height, never per cursor position: a band that
// resized with the row under the cursor would reflow the list on every ↑↓. The
// floor of 9 is title + fieldRowsH + three rows of tail — below that the band
// says less than the row it is previewing already does. The list keeps the rest.
func bandH(h int) int { return maxInt(minInt(13, h/2), minInt(9, h-1)) }

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// fitPath keeps a log or worktree path absolute. When it will not fit, whole
// segments go from the middle — "/Users/…/logs/docs.log" — so it still reads as
// a path. A mid-rune cut produces "/Users/dev/Proj…tool/" and reads as
// corruption.
func fitPath(p string, w int) string {
	if VW(p) <= w {
		return p
	}
	if w < 12 {
		return trunc(p, maxInt(w, 1))
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for k := len(segs) - 1; k >= 1; k-- {
		if cand := "/" + segs[0] + "/…/" + strings.Join(segs[len(segs)-k:], "/"); VW(cand) <= w {
			return cand
		}
	}
	if cand := "…/" + segs[len(segs)-1]; VW(cand) <= w {
		return cand
	}
	return trunc(p, w)
}

// slotRows pads (or clips) a field block to exactly fieldRowsH rows.
func slotRows(out []string, w int) []string {
	for len(out) < fieldRowsH {
		out = append(out, Pad("", w))
	}
	return out[:fieldRowsH]
}

func blankRows(w, n int) []string {
	out := make([]string, 0, maxInt(n, 0))
	for len(out) < n {
		out = append(out, Pad("", w))
	}
	return out
}

func isWorkerRow(lr *liveRow) bool { return lr.cmd == "" }

func liveGlyph(lr *liveRow) string {
	switch lr.state {
	case "waiting":
		return "◐"
	case "failed":
		return "✗"
	default:
		if isWorkerRow(lr) {
			return "◈"
		}
		return "●"
	}
}

// liveRowLine renders one row of a live section against the column grid,
// dropping the where-column rather than overflowing when the activity column
// would be left under eight cells.
func liveRowLine(lr *liveRow, sel bool, w int) string {
	marker := dim.Render("◇ ")
	name := Pad(lr.id, wName)
	if sel {
		marker = cy.Render("◆ ")
		name = cy.Render(Pad(lr.id, wName))
	}
	state := liveStyle[lr.state].Render(Pad(liveGlyph(lr)+" "+lr.state, wState))
	elapsed := dim.Render(Pad(lr.elapsed, wElapsed))
	used := 1 + wMarker + wName + wState + wElapsed
	whereText, whereStyle := whereCol(lr)
	ww := VW(whereText)
	where := whereStyle.Render(whereText)
	if ww > 0 && w-used-ww < 8 {
		ww, where = 0, ""
	}
	actW := maxInt(w-used-ww, 4)
	return Pad(" "+marker+name+state+elapsed+dim.Render(Pad(trunc(lr.doing, actW), actW))+where, w)
}

// liveSections is the Workers tab's list. A worker is not a command — it is a
// Claude session running mostly autonomously in its own worktree, with its own
// state words, its own fields and a real terminal session you can attach to —
// so it has its own tab, and never appears on the Commands tab. selIdx is the
// cursor's 0-based position among the workers.
func liveSections(w, selIdx int, empty bool) []string {
	rows := liveSectionRows(empty)
	out := []string{Pad(" "+bo.Render("Workers")+dim.Render("  "+itoa(len(rows))), w)}
	if len(rows) == 0 {
		return append(out, Pad(" "+dim.Render("no workers running — dispatch a bead from the tracker tab"), w))
	}
	for i, lr := range rows {
		out = append(out, liveRowLine(lr, i == selIdx, w))
	}
	return out
}

// liveSectionRows is every row the Workers tab draws. Live COMMANDS are not
// here — they render in the Commands list on their own row — so this is the one
// order the tab, the cursor and the band's "also running" count all read.
func liveSectionRows(empty bool) []*liveRow {
	if empty {
		return nil
	}
	var wks []*liveRow
	for i := range liveRows {
		if lr := &liveRows[i]; isWorkerRow(lr) {
			wks = append(wks, lr)
		}
	}
	return wks
}

// othersRunning is the right half of the band's title rule: how much is running
// that you are NOT looking at. Said once per screen, never once per row.
func othersRunning(sel *liveRow, empty bool) string {
	var rows []*liveRow
	if !empty {
		for i := range liveRows {
			rows = append(rows, &liveRows[i])
		}
	}
	if len(rows) == 0 {
		return "nothing running"
	}
	c, wk := 0, 0
	for _, lr := range rows {
		if sel != nil && lr.id == sel.id {
			continue
		}
		if isWorkerRow(lr) {
			wk++
		} else {
			c++
		}
	}
	if c == 0 && wk == 0 {
		return "nothing else running"
	}
	parts := ""
	if c > 0 {
		parts = itoa(c) + " command" + plural(c)
	}
	if wk > 0 {
		if parts != "" {
			parts += " · "
		}
		parts += itoa(wk) + " worker" + plural(wk)
	}
	return "also " + parts
}

// bandTitle is the rule between the list and the band: the previewed thing on
// the left, once, and the count of everything else running on the right. The
// identity wins the space — on a narrow terminal the count drops rather than
// squeezing the name it is a footnote to.
func bandTitle(w int, sel *liveRow, idle string, empty bool) string {
	var title string
	if sel != nil {
		title = liveStyle[sel.state].Render(liveGlyph(sel)) + " " + bo.Render(sel.id) +
			dim.Render("  "+sel.state+"  "+sel.elapsed)
	} else {
		title = bo.Render(idle) + dim.Render("  not running")
	}
	right := othersRunning(sel, empty)
	// Budget: "── " + title + " " + fill + suffix + " ──" = w, so the dashes are
	// whatever is left after everything with a fixed width. The identity wins
	// the space — on a narrow terminal the count drops rather than squeezing
	// the name it is a footnote to.
	const fixed = 7 // "── ", the space after the title, and " ──"
	if w-fixed-VW(right)-1 < VW(Strip(title)) && w-fixed-VW(right)-1 < 28 {
		right = ""
	}
	suffix := ""
	if right != "" {
		suffix = " " + right
	}
	// trunc counts raw bytes, and title carries color escapes, so cutting it
	// unconditionally spent the budget on invisible characters and clipped a
	// name that fits. Only cut when the VISIBLE width is actually over.
	budget := maxInt(w-fixed-VW(suffix), 8)
	if VW(Strip(title)) > budget {
		title = trunc(title, budget)
	}
	fill := maxInt(w-fixed-VW(Strip(title))-VW(suffix), 0)
	return dim.Render("── ") + title + dim.Render(" "+strings.Repeat("─", fill)+suffix+" ──")
}

// bandFields is the field block for a live row, held to the band's fixed slot.
// Name, state and elapsed are NOT here — the title rule above already said them
// once.
func bandFields(lr *liveRow, w int) []string {
	return slotRows(fieldRows(lr, w), w)
}

// fieldRows is bandFields sized to its own content. The attached view
// (hostedBody) draws these same rows under its breadcrumb, so the band and the
// attached view cannot disagree about what a row's fields are.
func fieldRows(lr *liveRow, w int) []string {
	lw := maxInt(w-11, 8)
	out := []string{" " + dim.Render("log      ") + dim.Render(fitPath(lr.logPath, lw))}
	if !isWorkerRow(lr) {
		// A command with no url gets no url row. In the band the slot is fixed
		// height, so the space is held either way — printing "url —" spends a
		// row saying a field is absent, which the blank row already says.
		for _, u := range lr.urls {
			out = append(out, " "+dim.Render("url      ")+cy.Render(trunc(u, lw)))
		}
		return padRows(out, w)
	}
	out = append(out,
		" "+dim.Render("branch   ")+cy.Render(trunc(lr.branch, lw)),
		" "+dim.Render("worktree ")+dim.Render(fitPath(lr.worktree, lw)),
	)
	// whereLabel already carries its own gloss ("· detached, outlives admin"), so
	// appending a second one printed the same fact twice on one row.
	out = append(out, " "+dim.Render("session  ")+trunc(whereLabel(lr), lw))
	return padRows(append(out, " "+dim.Render("now      ")+trunc(lr.doing, lw)), w)
}

// padRows pads every row to w without changing how many rows there are.
func padRows(out []string, w int) []string {
	for i, l := range out {
		out[i] = Pad(l, w)
	}
	return out
}

// bandIdle is the band while the cursor sits on something that is NOT running:
// the entry's own detail and its last run. Never a blank rect, never another
// row's tail — and never a row count that depends on what it has to say. "last
// run" and "last log" are always two rows, reading "never" and "—" for
// something nothing has ever run.
func bandIdle(e *commandEntry, w int) []string {
	lw := maxInt(w-11, 8)
	p := parentOf(e.parent)
	kind := p.kind
	if e.target != "" {
		kind = "target"
	}
	lastLog := ""
	// A small fixed set of entries carry a last log in the fixture, so the
	// band's "has been run before" state is depicted without the frame
	// depending on what a prior keypress dispatched.
	for _, m := range []string{"grafana deploy", "dev cf", "test gate"} {
		if m == e.resolved() {
			lastLog = root + "/.admin/logs/" + strings.ReplaceAll(e.resolved(), " ", "-") + ".log"
		}
	}
	lastRun := "never"
	logCell := dim.Render("—")
	if lastLog != "" {
		lastRun, logCell = "18m ago · exit 0", dim.Render(fitPath(lastLog, lw))
	}
	rows := []string{
		" " + dim.Render(Pad("kind", 9)) + trunc(kindLabel(kind), lw),
		" " + dim.Render(Pad("desc", 9)) + trunc(p.desc, lw),
		" " + dim.Render(Pad("last run", 9)) + dim.Render(lastRun),
		" " + dim.Render(Pad("last log", 9)) + logCell,
	}
	// A query-sourced kind="workflow" parent carries the tracker query enter
	// will actually run over — the one field that distinguishes it from a
	// shell row on this surface. p.query is empty for every other parent, so
	// this only ever fires for the fixture's one workflow entry.
	if p.query != "" {
		rows = append(rows, " "+dim.Render(Pad("query", 9))+trunc(p.query, lw))
	}
	return slotRows(rows, w)
}

// renderWorkBody is the Commands or Workers tab body: that tab's list
// scrolling in the top region, then the band. One layout at every geometry.
// list is the tab's rows, already built at workBodyWidth(w).
func renderWorkBody(w, h int, list []string, selEntry *commandEntry, selWorker *liveRow, empty bool) []string {
	fullW := workBodyWidth(w)
	bh := bandH(h)
	listH := maxInt(h-bh, 1)

	if len(list) > listH {
		start, end := scrollWindow(len(list), markedIdx(list), listH)
		win := append([]string{}, list[start:end]...)
		if start > 0 && len(win) > 0 {
			// The header of whichever section the window starts inside is pinned
			// to its first row. Without it a scrolled list shows rows with no
			// way to tell whether they are commands or workers — and the naive
			// window replaced the header itself with the "N more" marker.
			win[0] = Pad(" "+sectionHeaderAt(list, start)+dim.Render("   ↑ "+itoa(start)+" more"), fullW)
		}
		if end < len(list) && len(win) > 0 {
			win[len(win)-1] = dim.Render("   ↓ more")
		}
		list = win
	}

	// Each row already carries its own single leading space, so nothing is added
	// here — a second one put the whole list one column right of the band's
	// rules and fields beneath it.
	out := make([]string, 0, h)
	for i := 0; i < listH; i++ {
		out = append(out, Pad(at(list, i), w))
	}

	idleName := ""
	if selEntry != nil {
		idleName = selEntry.resolved()
	}
	out = append(out, bandTitle(w, selWorker, idleName, empty))
	switch {
	case selWorker != nil:
		fields := bandFields(selWorker, fullW)
		out = append(out, prefixRows(fields, fullW)...)
		out = append(out, insetRows(tailLines(selWorker.tail, maxInt(bh-1-fieldRowsH, 1), fullW-1), w)...)
	case selEntry != nil:
		out = append(out, prefixRows(bandIdle(selEntry, fullW), fullW)...)
		out = append(out, blankRows(w, maxInt(bh-2-fieldRowsH, 0))...)
		out = append(out, Pad(" "+dim.Render("output appears here while it runs"), fullW))
	}
	for len(out) < h {
		out = append(out, Pad("", w))
	}
	return out[:h]
}

// prefixRows pads a band block to the body width. The rows already carry their
// own one-column inset, so nothing is added here — a second space put the band's
// fields one column right of the rule above them.
// sectionHeaderAt is which section a scrolled window starting at row i is
// inside — the nearest header row at or above it. The headers are the only rows
// liveSections renders bold, which is what makes them findable without a
// parallel index the two could disagree about.
func sectionHeaderAt(sections []string, i int) string {
	for j := i; j >= 0 && j < len(sections); j-- {
		t := strings.TrimSpace(Strip(sections[j]))
		if strings.HasPrefix(t, "Commands") || strings.HasPrefix(t, "Running") || strings.HasPrefix(t, "Workers") {
			return bo.Render(strings.Fields(t)[0])
		}
	}
	return bo.Render("Commands")
}

// insetRows puts a band block in the same one-column inset the band's own
// fields use. The tail was flush at column 0 while the fields above it were
// not, so the block read as two different panes.
func insetRows(rows []string, w int) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = Pad(" "+r, w)
	}
	return out
}

// workBodyWidth is the width a Commands or Workers row is built at — the real
// dashboard's lw (internal/tui/workview.go's renderWorkBody).
func workBodyWidth(w int) int { return maxInt(w-2, 20) }

func prefixRows(rows []string, w int) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = Pad(r, w)
	}
	return out
}

func render(s State) string {
	w, h := s.W, s.H
	if w <= 0 {
		w = DumpWidth
	}
	if h <= 0 {
		h = DumpHeight
	}
	name := s.Get("screen")
	if name == "" {
		name = "commands"
	}
	sp := screens[name]
	// The empty screen depicts a project with nothing running and no workers.
	// The data axis says the same thing but is NoDump, so without the name check
	// the dumped frame was byte-identical to the populated one.
	empty := s.Is("data", "empty") || name == "commands-empty"
	tracker := s.Get("tracker")
	if tracker == "" {
		tracker = "beads"
	}
	src := trackerRepo(tracker)
	// header's readout has to match the SAME h workBody's bottomOrient check
	// actually compares against (the body height, after the header/tab/rule
	// rows are subtracted) — not the raw terminal height — or the number on
	// screen would lie about what the code just decided.
	// Attached, the tab bar is replaced by the attach breadcrumb: you cannot
	// switch tabs from inside a process's own terminal (every keystroke belongs
	// to the child), so a row of tabs there advertises navigation that does not
	// work. The breadcrumb spends that row saying what you are attached to.
	// The crumb has no blank row under it: the field rows hostedBody draws
	// first belong to it, so they sit directly beneath.
	rows := []string{header(w, h-5, src), tabBar(sp, w, s.Get("tracker"), empty), ""}
	if sp.hosted {
		rows = rows[:1]
		rows = append(rows, attachCrumb(attachedFor(name), w))
	}
	if _, ok := embedded[name]; ok {
		// repo-dash draws its own rule and footer for these — including the
		// modal footers, which are the mode.
		body := realTab(src, name, w, h-3)
		for len(body) < h-3 {
			body = append(body, "")
		}
		rows = append(rows, body[:h-3]...)
	} else {
		var body []string
		action := "dispatch"
		switch {
		case sp.hosted:
			body = hostedBody(w, h-2-len(rows), sp.hostedEmpty, attachedFor(name))
		default:
			// The Commands and Workers tabs are the same body: the tab's list
			// above, the band below. What differs is the list and where the
			// cursor sits — the screen axis names that, not a second layout.
			cursor := tabCursor(name, empty)
			fullW := workBodyWidth(w)
			var list []string
			var selLive *liveRow
			var selEntry *commandEntry
			if onWorkersTab(name) {
				rowsLive := liveSectionRows(empty)
				if cursor >= len(rowsLive) {
					cursor = 0
				}
				if cursor < len(rowsLive) {
					selLive = rowsLive[cursor]
				}
				list = liveSections(fullW, cursor, empty)
			} else {
				cmds := selectableCommands()
				if cursor >= len(cmds) {
					cursor = 0
				}
				if len(cmds) > 0 {
					selEntry = &cmds[cursor]
					// A Commands row for a command that is RUNNING previews the
					// running thing, not a description of it. The band follows
					// the row's state, the same rule ↵ follows.
					if lr := liveForEntry(cmds[cursor], empty); lr != nil {
						selLive, selEntry = lr, nil
					}
				}
				list = commandSections(fullW, cursor, empty)
			}
			action = rowAction(selLive, onWorkersTab(name))
			body = renderWorkBody(w, h-5, list, selEntry, selLive, empty)
		}
		bodyH := h - 2 - len(rows) // rule + keys footer below
		for len(body) < bodyH {
			body = append(body, "")
		}
		rows = append(rows, body[:bodyH]...)
		rows = append(rows, dim.Render(strings.Repeat("─", w)), Pad(" "+dim.Render(keys(sp, action)), w))
	}
	if sp.sheet {
		if sp.running {
			rows = overlayQuit(rows, w, h)
		} else {
			rows = overlaySheet(rows, w, h)
		}
	}
	for i := range rows {
		rows[i] = Pad(rows[i], w)
	}
	return strings.Join(rows, "\n")
}

// header shows the exact w×h this frame is rendering at, plus how many rows the
// live band is taking, so both are on screen instead of something to estimate
// from a screenshot. The old readout printed the w/2.3 orientation verdict; that
// verdict no longer exists — the band is full width at every geometry — so what
// is worth showing instead is the split it actually chose.
func header(w, h int, src repoSrc) string {
	name := src.nwo
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	l := name + "   " + cy.Render(src.branch) + "  " + gr.Render("✔ clean")
	// The readout shows the geometry verdict ALONE — never gated on hover —
	// because that gate is a separate design choice (bottomOrient only ever
	// applies while a hover mode is on) and folding it into this arrow was
	// itself the bug: it made the displayed math and the displayed verdict
	// disagree whenever hover was off, which is exactly what happened here.
	r := dim.Render(fmt.Sprintf("w=%d h=%d  list %d · band %d", w, h, h-bandH(h), bandH(h)))
	gap := w - VW(l) - VW(r) - 2
	if gap < 1 {
		gap = 1
	}
	return " " + l + strings.Repeat(" ", gap) + r
}

// tabBar is the real dashboard's tab row (internal/tui/view.go's tabBarSegs):
// Commands and Workers first, each with its count, then Repo, the tracker tab
// and Config, three spaces apart.
func tabBar(sp screenSpec, w int, tracker string, empty bool) string {
	trackerTab := "Beads 227"
	switch tracker {
	case "github":
		trackerTab = "Issues 99"
	case "prs":
		trackerTab = "PRs 5"
	}
	defs := []string{
		"Commands " + itoa(len(commandEntries)),
		"Workers " + itoa(len(liveSectionRows(empty))),
		"Repo", trackerTab, "Config",
	}
	active := sp.tab
	if sp.noMan {
		// No manifest, no commands and no workers: both tabs go.
		defs = defs[tabRepo:]
		active -= tabRepo
	}
	var b strings.Builder
	b.WriteString(" ")
	for i, t := range defs {
		if i > 0 {
			b.WriteString("   ")
		}
		label := " " + t + " "
		if i == active {
			b.WriteString(pill.Render(label))
		} else {
			b.WriteString(dim.Render(label))
		}
	}
	return Pad(b.String(), w)
}

// overlayQuit is the confirm the dashboard raises on quit while it is hosting
// running processes (admin-3rgp.63). It lists ONLY hosted processes — a Worker
// runs in its own pane and outlives the dashboard, so killing it here would be
// wrong.
func overlayQuit(rows []string, w, h int) []string {
	bw := 60
	box := []string{"┌" + strings.Repeat("─", bw) + "┐"}
	add := func(s string) { box = append(box, "│"+Pad(s, bw)+"│") }
	var hosted []*liveRow
	for i := range liveRows {
		if liveRows[i].hosted {
			hosted = append(hosted, &liveRows[i])
		}
	}
	add("  " + bo.Render("Quit — "+itoa(len(hosted))+" processes still running"))
	add("")
	for _, lr := range hosted {
		add("    " + liveStyle[lr.state].Render(Pad(lr.state, 9)) + Pad(lr.cmd, 10) +
			dim.Render(trunc(lr.doing, bw-28)))
	}
	add("")
	add("    " + dim.Render("Workers keep running — they have their own panes."))
	add("")
	add("    " + rd.Render("⏎ kill them and quit") + "     " + dim.Render("esc cancel"))
	box = append(box, "└"+strings.Repeat("─", bw)+"┘")
	return placeBox(rows, box, w, h, bw)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func overlaySheet(rows []string, w, h int) []string {
	bw := 60
	box := []string{"┌" + strings.Repeat("─", bw) + "┐"}
	add := func(s string) { box = append(box, "│"+Pad(s, bw)+"│") }
	add("  " + bo.Render("Dispatch admin-3rgp.29"))
	add("")
	add("    " + dim.Render("agent     ") + "claude")
	add("    " + dim.Render("model     ") + "sonnet")
	add("    " + dim.Render("branch    ") + "two-deployment-paths")
	add("    " + dim.Render("lands to  ") + cy.Render("feat/one-tool-go-merge"))
	add("")
	add("    " + gr.Render("⏎ dispatch") + "     e edit     " + dim.Render("esc cancel"))
	box = append(box, "└"+strings.Repeat("─", bw)+"┘")
	return placeBox(rows, box, w, h, bw)
}

// placeBox centers box over rows. Extracted so the quit confirm and the
// dispatch sheet share one placement rather than two copies that drift.
func placeBox(rows, box []string, w, h, bw int) []string {
	top := (h - len(box)) / 2
	left := (w - bw - 2) / 2
	if left < 0 {
		left = 0
	}
	for i, l := range box {
		y := top + i
		if y < 3 || y >= len(rows)-2 {
			continue
		}
		base := []rune(Strip(rows[y]))
		for len(base) < w {
			base = append(base, ' ')
		}
		tail := ""
		if left+bw+2 < len(base) {
			tail = string(base[left+bw+2:])
		}
		rows[y] = string(base[:left]) + l + tail
	}
	return rows
}

// keys is the footer, the real dashboard's for the same row
// (internal/tui/work.go's workFooterPairs, then view.go's keymap appending
// `o config` and `←→ tab`). On the Commands and Workers tabs it follows what
// the cursor is on — ↵ is the row's one primary action and its verb changes
// with the row's state (rowAction): attach while it runs, dispatch while it
// does not, transcript on a finished worker. Kill is `x`, shown only on a live
// row: `k` is cursor-up everywhere in this dashboard. `c collapse` folds the
// Commands tab's targets, so the Workers tab does not offer it.
func keys(sp screenSpec, action string) string {
	if sp.tab <= tabWorkers && !sp.hosted && !sp.sheet {
		k := "↑↓ move · ↵ " + action
		if action == "attach" {
			k += " · x kill"
		}
		if sp.tab == tabCommands {
			k += " · c collapse"
		}
		return k + " · q/esc quit · o config · ←→ tab"
	}
	switch {
	case sp.sheet && sp.running:
		return "⏎ kill them and quit · esc cancel"
	case sp.sheet:
		return "⏎ dispatch · e edit · esc cancel"
	case sp.hosted:
		return "esc detach · typing goes to the process"
	}
	return "↑↓ move · ←→ tab · q quit"
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

func trunc(s string, n int) string {
	r := []rune(s)
	if n < 2 {
		return ""
	}
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// scrollWindow mirrors internal/tui/view.go's window() exactly — the same
// "keep the cursor roughly centered" rule every other tab already scrolls
// by, reimplemented here rather than imported since this package can't
// reach that unexported helper.
// markedIdx returns the index of the line in ls carrying the cursor glyph
// mark() draws on the selected row, or 0 if nothing is marked. workBody
// tracks that index itself (cursorLeftIdx) as it builds `left`; here the
// left column is assembled by paletteBoxFit and renderWorkersColumn, which
// already mark the selected row, so this just finds it rather than
// threading a second index through both.
func markedIdx(ls []string) int {
	for i, l := range ls {
		if strings.Contains(l, "◆") {
			return i
		}
	}
	return 0
}

func scrollWindow(total, cursor, height int) (int, int) {
	if total <= height {
		return 0, total
	}
	start := cursor - height/2
	if start < 0 {
		start = 0
	}
	if start > total-height {
		start = total - height
	}
	return start, start + height
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
