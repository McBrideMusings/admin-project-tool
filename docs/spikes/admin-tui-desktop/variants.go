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
		// commands-palette is FIRST on purpose — this is the resolved design
		// (grill-me round 3, Q2: "compact strip plus search box, full list
		// gone by default"). The first Values entry is what launching the
		// prototype actually lands on, so putting it anywhere else silently
		// contradicts the answer that was picked. "commands" (the flat list,
		// below) is the old design kept for direct ⌥s comparison, not the
		// default anymore. Reordering renames no file — stateName() keys
		// dumps by value, not position — it only changes launch default and
		// ⌥s cycle order.
		"commands-palette", "commands-palette-empty", "commands-palette-typing", "commands-palette-browse",
		// The ten reference frames .19 names.
		"commands", "commands-workflow-selected", "repo", "issues", "issues-selected",
		"dispatch-sheet", "swarm", "config", "running", "no-manifest",
		// Every other surface repo-dash draws — see INVENTORY.md. Each is the real
		// frame, reached by driving the same keys a person would press.
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
	// Live-only on purpose, same as data/tracker: this is the hover-preview
	// spike (spike/work-tab-redesign) under review, not a settled design yet —
	// dumping it would multiply the ten reference frames by four and pin a
	// direction nobody picked. Try each on "running" (cursor on the waiting
	// `test` row, hosted) and "swarm" (cursor on the running `admin-3rgp.36`
	// worker, no local output) — those are the two screens that already put
	// the cursor on a live row.
	{Key: "hover", Label: "Hover", Values: []string{"off", "auto-tail", "split-pane", "inline-peek"}, Hotkey: "v", NoDump: true},
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
		resetPalette()
		resetPaletteMRU()
		resetLiveRows()
	},
	OnDump: func() { Blocking = true },
}}

// ---- the real tabs, rendered by repo-dash itself ----

type repoSrc struct{ root, nwo, branch string }

// Digit keys index the drawn tabs (model.go:349 — a tab renumbered by
// admin-3rgp.20, which gave every project with an admin.toml a real Work
// tab ahead of Repo). Every screen below that doesn't override the
// `tracker` axis renders against trackerRepo("beads") (fixtures.go), a
// temp-dir repo this prototype builds itself rather than one of three live
// checkouts on the developer's machine (admin-p55x) — so its four real drawn
// tabs are 1 Work, 2 Repo, 3 the tracker (Beads here), 4 Config. Each of the
// three fixtures is built to draw exactly that four-tab shape (fixtures.go's
// axisManifest), which is what keeps these constants safe to hardcode: they
// index a tab SET the fixture guarantees, not one inferred from a repo that
// could change shape under the prototype. Everything past the tab is the
// same key a person presses — see INVENTORY.md for the full map (written
// before admin-3rgp.20 landed and not renumbered here, since admin-3rgp.31
// retires this whole prototype next).
const (
	keyRepo    = "2"
	keyTracker = "3"
	keyConfig  = "4"
)

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

	"config":       {keyConfig},
	"config-local": {keyConfig, "tab"},
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
	tab     int
	noMan   bool
	sheet   bool
	cmdSel  int
	running bool
	// hosted is the terminal view over a Command's hosted pty — the dashboard
	// runs it on a pty it owns rather than in a herdr pane, and this is where
	// that output is read. Full width: a third of the screen truncates every
	// log line to "14:22:09 [INFO] test: ======…", which is unreadable.
	hosted      bool
	hostedEmpty bool
	// demoQuery seeds the palette's typing screen for a deterministic dump —
	// live typing overrides it the instant paletteQuery is non-empty (see
	// renderPaletteTyping).
	demoQuery string
}

// cmdSel indices below are row positions in workRows()'s always-expanded
// list: 0 build, 1-4 build's four targets (mac/ios/tv/server), 5 check,
// 6 deploy, 7 docs, 8 logs, 9 retire-merged, 10 test, 11-13 the three
// workers. Submenus no longer collapse, so unlike before this change these
// numbers don't shift depending on what's expanded — they're just each
// screen's row, once.
var screens = map[string]screenSpec{
	"commands":                   {tab: 0, cmdSel: 6},                 // deploy — an ordinary idle command
	"commands-workflow-selected": {tab: 0, cmdSel: 9},                 // retire-merged — a workflow row
	"running":                    {tab: 0, cmdSel: 10, running: true}, // test — hosted, waiting
	"repo":                       {tab: 1},
	"issues":                     {tab: 2},
	"issues-selected":            {tab: 2},
	"dispatch-sheet":             {tab: 2, sheet: true},
	"swarm":                      {tab: 0, cmdSel: 11}, // admin-3rgp.36 — worker, running
	"config":                     {tab: 3},
	"no-manifest":                {tab: 1, noMan: true},

	// Hosted-process surfaces (admin-3rgp.61/.63). Appended after the ten named
	// reference frames so no existing frame filename shifts.
	"hosted":       {tab: 0, cmdSel: 10, hosted: true},
	"hosted-empty": {tab: 0, cmdSel: 10, hosted: true, hostedEmpty: true},
	"hosted-quit":  {tab: 0, cmdSel: 10, sheet: true, running: true},

	// The command-palette redesign — see the block above render() for the
	// fixture and interaction. All four render through their own body
	// function (isPalette in render()), never workBody/hostedBody.
	"commands-palette":       {tab: 0},
	"commands-palette-empty": {tab: 0},
	// "dev c" — first word already locked (typed or tabbed to "dev "),
	// second word in progress ("c") — the one frame that shows both a
	// completed word AND a ghost suggestion for the next one at once.
	"commands-palette-typing": {tab: 0, demoQuery: "dev c"},
	"commands-palette-browse": {tab: 0},

	// The embedded modes. tab drives only the prototype's own tab bar and
	// footer; the body comes from repo-dash via the key sequence in `embedded`.
	"repo-commits":      {tab: 1},
	"repo-filter":       {tab: 1},
	"repo-new-worktree": {tab: 1},
	"tracker-detail":    {tab: 2},
	"tracker-graph":     {tab: 2},
	"tracker-blocking":  {tab: 2},
	"tracker-tree":      {tab: 2},
	"tracker-filter":    {tab: 2},
	"config-local":      {tab: 3},
}

var selDelta int

var workTabs = []string{"commands", "repo", "issues", "config"}

