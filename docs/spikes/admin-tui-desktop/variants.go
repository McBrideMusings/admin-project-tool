// Admin Dashboard — the settled design (admin-3rgp.19).
//
// harness.go supplies the picker, the axes, the footer bar, the geometry
// assertion and the dump. This file is the design.
//
//	admin prototype admin-tui-desktop     drive it — F1 cycles the screen
//	go run . -dump -dir .                 write the ten reference frames
//
// One axis, ten values, named exactly as admin-3rgp.19 lists them, so the dump
// produces the filenames .20 and .28 cite by path.
//
// Everything renders at s.W × s.H — the LIVE terminal size — and only the dump
// pins 96×30. A design that renders at a constant does not fill the window and
// cannot answer whether it survives 80 columns, which is the whole question.
package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	DumpWidth  = 96
	DumpHeight = 30
)

var Axes = []Axis{
	{Key: "screen", Label: "Screen", Values: []string{
		"commands", "commands-workflow-selected", "repo", "issues", "issues-selected",
		"dispatch-sheet", "swarm", "config", "running", "no-manifest",
	}},
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

type screenSpec struct {
	tab      int
	noMan    bool
	sheet    bool
	cmdSel   int
	issueSel int
	running  bool
}

var screens = map[string]screenSpec{
	"commands":                   {tab: 0, cmdSel: 2},
	"commands-workflow-selected": {tab: 0, cmdSel: 5},
	"running":                    {tab: 0, cmdSel: 6, running: true},
	"repo":                       {tab: 1},
	"issues":                     {tab: 2, issueSel: 1},
	"issues-selected":            {tab: 2, issueSel: 3},
	"dispatch-sheet":             {tab: 2, issueSel: 1, sheet: true},
	"swarm":                      {tab: 3},
	"config":                     {tab: 4},
	"no-manifest":                {tab: 1, noMan: true},
}

var Variants = []Variant{{
	Name:    "Settled",
	Caption: "the design settled in docs/PRD.md:82-111",
	Render:  render,
}}

// ---- rendering, all of it sized from s.W / s.H ----

func render(s State) string {
	w, h := s.W, s.H
	if w <= 0 {
		w = DumpWidth
	}
	if h <= 0 {
		h = DumpHeight
	}
	sp := screens[s.Get("screen")]
	if s.Get("screen") == "" {
		sp = screens["commands"]
	}

	rows := []string{header(w), tabBar(sp, w), strings.Repeat("─", w)}
	body := bodyOf(sp, w, h-5)
	for len(body) < h-5 {
		body = append(body, fillRow(sp, w))
	}
	rows = append(rows, body[:h-5]...)
	rows = append(rows, strings.Repeat("─", w), Pad(" "+dim.Render(keys(sp)), w))

	if sp.sheet {
		rows = overlaySheet(rows, w, h)
	}
	for i := range rows {
		rows[i] = Pad(rows[i], w)
	}
	return strings.Join(rows, "\n")
}

func header(w int) string {
	l := " " + bo.Render("admin") + dim.Render(" · admin-project-tool")
	r := cy.Render("feat/one-tool-go-merge") + " "
	gap := w - VW(l) - VW(r)
	if gap < 1 {
		gap = 1
	}
	return l + strings.Repeat(" ", gap) + r
}

func tabNames(sp screenSpec) []string {
	all := []string{"Commands", "Repo", "Issues", "Live", "Config"}
	if sp.noMan {
		return all[1:]
	}
	return all
}

func tabBar(sp screenSpec, w int) string {
	names := tabNames(sp)
	active := sp.tab
	if sp.noMan {
		active-- // Commands is absent, not disabled — indices shift
	}
	var b strings.Builder
	b.WriteString(" ")
	for i, n := range names {
		if i == active {
			b.WriteString(pill.Render(" " + n + " "))
		} else {
			b.WriteString(dim.Render("  " + n + "  "))
		}
	}
	return Pad(b.String(), w)
}

// leftW is the command list's column. It stays fixed while the reader takes the
// rest, so a wider terminal grows the pane that benefits from width.
const leftW = 30

func split(l, r string, w int) string {
	return " " + Pad(l, leftW) + dim.Render(" │ ") + r
}

func fillRow(sp screenSpec, w int) string {
	if sp.tab == 0 && !sp.noMan {
		return split("", "", w)
	}
	return ""
}

func mark(sel bool, s string) string {
	if sel {
		return cy.Render("▸ ") + s
	}
	return "  " + s
}

type cmdRow struct {
	name, last, kind, arch, note string
	steps                        [][4]string
	targets                      []string
}

var cmds = []cmdRow{
	{name: "build", last: "2h ago", kind: "shell", arch: "go-cli",
		steps: [][4]string{{"1", "shell", "go build ./...", "go-cli"}}},
	{name: "check", last: "2h ago", kind: "shell", arch: "(manifest)",
		steps: [][4]string{{"1", "shell", "admin check .", "(manifest)"}}},
	{name: "deploy", last: "18m ago", kind: "sequence", arch: "(manifest)", note: "exit 0 · 41s",
		steps: [][4]string{
			{"1", "shell", "bash install.sh", "simple"},
			{"2", "lua", "scripts/provision.lua", "apple"},
			{"3", "shell", "admin check .", "(manifest)"}}},
	{name: "docs", last: "—", kind: "shell", arch: "docs",
		steps: [][4]string{{"1", "shell", "bun run docs:dev", "docs"}}},
	{name: "logs", last: "—", kind: "logs", arch: "docker",
		steps: [][4]string{{"1", "logs", "admin logs dev", "docker"}}},
	{name: "retire-merged", last: "never", kind: "workflow", arch: "(manifest)",
		targets: []string{"fix-detector-parity-skip", "go-transient-kind-factories", "docs-adr-lua", "launchd-env-file"}},
	{name: "test", last: "6m ago", kind: "sequence", arch: "(manifest)",
		steps: [][4]string{
			{"1", "go", "go test ./...", "go-cli"},
			{"2", "shell", "bash tests/test_install.sh", "(manifest)"}}},
}

func bodyOf(sp screenSpec, w, h int) []string {
	switch {
	case sp.noMan:
		return repoBody(sp, w, true)
	case sp.tab == 0:
		return commandsBody(sp, w)
	case sp.tab == 1:
		return repoBody(sp, w, false)
	case sp.tab == 2:
		return issuesBody(sp, w)
	case sp.tab == 3:
		return liveBody(w)
	}
	return configBody(w)
}

func commandsBody(sp screenSpec, w int) []string {
	c := cmds[sp.cmdSel]
	var left, right []string
	left = append(left, dim.Render("  COMMAND          LAST RUN"), "")
	for i, cc := range cmds {
		last := dim.Render(cc.last)
		if sp.running && cc.name == "test" {
			last = yl.Render("running")
		}
		left = append(left, mark(i == sp.cmdSel, Pad(cc.name, 16)+" "+last))
	}

	title := bo.Render(c.name)
	if sp.running && c.name == "test" {
		title += dim.Render("  ·  ") + yl.Render("running") + dim.Render("  ·  00:41")
	}
	right = append(right, title, dim.Render(strings.Repeat("─", maxInt(10, w-leftW-6))))
	right = append(right, dim.Render("kind      ")+c.kind)
	right = append(right, dim.Render("archetype ")+c.arch)
	lr := c.last
	if c.note != "" {
		lr += dim.Render(" · " + c.note)
	}
	right = append(right, dim.Render("last run  ")+lr)
	right = append(right, dim.Render("log       ")+"~/.admin/logs/"+c.name+".log", "")

	switch {
	case c.targets != nil:
		right = append(right, dim.Render("source    ")+"query", "")
		right = append(right, bo.Render("TARGETS")+dim.Render("  resolved now · 4 match"))
		for _, t := range c.targets {
			right = append(right, "  "+t)
		}
		right = append(right, "", dim.Render("A query-sourced workflow lists here and runs"),
			dim.Render("bare. A selection-sourced one does not appear —"),
			dim.Render("it is offered on a row instead."))
	case sp.running && c.name == "test":
		right = append(right, bo.Render("OUTPUT"))
		for _, l := range []string{
			gr.Render("ok") + "   internal/archetype   " + dim.Render("cached"),
			gr.Render("ok") + "   internal/detect      " + dim.Render("0.63s"),
			gr.Render("ok") + "   internal/kinds       " + dim.Render("cached"),
			dim.Render("..") + "   internal/tui",
		} {
			right = append(right, "  "+l)
		}
	default:
		right = append(right, bo.Render("STEPS"))
		for _, st := range c.steps {
			right = append(right, "  "+dim.Render(st[0]+"  ")+Pad(st[1], 10)+st[2])
			right = append(right, "     "+dim.Render("from  ")+mg.Render(st[3]), "")
		}
	}

	n := maxInt(len(left), len(right))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, split(at(left, i), at(right, i), w))
	}
	return out
}

