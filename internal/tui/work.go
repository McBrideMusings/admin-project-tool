// work.go is the Work tab's data layer (admin-3rgp.20): resolving the
// project's admin.toml, building the combined command+worker row list, and
// dispatching a command. workview.go is the rendering half.
package tui

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// workRowData is the Work-tab payload riding on a rowItem — either a
// declared command (idle, or backed by a currently-running livework.Entry)
// or a genuine agent worker with no command behind it. The two shapes share
// almost no fields; see the epic's own note on why that is exactly why the
// detail pane has to be polymorphic rather than one wide field set.
type workRowData struct {
	// Command rows (rowCommand):
	cmdName      string
	cmdDesc      string
	cmdKind      string // the sole step's declared kind, or "sequence"
	cmdArchetype string
	steps        []workStepInfo
	// workflow is non-nil only for a query-sourced kind="workflow" command
	// (a selection-sourced one never reaches this list at all — see
	// declaredCommands). Actually dispatching it — resolving the query and
	// confirming the resolved set — is admin-3rgp.26's job.
	workflow *kinds.WorkflowTarget
	// live is the livework.Entry currently backing this command, if any —
	// set only for a process-kind entry (Worker == nil); a genuine agent
	// worker never folds into a command row, it gets its own row below.
	live    *livework.Entry
	lastRun time.Time
	logPath string

	// Worker rows (rowWorker):
	worker *livework.Entry
}

// workStepInfo is one step's display: its position, its declared (or
// defaulted) kind, and which archetype (or "(manifest)") contributed it.
type workStepInfo struct {
	idx        int
	name, kind string
	archetype  string
}

// loadWorkManifest resolves repoRoot's admin.toml against its declared
// archetypes — the same two calls cmd.resolveProjectManifest (cmd/
// manifest.go) makes for the CLI, duplicated here in package tui because
// cmd/ already imports tui and importing back would cycle. ok is false only
// when repoRoot has no admin.toml at all, never paired with a non-nil err,
// matching that function's own contract: "no manifest here" (fall through
// silently) is a different outcome from "a manifest here and it's broken"
// (a real error). project is the manifest before archetype merging — kept
// alongside merged (the resolved one) because computeOrigins needs the
// project's own declared Archetypes list and its own directly-declared
// commands/actions, neither of which survive the merge.
func loadWorkManifest(repoRoot string) (project, merged *manifest.Manifest, sp archetype.SearchPath, ok bool, err error) {
	if _, statErr := os.Stat(filepath.Join(repoRoot, manifest.Filename)); statErr != nil {
		return nil, nil, nil, false, nil
	}
	project, err = manifest.Load(repoRoot)
	if err != nil {
		return nil, nil, nil, true, err
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		return nil, nil, nil, true, herr
	}
	sp = archetype.DefaultSearchPath(home, repoRoot)
	merged, _, err = archetype.Resolve(project, sp)
	if err != nil {
		return nil, nil, nil, true, err
	}
	return project, merged, sp, true, nil
}

// computeOrigins walks project.Archetypes in declared order (last wins,
// matching archetype.Resolve's own ADR-0002 merge order) to find which
// archetype last contributed each command/action name, falling back to
// "(manifest)" for anything the project itself declares directly.
// archetype.Resolve doesn't expose this itself — its own merge log records
// only a COLLISION ("X from A overridden by B"), not "here is who owns
// every key" — so this is a second, read-only walk of the same search path,
// purely for display.
func computeOrigins(project *manifest.Manifest, sp archetype.SearchPath) (cmds, actions map[string]string) {
	cmds, actions = map[string]string{}, map[string]string{}
	for _, name := range project.Archetypes {
		arch, err := archetype.Load(sp, name)
		if err != nil {
			continue
		}
		for c := range arch.Partial.Custom {
			cmds[c] = name
		}
		for c := range arch.Partial.Overrides {
			cmds[c] = name
		}
		for a := range arch.Partial.Actions {
			actions[a] = name
		}
	}
	// The project always wins (ADR-0009), including a field-level override
	// of a command an archetype already provides — from the display's point
	// of view the manifest is still the most specific place that named this
	// command, exactly like every other manifest-level key.
	for c := range project.Custom {
		cmds[c] = "(manifest)"
	}
	for c := range project.Overrides {
		cmds[c] = "(manifest)"
	}
	for a := range project.Actions {
		actions[a] = "(manifest)"
	}
	return cmds, actions
}

