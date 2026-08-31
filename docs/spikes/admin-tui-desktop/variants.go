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
// drifts. PRD.md:124 names that seam as the one for everything visual.
//
// Which tracker tabs exist is a property of the repo, not a setting: repo-dash
// derives it from hasBeads and usesPRs (internal/tui/model.go:352). A repo with
// .beads/ gets a Beads tab and no Issues tab, and one without gets Issues and
// PRs. So the tracker tweak swaps which repo the embedded frame is rendered
// against, because that is the only honest way to see both.
package main

import (
	"context"
	"fmt"
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

var Variants = []Variant{{
	Name:    "Detail-right",
	Caption: "interleaved rows, with a third of the screen showing whatever the cursor is on",
	Render:  render,
	Key:     onKey,
	Reset:   func() { selDelta = 0 },
}}

// ---- the real tabs, rendered by repo-dash itself ----

type repoSrc struct{ root, nwo, branch string }

var trackerRepos = map[string]repoSrc{
	// This repo: has .beads/, so repo-dash draws Repo · Beads · Config.
	"beads": {"/Users/pierce/Projects/admin-project-tool",
		"McBrideMusings/admin-project-tool", "feat/one-tool-go-merge"},
	// No .beads/, so the same code draws Repo · Issues · Config instead.
	"github": {"/Users/pierce/Projects/etv-station", "McBrideMusings/etv-station", "main"},
	// usesPRs() is len(remote.PRs) > 0 (model.go:333) — the PRs tab exists only
	// while the repo actually has open PRs, so seeing it needs a repo that does.
	"prs": {"/Users/pierce/Projects/nts-desktop", "McBrideMusings/nts-desktop", "main"},
}

// Digit keys index the drawn tabs (model.go:349); both repos above draw three:
// 1 Repo, 2 the tracker, 3 Config. Everything past the tab is the same key a
// person presses — see INVENTORY.md for the full map.
const (
	keyRepo    = "1"
	keyTracker = "2"
	keyConfig  = "3"
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
)

// realTab returns repo-dash's own frame from the body down — body, rule AND
// footer. The footer has to come too: in a modal state it IS the mode. The
// new-worktree prompt, the filter query and its match count, the delete
// confirm and "press any key to dismiss" are all drawn there (view.go:500),
// so a spliced-off footer hides the very thing those screens exist to show.
//
// Its frame is header, tab bar, blank, then the rest, so asking for h+3 rows
// and dropping the first 3 yields exactly h.
//
// Cached because Frame runs the real Init commands and waits for them to land;
// re-rendering on every keystroke would make the prototype unusable.
func realTab(src repoSrc, keys []string, w, h int) []string {
	ck := fmt.Sprintf("%s|%s|%d|%d", src.root, strings.Join(keys, ","), w, h)
	frameMu.Lock()
	if rows, ok := frameCache[ck]; ok {
		frameMu.Unlock()
		return rows
	}
	frameMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := tui.New(ctx, src.root, src.nwo, src.branch, "main")
	out := tui.Frame(ctx, m, tui.FrameOptions{
		Width: w, Height: h + 3, Keys: keys, Settle: 900 * time.Millisecond,
	})
	lines := strings.Split(out, "\n")
	body := []string{dim.Render("   (no frame)")}
	if len(lines) > 3 {
		body = lines[3:]
	}
	rows := make([]string, h)
	for i := range rows {
		rows[i] = at(body, i)
	}
	frameMu.Lock()
	frameCache[ck] = rows
	frameMu.Unlock()
	return rows
}

// ---- work tab: the design ----

type screenSpec struct {
	tab     int
	noMan   bool
	sheet   bool
	cmdSel  int
	running bool
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
	case "tab", "right", "l":
		set("screen", workTabs[(idx+1)%len(workTabs)])
		selDelta = 0
		return true
	case "shift+tab", "left", "h":
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
		}
		return true
	case "esc":
		if cur == "dispatch-sheet" {
			set("screen", "issues")
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

type cmdRow struct {
	name, desc, last, kind, arch, note string
	steps                              [][4]string
	targets                            []string
}

var cmds = []cmdRow{
	{name: "build", desc: "Compile the binary", last: "2h ago", kind: "shell", arch: "go-cli",
		steps: [][4]string{{"1", "shell", "go build ./...", "go-cli"}}},
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

type liveRow struct{ state, id, doing, where, cmd string }

var liveRows = []liveRow{
	{"running", "admin-3rgp.36", "writing internal/lua/host.go", "pane 3", ""},
	{"waiting", "admin-3rgp.7", "needs input: root command name", "pane 4", ""},
	{"failed", "admin-3rgp.33", "go test ./internal/detect FAIL", "pane 5", ""},
	{"running", "admin docs", "vitepress dev · localhost:5173", "pane 1", "docs"},
	{"running", "admin test", "internal/tui · 00:41", "pane 6", "test"},
}

var liveStyle = map[string]lipgloss.Style{
	"running": gr, "waiting": yl, "done": dim, "failed": rd,
}

type workRow struct {
	cmd    *cmdRow
	worker *liveRow
}

func workRows(empty bool) []workRow {
	if empty {
		return nil
	}
	var out []workRow
	for i := range cmds {
		out = append(out, workRow{cmd: &cmds[i]})
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
	rows := workRows(empty)
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
		row := mark(i == sel, Pad(r.cmd.name, 16))
		if lr, ok := live[r.cmd.name]; ok {
			row += liveStyle[lr.state].Render(Pad(lr.state, 9)) +
				dim.Render(Pad(trunc(lr.doing, dw), dw)+" "+lr.where)
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
			dim.Render(Pad(trunc(r.worker.doing, dw), dw)+" "+r.worker.where))
	}

	right := detailOf(rows[sel], rw)
	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		out = append(out, " "+Pad(at(left, i), lw)+dim.Render("│ ")+at(right, i))
	}
	return out
}

// detailOf is the polymorphic half: the pane's shape follows the row's kind, so
// a command and a worker never have to be squeezed into one field set.
func detailOf(r workRow, w int) []string {
	if r.worker != nil {
		wk := r.worker
		return []string{
			bo.Render(wk.id),
			liveStyle[wk.state].Render(wk.state) + dim.Render("  ·  "+wk.where),
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
			dim.Render("a") + " attach   " + dim.Render("k") + " kill   " + dim.Render("r") + " retry",
		}
	}
	c := r.cmd
	out := []string{
		bo.Render(c.name),
		dim.Render(c.kind + "  ·  " + c.arch),
		"",
		dim.Render("last run"),
		"  " + c.last,
	}
	if c.note != "" {
		out = append(out, "  "+dim.Render(c.note))
	}
	out = append(out, "", dim.Render("log"), "  "+dim.Render("~/.admin/logs/"+c.name+".log"), "")
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
	src := trackerRepos[s.Get("tracker")]
	if src.root == "" {
		src = trackerRepos["beads"]
	}

	rows := []string{header(w, src), tabBar(sp, w, s.Get("tracker")), ""}
	if ks, ok := embedded[name]; ok {
		// repo-dash draws its own rule and footer for these — including the
		// modal footers, which are the mode.
		body := realTab(src, ks, w, h-3)
		for len(body) < h-3 {
			body = append(body, "")
		}
		rows = append(rows, body[:h-3]...)
	} else {
		body := workBody(sp, w, h-5, empty)
		for len(body) < h-5 {
			body = append(body, "")
		}
		rows = append(rows, body[:h-5]...)
		rows = append(rows, strings.Repeat("─", w), Pad(" "+dim.Render(keys(sp)), w))
	}
	if sp.sheet {
		rows = overlaySheet(rows, w, h)
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
	case sp.sheet:
		return "⏎ dispatch · e edit · esc cancel"
	case sp.tab == 0:
		return "↑↓ move · ↵ run or attach · k kill · r retry · ←→ tab · q quit"
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