func repoBody(sp screenSpec, w int, noMan bool) []string {
	var o []string
	o = append(o, " "+bo.Render("BRANCHES"))
	if noMan {
		o = append(o, " "+mark(true, gr.Render("* ")+Pad("main", 32)+dim.Render("↑0 ↓0        4d   Initial commit")))
		o = append(o, "", " "+bo.Render("WORKTREES"), dim.Render("   (none)"))
		o = append(o, "", " "+bo.Render("STASHES")+dim.Render("   (none)"), "")
		o = append(o, dim.Render(" No admin.toml here, so there is no Commands tab. A tab the project"))
		o = append(o, dim.Render(" cannot support is absent, never present-but-disabled."), "")
		o = append(o, "   "+cy.Render("admin new")+dim.Render("    scaffold a manifest here"))
		return o
	}
	for i, b := range []struct {
		name, a, bh, wt, age, subj string
		cur                        bool
	}{
		{"feat/one-tool-go-merge", "1", "0", "", "2m", "docs(spikes): rebuild the dashboard prototype", true},
		{"fix-detector-parity-skip", "1", "0", "wt", "2d", "fix(detect): fail parity test instead of skip", false},
		{"main", "0", "0", "", "15h", "Merge pull request #1016", false},
	} {
		pre := "  "
		if b.cur {
			pre = gr.Render("* ")
		}
		wt := "  "
		if b.wt != "" {
			wt = mg.Render("wt")
		}
		o = append(o, " "+mark(i == 0, pre+Pad(b.name, 28)+dim.Render("↑"+b.a+" ↓"+b.bh)+"  "+wt+dim.Render("  "+Pad(b.age, 5)+b.subj)))
	}
	o = append(o, "", " "+bo.Render("WORKTREES"))
	o = append(o, "   ~/.worktrees/admin-project-tool/fix-detector-parity-skip     "+gr.Render("clean"))
	o = append(o, "", " "+bo.Render("STASHES")+dim.Render("   (none)")) // empty section collapses to one line
	o = append(o, "", " "+bo.Render("RECENT"))
	for _, c := range [][2]string{
		{"1758e7b", "docs(spikes): rebuild the admin dashboard prototype"},
		{"db2711a", "docs(adr): Lua and Bash are the archetype authoring languages"},
		{"f738934", "feat(launchd): lift allowlisted env vars from a dotenv file"},
	} {
		o = append(o, "   "+yl.Render(c[0])+"  "+dim.Render(c[1]))
	}
	return o
}