// actionKind resolves a raw action's declared kind, defaulting to "shell" —
// mirroring kinds.RunAction's own default (internal/kinds/dispatch.go)
// exactly, so a command's displayed kind never disagrees with what running
// it would actually dispatch.
func actionKind(a manifest.Action) string {
	if v, ok := a["kind"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "shell"
}

// soleAction returns c's one action, when it has exactly one step — the
// shape both the "single-step command's own kind" and "is this a workflow"
// checks need.
func (m Model) soleAction(c manifest.Command) (manifest.Action, bool) {
	if len(c.Steps) != 1 {
		return manifest.Action{}, false
	}
	a, ok := m.manifest.Actions[c.Steps[0]]
	return a, ok
}

// commandKind is a command's displayed kind: a single step's own declared
// kind, or "sequence" for more than one — matching the reference frames
// (docs/spikes/admin-tui-desktop/commands.txt: "deploy" with three steps
// shows "sequence", "build" with one shows "shell"). "?" marks a step
// naming an undeclared action, which a broken manifest can produce and
// which check (not this tab) is the place to reject.
func (m Model) commandKind(c manifest.Command) string {
	switch len(c.Steps) {
	case 0:
		return "?"
	case 1:
		if a, ok := m.soleAction(c); ok {
			return actionKind(a)
		}
		return "?"
	default:
		return "sequence"
	}
}

// commandSteps builds the detail pane's per-step listing: each step's
// position, its declared (or defaulted) kind, and which archetype (or
// "(manifest)") contributed the action it names.
func (m Model) commandSteps(c manifest.Command) []workStepInfo {
	out := make([]workStepInfo, 0, len(c.Steps))
	for i, s := range c.Steps {
		kind := "?"
		if a, ok := m.manifest.Actions[s]; ok {
			kind = actionKind(a)
		}
		arch := m.actionOrigin[s]
		if arch == "" {
			arch = "(manifest)"
		}
		out = append(out, workStepInfo{idx: i + 1, name: s, kind: kind, archetype: arch})
	}
	return out
}

// commandLastRun is the best-effort "last run" and "log" the detail pane
// shows: a glob over internal/luahost's own log directory convention
// (<repoRoot>/tmp/<stem>.<timestamp>.log — internal/luahost/host.go's LogDir
// default) for each of the command's step names, taking the newest match
// across all of them. This is real filesystem state, not a persistence
// layer this ticket invents: a step whose body never calls
// set_log_context(name) under its own action name simply has nothing to
// find, and degrades to "never" rather than a fabricated guess.
func commandLastRun(repoRoot string, c manifest.Command) (last time.Time, logPath string) {
	logDir := filepath.Join(repoRoot, "tmp")
	for _, step := range c.Steps {
		matches, _ := filepath.Glob(filepath.Join(logDir, step+".*.log"))
		for _, p := range matches {
			info, err := os.Stat(p)
			if err != nil {
				continue
			}
			if info.ModTime().After(last) {
				last, logPath = info.ModTime(), p
			}
		}
	}
	return last, logPath
}

// workCmdRow pairs a declared command's name with its parsed body — what
// declaredCommands orders and workRows renders.
type workCmdRow struct {
	name string
	cmd  manifest.Command
}

// declaredCommands is every command the Work tab offers, in a stable order
// that never depends on live state (the ticket's own "does not jump when
// something starts or stops"): the manifest's declared `order` when it sets
// one (Order wins outright — it exists specifically to say how the menu
// should read), otherwise every command grouped by its own `group` then
// `priority` then name, ascending, matching what those fields already mean
// on manifest.Command. hide_commands is dropped, and a workflow whose `over`
// is "selection" never appears here at all — WorkflowTarget.
// AppearsInCommandList (internal/kinds/workflow.go) is the single source of
// truth for that rule.
func (m Model) declaredCommands() []workCmdRow {
	if !m.manifestOK {
		return nil
	}
	hidden := map[string]bool{}
	for _, h := range m.manifest.HideCommands {
		hidden[h] = true
	}
	lookup := func(name string) (manifest.Command, bool) {
		if c, ok := m.manifest.Custom[name]; ok {
			return c, true
		}
		c, ok := m.manifest.Overrides[name]
		return c, ok
	}
	seen := map[string]bool{}
	var out []workCmdRow
	add := func(name string, c manifest.Command) {
		if hidden[name] || seen[name] {
			return
		}
		if a, ok := m.soleAction(c); ok && actionKind(a) == "workflow" {
			if target, err := kinds.ParseWorkflowOver(c.Steps[0], a); err == nil && !target.AppearsInCommandList() {
				return
			}
		}
		seen[name] = true
		out = append(out, workCmdRow{name: name, cmd: c})
	}

	if len(m.manifest.Order) > 0 {
		for _, name := range m.manifest.Order {
			if name == manifest.Spacer {
				continue
			}
			if c, ok := lookup(name); ok {
				add(name, c)
			}
		}
		rest := unseenCommandNames(m.manifest, seen)
		sort.Strings(rest)
		for _, name := range rest {
			if c, ok := lookup(name); ok {
				add(name, c)
			}
		}
		return out
	}

	type item struct {
		name string
		cmd  manifest.Command
	}
	var items []item
	for name, c := range m.manifest.Custom {
		items = append(items, item{name, c})
	}
	for name, c := range m.manifest.Overrides {
		if _, exists := m.manifest.Custom[name]; !exists {
			items = append(items, item{name, c})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].cmd.Group != items[j].cmd.Group {
			return items[i].cmd.Group < items[j].cmd.Group
		}
		if items[i].cmd.Priority != items[j].cmd.Priority {
			return items[i].cmd.Priority < items[j].cmd.Priority
		}
		return items[i].name < items[j].name
	})
	for _, it := range items {
		add(it.name, it.cmd)
	}
	return out
}

