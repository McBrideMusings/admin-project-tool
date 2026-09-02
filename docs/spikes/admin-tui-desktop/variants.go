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
// (internal/tui/model.go's trackerKind/wantsPRs, read by visibleTabs). A repo
// declaring tracker = "beads" gets a Beads tab; one declaring "github" gets
// Issues instead; a repo declaring uses_pull_requests = true gets PRs
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
		// The ten reference frames .19 names, first so their filenames are stable.
		"commands", "commands-workflow-selected", "repo", "issues", "issues-selected",
		"dispatch-sheet", "swarm", "config", "running", "no-manifest",
		// Every other surface repo-dash draws — see INVENTORY.md. Each is the real
		// frame, reached by driving the same keys a person would press.
		"repo-commits", "repo-filter", "repo-new-worktree",
		"tracker-detail", "tracker-graph", "tracker-blocking", "tracker-tree", "tracker-filter",
		"config-local",
		// Hosted-process surfaces, last so the frame names above stay put.
		"hosted", "hosted-empty", "hosted-quit", "commands-targets-open",
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
	Reset:   func() { selDelta = 0; expanded = "" },
	OnDump:  func() { Blocking = true },
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
	// openAll expands every multi-target command, so the dump has a frame
	// showing the submenu without depending on a keypress having happened.
	openAll bool
}

var screens = map[string]screenSpec{
	"commands":                   {tab: 0, cmdSel: 2},
	"commands-workflow-selected": {tab: 0, cmdSel: 5},
	"running":                    {tab: 0, cmdSel: 6, running: true},
	"repo":                       {tab: 1},
	"issues":                     {tab: 2},
	"issues-selected":            {tab: 2},
	"dispatch-sheet":             {tab: 2, sheet: true},
	"swarm":                      {tab: 0, cmdSel: 7},
	"config":                     {tab: 3},
	"no-manifest":                {tab: 1, noMan: true},

	// Hosted-process surfaces (admin-3rgp.61/.63). Appended after the ten named
	// reference frames so no existing frame filename shifts.
	"hosted":       {tab: 0, cmdSel: 6, hosted: true},
	"hosted-empty": {tab: 0, cmdSel: 6, hosted: true, hostedEmpty: true},
	"hosted-quit":  {tab: 0, cmdSel: 6, sheet: true, running: true},

	// The multi-target submenu open, cursor on one of its targets — the
	// interaction a person uses to pick a target without leaving the list.
	"commands-targets-open": {tab: 0, cmdSel: 2, openAll: true},

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
		cur = "commands"
	}
	idx := 0
	for i, t := range workTabs {
		if screens[t].tab == screens[cur].tab {
			idx = i
		}
	}
	switch key {
	// → and ← switch tabs, EXCEPT on a multi-target row, where they open and
	// close its submenu in place. The submenu is never a separate screen, so
	// there is nothing to come back from — which is why it can share the key
	// that moves sideways everywhere else without ambiguity: on any other row
	// there is no submenu to open, and the tab switch is the only thing → can
	// mean.
	case "tab", "right", "l":
		if key == "right" {
			if c := cmdUnderCursor(cur); c != nil && hasSubs(c) && expanded != c.name {
				expanded = c.name
				return true
			}
		}
		set("screen", workTabs[(idx+1)%len(workTabs)])
		selDelta = 0
		return true
	case "shift+tab", "left", "h":
		if key == "left" {
			if c := cmdUnderCursor(cur); c != nil && hasSubs(c) && expanded == c.name {
				expanded = ""
				return true
			}
		}
		set("screen", workTabs[(idx-1+len(workTabs))%len(workTabs)])
		selDelta = 0
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
		case "commands", "commands-workflow-selected", "running", "swarm":
			// On a closed multi-target row, ↵ opens the submenu rather than
			// running — there is nothing unambiguous to run yet.
			if c := cmdUnderCursor(cur); c != nil && hasSubs(c) && expanded != c.name {
				expanded = c.name
				return true
			}
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
	rows := workRows(false, sp.openAll)
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
}