type issueRow struct{ id, pri, title, blocker string }

var readyIssues = []issueRow{
	{"admin-3rgp.36", "P1", "Embed a Lua VM and define the host API", ""},
	{"admin-3rgp.29", "P1", "Two deployment paths: go install, archetypes", ""},
	{"admin-3rgp.7", "P1", "Decide how admin <command> reaches the CLI", ""},
}

var blockedIssues = []issueRow{
	{"admin-3rgp.20", "P1", "Commands tab with the resolved-action reader", ".19"},
	{"admin-3rgp.28", "P1", "The live-work tab: workers and processes", ".19"},
	{"admin-3rgp.35", "P1", "Implement the transient kind factories", ".36"},
	{"admin-3rgp.37", "P1", "Rewrite the 14 archetypes in Lua and Bash", ".36"},
}

func issuesBody(sp screenSpec, w int) []string {
	all := append(append([]issueRow{}, readyIssues...), blockedIssues...)
	cur := all[minInt(sp.issueSel, len(all)-1)]
	rw := 44

	var left, right []string
	left = append(left, bo.Render("READY")+dim.Render("   tracker: beads"))
	for i, is := range readyIssues {
		left = append(left, mark(i == sp.issueSel, dim.Render(is.pri)+" "+trunc(is.title, 34)))
	}
	left = append(left, "", bo.Render("BLOCKED"))
	for i, is := range blockedIssues {
		left = append(left, mark(len(readyIssues)+i == sp.issueSel,
			dim.Render(is.pri)+" "+trunc(is.title, 28)+rd.Render(" ←"+is.blocker)))
	}

	right = append(right, bo.Render(cur.id)+dim.Render("   "+cur.pri+"  open"))
	right = append(right, dim.Render(strings.Repeat("─", maxInt(10, w-rw-6))))
	right = append(right, cur.title, "")
	if cur.blocker != "" {
		right = append(right, bo.Render("BLOCKED BY"), "  "+rd.Render("admin-3rgp"+cur.blocker))
	} else {
		right = append(right, bo.Render("READY")+dim.Render("  no open blockers"))
	}
	right = append(right, "", bo.Render("LABELS")+dim.Render("  afk"))

	n := maxInt(len(left), len(right))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, " "+Pad(at(left, i), rw)+dim.Render(" │ ")+at(right, i))
	}
	return out
}

