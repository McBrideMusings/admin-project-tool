// Reference prototype for the admin dashboard TUI (admin-3rgp.19).
//
// Throwaway, but runnable. This is the settled design as a real bubbletea app —
// tabs switch, lists move, the reader pane tracks the selection, the dispatch
// sheet opens and closes. Run it and drive it:
//
//	go run ./docs/spikes/admin-tui-desktop
//
// The ten committed .txt frames are dumps of this same View(), not hand-written
// strings, so a frame can never drift from the prototype it claims to depict:
//
//	go run ./docs/spikes/admin-tui-desktop -dump
//
// Colour follows the rules pinned by internal/tui/design_parity_test.go: 16-colour
// ANSI theme slots, never hardcoded RGB, and the active tab is a pill — the only
// background-painted element on the tab bar. Dumps are ANSI-stripped, so a .txt
// pins geometry while the parity tests pin colour.
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	W = 96
	H = 30
)

// ---------- palette: ANSI slots only ----------

var (
	dim      = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))
	cyan     = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(6))
	green    = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2))
	yellow   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(3))
	red      = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(1))
	magenta  = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(5))
	bold     = lipgloss.NewStyle().Bold(true)
	pill     = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(0)).Background(lipgloss.ANSIColor(6)).Bold(true)
	inactive = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(7))
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func strip(s string) string { return ansiRe.ReplaceAllString(s, "") }

// vw is visible width — runes after ANSI is removed. Box-drawing characters are
// three bytes each, so byte length is always wrong here.
func vw(s string) int { return len([]rune(strip(s))) }