// unseenCommandNames is every command name declared (custom or
// archetype-provided) that isn't already in seen — declaredCommands' own
// "whatever the declared order left out" fallback.
func unseenCommandNames(m *manifest.Manifest, seen map[string]bool) []string {
	set := map[string]bool{}
	for name := range m.Custom {
		if !seen[name] {
			set[name] = true
		}
	}
	for name := range m.Overrides {
		if !seen[name] {
			set[name] = true
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	return out
}

// liveEntryFor returns the process-kind livework.Entry currently backing
// c, if any: a LongRunning action is recorded under its own action name
// (internal/kinds/pane.go's recordLiveWork), so a command counts as live
// whenever one of its steps names an entry in byName (which the caller has
// already filtered to Worker == nil — a genuine agent worker never folds
// into a command row).
func liveEntryFor(c manifest.Command, byName map[string]*livework.Entry) *livework.Entry {
	for _, s := range c.Steps {
		if e, ok := byName[s]; ok {
			return e
		}
	}
	return nil
}

// liveWorkers is every registry entry that IS a genuine agent worker
// (Worker != nil) — the Work tab's "Workers" section, in the registry's own
// insertion order (livework.Registry.List already guarantees that), which is
// what keeps the section from reordering itself as entries change state.
func liveWorkers() []*livework.Entry {
	var out []*livework.Entry
	for _, e := range livework.Default.List() {
		if e.Worker != nil {
			out = append(out, e)
		}
	}
	return out
}

// cmdOriginOf is a command's displayed archetype, falling back to
// "(manifest)" for a command computeOrigins somehow never saw (defensive;
// every declared command comes from Custom, Overrides, or an archetype's
// own, all of which computeOrigins covers).
func (m Model) cmdOriginOf(name string) string {
	if o, ok := m.cmdOrigin[name]; ok {
		return o
	}
	return "(manifest)"
}

// commandRowItem builds one Work-tab row for a declared command, resolving
// its live status (if any), its workflow target (if it is one), and its
// best-effort last-run/log.
func (m Model) commandRowItem(cr workCmdRow, byName map[string]*livework.Entry) rowItem {
	wd := &workRowData{
		cmdName:      cr.name,
		cmdDesc:      cr.cmd.Desc,
		cmdKind:      m.commandKind(cr.cmd),
		cmdArchetype: m.cmdOriginOf(cr.name),
		steps:        m.commandSteps(cr.cmd),
		live:         liveEntryFor(cr.cmd, byName),
	}
	if a, ok := m.soleAction(cr.cmd); ok && actionKind(a) == "workflow" {
		if target, err := kinds.ParseWorkflowOver(cr.cmd.Steps[0], a); err == nil {
			wd.workflow = &target
		}
	}
	wd.lastRun, wd.logPath = commandLastRun(m.repoRoot, cr.cmd)
	return rowItem{kind: rowCommand, label: cr.name + " " + cr.cmd.Desc, work: wd}
}

// workRows builds the Work tab's one combined list: every declared command
// first, then any live-work entry that IS a genuine agent worker — the
// ticket's own "commands first, then any worker with no command behind it."
// A process-kind entry (a LongRunning command actually running) never gets
// a row of its own; it folds into its command's row via commandRowItem's
// live lookup instead.
func (m Model) workRows() []rowItem {
	if !m.manifestOK {
		return nil
	}
	cmdRows := m.declaredCommands()
	byName := map[string]*livework.Entry{}
	for _, e := range livework.Default.List() {
		if e.Worker == nil {
			byName[e.Name] = e
		}
	}

	var rows []rowItem
	if len(cmdRows) > 0 {
		rows = append(rows, headerRow("Commands"))
		for _, cr := range cmdRows {
			rows = append(rows, m.commandRowItem(cr, byName))
		}
	}
	workers := liveWorkers()
	if len(workers) > 0 {
		if len(rows) > 0 {
			rows = append(rows, blankRow())
		}
		rows = append(rows, headerRow("Workers"))
		for _, e := range workers {
			rows = append(rows, rowItem{
				kind:  rowWorker,
				label: e.Name + " " + e.Activity,
				work:  &workRowData{worker: e},
			})
		}
	}
	if len(rows) == 0 {
		return []rowItem{{kind: rowCenter, label: "nothing here yet — admin.toml declares no commands"}}
	}
	return rows
}

// workRowCount is the tab bar's badge count for Work — every selectable row
// (commands plus workers), matching how the PRs/Issues/Beads badges already
// count their own rows.
func (m Model) workRowCount() int {
	n := 0
	for _, r := range m.workRows() {
		if r.kind == rowCommand || r.kind == rowWorker {
			n++
		}
	}
	return n
}

// workDispatchDoneMsg carries a dispatched command's outcome back to Update.
type workDispatchDoneMsg struct {
	name string
	err  error
}

// cmdExecCommand adapts kinds.RunCommand to bubbletea's tea.Exec. A
// dispatched Runner writes to the real terminal — its Host defaults Stdout
// to os.Stdout, and a shell/lua body's frame opens a DECSTBM scroll region
// directly on it (internal/kinds/shell.go, internal/kinds/frame.go) — which
// would corrupt the dashboard's own alt-screen rendering if run
// concurrently with it. tea.Exec is the same mechanism configtab.go already
// uses to hand the terminal to $EDITOR: it releases the terminal before Run
// and restores it after, so a Transient command's own output briefly takes
// over the screen exactly as running it from a plain shell would, and a
// LongRunning one (which returns almost immediately, having only opened a
// herdr pane) barely flickers.
type cmdExecCommand struct {
	manifest *manifest.Manifest
	name     string
}

func (c *cmdExecCommand) Run() error {
	return kinds.RunCommand(c.manifest, kinds.Default, c.name, nil)
}
func (c *cmdExecCommand) SetStdin(io.Reader)  {}
func (c *cmdExecCommand) SetStdout(io.Writer) {}
func (c *cmdExecCommand) SetStderr(io.Writer) {}

// dispatchCommandCmd runs name via tea.Exec — see cmdExecCommand's doc
// comment for why dispatch cannot be a plain background tea.Cmd.
func dispatchCommandCmd(m *manifest.Manifest, name string) tea.Cmd {
	c := &cmdExecCommand{manifest: m, name: name}
	return tea.Exec(c, func(err error) tea.Msg {
		return workDispatchDoneMsg{name: name, err: err}
	})
}

// activateCommand handles enter on a command row: a query-sourced workflow
// is left to admin-3rgp.26 (resolving its query and confirming the set
// belongs there, not to a bare enter here); an already-live command attaches
// to its pane instead of dispatching a second run on top of the one that
// owns its log file and registry entry; anything else dispatches.
func (m Model) activateCommand(wd *workRowData) (tea.Model, tea.Cmd) {
	if wd.workflow != nil {
		m.setStatus("dispatching a query-sourced workflow needs confirmation — admin-3rgp.26", cDim)
		return m, nil
	}
	if wd.live != nil {
		return m.attachEntry(wd.live)
	}
	return m, dispatchCommandCmd(m.manifest, wd.cmdName)
}

// attachEntry focuses e's pane. A nil e or one with no Attach op (a producer
// that never wired one) is a silent no-op, matching every other action here
// that requires a live row to act on.
func (m Model) attachEntry(e *livework.Entry) (tea.Model, tea.Cmd) {
	if e == nil || e.Ops.Attach == nil {
		return m, nil
	}
	if err := e.Ops.Attach(); err != nil {
		m.setStatus("attach failed: "+err.Error(), cRed)
	}
	return m, nil
}

// currentLiveEntry resolves the row under the cursor to the livework.Entry
// its a/k/r actions operate on: a worker row's own entry, or a command
// row's currently-running entry (nil for an idle command, or any other row
// — a, k and r are then no-ops).
func (m Model) currentLiveEntry() *livework.Entry {
	r, ok := m.currentRow()
	if !ok || r.work == nil {
		return nil
	}
	switch r.kind {
	case rowWorker:
		return r.work.worker
	case rowCommand:
		return r.work.live
	}
	return nil
}

func (m Model) attachCurrentWork() (tea.Model, tea.Cmd) {
	return m.attachEntry(m.currentLiveEntry())
}

func (m Model) killCurrentWork() (tea.Model, tea.Cmd) {
	e := m.currentLiveEntry()
	if e == nil || e.Ops.Kill == nil {
		return m, nil
	}
	id := e.ID
	if err := e.Ops.Kill(); err != nil {
		m.setStatus("kill failed: "+err.Error(), cRed)
	} else {
		m.setStatus("killed "+id, cDim)
	}
	return m, nil
}

func (m Model) retryCurrentWork() (tea.Model, tea.Cmd) {
	e := m.currentLiveEntry()
	if e == nil || e.Ops.Retry == nil {
		return m, nil
	}
	id := e.ID
	if err := e.Ops.Retry(); err != nil {
		m.setStatus("retry failed: "+err.Error(), cRed)
	} else {
		m.setStatus("retrying "+id, cCyan)
	}
	return m, nil
}

// workTickMsg fires on a repeating timer to repaint the Work tab so a
// livework.Entry another goroutine mutated (a herdr pane settling, an agent
// finishing) shows up without needing a keypress — see Init's own comment.
type workTickMsg struct{}

func workTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return workTickMsg{} })
}