func liveBody(w int) []string {
	st := map[string]lipgloss.Style{"running": gr, "waiting": yl, "done": dim, "failed": rd}
	line := func(sel bool, s, id, br, doing, pane string) string {
		return " " + mark(sel, st[s].Render(Pad(s, 9))+Pad(id, 15)+Pad(br, 18)+dim.Render(Pad(doing, 32)+pane))
	}
	var o []string
	o = append(o, " "+bo.Render("AGENT WORKERS"))
	o = append(o, line(false, "running", "admin-3rgp.36", "embed-lua-vm", "writing internal/lua/host.go", "pane 3"))
	o = append(o, line(true, "waiting", "admin-3rgp.7", "cli-identity", "needs input: root command name", "pane 4"))
	o = append(o, line(false, "done", "admin-3rgp.34", "fix-parity-skip", "verdict PASS · c31e994", "pane 2"))
	o = append(o, line(false, "failed", "admin-3rgp.33", "detector-clear", "go test ./internal/detect FAIL", "pane 5"))
	o = append(o, "", " "+bo.Render("LONG-RUNNING PROCESSES"))
	o = append(o, line(false, "running", "admin docs", "vitepress dev", "http://localhost:5173", "pane 1"))
	o = append(o, "", dim.Render(" One registry, both kinds. \"waiting\" is distinguishable from \"running\""))
	o = append(o, dim.Render(" without attaching to anything."))
	return o
}

func configBody(w int) []string {
	rows := [][3]string{
		{"tracker", "beads", "admin.toml"},
		{"uses_pull_requests", "false", "admin.toml"},
		{"identity.login", "pierce", "admin.toml"},
		{"worktree_root", "~/.worktrees/admin-project-tool", "default"},
		{"populate_patterns", "admin.toml, .env*, CLAUDE.local.md", "default"},
		{"default_agent", "claude", "default"},
		{"concurrency_ceiling", "4", "admin.toml"},
		{"enter.branch", "checkout", "default"},
		{"enter.issue", "open", "default"},
	}
	o := []string{" " + bo.Render("RESOLVED CONFIGURATION") + dim.Render("                    source"), ""}
	for _, r := range rows {
		src := dim.Render(r[2])
		if r[2] == "admin.toml" {
			src = cy.Render(r[2])
		}
		o = append(o, "   "+Pad(r[0], 22)+Pad(r[1], 36)+src)
	}
	o = append(o, "", dim.Render(" Values render resolved. Editing the manifest from here is out of scope."))
	return o
}

func overlaySheet(rows []string, w, h int) []string {
	box := []string{}
	bw := 60
	add := func(s string) { box = append(box, "│"+Pad(s, bw)+"│") }
	box = append(box, "┌"+strings.Repeat("─", bw)+"┐")
	add("  " + bo.Render("Dispatch admin-3rgp.29"))
	add("")
	add("    " + dim.Render("agent     ") + "claude")
	add("    " + dim.Render("model     ") + "sonnet")
	add("    " + dim.Render("branch    ") + "two-deployment-paths")
	add("    " + dim.Render("worktree  ") + dim.Render("~/.worktrees/…/two-deployment-paths"))
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
		return "⏎ dispatch   e edit   esc cancel"
	case sp.noMan:
		return "↑↓ move   ⏎ checkout   ⇥ switch tab   q quit"
	case sp.tab == 0:
		return "↑↓ move   ⏎ run   ⇥ switch tab   / filter   ? help   q quit"
	case sp.tab == 1:
		return "↑↓ move   ⏎ checkout   w worktree   f fetch   ⇥ switch tab   q quit"
	case sp.tab == 2:
		return "↑↓ move   ⏎ dispatch   c claim   b branch   ⇥ switch tab   q quit"
	case sp.tab == 3:
		return "↑↓ move   a attach   k kill   r retry   ⇥ switch tab   q quit"
	}
	return "↑↓ move   ⇥ switch tab   q quit"
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}
func trunc(s string, n int) string {
	r := []rune(s)
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
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