func pad(s string, w int) string {
	if n := vw(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// ---------- data ----------

type cmd struct {
	name, last, kind, arch, note string
	// n, kind, run, contributing archetype — the reader names the archetype
	// per STEP, not per command, because a merged command routinely mixes them.
	steps    [][4]string
	workflow bool
	targets  []string
}

var commandList = []cmd{
	{name: "build", last: "2h ago", kind: "shell", arch: "go-cli",
		steps: [][4]string{{"1", "shell", "go build ./...", "go-cli"}}},
	{name: "check", last: "2h ago", kind: "shell", arch: "(manifest)",
		steps: [][4]string{{"1", "shell", "admin check .", "(manifest)"}}},
	{name: "deploy", last: "18m ago", kind: "sequence", arch: "(manifest)",
		note: "exit 0 · 41s",
		steps: [][4]string{
			{"1", "shell", "bash install.sh", "simple"},
			{"2", "lua", "scripts/provision.lua", "apple"},
			{"3", "shell", "admin check .", "(manifest)"},
		}},
	{name: "docs", last: "—", kind: "shell", arch: "docs",
		steps: [][4]string{{"1", "shell", "bun run docs:dev", "docs"}}},
	{name: "logs", last: "—", kind: "logs", arch: "(manifest)",
		steps: [][4]string{{"1", "logs", "admin logs dev", "docker"}}},
	{name: "retire-merged", last: "never", kind: "workflow", arch: "(manifest)", workflow: true,
		targets: []string{"fix-detector-parity-skip", "go-transient-kind-factories", "docs-adr-lua", "launchd-env-file"}},
	{name: "test", last: "6m ago", kind: "sequence", arch: "(manifest)",
		steps: [][4]string{
			{"1", "go", "go test ./...", "go-cli"},
			{"2", "shell", "bash tests/test_install.sh", "(manifest)"},
		}},
}

type branch struct {
	name, ahead, behind, wt, age, subject string
	current                               bool
}

var branches = []branch{
	{name: "feat/one-tool-go-merge", ahead: "1", behind: "0", wt: "", age: "2m", subject: "docs(adr): Lua and Bash are the archetype", current: true},
	{name: "fix-detector-parity-skip", ahead: "1", behind: "0", wt: "wt", age: "2d", subject: "fix(detect): fail parity test instead of"},
	{name: "main", ahead: "0", behind: "0", wt: "", age: "15h", subject: "Merge pull request #1016"},
}

type issue struct{ id, pri, title, blocker string }

var ready = []issue{
	{"admin-3rgp.36", "P1", "Embed a Lua VM and define the host API", ""},
	{"admin-3rgp.29", "P1", "Two deployment paths: go install, archetypes", ""},
	{"admin-3rgp.7", "P1", "Decide how admin <command> reaches the CLI", ""},
}

var blocked = []issue{
	{"admin-3rgp.20", "P1", "Commands tab with the resolved-action reader", ".19"},
	{"admin-3rgp.28", "P1", "The live-work tab: workers and processes", ".19"},
	{"admin-3rgp.35", "P1", "Implement the transient kind factories", ".36"},
	{"admin-3rgp.37", "P1", "Rewrite the 14 archetypes in Lua and Bash", ".36"},
}

type worker struct{ state, id, branch, doing, pane string }

var workers = []worker{
	{"running", "admin-3rgp.36", "embed-lua-vm", "writing internal/lua/host.go", "pane 3"},
	{"waiting", "admin-3rgp.7", "cli-identity", "needs input: root command name", "pane 4"},
	{"done", "admin-3rgp.34", "fix-parity-skip", "verdict PASS · c31e994", "pane 2"},
	{"failed", "admin-3rgp.33", "detector-clear", "go test ./internal/detect FAIL", "pane 5"},
}

var processes = []worker{
	{"running", "admin docs", "vitepress dev", "http://localhost:5173", "pane 1"},
}

var configRows = [][3]string{
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

// ---------- model ----------

type tabID int

const (
	tCommands tabID = iota
	tRepo
	tIssues
	tLive
	tConfig
)

type Model struct {
	tab        tabID
	cur        map[tabID]int
	sheet      bool
	running    bool
	noManifest bool
}

func New() Model { return Model{cur: map[tabID]int{}} }

// tabs returns the visible tabs. A tab the project cannot support is absent, not
// disabled — no manifest means no Commands tab at all.
func (m Model) tabs() []struct {
	id   tabID
	name string
} {
	all := []struct {
		id   tabID
		name string
	}{
		{tCommands, "Commands"}, {tRepo, "Repo"}, {tIssues, "Issues"},
		{tLive, "Live"}, {tConfig, "Config"},
	}
	if m.noManifest {
		return all[1:]
	}
	return all
}

func (m Model) rowsOn(t tabID) int {
	switch t {
	case tCommands:
		return len(commandList)
	case tRepo:
		return len(branches)
	case tIssues:
		return len(ready) + len(blocked)
	case tLive:
		return len(workers) + len(processes)
	}
	return 0
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.sheet {
		switch k.String() {
		case "esc", "q":
			m.sheet = false
		case "enter":
			m.sheet = false
		}
		return m, nil
	}
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab", "right", "l":
		vis := m.tabs()
		for i, t := range vis {
			if t.id == m.tab {
				m.tab = vis[(i+1)%len(vis)].id
				return m, nil
			}
		}
		m.tab = vis[0].id
	case "shift+tab", "left", "h":
		vis := m.tabs()
		for i, t := range vis {
			if t.id == m.tab {
				m.tab = vis[(i-1+len(vis))%len(vis)].id
				return m, nil
			}
		}
	case "down", "j":
		if n := m.rowsOn(m.tab); n > 0 {
			m.cur[m.tab] = (m.cur[m.tab] + 1) % n
		}
	case "up", "k":
		if n := m.rowsOn(m.tab); n > 0 {
			m.cur[m.tab] = (m.cur[m.tab] - 1 + n) % n
		}
	case "enter":
		if m.tab == tIssues {
			m.sheet = true
		}
		if m.tab == tCommands {
			m.running = !m.running
		}
	case "m":
		m.noManifest = !m.noManifest
		m.tab = tRepo
	}
	return m, nil
}

// ---------- view ----------

func (m Model) View() string {
	var b []string
	b = append(b, m.headerRow())
	b = append(b, m.tabBar())
	b = append(b, strings.Repeat("─", W))
	body := m.body()
	// header + tab bar + rule + body + rule + footer == H
	for len(body) < H-5 {
		body = append(body, m.fill())
	}
	b = append(b, body[:H-5]...)
	b = append(b, strings.Repeat("─", W))
	b = append(b, pad(" "+dim.Render(m.keys()), W))
	if m.sheet {
		b = overlay(b, sheetBox())
	}
	for i := range b {
		b[i] = pad(b[i], W)
	}
	return strings.Join(b, "\n")
}

func (m Model) headerRow() string {
	l := " " + bold.Render("admin") + dim.Render(" · admin-project-tool")
	r := cyan.Render("feat/one-tool-go-merge") + " "
	return l + strings.Repeat(" ", max(1, W-vw(l)-vw(r))) + r
}

func (m Model) tabBar() string {
	var sb strings.Builder
	sb.WriteString(" ")
	for _, t := range m.tabs() {
		if t.id == m.tab {
			sb.WriteString(pill.Render(" " + t.name + " "))
		} else {
			sb.WriteString(inactive.Render("  " + t.name + "  "))
		}
	}
	return sb.String()
}

func (m Model) fill() string {
	if m.tab == tCommands && !m.noManifest {
		return split("", "", 30)
	}
	if m.tab == tIssues && !m.sheet {
		return split("", "", 44)
	}
	return ""
}

func (m Model) keys() string {
	if m.sheet {
		return "⏎ dispatch   e edit   esc cancel"
	}
	switch m.tab {
	case tCommands:
		return "↑↓ move   ⏎ run   ⇥ switch tab   / filter   m toggle manifest   q quit"
	case tRepo:
		return "↑↓ move   ⏎ checkout   w worktree   f fetch   ⇥ switch tab   q quit"
	case tIssues:
		return "↑↓ move   ⏎ dispatch   c claim   b branch   ⇥ switch tab   q quit"
	case tLive:
		return "↑↓ move   a attach   k kill   r retry   ⇥ switch tab   q quit"
	}
	return "↑↓ move   ⇥ switch tab   q quit"
}

func split(l, r string, lw int) string {
	return " " + pad(l, lw) + dim.Render(" │ ") + r
}

func rowMark(sel bool, s string) string {
	if sel {
		return cyan.Render("▸ ") + s
	}
	return "  " + s
}

func (m Model) body() []string {
	switch m.tab {
	case tCommands:
		return m.viewCommands()
	case tRepo:
		return m.viewRepo()
	case tIssues:
		return m.viewIssues()
	case tLive:
		return m.viewLive()
	case tConfig:
		return m.viewConfig()
	}
	return nil
}

func (m Model) viewCommands() []string {
	lw := 30
	sel := m.cur[tCommands]
	c := commandList[sel]
	var right []string
	right = append(right, bold.Render(c.name)+running(m.running, c.name))
	right = append(right, dim.Render(strings.Repeat("─", 58)))
	right = append(right, dim.Render("kind      ")+c.kind)
	right = append(right, dim.Render("archetype ")+c.arch)
	right = append(right, dim.Render("last run  ")+c.last+ifs(c.note != "", " · "+c.note))
	right = append(right, dim.Render("log       ")+"~/.admin/logs/"+c.name+".log")
	right = append(right, "")
	if c.workflow {
		right = append(right, dim.Render("source    ")+"query")
		right = append(right, "")
		right = append(right, bold.Render("TARGETS")+dim.Render("  resolved now · 4 match"))
		for _, t := range c.targets {
			right = append(right, "  "+t)
		}
		right = append(right, "")
		right = append(right, dim.Render("A query-sourced workflow lists here and"))
		right = append(right, dim.Render("runs bare. A selection-sourced one does"))
		right = append(right, dim.Render("not appear — it is offered on a row."))
	} else if m.running && c.name == "test" {
		right = append(right, bold.Render("OUTPUT"))
		for _, l := range []string{
			green.Render("ok") + "   internal/archetype   cached",
			green.Render("ok") + "   internal/detect      0.63s",
			green.Render("ok") + "   internal/kinds       cached",
			dim.Render("..") + "   internal/tui",
		} {
			right = append(right, "  "+l)
		}
	} else {
		right = append(right, bold.Render("STEPS"))
		for _, s := range c.steps {
			right = append(right, "  "+dim.Render(s[0]+"  ")+pad(s[1], 10)+s[2])
			right = append(right, "     "+dim.Render("from  ")+magenta.Render(s[3]))
			right = append(right, "")
		}
	}

	var left []string
	left = append(left, dim.Render("  COMMAND          LAST RUN"))
	left = append(left, "")
	for i, cc := range commandList {
		last := cc.last
		if m.running && cc.name == "test" {
			last = yellow.Render("running")
		}
		left = append(left, rowMark(i == sel, pad(cc.name, 16)+" "+dim.Render(last)))
	}

	n := max(len(left), len(right))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, split(at(left, i), at(right, i), lw))
	}
	return out
}

