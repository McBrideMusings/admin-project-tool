// Admin Dashboard — the settled design (admin-3rgp.19).
//
// harness.go supplies the picker, the axes, the footer bar, the geometry
// assertion and the dump. This file is the design.
//
//	admin prototype admin-tui-desktop     drive it — tab/←→ switch, ↑↓ move, ⏎ act
//	go run . -dump -dir .                 write the ten reference frames
//
// One axis, ten values, named exactly as admin-3rgp.19 lists them, so the dump
// produces the filenames .20 and .28 cite by path.
//
// Repo and the list tabs deliberately reproduce repo-dash's own rendering —
// header line, counted tab bar, titled panes, section-header-plus-rule rows —
// because those tabs already exist and the merge does not restyle them. The
// reference is internal/tui's protoModel, dumped headless. Only Commands and
// the live-work tab are new.
package main

import (
	"fmt"
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
	Key:     onKey,
	Reset:   func() { selDelta = 0 },
}}

// selDelta is the live cursor offset on top of whatever row the current screen
// starts on. Reset clears it, so a dumped frame never inherits the previous
// frame's cursor.
var selDelta int

var tabScreens = []string{"commands", "repo", "issues", "swarm", "config"}

func onKey(key string, s State, set func(axis, value string)) bool {
	cur := s.Get("screen")
	if cur == "" {
		cur = "commands"
	}
	sp := screens[cur]

	switch key {
	case "tab", "right", "l":
		set("screen", tabScreens[(sp.tab+1)%len(tabScreens)])
		selDelta = 0
		return true
	case "shift+tab", "left", "h":
		set("screen", tabScreens[(sp.tab-1+len(tabScreens))%len(tabScreens)])
		selDelta = 0
		return true
	case "down", "j":
		selDelta++
		return true
	case "up", "k":
		selDelta--
		return true
	case "enter":
		switch cur {
		case "issues", "issues-selected":
			set("screen", "dispatch-sheet")
		case "dispatch-sheet":
			set("screen", "issues")
		case "commands", "commands-workflow-selected":
			set("screen", "running")
		case "running":
			set("screen", "commands")
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

// ---- frame ----

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

	rows := []string{header(w), tabBar(sp, w), ""}
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

// header mirrors repo-dash: repo, branch, worktree state, right-aligned
// freshness word.
func header(w int) string {
	l := "admin-project-tool   " + cy.Render("feat/one-tool-go-merge") + "  " + gr.Render("✔ clean")
	r := dim.Render("live")
	gap := w - VW(l) - VW(r) - 2
	if gap < 1 {
		gap = 1
	}
	return " " + l + strings.Repeat(" ", gap) + r
}

type tabDef struct {
	name  string
	count int
}

func tabDefs(sp screenSpec) []tabDef {
	all := []tabDef{{"Commands", 7}, {"Repo", 0}, {"Issues", 7}, {"Live", 5}, {"Config", 0}}
	if sp.noMan {
		return all[1:]
	}
	return all
}

func tabBar(sp screenSpec, w int) string {
	defs := tabDefs(sp)
	active := sp.tab
	if sp.noMan {
		active--
	}
	var b strings.Builder
	b.WriteString(" ")
	for i, t := range defs {
		label := " " + t.name
		if t.count > 0 {
			label += " " + fmt.Sprint(t.count)
		}
		label += " "
		if i == active {
			b.WriteString(pill.Render(label))
		} else {
			b.WriteString(dim.Render(label))
		}
		b.WriteString("  ")
	}
	return Pad(b.String(), w)
}

// section is repo-dash's list-tab pattern: a label, a right-aligned count, a rule.
func section(label string, count, w int) []string {
	c := fmt.Sprint(count)
	gap := w - 1 - VW(label) - len(c) - 1
	if gap < 1 {
		gap = 1
	}
	return []string{
		" " + bo.Render(label) + strings.Repeat(" ", gap) + dim.Render(c),
		strings.Repeat("─", w),
	}
}

// pane draws a titled box — repo-dash's Branches / Recent-commits shape.
func pane(title string, count, w int, body []string, h int) []string {
	inner := w - 2
	head := "─ " + title + " "
	tail := ""
	if count > 0 {
		tail = fmt.Sprint(count) + " "
	}
	// VW, not len: the leading ─ is three bytes, and a byte count makes the box
	// shorter than its contents so the right edge drifts.
	fill := inner - VW(head) - VW(tail)
	if fill < 1 {
		fill = 1
	}
	out := []string{dim.Render("┌" + head + strings.Repeat("─", fill) + tail + "┐")}
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(body) {
			line = body[i]
		}
		out = append(out, dim.Render("│")+Pad(line, inner)+dim.Render("│"))
	}
	out = append(out, dim.Render("└"+strings.Repeat("─", inner)+"┘"))
	return out
}

func mark(sel bool, s string) string {
	if sel {
		return cy.Render("◆ ") + s
	}
	return dim.Render("◇ ") + s
}

const leftW = 26

func split(l, r string, w int) string {
	return " " + Pad(l, leftW) + dim.Render(" │ ") + r
}

func fillRow(sp screenSpec, w int) string {
	if sp.tab == 0 && !sp.noMan {
		return split("", "", w)
	}
	return ""
}

// ---- commands ----

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

func commandsBody(sp screenSpec, w int) []string {
	sel := wrap(sp.cmdSel, len(cmds))
	c := cmds[sel]

	// Names only. No column header, and no last-run column: last run is one of
	// the reader's fields, and repeating it in the list spent the width the
	// longest command name needs.
	var left []string
	for i, cc := range cmds {
		row := cc.name
		if sp.running && cc.name == "test" {
			row += " " + yl.Render("●")
		}
		left = append(left, mark(i == sel, row))
	}

	title := bo.Render(c.name)
	if sp.running && c.name == "test" {
		title += dim.Render("  ·  ") + yl.Render("running") + dim.Render("  ·  00:41")
	}
	var right []string
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
		right = append(right, bo.Render("Targets")+dim.Render("  resolved now · 4 match"))
		for _, t := range c.targets {
			right = append(right, "  "+t)
		}
	case sp.running && c.name == "test":
		right = append(right, bo.Render("Output"))
		for _, l := range []string{
			gr.Render("ok") + "   internal/archetype   " + dim.Render("cached"),
			gr.Render("ok") + "   internal/detect      " + dim.Render("0.63s"),
			gr.Render("ok") + "   internal/kinds       " + dim.Render("cached"),
			dim.Render("..") + "   internal/tui",
		} {
			right = append(right, "  "+l)
		}
	default:
		right = append(right, bo.Render("Steps"))
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

// ---- repo ----

func repoBody(w, h int, noMan bool) []string {
	lw := w * 58 / 100
	rw := w - lw - 3

	var branches []string
	if noMan {
		branches = []string{mark(true, Pad("main", lw-12) + dim.Render("4d"))}
	} else {
		branches = []string{
			mark(true, Pad("feat/one-tool-go-merge", lw-12)+dim.Render("2m")),
			"",
			dim.Render(" Continue Working  ── you · keep ──────  1"),
			mark(false, Pad("fix-detector-parity-skip", lw-16)+mg.Render("wt")+"  "+dim.Render("2d")),
			dim.Render("   ↳ commit fix(detect): fail parity test…"),
			"",
			dim.Render(" Merged  ── keep ──────────────────────  1"),
			mark(false, Pad("main", lw-12)+dim.Render("15h")),
		}
	}
	left := pane("Branches", len(branches), lw, branches, h)

	commits := []string{
		yl.Render("fdf3b2e") + dim.Render("  feat(spikes): interactive…   2m"),
		yl.Render("db2711a") + dim.Render("  docs(adr): Lua and Bash…     3h"),
	}
	if noMan {
		commits = []string{yl.Render("a1b2c3d") + dim.Render("  Initial commit              4d")}
	}
	topH := h - 6
	right := pane("Recent commits", len(commits), rw, commits, topH)
	right = append(right, pane("Stashes", 0, rw, []string{dim.Render("(none)")}, h-topH)...)

	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		out = append(out, " "+at(left, i)+" "+at(right, i))
	}
	return out
}

// ---- issues ----

type issueRow struct{ id, title, meta string }

var readyIssues = []issueRow{
	{"admin-3rgp.36", "Embed a Lua VM and define the host API", "afk"},
	{"admin-3rgp.29", "Two deployment paths: go install, archetypes", "afk"},
	{"admin-3rgp.7", "Decide how admin <command> reaches the CLI", "decision"},
}

var blockedIssues = []issueRow{
	{"admin-3rgp.20", "Commands tab with the resolved-action reader", "← .19"},
	{"admin-3rgp.28", "The live-work tab: workers and processes", "← .19"},
	{"admin-3rgp.35", "Implement the transient kind factories", "← .36"},
	{"admin-3rgp.37", "Rewrite the 14 archetypes in Lua and Bash", "← .36"},
}

func issueLine(sel bool, is issueRow, w int, blocked bool) string {
	cursor := "  "
	if sel {
		cursor = cy.Render("▸ ")
	}
	meta := dim.Render(is.meta)
	if blocked {
		meta = rd.Render(is.meta)
	}
	titleW := maxInt(10, w-22-VW(is.meta))
	return " " + cursor + Pad(is.id, 16) + Pad(is.title, titleW) + meta
}

func issuesBody(sp screenSpec, w int) []string {
	all := append(append([]issueRow{}, readyIssues...), blockedIssues...)
	isel := wrap(sp.issueSel, len(all))

	out := section("Ready", len(readyIssues), w)
	for i, is := range readyIssues {
		out = append(out, issueLine(i == isel, is, w, false))
	}
	out = append(out, "")
	out = append(out, section("Blocked", len(blockedIssues), w)...)
	for i, is := range blockedIssues {
		out = append(out, issueLine(len(readyIssues)+i == isel, is, w, true))
	}
	return out
}

// ---- live work ----

func liveBody(w int) []string {
	st := map[string]lipgloss.Style{"running": gr, "waiting": yl, "done": dim, "failed": rd}
	row := func(sel bool, s, id, br, doing, where string) string {
		cursor := "  "
		if sel {
			cursor = cy.Render("▸ ")
		}
		return " " + cursor + st[s].Render(Pad(s, 9)) + Pad(id, 16) + Pad(br, 18) +
			dim.Render(Pad(doing, maxInt(6, w-53))+where)
	}
	out := section("Agent workers", 4, w)
	out = append(out,
		row(false, "running", "admin-3rgp.36", "embed-lua-vm", "writing internal/lua/host.go", "pane 3"),
		row(true, "waiting", "admin-3rgp.7", "cli-identity", "needs input: root command", "pane 4"),
		row(false, "done", "admin-3rgp.34", "fix-parity-skip", "verdict PASS · c31e994", "pane 2"),
		row(false, "failed", "admin-3rgp.33", "detector-clear", "go test ./internal/detect FAIL", "pane 5"),
	)
	out = append(out, "")
	out = append(out, section("Long-running processes", 1, w)...)
	out = append(out, row(false, "running", "admin docs", "vitepress dev", "http://localhost:5173", "pane 1"))
	return out
}

// ---- config ----

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
	out := section("Resolved configuration", len(rows), w)
	for _, r := range rows {
		src := dim.Render(r[2])
		if r[2] == "admin.toml" {
			src = cy.Render(r[2])
		}
		out = append(out, "   "+Pad(r[0], 22)+Pad(r[1], maxInt(10, w-40))+src)
	}
	return out
}

// ---- dispatch sheet ----

func overlaySheet(rows []string, w, h int) []string {
	bw := 60
	box := []string{"┌" + strings.Repeat("─", bw) + "┐"}
	add := func(s string) { box = append(box, "│"+Pad(s, bw)+"│") }
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

// ---- routing ----

func bodyOf(sp screenSpec, w, h int) []string {
	switch {
	case sp.noMan:
		return repoBody(w, h, true)
	case sp.tab == 0:
		return commandsBody(sp, w)
	case sp.tab == 1:
		return repoBody(w, h, false)
	case sp.tab == 2:
		return issuesBody(sp, w)
	case sp.tab == 3:
		return liveBody(w)
	}
	return configBody(w)
}

func keys(sp screenSpec) string {
	switch {
	case sp.sheet:
		return "⏎ dispatch · e edit · esc cancel"
	case sp.noMan:
		return "↑↓ move · ↵ checkout · admin new scaffolds a manifest · ←→ tab"
	case sp.tab == 0:
		return "↑↓ move · ↵ run · / filter · L log · ←→ tab · q quit"
	case sp.tab == 1:
		return "↑↓ move · ↵ checkout · ⌥↵/W worktree · p pull · x delete · ←→ tab"
	case sp.tab == 2:
		return "↑↓ move · ↵ dispatch · c claim · b branch · / filter · ←→ tab"
	case sp.tab == 3:
		return "↑↓ move · a attach · k kill · r retry · ←→ tab"
	}
	return "↑↓ move · ←→ tab · q quit"
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