var liveRows = []liveRow{
	{state: "running", id: "admin-3rgp.36", doing: "writing internal/lua/host.go", pane: "pane 3"},
	{state: "waiting", id: "admin-3rgp.7", doing: "needs input: root command name", pane: "pane 4"},
	{state: "failed", id: "admin-3rgp.33", doing: "go test ./internal/detect FAIL", pane: "pane 5"},
	// A Command's live work is hosted by the dashboard on a pty it owns, not
	// dispatched to a pane — so `where` says "hosted", and quitting the
	// dashboard kills it. A Worker above still runs in its own pane and
	// outlives the dashboard on purpose; the column is what tells them apart.
	{state: "running", id: "admin docs", doing: "vitepress dev · localhost:5173", cmd: "docs", hosted: true},
	{state: "waiting", id: "admin test", doing: "Enter a number, or q to cancel:", cmd: "test", hosted: true},
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

// expanded is the ONE command whose submenu is open, matching the shipped
// Model.expandedCmd — opening one closes any other, so the list never grows
// two submenus deep. Live state outside State, so Reset must clear it or a
// dump inherits whatever the last keypress left open.
var expanded string

// hasSubs reports whether c gets a disclosure arrow. A command with exactly
// one target does not: the CLI already runs a lone target without asking, so
// an expander onto a single child would be a menu with nothing to pick.
func hasSubs(c *cmdRow) bool { return len(c.subs) > 1 }

func workRows(empty, openAll bool) []workRow {
	if empty {
		return nil
	}
	var out []workRow
	for i := range cmds {
		out = append(out, workRow{cmd: &cmds[i]})
		if expanded != cmds[i].name && !openAll {
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

func workBody(sp screenSpec, w, h int, empty bool) []string {
	rows := workRows(empty, sp.openAll)
	if len(rows) == 0 {
		return []string{
			" " + bo.Render("Work") + dim.Render("  0"),
			strings.Repeat("─", w),
			dim.Render("   nothing here yet — admin.toml declares no commands"),
		}
	}
	sel := wrap(sp.cmdSel, len(rows))
	lw := w*2/3 - 2
	rw := w - lw - 3
	dw := maxInt(4, lw-34)

	live := map[string]*liveRow{}
	for i := range liveRows {
		if liveRows[i].cmd != "" {
			live[liveRows[i].cmd] = &liveRows[i]
		}
	}

	var left []string
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
			}
			left = append(left, mark(i == sel, "")+dim.Render(conn)+key+
				dim.Render(trunc(sub[1], maxInt(6, lw-24))))
			continue
		}
		// A multi-target command wears a disclosure arrow — ▸ closed, ▾ open —
		// in the name column. It goes here rather than replacing the cursor
		// diamond so the row still shows, by shape alone, where the cursor is.
		name := Pad(r.cmd.name, 16)
		if hasSubs(r.cmd) {
			arrow := "▸ "
			if expanded == r.cmd.name || sp.openAll {
				arrow = "▾ "
			}
			name = dim.Render(arrow) + Pad(r.cmd.name, 14)
		}
		row := mark(i == sel, name)
		if lr, ok := live[r.cmd.name]; ok {
			row += liveStyle[lr.state].Render(Pad(lr.state, 9)) + liveWhere(lr, dw)
		} else {
			row += dim.Render(Pad(r.cmd.kind, 9) + trunc(r.cmd.desc, maxInt(6, lw-27)))
		}
		left = append(left, row)
	}
	left = append(left, "", dim.Render(" Workers"))
	for i, r := range rows {
		if r.worker == nil {
			continue
		}
		left = append(left, mark(i == sel, Pad(r.worker.id, 16))+
			liveStyle[r.worker.state].Render(Pad(r.worker.state, 9))+
			liveWhere(r.worker, dw))
	}

	right := detailOf(rows[sel], rw)
	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		out = append(out, " "+Pad(at(left, i), lw)+dim.Render("│ ")+at(right, i))
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
// a command and a worker never have to be squeezed into one field set.
func detailOf(r workRow, w int) []string {
	if r.worker != nil {
		wk := r.worker
		return []string{
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
			"",
			dim.Render("a") + " attach   " + dim.Render("k") + " kill",
		}
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
			dim.Render("↵") + " run   " + dim.Render("←") + " collapse",
		}
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
		name = "commands"
	}
	sp := screens[name]
	empty := s.Is("data", "empty")
	tracker := s.Get("tracker")
	if tracker == "" {
		tracker = "beads"
	}
	src := trackerRepo(tracker)

	rows := []string{header(w, src), tabBar(sp, w, s.Get("tracker")), ""}
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
		if sp.hosted {
			body = hostedBody(w, h-5, sp.hostedEmpty)
		} else {
			body = workBody(sp, w, h-5, empty)
		}
		for len(body) < h-5 {
			body = append(body, "")
		}
		rows = append(rows, body[:h-5]...)
		rows = append(rows, dim.Render(strings.Repeat("─", w)), Pad(" "+dim.Render(keys(sp)), w))
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

func header(w int, src repoSrc) string {
	name := src.nwo
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	l := name + "   " + cy.Render(src.branch) + "  " + gr.Render("✔ clean")
	r := dim.Render("live")
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

func keys(sp screenSpec) string {
	switch {
	case sp.sheet && sp.running:
		return "⏎ kill them and quit · esc cancel"
	case sp.sheet:
		return "⏎ dispatch · e edit · esc cancel"
	case sp.hosted:
		return "q/esc detach · ↑↓ scroll · k kill · typing goes to the process"
	case sp.tab == 0:
		return "↑↓ move · ↵ run or open · →← submenu · k kill · q/esc quit"
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