func running(on bool, name string) string {
	if on && name == "test" {
		return dim.Render("  ·  ") + yellow.Render("running") + dim.Render("  ·  00:41")
	}
	return ""
}

func (m Model) viewRepo() []string {
	sel := m.cur[tRepo]
	var o []string
	o = append(o, " "+bold.Render("BRANCHES"))
	for i, b := range branches {
		mark := "  "
		if b.current {
			mark = green.Render("* ")
		}
		wt := "  "
		if b.wt != "" {
			wt = magenta.Render("wt")
		}
		line := pad(b.name, 32) + dim.Render("↑")+ b.ahead + dim.Render(" ↓") + b.behind + "  " + wt +
			dim.Render("  "+pad(b.age, 5)+b.subject)
		o = append(o, " "+rowMark(i == sel, mark+line))
	}
	o = append(o, "")
	o = append(o, " "+bold.Render("WORKTREES"))
	o = append(o, "   ~/.worktrees/admin-project-tool/fix-detector-parity-skip     "+green.Render("clean"))
	o = append(o, "")
	o = append(o, " "+bold.Render("STASHES")+dim.Render("   (none)")) // empty section collapses to one line
	o = append(o, "")
	o = append(o, " "+bold.Render("RECENT"))
	for _, c := range [][2]string{
		{"d7d90d7", "docs(spikes): rebuild the admin dashboard reference frames"},
		{"db2711a", "docs(adr): Lua and Bash are the archetype authoring languages"},
		{"f738934", "feat(launchd): lift allowlisted env vars from a dotenv file"},
	} {
		o = append(o, "   "+yellow.Render(c[0])+"  "+dim.Render(c[1]))
	}
	return o
}