func onKey(key string, s State, set func(axis, value string)) bool {
	cur := s.Get("screen")
	if cur == "" {
		cur = "commands-palette"
	}
	if isPalette(cur) {
		return onPaletteKey(key, cur, set)
	}
	// "p" reaches the palette redesign from the original flat list, so both
	// are one keypress apart for direct comparison — the ask this spike
	// round exists to answer.
	// tab==0 && !hosted && !sheet: the plain list and its detail-focused
	// screens, never the hosted pty view (its own footer promises "typing
	// goes to the process" — 'p' must reach it, not hijack it) or an open
	// overlay (its own keys own every keystroke while it's up).
	if key == "p" {
		if sp := screens[cur]; sp.tab == 0 && !sp.hosted && !sp.sheet {
			set("screen", "commands-palette")
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
		return true
	case "shift+tab", "left", "h":
		set("screen", workTabs[(idx-1+len(workTabs))%len(workTabs)])
		selDelta = 0
		return true
	case " ":
		// Only "inline-peek" binds space — the other two hover modes already
		// show their preview without a keypress, so space would either do
		// nothing (confusing on its own) or double as a second way to reach
		// the same state (redundant). Only a hosted row (hasRing) has
		// anything to peek; a worker's row has no local bytes regardless of
		// hover mode.
		if s.Get("hover") != "inline-peek" {
			return false
		}
		if c := cmdUnderCursor(cur); c != nil {
			for i := range liveRows {
				if liveRows[i].cmd == c.name && liveRows[i].hasRing {
					peeked[c.name] = !peeked[c.name]
					return true
				}
			}
		}
		return false
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
		case "commands", "commands-workflow-selected", "running", "swarm":
			set("screen", "hosted")
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
			set("screen", "commands")
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

// cmdUnderCursor resolves the command the cursor is sitting on, following the
// same wrap the renderer uses so what a key acts on is always the row you can
// see highlighted. A child row resolves to its parent, so ← closes the submenu
// you are standing inside rather than doing nothing.
func cmdUnderCursor(screen string) *cmdRow {
	sp, ok := screens[screen]
	if !ok || sp.tab != 0 {
		return nil
	}
	rows := workRows(false)
	if len(rows) == 0 {
		return nil
	}
	return rows[wrap(sp.cmdSel, len(rows))].cmd
}

func wrap(base, n int) int {
	if n <= 0 {
		return 0
	}
	return ((base+selDelta)%n + n) % n
}

type cmdRow struct {
	name, desc, last, kind, arch, note string
	steps                              [][4]string
	targets                            []string
	// subs are a multi-target command's declared targets: a submenu that opens
	// under the row rather than a separate screen, so picking one never leaves
	// the list. Each is {key, what it runs}.
	subs [][2]string
}

var cmds = []cmdRow{
	{name: "build", desc: "Build: mac | ios | tv | server [--clean]", last: "2h ago",
		kind: "multi-target", arch: "apple",
		steps: [][4]string{{"1", "multi-target", "build", "apple"}},
		subs: [][2]string{
			{"mac", "@build-mac"},
			{"ios", "@build-ios"},
			{"tv", "@build-tv"},
			{"server", "cargo build --release --manifest-path server/Cargo.toml"},
		}},
	{name: "check", desc: "Parse and resolve admin.toml", last: "2h ago", kind: "shell", arch: "(manifest)",
		steps: [][4]string{{"1", "shell", "admin check .", "(manifest)"}}},
	{name: "deploy", desc: "Install to ~/.admin, provision the userscript", last: "18m ago",
		kind: "sequence", arch: "(manifest)", note: "exit 0 · 41s",
		steps: [][4]string{
			{"1", "shell", "bash install.sh", "simple"},
			{"2", "lua", "scripts/provision.lua", "apple"},
			{"3", "shell", "admin check .", "(manifest)"}}},
	{name: "docs", desc: "Serve the docs site with hot reload", last: "—", kind: "shell", arch: "docs",
		steps: [][4]string{{"1", "shell", "bun run docs:dev", "docs"}}},
	{name: "logs", desc: "Tail a log target", last: "—", kind: "logs", arch: "docker",
		steps: [][4]string{{"1", "logs", "admin logs dev", "docker"}}},
	{name: "retire-merged", desc: "Remove worktrees whose branch is merged", last: "never",
		kind: "workflow", arch: "(manifest)",
		targets: []string{"fix-detector-parity-skip", "go-transient-kind-factories", "docs-adr-lua", "launchd-env-file"}},
	{name: "test", desc: "pytest plus the shell suite", last: "6m ago", kind: "sequence", arch: "(manifest)",
		steps: [][4]string{
			{"1", "go", "go test ./...", "go-cli"},
			{"2", "shell", "bash tests/test_install.sh", "(manifest)"}}},
}

// liveRow is one live thing. `hosted` is the distinction that matters and the
// one the old "pane N" column got wrong: a hosted row is a CHILD PROCESS on a
// pty this dashboard owns — it dies when the dashboard quits, and its output
// is readable in-app. A detached row is a herdr pane running its own agent
// session: not a child, not ours to kill on quit, reachable only by focusing
// that pane. The two need different verbs and different lifetimes, so the list
// has to say which it is at a glance rather than implying "pane" for both.
type liveRow struct {
	state, id, doing, cmd string
	hosted                bool
	pane                  string // herdr pane id — empty for a hosted child

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
}

// liveRowsSeed / liveRows: liveRows now grows at runtime — dispatching a
// palette entry materializes it here (dispatchPalette), and `k` removes it
// (killLiveRow) — so, same as paletteMRU/paletteMRUSeed, a dump has to
// restore the seed every frame or an earlier interactive dispatch would
// leak into it.
var liveRowsSeed = []liveRow{
	{state: "running", id: "admin-3rgp.36", doing: "writing internal/lua/host.go", pane: "pane 3",
		lastCmd: "review admin-3rgp.36 (workflow: review-changes)",
		hasRing: true, tail: []string{
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
		lastCmd: "implement admin-3rgp.7", hasRing: true, tail: []string{
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
		lastCmd: "implement admin-3rgp.33", hasRing: true, tail: []string{
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
	{state: "running", id: "admin docs", doing: "vitepress dev · localhost:5173", cmd: "docs", hosted: true,
		lastCmd: "bun run docs:dev", hasRing: true,
		logPath: "~/.admin/logs/docs.log", urls: []string{"http://localhost:5173"},
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
	{state: "waiting", id: "admin test", doing: "Enter a number, or q to cancel:", cmd: "test", hosted: true,
		lastCmd: ".venv/bin/pytest -q", hasRing: true,
		logPath: "~/.admin/logs/test.log",
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

type workRow struct {
	cmd    *cmdRow
	worker *liveRow
	// target is set on a submenu row: the index into cmd.subs, and whether it
	// is the last one (which decides the tree connector).
	target  int
	isChild bool
	last    bool
}

// hasSubs reports whether c has a submenu worth showing. A command with
// exactly one target doesn't: the CLI already runs a lone target without
// asking, so a submenu of one would offer nothing to pick. Every command
// that does have subs shows them always, directly under its row — there is
// no collapsed state left to gate on and no arrow left to disclose it.
func hasSubs(c *cmdRow) bool { return len(c.subs) > 1 }

func workRows(empty bool) []workRow {
	if empty {
		return nil
	}
	var out []workRow
	for i := range cmds {
		out = append(out, workRow{cmd: &cmds[i]})
		if !hasSubs(&cmds[i]) {
			continue
		}
		for j := range cmds[i].subs {
			out = append(out, workRow{
				cmd: &cmds[i], target: j, isChild: true,
				last: j == len(cmds[i].subs)-1,
			})
		}
	}
	for i := range liveRows {
		if liveRows[i].cmd == "" {
			out = append(out, workRow{worker: &liveRows[i]})
		}
	}
	return out
}

func mark(sel bool, s string) string {
	if sel {
		return cy.Render("◆ ") + s
	}
	return dim.Render("◇ ") + s
}

func workBody(sp screenSpec, w, h int, empty bool, hover string) []string {
	rows := workRows(empty)
	if len(rows) == 0 {
		return []string{
			" " + bo.Render("Work") + dim.Render("  0"),
			strings.Repeat("─", w),
			dim.Render("   nothing here yet — admin.toml declares no commands"),
		}
	}
	sel := wrap(sp.cmdSel, len(rows))
	// bottomOrient: weight width by the cell aspect ratio, then compare.
	// 2.3 (not 2.0) because pierce's real terminal measured 132×59 — a
	// raw ratio of 2.24 — and read as visually tall; 2.0 called that
	// "wide" by a hair (66 vs 59). 2.3 flips that exact case to bottom
	// while still leaving 96×30 (the ten committed reference frames) and a
	// plainly landscape 70×30 window classified as wide, so the built-in
	// numbers this repo already ships stay unchanged.
	bottomOrient := hover != "off" && float64(w)/2.3 < float64(h)
	var lw, rw int
	if bottomOrient {
		lw = maxInt(w-2, 20)
		rw = lw
	} else {
		lw = w*2/3 - 2
		rw = w - lw - 3
	}
	dw := maxInt(4, lw-34)

	live := map[string]*liveRow{}
	for i := range liveRows {
		if liveRows[i].cmd != "" {
			live[liveRows[i].cmd] = &liveRows[i]
		}
	}

	var left []string
	// cursorLeftIdx is the line in `left` the selected row actually landed
	// on — needed for scrolling below, since `left` has more lines than
	// `rows` (subtext, blanks, peeked tails), so the row index `sel` alone
	// can't say which rendered line to keep on screen.
	cursorLeftIdx := 0
	left = append(left, dim.Render(" Commands"))
	for i, r := range rows {
		if r.cmd == nil {
			continue
		}
		// A submenu row: a tree connector, then the target key in the same
		// name column every other row uses, so the submenu reads as belonging
		// to the row above it rather than as a new list.
		if r.isChild {
			conn := "├ "
			if r.last {
				conn = "└ "
			}
			sub := r.cmd.subs[r.target]
			key := Pad(sub[0], 14)
			if i == sel {
				key = cy.Render(key)
				cursorLeftIdx = len(left)
			}
			left = append(left, mark(i == sel, "")+dim.Render(conn)+key+
				dim.Render(trunc(sub[1], maxInt(6, lw-24))))
			continue
		}
		// No disclosure arrow: a multi-target command's submenu is always
		// shown below it (see hasSubs), so there is nothing left to signal
		// as openable — the name column is the same width every row uses.
		if i == sel {
			cursorLeftIdx = len(left)
		}
		row := mark(i == sel, Pad(r.cmd.name, 16))
		var lr *liveRow
		if v, ok := live[r.cmd.name]; ok {
			lr = v
			row += liveStyle[lr.state].Render(Pad(lr.state, 9)) + liveWhere(lr, dw)
		} else {
			row += dim.Render(Pad(r.cmd.kind, 9) + trunc(r.cmd.desc, maxInt(6, lw-27)))
		}
		left = append(left, row)
		if hover != "off" && lr != nil {
			left = append(left, subtextRow(lr, lw))
			if hover == "inline-peek" && lr.hasRing && peeked[lr.cmd] {
				left = append(left, peekLines(lr, lw)...)
			}
		}
	}
	left = append(left, "", dim.Render(" Workers"))
	for i, r := range rows {
		if r.worker == nil {
			continue
		}
		if i == sel {
			cursorLeftIdx = len(left)
		}
		left = append(left, mark(i == sel, Pad(r.worker.id, 16))+
			liveStyle[r.worker.state].Render(Pad(r.worker.state, 9))+
			liveWhere(r.worker, dw))
		if hover != "off" {
			left = append(left, subtextRow(r.worker, lw))
		}
	}

	if bottomOrient {
		// This is a PREVIEW, not an attach — attaching for real is the
		// existing full-screen "hosted" view (hostedBody, reached with `a`),
		// which already owns the whole frame and its own detach/kill footer.
		// A preview earns real space but not the whole screen, so it gets a
		// capped band: never smaller than previewMinH (so it isn't a sliver
		// even on a short terminal), never bigger than previewMaxH (so it
		// isn't the whole screen on a tall one either) — a fixed max height
		// like the attach view's own full-screen case gets NO cap because it
		// isn't this code path at all.
		const previewMinH, previewMaxH = 6, 16
		const gapRows = 2 // breathing room between the list and the preview

		listH := len(left)
		if cap := maxInt(6, h*2/5); listH > cap {
			listH = cap
		}
		if listH > h {
			listH = h
		}
		detailH := h - listH - gapRows - 1
		if detailH > previewMaxH {
			detailH = previewMaxH
		}
		if detailH < previewMinH && h-listH-gapRows-1 >= previewMinH {
			detailH = previewMinH
		}
		if detailH < 0 {
			detailH = 0
		}
		// Reasonable width for the preview band alone — the list above it
		// still uses the full lw, but a preview that also went edge to edge
		// on a very wide terminal would read as more important than it is.
		previewW := lw
		if previewW > 100 {
			previewW = 100
		}
		detail := detailOf(rows[sel], previewW, maxInt(detailH, 1), hover)

		// The list only gets a fraction of the height here (unlike the
		// right-split layout, which hands it the whole frame), so it now
		// overflows far more readily — scroll it exactly the way every
		// other tab already does (internal/tui/view.go's window): keep the
		// cursor on screen, "↑ N more" / "↓ more" mark what's hidden.
		listLines := left
		if len(left) > listH {
			start, end := scrollWindow(len(left), cursorLeftIdx, listH)
			listLines = append([]string{}, left[start:end]...)
			if start > 0 && len(listLines) > 0 {
				listLines[0] = dim.Render("  ↑ " + itoa(start) + " more")
			}
			if end < len(left) && len(listLines) > 0 {
				listLines[len(listLines)-1] = dim.Render("  ↓ more")
			}
		}

		out := make([]string, 0, h)
		for i := 0; i < listH; i++ {
			out = append(out, " "+Pad(at(listLines, i), lw))
		}
		for i := 0; i < gapRows && len(out) < h; i++ {
			out = append(out, "")
		}
		if detailH > 0 {
			out = append(out, dim.Render(strings.Repeat("─", w)))
			for i := 0; i < detailH; i++ {
				out = append(out, " "+Pad(at(detail, i), previewW))
			}
		}
		for len(out) < h {
			out = append(out, "")
		}
		return out[:h]
	}

	right := detailOf(rows[sel], rw, h, hover)
	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		out = append(out, " "+Pad(at(left, i), lw)+dim.Render("│ ")+at(right, i))
	}
	return out
}

// peeked tracks which hosted rows (by command name) are expanded inline
// under "inline-peek" — several at once, since each is independent. Live
// state outside State, so Reset must clear it or a dump inherits whatever
// the last keypress left open (in practice never dumped: hover is NoDump).
var peeked = map[string]bool{}

// subtextRow is the hover-preview spike's other half of the brief: the last
// command/instruction actually sent to a live row, in small dim text
// directly beneath it — independent of which hover treatment is picked,
// since it costs one line and answers a question ("what did I just run
// here?") none of the three treatments otherwise answer at a glance.
func subtextRow(lr *liveRow, w int) string {
	return dim.Render("     └ " + trunc(lr.lastCmd, maxInt(4, w-9)))
}

// peekLines is "inline-peek"'s expansion: a few lines of lr's own tail,
// indented under its row with the same tree-connector glyph the multi-target
// submenu already uses, so this reads as the same kind of disclosure rather
// than a second mechanism.
func peekLines(lr *liveRow, w int) []string {
	n := 5
	t := lr.tail
	if len(t) > n {
		t = t[len(t)-n:]
	}
	out := make([]string, len(t))
	for i, l := range t {
		conn := "  │ "
		if i == len(t)-1 {
			conn = "  └ "
		}
		out[i] = dim.Render("   " + conn + trunc(l, maxInt(4, w-9)))
	}
	return out
}

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
func hostedBody(w, h int, empty bool) []string {
	// The header states what the process is actually doing, and the output below
	// has to agree: this run ends on the multi-target picker, so it is waiting for
	// input, not running.
	out := []string{
		" " + yl.Render("◐") + " " + bo.Render("test") + dim.Render("  ·  waiting  ·  0m38s  ·  ") +
			"Enter a number, or q to cancel:",
		dim.Render(strings.Repeat("╌", w)),
	}
	if empty {
		return append(out, "", " "+dim.Render("waiting for output…"))
	}
	body := hostedOut
	// The child's footer is not optional chrome — it is the log path. Reserve
	// its rows first and let the output scroll, rather than letting it fall off.
	if n := h - len(out) - 3; n > 0 && n < len(body) {
		body = body[len(body)-n:]
	}
	for _, l := range body {
		out = append(out, " "+outLine(l, w-2))
	}
	el := "0m38s"
	out = append(out, "",
		" "+dim.Render("─── test "+strings.Repeat("─", maxInt(0, w-22-VW(el)))+" "+el+" ───"),
		" "+dim.Render("log")+"  "+cy.Render("~/.admin/logs/test.log"))
	return out
}

// detailOf is the polymorphic half: the pane's shape follows the row's kind, so
// a command and a worker never have to be squeezed into one field set. hover
// is the hover-preview spike's axis — "off" reproduces the shipped pane
// exactly; the other three are compared in place against everything else
// this prototype already draws (the dispatch sheet, the full hosted view,
// tab switching), which is the whole point of putting them here rather than
// in a prototype of their own.
func detailOf(r workRow, w, h int, hover string) []string {
	if r.worker != nil {
		wk := r.worker
		if hover == "auto-tail" && wk.hasRing {
			// Header: who and where, plus the ONE fact worth keeping —
			// nothing else. Branch/worktree/elapsed are gone in this mode
			// on purpose, same rule as a hosted command: the rest of the
			// space is the tail, not a field list.
			out := []string{
				bo.Render(wk.id),
				liveStyle[wk.state].Render(wk.state) + dim.Render("  ·  ") + whereLabel(wk),
				"",
			}
			n := maxInt(4, h-len(out)-1)
			out = append(out, tailLines(wk.tail, n, w)...)
			return append(out, dim.Render("a")+" attach   "+dim.Render("k")+" kill")
		}
		base := []string{
			bo.Render(wk.id),
			liveStyle[wk.state].Render(wk.state) + dim.Render("  ·  ") + whereLabel(wk),
			"",
			dim.Render("now"),
			"  " + trunc(wk.doing, w-3),
			"",
			dim.Render("branch"),
			"  " + cy.Render("embed-lua-vm"),
			dim.Render("worktree"),
			"  " + dim.Render("~/.worktrees/…/embed-lua-vm"),
			dim.Render("elapsed"),
			"  12m",
		}
		if hover == "split-pane" {
			base = append(base, "", dim.Render("┌─ output "+strings.Repeat("─", maxInt(w-11, 0))))
			base = append(base, tailBoxed(wk.tail, 6, w)...)
			base = append(base, dim.Render("└"+strings.Repeat("─", maxInt(w-1, 0))))
		}
		return append(base, "", dim.Render("a")+" attach   "+dim.Render("k")+" kill")
	}
	c := r.cmd
	// A selected target names itself as "<command> <target>" — the same shape
	// you would type — and shows what that one target actually runs, which is
	// the question the submenu exists to answer.
	if r.isChild {
		sub := c.subs[r.target]
		return []string{
			bo.Render(c.name + " " + sub[0]),
			dim.Render(c.kind + "  ·  " + c.arch),
			"",
			dim.Render("runs"),
			"  " + trunc(sub[1], w-3),
			"",
			dim.Render("log"),
			"  " + dim.Render("~/.admin/logs/"+c.name+"-"+sub[0]+".log"),
			"",
			dim.Render("↵") + " run",
		}
	}
	// A hosted command (docs, test) has a real Ring to show — auto-tail
	// replaces the whole pane with it, split-pane appends it below the
	// fields that are already here. Neither applies to an idle command
	// (build, check, …), which has no lastLive at all.
	var lastLive *liveRow
	for i := range liveRows {
		if liveRows[i].cmd == c.name {
			lastLive = &liveRows[i]
		}
	}
	if lastLive != nil && lastLive.hasRing && hover == "auto-tail" {
		// No "steps" here, no re-rendered child footer either — the header
		// is exactly what internal/frame's own pinned footer already
		// resolves for this command (log path, then its urls), because
		// that's real configured information, not the process's own
		// stdout replayed a second time. Everything past it is tail.
		out := []string{
			bo.Render(c.name),
			liveStyle[lastLive.state].Render(lastLive.state),
			"",
			dim.Render("log") + "  " + dim.Render(lastLive.logPath),
		}
		for _, u := range lastLive.urls {
			out = append(out, dim.Render("url")+"  "+cy.Render(u))
		}
		out = append(out, "")
		n := maxInt(4, h-len(out)-1)
		out = append(out, tailLines(lastLive.tail, n, w)...)
		return append(out, dim.Render("a")+" attach")
	}

	out := []string{
		bo.Render(c.name),
		dim.Render(c.kind + "  ·  " + c.arch),
		"",
	}
	out = append(out, dim.Render("log"), "  "+dim.Render("~/.admin/logs/"+c.name+".log"), "")
	if c.targets != nil {
		out = append(out, dim.Render("targets  query · 4"))
		for _, t := range c.targets {
			out = append(out, "  "+trunc(t, w-3))
		}
		return out
	}
	out = append(out, dim.Render("steps"))
	for _, st := range c.steps {
		out = append(out, "  "+dim.Render(st[0]+" ")+st[1])
		out = append(out, "    "+trunc(st[2], w-5))
		out = append(out, "    "+dim.Render("from ")+mg.Render(st[3]))
	}
	if lastLive != nil && lastLive.hasRing && hover == "split-pane" {
		out = append(out, "", dim.Render("┌─ output "+strings.Repeat("─", maxInt(w-11, 0))))
		out = append(out, tailBoxed(lastLive.tail, 6, w)...)
		out = append(out, dim.Render("└"+strings.Repeat("─", maxInt(w-1, 0))))
	}
	return out
}

// tailLines returns the last n lines of tail, padded to exactly n lines so
// the pane never jitters as output scrolls in — the same "fixed height" rule
// the split-pane box below relies on for the opposite reason (an empty box
// that doesn't move once something starts running).
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
		out[i] = dim.Render(trunc(t[i-pad], w))
	}
	return out
}

func tailBoxed(tail []string, n, w int) []string {
	lines := tailLines(tail, n, w-2)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = dim.Render("│ ") + l
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
// paletteEntries is deliberately separate from `cmds`/liveRows above — the
// original ten reference frames (commands.txt, swarm.txt, …) render from
// that smaller admin-project-tool-shaped fixture and must not shift under
// this change. The palette screens are new frames, appended to the screen
// axis, with their own fixture.

// paletteEntry is one FULLY RESOLVED invocation — what actually gets
// dispatched. "deploy grafana" is an entry; "deploy" alone (the parent,
// ambiguous among 8 targets) never is.
type paletteEntry struct {
	parent, target, run string
}

func (e paletteEntry) resolved() string {
	if e.target == "" {
		return e.parent
	}
	return e.parent + " " + e.target
}

// paletteParent is the browse screen's view — same shape the flat list
// already renders today (commands.txt, swarm.txt), just against the denser
// fixture, to prove browse mode still works at this scale rather than
// merely existing in theory.
type paletteParent struct {
	name, kind, desc string
	targets          []string // resolved suffixes, "" entry means no targets
}

var paletteParents = []paletteParent{
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
	{name: "grafana", kind: "multi-target", desc: "Grafana alert rules (export / diff / deploy)",
		targets: []string{"deploy", "diff", "export"}},
	{name: "xplay-trail", kind: "multi-target", desc: "Trail xplay transfers live (preview|staging|prod, or raw <flags>)",
		targets: []string{"preview", "prod", "raw", "staging"}},
	{name: "lint", kind: "shell", desc: "Lint and auto-fix all apps and packages"},
	{name: "fmt", kind: "shell", desc: "Format all files"},
	{name: "clean", kind: "shell", desc: "Remove build artifacts"},
}

// paletteEntries is paletteParents flattened to the resolved shape fuzzy
// search actually matches against — the whole point being that "cf" has to
// find "dev cf" and "kill cf" directly, never just "dev" or "kill".
var paletteEntries = func() []paletteEntry {
	var out []paletteEntry
	for _, p := range paletteParents {
		if len(p.targets) == 0 {
			out = append(out, paletteEntry{parent: p.name, run: "bash " + p.name + ".sh"})
			continue
		}
		for _, t := range p.targets {
			out = append(out, paletteEntry{parent: p.name, target: t, run: "cd apps && bunx wrangler " + p.name + " --target " + t})
		}
	}
	return out
}()

// paletteMRUSeed / paletteMRU: the strip's live state, seeded PARTIAL (3 of
// the strip's own N=5), never a full guessed set, since a fresh project has
// no history to guess from (grill-me round 3, Q7: "empty or partial...
// never seeded with a guess"). dispatchPalette below is what grows it;
// resetPalette below restores the seed, so a dump is deterministic no
// matter what a prior interactive keypress did to it.
var paletteMRUSeed = []string{"grafana deploy", "dev cf", "test gate"}
var paletteMRU = append([]string{}, paletteMRUSeed...)

const paletteStripN = 5

var (
	paletteQuery  string
	paletteCursor int
	paletteStatus string
)

func isPalette(screen string) bool { return strings.HasPrefix(screen, "commands-palette") }

func resetPalette() {
	paletteQuery, paletteCursor, paletteStatus = "", 0, ""
}

// resetPaletteMRU is the DUMP's reset (called from Variants[0].Reset, not
// from resetPaletteMRU — entering/leaving typing mode must NOT wipe usage
// history, only a fresh dump frame should) — restores the seed so a dump
// is deterministic regardless of what a prior interactive Enter dispatched.
func resetPaletteMRU() {
	paletteMRU = append([]string{}, paletteMRUSeed...)
}

// fuzzyScore is a subsequence match: every rune of query must appear in
// target IN ORDER (case-insensitive); score rewards contiguous runs and a
// literal prefix match, so "cf" ranks "dev cf" and "kill cf" above a
// scattered match like "chat-moderation" — realistic enough for the
// question this spike is answering without pulling in a real fuzzy-match
// library the shipped tool doesn't otherwise depend on.
func fuzzyScore(query, target string) (int, bool) {
	q, t := strings.ToLower(query), strings.ToLower(target)
	if q == "" {
		return 0, true
	}
	score, ti, lastMatch := 0, 0, -1
	for _, qc := range q {
		found := false
		for ; ti < len(t); ti++ {
			if rune(t[ti]) == qc {
				if lastMatch == ti-1 {
					score += 3 // contiguous run
				} else {
					score++
				}
				lastMatch = ti
				ti++
				found = true
				break
			}
		}
		if !found {
			return 0, false
		}
	}
	if strings.HasPrefix(t, q) {
		score += 10
	}
	return score, true
}

// ---- word-by-word completion ----
//
// pierce's correction (round 3, second pass): tab was replacing the WHOLE
// query with the whole resolved string in one press. Real command-palette
// tab-completion goes word by word — "d" Tab → "dev ", then "c" Tab →
// "dev cf" — with the remainder of the top match shown inline, dim, as you
// type, before you ever press Tab. That's what everything below builds:
// paletteWords splits what's already fully typed from the word still being
// typed; paletteCandidates ranks entries consistent with the typed words
// and scores the REST by the in-progress word only (never the whole
// string); paletteGhost is the dim suffix shown after the cursor.

// paletteWords splits query into its complete (space-terminated) words and
// the partial word still being typed — "" if query ends in a space or is
// empty, meaning nothing is in progress yet.
func paletteWords(query string) (complete []string, partial string) {
	fields := strings.Fields(query)
	if query == "" || strings.HasSuffix(query, " ") {
		return fields, ""
	}
	if len(fields) == 0 {
		return nil, ""
	}
	return fields[:len(fields)-1], fields[len(fields)-1]
}

// tokenAt is a resolved entry's word at position idx: 0 is the parent
// command, 1 is its target ("" for a single command like lint/fmt/clean,
// which only ever has a word 0).
func tokenAt(e paletteEntry, idx int) string {
	switch idx {
	case 0:
		return e.parent
	case 1:
		return e.target
	}
	return ""
}

func matchesPrefix(complete []string, e paletteEntry) bool {
	for i, w := range complete {
		t := tokenAt(e, i)
		if t == "" || !strings.HasPrefix(strings.ToLower(t), strings.ToLower(w)) {
			return false
		}
	}
	return true
}

// paletteCandidates is every entry consistent with the words already fully
// typed, ranked by how well the IN-PROGRESS word matches the token at that
// same position — "dev c" ranks by "c" against each candidate's target
// (having already filtered to parent-starts-with-"dev"), never by a fuzzy
// match across the whole resolved string.
func paletteCandidates(query string) (cands []paletteEntry, wordIdx int, partial string) {
	var complete []string
	complete, partial = paletteWords(query)
	wordIdx = len(complete)
	type scored struct {
		e paletteEntry
		s int
	}
	var out []scored
	for _, e := range paletteEntries {
		if !matchesPrefix(complete, e) {
			continue
		}
		tok := tokenAt(e, wordIdx)
		if tok == "" && partial != "" {
			continue // this entry has no word at this position — nothing to match the partial against
		}
		if s, ok := fuzzyScore(partial, tok); ok {
			out = append(out, scored{e, s})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].s != out[j].s {
			return out[i].s > out[j].s
		}
		return out[i].e.resolved() < out[j].e.resolved()
	})
	cands = make([]paletteEntry, len(out))
	for i, sc := range out {
		cands[i] = sc.e
	}
	return cands, wordIdx, partial
}

// paletteGhost is the dim inline suggestion — the rest of the TOP
// candidate's current-word token, past what's already typed. Only shown
// when that token genuinely starts with the typed partial (a real
// completion, not a fuzzy guess dressed up as one).
func paletteGhost(cands []paletteEntry, wordIdx int, partial string) string {
	if len(cands) == 0 {
		return ""
	}
	tok := tokenAt(cands[0], wordIdx)
	if !strings.HasPrefix(strings.ToLower(tok), strings.ToLower(partial)) {
		return ""
	}
	return tok[len(partial):]
}

// paletteAcceptGhost is Tab's effect: lock in the current word (typed text
// plus its ghost) and, if the top candidate has a next word, add a
// trailing space so typing continues straight into it.
func paletteAcceptGhost(query string) string {
	cands, wordIdx, partial := paletteCandidates(query)
	ghost := paletteGhost(cands, wordIdx, partial)
	if ghost == "" {
		return query
	}
	complete, _ := paletteWords(query)
	var b strings.Builder
	for _, w := range complete {
		b.WriteString(w)
		b.WriteString(" ")
	}
	b.WriteString(partial + ghost)
	if tokenAt(cands[0], wordIdx+1) != "" {
		b.WriteString(" ")
	}
	return b.String()
}

// dispatchPalette is Enter's effect: move e to the front of the MRU
// (deduped, capped), and leave a one-line confirmation — the live proof
// that the strip actually updates from real usage, not just a mockup of
// one fixed state.
func dispatchPalette(e paletteEntry) {
	r := e.resolved()
	next := []string{r}
	for _, m := range paletteMRU {
		if m != r {
			next = append(next, m)
		}
	}
	if len(next) > paletteStripN {
		next = next[:paletteStripN]
	}
	paletteMRU = next
	paletteStatus = "dispatched: " + r

	// Materialize it as a live Workers row — the whole point of pierce's
	// correction here: dispatching from the palette has to actually show
	// up somewhere to attach to, kill, or trail, not just move a name to
	// the top of the strip. cmd is left "" on purpose so it renders in
	// Workers (renderWorkersColumn's own filter), same as docs/test above
	// — a palette dispatch and a hosted command are the same shape.
	for i := range liveRows {
		if liveRows[i].cmd == "" && liveRows[i].id == r {
			return // already running — dispatching again just re-selects it
		}
	}
	liveRows = append(liveRows, liveRow{
		state: "running", id: r, doing: "starting…", hosted: true, lastCmd: e.run,
		// hasRing + tail: a palette dispatch is a hosted command exactly like
		// docs/test above (comment at dispatchPalette's own call site says
		// so), so it gets the same shape — a real Ring, not a field-list-only
		// row. A process that just started legitimately has only a line or
		// two of output; tailLines already pads short tails to a fixed
		// height (checked, not assumed — see tailLines above), so this
		// doesn't jitter once real output starts arriving.
		hasRing: true, tail: []string{
			"$ " + e.run,
			"",
			"14:22:58 starting…",
		},
	})
}

// paletteWorkerRows is the Workers section's own data, addressable by
// index for combined cursor math — the same filter renderWorkersColumn
// uses, returned as pointers so killLiveRow can remove exactly the one
// selected.
func paletteWorkerRows() []*liveRow {
	var out []*liveRow
	for i := range liveRows {
		if liveRows[i].cmd == "" {
			out = append(out, &liveRows[i])
		}
	}
	return out
}

func killLiveRow(id string) {
	out := liveRows[:0]
	for _, lr := range liveRows {
		if lr.cmd == "" && lr.id == id {
			continue
		}
		out = append(out, lr)
	}
	liveRows = out
}

// paletteBoxRows is the box's own row list — MRU entries on the idle
// screens, fuzzy candidates on the typing screen — as resolved
// paletteEntry values, so combined cursor math (box rows first, Workers
// rows after) works the same regardless of which screen the cursor is on.
func paletteBoxRows(cur string) []paletteEntry {
	switch cur {
	case "commands-palette", "commands-palette-empty":
		mru := paletteMRU
		if cur == "commands-palette-empty" {
			mru = nil
		}
		var out []paletteEntry
		for _, m := range mru {
			for _, e := range paletteEntries {
				if e.resolved() == m {
					out = append(out, e)
				}
			}
		}
		return out
	case "commands-palette-typing":
		cands, _, _ := paletteCandidates(paletteQuery)
		return cands
	}
	return nil
}

// paletteMoveCursor and paletteActivate are shared between the idle and
// typing screens — both are "box rows first, Workers rows after" over one
// combined index, per pierce's correction ("I can no longer move the
// cursor down to the workers"). Only what counts as a box row differs
// between the two screens (paletteBoxRows already abstracts that).
func paletteMoveCursor(cur string, delta int) {
	total := len(paletteBoxRows(cur)) + len(paletteWorkerRows())
	if total > 0 {
		paletteCursor = (paletteCursor + delta + total) % total
	}
}

// paletteActivate is Enter: dispatch if the cursor is on a box row, attach
// if it's on a Workers row — the two things Enter already means elsewhere
// on this tab (run vs. attach), unified behind one key here too.
func paletteActivate(cur string) {
	box := paletteBoxRows(cur)
	if paletteCursor < len(box) {
		dispatchPalette(box[paletteCursor])
		return
	}
	if wr := paletteWorkerRows(); paletteCursor-len(box) < len(wr) {
		paletteStatus = "attached to " + wr[paletteCursor-len(box)].id
	}
}

// paletteKillSelected is `k` on a Workers row — idle screen only (see
// onPaletteKey: in typing mode every letter is query text, so kill moves
// to ctrl+k there instead of colliding with it).
func paletteKillSelected(cur string) {
	box := paletteBoxRows(cur)
	wr := paletteWorkerRows()
	if idx := paletteCursor - len(box); idx >= 0 && idx < len(wr) {
		killLiveRow(wr[idx].id)
		if paletteCursor > 0 {
			paletteCursor--
		}
	}
}

func onPaletteKey(key string, cur string, set func(axis, value string)) bool {
	switch cur {
	case "commands-palette", "commands-palette-empty":
		switch key {
		case "/":
			resetPalette()
			set("screen", "commands-palette-typing")
			return true
		case "b":
			set("screen", "commands-palette-browse")
			return true
		case "down", "j":
			paletteMoveCursor(cur, 1)
			return true
		case "up":
			paletteMoveCursor(cur, -1)
			return true
		case "enter":
			paletteActivate(cur)
			return true
		case "a":
			if wr := paletteWorkerRows(); paletteCursor-len(paletteBoxRows(cur)) < len(wr) {
				paletteActivate(cur) // same effect as enter on a worker row
			}
			return true
		case "k":
			paletteKillSelected(cur)
			return true
		}
	case "commands-palette-typing":
		switch key {
		case "esc":
			resetPalette()
			set("screen", "commands-palette")
			return true
		case "backspace":
			if paletteQuery != "" {
				r := []rune(paletteQuery)
				paletteQuery = string(r[:len(r)-1])
				paletteCursor = 0
			} else {
				set("screen", "commands-palette")
			}
			return true
		case "tab":
			paletteQuery = paletteAcceptGhost(paletteQuery)
			paletteCursor = 0
			return true
		case "down":
			paletteMoveCursor(cur, 1)
			return true
		case "up":
			paletteMoveCursor(cur, -1)
			return true
		case "enter":
			box := paletteBoxRows(cur)
			onBoxRow := paletteCursor < len(box)
			paletteActivate(cur)
			if onBoxRow {
				resetPalette()
				set("screen", "commands-palette")
			}
			return true
		case "ctrl+k":
			// Plain `k` means kill on the idle screen, but here every
			// letter is query text — ctrl+k reaches the same action
			// without swallowing a real search character.
			paletteKillSelected(cur)
			return true
		default:
			if len([]rune(key)) == 1 {
				paletteQuery += key
				paletteCursor = 0
				return true
			}
		}
	case "commands-palette-browse":
		switch key {
		case "esc", "b":
			set("screen", "commands-palette")
			return true
		}
	}
	return false
}

// paletteBoxFit draws a box sized to its content — height h, never
// stretched to fill the pane. This is the third-pass correction: the
// earlier version filled the WHOLE Work-tab body with the box, which threw
// away the Workers section and the right-hand detail pane entirely
// ("you've completely erased the details view and any space for the
// workers"). The box now sits at the TOP of the left column only, same as
// Alfred/Spotlight/Raycast dock their search box near the top of their own
// window instead of filling it — pierce's own reference screenshots.
func paletteBoxFit(w int, title string, body []string) []string {
	// No leading-margin space this time — unlike the earlier full-height
	// version, this box's caller (renderPaletteScreen) supplies its own
	// leading " " once, for the whole two-column row, so baking a second
	// one in here double-counted a column and left every content row two
	// short of the border rows — the mismatched "┘│" vs "│  │" gap pierce
	// spotted. Budget: "│ "+inner+" │" = w  →  inner = w-4. Top border:
	// "┌─ "+title+" "+dashes+"┐" = w  →  dashes = w-5-VW(title).
	inner := maxInt(w-4, 8)
	dashes := maxInt(w-5-VW(title), 0)
	out := []string{"┌─ " + bo.Render(title) + " " + dim.Render(strings.Repeat("─", dashes)) + "┐"}
	for _, l := range body {
		out = append(out, "│ "+Pad(l, inner)+" │")
	}
	out = append(out, "└"+strings.Repeat("─", w-2)+"┘")
	return out
}

// paletteIdleContent is the box's inner lines for the idle screen — the
// MRU strip. Empty of anything but the "press / to search" hint until
// something's actually been dispatched (never a guessed seed). boxSelected
// is false once the combined cursor has moved past the strip into Workers
// — a row here must not still show ◆ once the real selection has moved on.
func paletteIdleContent(w int, empty, boxSelected bool) []string {
	body := []string{dim.Render("Press ") + bo.Render("/") + dim.Render(" to search · ") + bo.Render("b") + dim.Render(" to browse all"), ""}
	mru := paletteMRU
	if empty {
		mru = nil
	}
	if len(mru) == 0 {
		body = append(body, dim.Render("no commands run yet"))
	} else {
		body = append(body, dim.Render(itoa(len(mru))+" recent"))
		for i, m := range mru {
			var run string
			for _, e := range paletteEntries {
				if e.resolved() == m {
					run = e.run
				}
			}
			body = append(body, mark(boxSelected && i == paletteCursor, Pad(m, 20))+dim.Render(trunc(run, maxInt(6, w-30))))
		}
	}
	if paletteStatus != "" {
		body = append(body, "", gr.Render(paletteStatus))
	}
	return body
}

// paletteTypingContent is the box's inner lines for the typing screen.
// Typed text renders plain, the rest of the top candidate's CURRENT WORD
// renders dim right after the cursor (paletteGhost) — visible before Tab
// is ever pressed — and the candidate list narrows by word position, not
// a whole-string fuzzy match. Capped to a handful of rows: the box no
// longer owns the whole pane, so it can't grow to show everything either.
func paletteTypingContent(w int, demoQuery string, boxSelected bool) []string {
	q := paletteQuery
	if q == "" && Blocking {
		// ONLY substitute for a deterministic dump frame — Blocking is set
		// exclusively inside the dump's OnDump hook, never during live
		// driving, so pressing `/` interactively always starts genuinely
		// empty.
		q = demoQuery
	}
	cands, wordIdx, partial := paletteCandidates(q)
	ghost := paletteGhost(cands, wordIdx, partial)

	body := []string{"/ " + bo.Render(q) + dim.Render(ghost) + cy.Render("▏"), ""}
	if len(cands) == 0 {
		body = append(body, dim.Render("no match"))
		return body
	}
	const maxRows = 5
	cursor := paletteCursor
	if !boxSelected {
		cursor = 0 // scroll window centers on the box's own top row instead
	}
	start, end := scrollWindow(len(cands), cursor, maxRows)
	for i := start; i < end; i++ {
		e := cands[i]
		row := mark(boxSelected && i == paletteCursor, Pad(e.resolved(), 24))
		row += dim.Render(trunc(e.run, maxInt(6, w-34)))
		body = append(body, row)
	}
	body = append(body, dim.Render(itoa(len(cands))+" match"+plural(len(cands))+" · tab complete word · ↵ dispatch"))
	return body
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

// renderWorkersColumn is the Work tab's Workers section, standalone —
// reads liveRows directly (the same underlying data workBody's own copy of
// this loop reads through workRows()'s wrapper) since the palette screens
// have no workRows()-built row list of their own to index into. selIdx is
// this section's own 0-based position of the combined cursor, -1 when
// selection is still up in the box.
func renderWorkersColumn(lw, selIdx int) []string {
	dw := maxInt(4, lw-34)
	out := []string{"", dim.Render(" Workers")}
	i := 0
	for r := range liveRows {
		if liveRows[r].cmd != "" {
			continue
		}
		lr := &liveRows[r]
		out = append(out, mark(i == selIdx, Pad(lr.id, 16))+liveStyle[lr.state].Render(Pad(lr.state, 9))+liveWhere(lr, dw))
		i++
	}
	if i == 0 {
		out = append(out, dim.Render(" no workers running"))
	}
	return out
}

// paletteEntryDetail is the right column for whichever entry the palette
// currently has selected — same job internal/tui/workview.go's
// commandDetail does for the flat list, scaled to what a resolved
// invocation actually has: no steps to enumerate, just what it is and
// what it runs.
func paletteEntryDetail(e paletteEntry, w int) []string {
	var parent paletteParent
	for _, p := range paletteParents {
		if p.name == e.parent {
			parent = p
		}
	}
	out := []string{
		bo.Render(e.resolved()),
		dim.Render(parent.kind + "  ·  " + parent.desc),
		"",
		dim.Render("runs"),
		"  " + trunc(e.run, maxInt(6, w-3)),
	}
	if e.target != "" {
		out = append(out, "", dim.Render("part of"), "  "+e.parent+dim.Render(" ("+itoa(len(parent.targets))+" targets)"))
	}
	return out
}

// paletteWorkerDetail is the right column when the combined cursor has
// moved onto a Workers row — the answer to "when I dispatch dev cf it
// should appear in the workers section and then I can trail its logs, or
// kill it, or do whatever": this is that "whatever", the same shape
// detailOf's worker branch renders for the rest of the tab.
// hover was never threaded through here at all until pierce caught it —
// the footer showed "Hover: auto-tail" while the worker pane kept
// rendering the plain now/last fields regardless. Same treatment
// detailOf's worker branch already has, reused rather than reinvented.
func paletteWorkerDetail(lr *liveRow, w, h int, hover string) []string {
	if hover == "auto-tail" && lr.hasRing {
		out := []string{
			bo.Render(lr.id),
			liveStyle[lr.state].Render(lr.state) + dim.Render("  ·  ") + whereLabel(lr),
			"",
		}
		n := maxInt(4, h-len(out)-1)
		out = append(out, tailLines(lr.tail, n, w)...)
		return append(out, dim.Render("a")+" attach   "+dim.Render("k")+" kill")
	}
	out := []string{
		bo.Render(lr.id),
		liveStyle[lr.state].Render(lr.state) + dim.Render("  ·  ") + whereLabel(lr),
		"",
		dim.Render("now"),
		"  " + trunc(lr.doing, maxInt(6, w-3)),
	}
	if lr.lastCmd != "" {
		out = append(out, "", dim.Render("last"), "  "+trunc(lr.lastCmd, maxInt(6, w-3)))
	}
	if hover == "split-pane" && lr.hasRing {
		out = append(out, "", dim.Render("┌─ output "+strings.Repeat("─", maxInt(w-11, 0))))
		out = append(out, tailBoxed(lr.tail, 6, w)...)
		out = append(out, dim.Render("└"+strings.Repeat("─", maxInt(w-1, 0))))
	}
	return append(out, "", dim.Render("a")+" attach   "+dim.Render("k")+" kill")
}

// renderPaletteScreen assembles the two-column Work-tab skeleton
// (workBody's own shape) around whichever palette content is active: the
// box sits at the top of the LEFT column, Workers renders below it exactly
// as it does everywhere else on this tab, and the RIGHT column stays a
// real detail pane for whatever's selected — box entry or Workers row,
// never erasing the other two.
func renderPaletteScreen(w, h int, boxTitle string, boxBody []string, selEntry *paletteEntry, selWorker *liveRow, workerSelIdx int, hover string) []string {
	// bottomOrient reuses the EXACT same geometry test header() already
	// prints on screen ("w=… h=… w/2.3=…→…") — no hover gate, because that
	// gate belongs to workBody's tail-preview feature, not to whether this
	// screen's own list+detail split should stack. This was the real bug
	// pierce hit: header() always told the truth about the geometry, but
	// this function never listened to it and just hard-coded a left/right
	// split regardless.
	bottomOrient := float64(w)/2.3 < float64(h)

	if bottomOrient {
		fullW := maxInt(w-2, 20)
		left := paletteBoxFit(fullW, boxTitle, boxBody)
		left = append(left, renderWorkersColumn(fullW, workerSelIdx)...)

		var detail []string
		switch {
		case selWorker != nil:
			detail = paletteWorkerDetail(selWorker, fullW, maxInt(h/3, 6), hover)
		case selEntry != nil:
			detail = paletteEntryDetail(*selEntry, fullW)
		}

		const previewMinH, previewMaxH = 6, 14
		detailH := h - len(left) - 1
		if detailH > previewMaxH {
			detailH = previewMaxH
		}
		if detailH < previewMinH && h-len(left)-1 >= previewMinH {
			detailH = previewMinH
		}
		if detailH < 0 {
			detailH = 0
		}

		out := make([]string, 0, h)
		for _, l := range left {
			out = append(out, " "+Pad(l, fullW))
		}
		if detailH > 0 {
			out = append(out, dim.Render(strings.Repeat("─", w)))
			for i := 0; i < detailH; i++ {
				out = append(out, " "+Pad(at(detail, i), fullW))
			}
		}
		for len(out) < h {
			out = append(out, "")
		}
		return out[:h]
	}

	lw := w*2/3 - 2
	rw := w - lw - 3

	left := paletteBoxFit(lw, boxTitle, boxBody)
	left = append(left, renderWorkersColumn(lw, workerSelIdx)...)

	var right []string
	switch {
	case selWorker != nil:
		right = paletteWorkerDetail(selWorker, rw, h, hover)
	case selEntry != nil:
		right = paletteEntryDetail(*selEntry, rw)
	}

	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		out = append(out, " "+Pad(at(left, i), lw)+dim.Render("│ ")+at(right, i))
	}
	return out
}

func renderPaletteBrowse(w, h int) []string {
	bottomOrient := float64(w)/2.3 < float64(h)
	lw := w*2/3 - 2
	if bottomOrient {
		lw = maxInt(w-2, 20)
	}
	rw := w - lw - 3

	var left []string
	for _, p := range paletteParents {
		desc := trunc(p.desc, maxInt(6, lw-len(p.name)-14))
		left = append(left, dim.Render("◇ ")+bo.Render(Pad(p.name, 14))+dim.Render(Pad(p.kind, 13)+desc))
		for i, t := range p.targets {
			conn := "├ "
			if i == len(p.targets)-1 {
				conn = "└ "
			}
			for _, e := range paletteEntries {
				if e.parent == p.name && e.target == t {
					left = append(left, "  "+dim.Render(conn)+Pad(trunc(t, 13), 15)+dim.Render(trunc(e.run, maxInt(6, lw-33))))
				}
			}
		}
	}
	left = append(left, renderWorkersColumn(lw, -1)...)

	if bottomOrient {
		out := make([]string, 0, h)
		listH := h - 3
		if listH > len(left) {
			listH = len(left)
		}
		start, end := scrollWindow(len(left), 0, maxInt(listH, 1))
		for _, l := range left[start:end] {
			out = append(out, " "+Pad(l, lw))
		}
		out = append(out, dim.Render(strings.Repeat("─", w)))
		out = append(out, " "+dim.Render("↑↓ scroll · / search · esc back to palette"))
		for len(out) < h {
			out = append(out, "")
		}
		return out[:h]
	}
	if len(left) > h {
		start, end := scrollWindow(len(left), 0, h)
		left = left[start:end]
	}

	right := []string{
		dim.Render(trunc("browsing every command", rw)),
		"",
		dim.Render(trunc("↑↓ scroll · / search · esc back", rw)),
	}

	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		out = append(out, " "+Pad(at(left, i), lw)+dim.Render("│ ")+Pad(at(right, i), rw))
	}
	return out
}

// ---- frame ----

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
		name = "commands-palette"
	}
	sp := screens[name]
	empty := s.Is("data", "empty")
	tracker := s.Get("tracker")
	if tracker == "" {
		tracker = "beads"
	}
	src := trackerRepo(tracker)
	hover := s.Get("hover")
	if hover == "" {
		hover = "off" // NoDump axes are absent from the dump's combos(), unlike live drive
	}

	// header's readout has to match the SAME h workBody's bottomOrient check
	// actually compares against (the body height, after the header/tab/rule
	// rows are subtracted) — not the raw terminal height — or the number on
	// screen would lie about what the code just decided.
	rows := []string{header(w, h-5, hover, src), tabBar(sp, w, s.Get("tracker")), ""}
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
		switch {
		case name == "commands-palette", name == "commands-palette-empty":
			empty := name == "commands-palette-empty"
			box := paletteBoxRows(name)
			boxSelected := paletteCursor < len(box)
			var selEntry *paletteEntry
			var selWorker *liveRow
			workerSelIdx := -1
			if boxSelected {
				selEntry = &box[paletteCursor]
			} else if wr := paletteWorkerRows(); paletteCursor-len(box) < len(wr) {
				workerSelIdx = paletteCursor - len(box)
				selWorker = wr[workerSelIdx]
			}
			content := paletteIdleContent(w*2/3-2, empty, boxSelected)
			body = renderPaletteScreen(w, h-5, "Command Palette", content, selEntry, selWorker, workerSelIdx, hover)
		case name == "commands-palette-typing":
			box := paletteBoxRows(name)
			boxSelected := paletteCursor < len(box)
			var selEntry *paletteEntry
			var selWorker *liveRow
			workerSelIdx := -1
			if boxSelected && paletteCursor < len(box) {
				selEntry = &box[paletteCursor]
			} else if wr := paletteWorkerRows(); paletteCursor-len(box) < len(wr) {
				workerSelIdx = paletteCursor - len(box)
				selWorker = wr[workerSelIdx]
			}
			content := paletteTypingContent(w*2/3-2, sp.demoQuery, boxSelected)
			body = renderPaletteScreen(w, h-5, "Command Palette", content, selEntry, selWorker, workerSelIdx, hover)
		case name == "commands-palette-browse":
			body = renderPaletteBrowse(w, h-5)
		case sp.hosted:
			body = hostedBody(w, h-5, sp.hostedEmpty)
		default:
			body = workBody(sp, w, h-5, empty, hover)
		}
		for len(body) < h-5 {
			body = append(body, "")
		}
		rows = append(rows, body[:h-5]...)
		rows = append(rows, dim.Render(strings.Repeat("─", w)), Pad(" "+dim.Render(keys(name, sp)), w))
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

// header shows the exact w×h this frame is rendering at, and which way the
// hover-orientation check just came down, so that's on screen instead of
// something to estimate from a screenshot: "w=132 h=61 w/2=66<h→bottom" or
// "…→right", read directly off the number the code actually compared.
func header(w, h int, hover string, src repoSrc) string {
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
	weighted := float64(w) / 2.3
	orient := "right"
	if weighted < float64(h) {
		orient = "bottom"
	}
	hoverNote := ""
	if hover == "off" {
		hoverNote = " (hover off, so unused)"
	}
	r := dim.Render(fmt.Sprintf("w=%d h=%d w/2.3=%.0f→%s%s", w, h, weighted, orient, hoverNote))
	gap := w - VW(l) - VW(r) - 2
	if gap < 1 {
		gap = 1
	}
	return " " + l + strings.Repeat(" ", gap) + r
}

func tabBar(sp screenSpec, w int, tracker string) string {
	trackerTab := "Beads 227"
	switch tracker {
	case "github":
		trackerTab = "Issues 99"
	case "prs":
		trackerTab = "PRs 5"
	}
	defs := []string{"Work 12", "Repo", trackerTab, "Config"}
	active := sp.tab
	if sp.noMan {
		defs = defs[1:]
		active--
	}
	var b strings.Builder
	b.WriteString(" ")
	for i, t := range defs {
		label := " " + t + " "
		if i == active {
			b.WriteString(pill.Render(label))
		} else {
			b.WriteString(dim.Render(label))
		}
		b.WriteString("  ")
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

func keys(name string, sp screenSpec) string {
	switch name {
	case "commands-palette", "commands-palette-empty":
		return "↑↓ move · ↵ dispatch · / search · b browse · q/esc quit"
	case "commands-palette-typing":
		return "type to search · tab complete · ↑↓ move · ↵ dispatch · esc cancel"
	case "commands-palette-browse":
		return "↑↓ scroll · / search · esc back to palette"
	}
	switch {
	case sp.sheet && sp.running:
		return "⏎ kill them and quit · esc cancel"
	case sp.sheet:
		return "⏎ dispatch · e edit · esc cancel"
	case sp.hosted:
		return "q/esc detach · ↑↓ scroll · k kill · typing goes to the process"
	case sp.tab == 0:
		return "↑↓ move · ↵ run or open · k kill · p palette · q/esc quit"
	case sp.tab == 1:
		return "↑↓ move · ↵ checkout · ⌥↵/W worktree · p pull · ←→ tab"
	case sp.tab == 2:
		return "↑↓ move · ↵ start work · o open · / filter · ←→ tab"
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