func (m Model) viewIssues() []string {
	lw := 44
	sel := m.cur[tIssues]
	all := append(append([]issue{}, ready...), blocked...)
	cursel := all[sel]

	var left []string
	left = append(left, bold.Render("READY")+dim.Render("            tracker: beads"))
	for i, is := range ready {
		left = append(left, rowMark(i == sel, dim.Render(is.pri)+" "+trunc(is.title, 36)))
	}
	left = append(left, "")
	left = append(left, bold.Render("BLOCKED"))
	for i, is := range blocked {
		left = append(left, rowMark(len(ready)+i == sel, dim.Render(is.pri)+" "+trunc(is.title, 30)+red.Render(" ←"+is.blocker)))
	}

	var right []string
	right = append(right, bold.Render(cursel.id)+dim.Render("   "+cursel.pri+"  open"))
	right = append(right, dim.Render(strings.Repeat("─", 46)))
	right = append(right, cursel.title)
	right = append(right, "")
	if cursel.blocker != "" {
		right = append(right, bold.Render("BLOCKED BY"))
		right = append(right, "  "+red.Render("admin-3rgp"+cursel.blocker))
	} else {
		right = append(right, bold.Render("READY")+dim.Render("  no open blockers"))
	}
	right = append(right, "")
	right = append(right, bold.Render("LABELS")+dim.Render("  afk"))
	right = append(right, "")
	right = append(right, dim.Render("⏎ opens the dispatch sheet."))

	n := max(len(left), len(right))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, split(at(left, i), at(right, i), lw))
	}
	return out
}

func (m Model) viewLive() []string {
	sel := m.cur[tLive]
	stateCol := map[string]lipgloss.Style{
		"running": green, "waiting": yellow, "done": dim, "failed": red,
	}
	var o []string
	o = append(o, " "+bold.Render("AGENT WORKERS"))
	for i, w := range workers {
		o = append(o, " "+rowMark(i == sel, stateCol[w.state].Render(pad(w.state, 9))+pad(w.id, 15)+pad(w.branch, 18)+dim.Render(pad(w.doing, 32)+w.pane)))
	}
	o = append(o, "")
	o = append(o, " "+bold.Render("LONG-RUNNING PROCESSES"))
	for i, w := range processes {
		o = append(o, " "+rowMark(len(workers)+i == sel, stateCol[w.state].Render(pad(w.state, 9))+pad(w.id, 15)+pad(w.branch, 18)+dim.Render(pad(w.doing, 32)+w.pane)))
	}
	o = append(o, "")
	o = append(o, dim.Render(" One registry, both kinds. \"waiting\" is distinguishable from \"running\""))
	o = append(o, dim.Render(" without attaching to anything."))
	return o
}

func (m Model) viewConfig() []string {
	var o []string
	o = append(o, " "+bold.Render("RESOLVED CONFIGURATION")+dim.Render("                          source"))
	o = append(o, "")
	for _, r := range configRows {
		src := dim.Render(r[2])
		if r[2] == "admin.toml" {
			src = cyan.Render(r[2])
		}
		o = append(o, "   "+pad(r[0], 22)+pad(r[1], 36)+src)
	}
	o = append(o, "")
	o = append(o, dim.Render(" Values render resolved. Editing the manifest from here is out of scope."))
	return o
}

func sheetBox() []string {
	w := 62
	var o []string
	o = append(o, "┌"+strings.Repeat("─", w)+"┐")
	add := func(s string) { o = append(o, "│"+pad(s, w)+"│") }
	add("  " + bold.Render("Dispatch admin-3rgp.36"))
	add("")
	add("    " + dim.Render("agent     ") + "claude")
	add("    " + dim.Render("model     ") + "sonnet")
	add("    " + dim.Render("branch    ") + "embed-lua-vm")
	add("    " + dim.Render("worktree  ") + "~/.worktrees/admin-project-tool/embed-lua-vm")
	add("    " + dim.Render("lands to  ") + cyan.Render("feat/one-tool-go-merge"))
	add("")
	add("    " + green.Render("⏎ dispatch") + "     e edit     " + dim.Render("esc cancel"))
	o = append(o, "└"+strings.Repeat("─", w)+"┘")
	return o
}

// overlay centres a box over the frame body.
func overlay(base, box []string) []string {
	top := (H - len(box)) / 2
	left := (W - vw(box[0])) / 2
	out := append([]string{}, base...)
	for i, l := range box {
		y := top + i
		if y < 3 || y >= H-2 {
			continue
		}
		row := []rune(strip(out[y]))
		for len(row) < W {
			row = append(row, ' ')
		}
		out[y] = string(row[:left]) + l + string(row[min(len(row), left+vw(l)):])
	}
	return out
}

// ---------- helpers ----------

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}
func ifs(c bool, s string) string {
	if c {
		return s
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
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------- dump ----------

type state struct {
	name  string
	apply func(*Model)
}

var states = []state{
	{"commands", func(m *Model) { m.tab = tCommands; m.cur[tCommands] = 2 }},
	{"commands-workflow-selected", func(m *Model) { m.tab = tCommands; m.cur[tCommands] = 5 }},
	{"repo", func(m *Model) { m.tab = tRepo }},
	{"issues", func(m *Model) { m.tab = tIssues }},
	{"issues-selected", func(m *Model) { m.tab = tIssues; m.cur[tIssues] = 3 }},
	{"dispatch-sheet", func(m *Model) { m.tab = tIssues; m.sheet = true }},
	{"swarm", func(m *Model) { m.tab = tLive; m.cur[tLive] = 1 }},
	{"config", func(m *Model) { m.tab = tConfig }},
	{"running", func(m *Model) { m.tab = tCommands; m.cur[tCommands] = 6; m.running = true }},
	{"no-manifest", func(m *Model) { m.noManifest = true; m.tab = tRepo }},
}

func dump(dir string) int {
	lipgloss.SetColorProfile(0) // Ascii: no escapes in the dumped frames
	bad := 0
	for _, s := range states {
		m := New()
		s.apply(&m)
		body := strip(m.View())
		lines := strings.Split(body, "\n")
		if len(lines) != H {
			fmt.Printf("FAIL %-28s %d rows, want %d\n", s.name, len(lines), H)
			bad++
		}
		for i, l := range lines {
			if n := len([]rune(l)); n != W {
				fmt.Printf("FAIL %-28s row %d is %d cols, want %d\n", s.name, i, n, W)
				bad++
			}
		}
		if err := os.WriteFile(dir+"/"+s.name+".txt", []byte(body+"\n"), 0o644); err != nil {
			fmt.Println("write:", err)
			return 1
		}
		fmt.Printf("ok   %-28s %dx%d\n", s.name, W, H)
	}
	if bad > 0 {
		fmt.Printf("\n%d geometry violations\n", bad)
		return 1
	}
	fmt.Printf("\n%d frames, all %dx%d\n", len(states), W, H)
	return 0
}

func main() {
	d := flag.Bool("dump", false, "write the reference frames as .txt and exit")
	dir := flag.String("dir", ".", "directory to write frames into")
	flag.Parse()
	if *d {
		os.Exit(dump(*dir))
	}
	if _, err := tea.NewProgram(New(), tea.WithAltScreen()).Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
