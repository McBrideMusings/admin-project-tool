// work.go is the Work tab's data layer (admin-3rgp.20): resolving the
// project's admin.toml, building the combined command+worker row list, and
// dispatching a command. workview.go is the rendering half.
package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/frame"
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
	// declaredCommands). workflowAction is that same step's raw action body
	// — kept alongside workflow because admin-3rgp.26's resolve/confirm/
	// dispatch chain (workflowconfirm.go) needs the whole action, not just
	// its parsed `over`, to resolve the configured agent and hand off to
	// kinds.DispatchTargets exactly as it would from any other caller.
	workflow       *kinds.WorkflowTarget
	workflowAction manifest.Action
	// live is the livework.Entry currently backing this command, if any —
	// set only for a process-kind entry (Worker == nil); a genuine agent
	// worker never folds into a command row, it gets its own row below.
	live    *livework.Entry
	logPath string
	// pending is true while this command's dispatch tea.Cmd is in flight but
	// has not yet returned a workDispatchStartedMsg — the window between the
	// keypress and the point a livework.Entry (or a dispatch error) exists.
	// Mirrors Model.pendingDispatch[cmdName]; kept as its own field (rather
	// than read live from the Model during render) for the same reason live
	// is: workRows() rebuilds every rowItem from scratch each frame, so the
	// value is resolved once at build time and read by both the child-row
	// builder (targetRows) and the renderer.
	pending bool
	// targets is a kind="multi-target" sole step's target list, in the
	// order kinds.TargetKeys gives (the same order the CLI picker offers).
	// Non-empty is exactly the condition that makes this row a group header
	// rather than something enter dispatches directly: its targets always
	// render beneath it, and enter on one of those dispatches it.
	targets []string
	// targetRuns maps each target key to what it does — its shell command,
	// or "@action" when it delegates — so the target group says what each
	// row will run rather than restating its own name.
	targetRuns map[string]string
	// targetURLs maps each target key to its own resolved urls (the same
	// per-target inheritance the pinned footer applies — kinds.FrameURLs),
	// so targetRows can hand each child row its own list without a second
	// Model-aware resolver.
	targetURLs map[string][]frame.URLEntry
	// urls is this row's own resolved urls (admin-1azz): a plain command's
	// action-level list, or — for a single-target multi-target action,
	// which the CLI auto-picks with no prompt — that one target's list.
	// Empty for a >1-target menu row, whose targets carry their own
	// (targetURLs) instead.
	urls []frame.URLEntry

	// Target rows (rowCommandTarget): one target of an expanded command.
	// cmdName holds the PARENT command's name on these rows too, so
	// dispatch reads the same field on either shape.
	targetKey string
	// targetRun is that target's own run command, shown beside its name.
	targetRun string
	// targetLast marks the final target of an expanded command, so the
	// renderer can close the tree with └ instead of ├.
	targetLast bool

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
	manifest.LoadDotenv(repoRoot) // same ordering as cmd.resolveProjectManifest
	project, err = manifest.Load(repoRoot)
	if err != nil {
		return nil, nil, nil, true, err
	}
	sp, spErr := archetype.SearchPathFor(repoRoot)
	if spErr != nil {
		return nil, nil, nil, true, spErr
	}
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
//
// keys does the same job for the handful of scalar top-level manifest keys
// the Config tab renders (admin-3rgp.21: "tracker", "uses_pull_requests",
// plus "identity", "worktrees", "beads", "workflow" and "populate" per the
// retire-the-config-tiers decision) — named by manifest.go's own key string,
// mapping to the archetype that last declared it or "(manifest)". A key
// absent from the map was never declared anywhere, which the Config tab
// renders as defaulted rather than guessing a source for it.
func computeOrigins(project *manifest.Manifest, sp archetype.SearchPath) (cmds, actions, keys map[string]string) {
	cmds, actions, keys = map[string]string{}, map[string]string{}, map[string]string{}

	// The implicit base archetype (archetype.Resolve's own first merge step)
	// attributed the same way as any declared archetype, unless the project
	// lists "base" explicitly — matching Resolve's own skip so the two never
	// disagree about who contributed a key. A search path with no base is a
	// silent no-op, per LoadBase.
	declaresBase := false
	for _, name := range project.Archetypes {
		if name == archetype.BaseName {
			declaresBase = true
			break
		}
	}
	if !declaresBase {
		if base, ok, err := archetype.LoadBase(sp); err == nil && ok {
			applyOrigin(base.Partial, archetype.BaseName, cmds, actions, keys)
		}
	}

	for _, name := range project.Archetypes {
		arch, err := archetype.Load(sp, name)
		if err != nil {
			continue
		}
		applyOrigin(arch.Partial, name, cmds, actions, keys)
	}
	// The project always wins (ADR-0009), including a field-level override
	// of a command an archetype already provides — from the display's point
	// of view the manifest is still the most specific place that named this
	// command, exactly like every other manifest-level key.
	applyOrigin(project, "(manifest)", cmds, actions, keys)
	return cmds, actions, keys
}

// applyOrigin records m's own directly-declared commands/actions/keys as
// having come from label, into cmds/actions/keys — the one body computeOrigins
// runs three times (once for the implicit base, once per declared archetype,
// once for the project manifest itself) so all three attributions can never
// drift apart.
func applyOrigin(m *manifest.Manifest, label string, cmds, actions, keys map[string]string) {
	for c := range m.Custom {
		cmds[c] = label
	}
	for c := range m.Overrides {
		cmds[c] = label
	}
	for a := range m.Actions {
		actions[a] = label
	}
	if m.HasTracker {
		keys["tracker"] = label
	}
	if m.HasUsesPullRequests {
		keys["uses_pull_requests"] = label
	}
	if len(m.Identity) > 0 {
		keys["identity"] = label
	}
	if len(m.Worktrees) > 0 {
		keys["worktrees"] = label
	}
	if len(m.Beads) > 0 {
		keys["beads"] = label
	}
	if len(m.Workflow) > 0 {
		keys["workflow"] = label
	}
	if len(m.Populate) > 0 {
		keys["populate"] = label
	}
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

// commandLogDir is the log directory a command's log file lands under —
// <repoRoot>/tmp by default, overridden by the manifest's `[logging] dir`,
// mirroring luahost.LogContext.Configure's own resolution (dispatch.go's
// nameCommandLog is what actually configures the process context this way
// at run time; this is the Work tab's read-only mirror of that same rule).
func commandLogDir(repoRoot string, m *manifest.Manifest) string {
	dir := "tmp"
	if d, ok := m.Logging["dir"].(string); ok && d != "" {
		dir = d
	}
	return filepath.Join(repoRoot, dir)
}

// logStemTimestamp extracts the "<stem>.<TIMESTAMP>.log" suffix's timestamp
// segment, which sorts correctly as a plain string (the format is
// YYYY-MM-DD_HHMMSS) — used so the newest-of-several-matches pick doesn't
// need a filesystem stat call, since a "which file is really newest" tie
// only needs to compare the name luahost itself chose.
func logStemTimestamp(path string) string {
	base := filepath.Base(path)
	// base is "<stem>.<ts>.log"; the timestamp is always the last
	// dot-segment before ".log".
	base = strings.TrimSuffix(base, ".log")
	i := strings.LastIndex(base, ".")
	if i < 0 {
		return ""
	}
	return base[i+1:]
}

// commandLogPath is the "log" the detail pane shows: since admin-rc82 every
// command names its log deterministically before its first step runs (the
// command's own name, dispatch.go's nameCommandLog), refined only when a
// step's own kind (a multi-target target, a framed action) sets a more
// specific name before first use — so the real stem is one of: the command
// name itself, one of its step (action) names, or "<step>-<target>" for a
// multi-target step. Globbing all of those and taking the newest by
// filename timestamp covers every shape without needing an mtime tiebreak
// or a per-step ranking loop — naming can no longer collide ACROSS two
// different commands, only within one command's own set of possible stems.
// A command that has genuinely never run matches nothing here and degrades
// to "none yet", same as before.
func commandLogPath(repoRoot string, m *manifest.Manifest, name string, c manifest.Command) string {
	logDir := commandLogDir(repoRoot, m)
	stems := map[string]bool{name: true}
	for _, step := range c.Steps {
		stems[step] = true
	}
	var best, bestTS string
	for stem := range stems {
		var matches []string
		if ms, err := filepath.Glob(filepath.Join(logDir, stem+".*.log")); err == nil {
			matches = append(matches, ms...)
		}
		if ms, err := filepath.Glob(filepath.Join(logDir, stem+"-*.*.log")); err == nil {
			matches = append(matches, ms...)
		}
		for _, p := range matches {
			ts := logStemTimestamp(p)
			if ts > bestTS {
				bestTS, best = ts, p
			}
		}
	}
	return best
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
// truth for that rule, applied by workTabInclude below.
func (m Model) declaredCommands() []workCmdRow {
	return m.collectCommands(m.workTabInclude)
}

// workTabInclude is declaredCommands' filter: every command except a
// selection-sourced kind="workflow" one, which never appears in the Work
// tab's command list — see AppearsInCommandList's own doc comment for why.
func (m Model) workTabInclude(c manifest.Command) bool {
	if a, ok := m.soleAction(c); ok && actionKind(a) == "workflow" {
		if target, err := kinds.ParseWorkflowOver(c.Steps[0], a); err == nil && !target.AppearsInCommandList() {
			return false
		}
	}
	return true
}

// selectionWorkflows is the picker's own candidate list (admin-3rgp.27,
// selectionworkflow.go): every declared command that IS a selection-sourced
// kind="workflow" action — workTabInclude's exact complement — in the same
// stable order declaredCommands uses, so a command lists in the same
// relative place whichever of the two lists it belongs to.
func (m Model) selectionWorkflows() []workCmdRow {
	return m.collectCommands(m.isSelectionWorkflow)
}

// isSelectionWorkflow reports whether c is a kind="workflow" action whose
// `over` is "selection" — the one predicate selectionWorkflows needs beyond
// what soleAction/actionKind/ParseWorkflowOver already give declaredCommands.
func (m Model) isSelectionWorkflow(c manifest.Command) bool {
	a, ok := m.soleAction(c)
	if !ok || actionKind(a) != "workflow" {
		return false
	}
	target, err := kinds.ParseWorkflowOver(c.Steps[0], a)
	return err == nil && target.Source == kinds.WorkflowSourceSelection
}

// collectCommands walks every declared command (manifest order when set,
// else group/priority/name) exactly as declaredCommands always has, handing
// each one to include so the two lists above can share every rule except
// which side of AppearsInCommandList they want: hide_commands, dedup against
// Custom/Overrides, and the manifest-order-vs-grouped-fallback ordering.
func (m Model) collectCommands(include func(manifest.Command) bool) []workCmdRow {
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
		if hidden[name] || seen[name] || !include(c) {
			return
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
// into a command row). For a genuine multi-target menu (more than one
// declared target) the registry keys the entry on "<action>:<target>"
// instead (internal/kinds.multiTargetLiveID) once a target is actually
// picked, so this lookup by the bare step name naturally returns nil for
// the parent row while targetRows resolves each target's own qualified
// entry below — a parent holder row is never "live" on its own account.
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
		pending:      m.pendingDispatch[cr.name],
	}
	if a, ok := m.soleAction(cr.cmd); ok {
		kind := actionKind(a)
		var keys []string
		if kind == "multi-target" {
			keys = kinds.TargetKeys(a)
		}
		switch kind {
		case "workflow":
			if target, err := kinds.ParseWorkflowOver(cr.cmd.Steps[0], a); err == nil {
				wd.workflow = &target
				wd.workflowAction = a
			}
		case "multi-target":
			// A single-target action isn't a menu — the CLI picks that
			// target without prompting (makeMultiTarget's "is this
			// project's only target"), so the row stays a plain dispatch
			// rather than a group with a target list under it.
			if len(keys) > 1 {
				wd.targets = keys
				wd.targetRuns = map[string]string{}
				wd.targetURLs = map[string][]frame.URLEntry{}
				for _, k := range keys {
					wd.targetRuns[k] = kinds.TargetSummary(a, k)
					if u, err := kinds.FrameURLs(a, k, m.manifest); err == nil {
						wd.targetURLs[k] = u
					}
				}
			}
		}
		// A row that runs directly — not a query-resolved workflow, not a
		// >1-target menu — resolves its own urls the same path the footer
		// would use once it runs: the action's own list, or, for a
		// single-target multi-target (auto-picked, never a menu), that one
		// target's inherited list.
		if kind != "workflow" && len(wd.targets) == 0 {
			target := ""
			if kind == "multi-target" && len(keys) == 1 {
				target = keys[0]
			}
			if u, err := kinds.FrameURLs(a, target, m.manifest); err == nil {
				wd.urls = u
			}
		}
	}
	if wd.live != nil && wd.live.LogPath != "" {
		wd.logPath = wd.live.LogPath
	} else {
		wd.logPath = commandLogPath(m.manifestDir(), m.manifest, cr.name, cr.cmd)
	}
	return rowItem{kind: rowCommand, label: cr.name + " " + cr.cmd.Desc, work: wd}
}

// targetRows is a command's target group: one rowCommandTarget per target of
// parent, or nothing at all when parent has no targets. They always render —
// there is no collapsed state — and sit directly under their parent in the
// same flat row list, so every cursor, filter, window and hit-test rule
// already written for the Work tab applies to them with no special case.
//
// byName is the same registry-name -> entry map workRows() built for the
// parent rows: once a target is actually dispatched, its entry is keyed on
// "<cmdName>:<targetKey>" (internal/kinds.multiTargetLiveID), so each target
// row resolves its OWN live entry here rather than inheriting whatever the
// parent happened to find — the fix for a parent holder row showing running
// for every target sharing it, and for a running target showing no
// indicator at all.
func targetRows(parent rowItem, byName map[string]*livework.Entry) []rowItem {
	wd := parent.work
	if wd == nil {
		return nil
	}
	out := make([]rowItem, 0, len(wd.targets))
	for i, key := range wd.targets {
		out = append(out, rowItem{
			kind:  rowCommandTarget,
			label: wd.cmdName + " " + key,
			work: &workRowData{
				cmdName:      wd.cmdName,
				cmdDesc:      wd.cmdDesc,
				cmdKind:      wd.cmdKind,
				cmdArchetype: wd.cmdArchetype,
				steps:        wd.steps,
				logPath:      wd.logPath,
				targetKey:    key,
				targetRun:    wd.targetRuns[key],
				targetLast:   i == len(wd.targets)-1,
				urls:         wd.targetURLs[key],
				// This target's own qualified entry, if one is running —
				// see the byName doc comment above. renderTargetRow now
				// renders state/activity/where from this exactly as
				// renderWorkRow does for a plain command row.
				live: byName[wd.cmdName+":"+key],
				// Same reasoning as live just above: a multi-target
				// dispatch is pending under the parent action's own name
				// until a livework.Entry registers (there is no target
				// identity to key on during that transient window — see
				// dispatchCommandCmd/activateCommandTarget), so this is
				// what activateCommandTarget's second-enter guard reads to
				// turn a second enter on a starting command's target into a
				// queued attach instead of a second run (admin-qk77).
				pending: wd.pending,
			},
		})
	}
	return out
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
	rows = append(rows, headerRow("Commands"))
	if len(cmdRows) == 0 {
		rows = append(rows, rowItem{kind: rowText, label: "admin.toml declares no commands"})
	} else {
		for _, cr := range cmdRows {
			item := m.commandRowItem(cr, byName)
			rows = append(rows, item)
			if targets := targetRows(item, byName); len(targets) > 0 {
				rows = append(rows, targets...)
				rows = append(rows, ruleRow())
			}
		}
	}

	rows = append(rows, blankRow(), headerRow("Workers"))
	workers := liveWorkers()
	if len(workers) == 0 {
		rows = append(rows, rowItem{kind: rowText, label: "no workers running — dispatch a command to start one"})
	} else {
		for _, e := range workers {
			label := e.Name
			if e.Activity != "" {
				label = e.Name + " " + e.Activity
			}
			rows = append(rows, rowItem{
				kind:  rowWorker,
				label: label,
				work:  &workRowData{worker: e},
			})
		}
	}
	return rows
}

// workRowCount is the tab bar's badge count for Work — every selectable row
// (commands plus workers), matching how the PRs/Issues/Beads badges already
// count their own rows.
func (m Model) workRowCount() int {
	n := 0
	for _, r := range m.workRows() {
		if r.kind == rowCommand || r.kind == rowCommandTarget || r.kind == rowWorker {
			n++
		}
	}
	return n
}

// workDispatchStartedMsg carries a hosted dispatch's START outcome back to
// Update — admin-3rgp.61 replaced the old tea.Exec dispatch (which released
// the dashboard's terminal for the run's whole duration and reported its
// exit status when the run finished) with a hosted pty the dashboard never
// blocks on, so there is no longer an exit status to report at dispatch
// time: err here means the pty itself failed to spawn, not that the command
// finished badly. A nil err means the command is now running on its own
// pty, tracked by the live-work entry StartHostedPTY registered.
type workDispatchStartedMsg struct {
	name string
	err  error
}

// startHostedRun is kinds.StartHostedPTY behind a package-level var so a
// test can stub it without spawning a real pty — mirrors pane.go's
// selfExecutable seam.
var startHostedRun = kinds.StartHostedPTY

// dispatchCommandCmd starts name and returns immediately: unlike the
// tea.Exec dispatch it replaces, this is an ordinary asynchronous tea.Cmd.
// It routes through kinds.RunCommand with a dashboard CommandProducer —
// admin-ysxr.4's collapsed entry point — rather than calling StartHostedPTY
// directly, so a command whose first step declares `detach = true` against
// a declared [terminal] dispatches through that launcher instead: the
// dashboard is never the right process to host a run the project itself
// asked to detach. Producer.Launch is exactly the old unconditional call:
// starts name on a dashboard-owned pty — kinds.RunCommand, reached via the
// hidden "__run-command" re-entry verb StartHostedPTY re-execs into and
// running inline there because that verb marks reentry, runs the command's
// whole step loop on it — so the run's own output, and any pinned footer it
// opens, land there instead of on the dashboard's alt-screen — the
// no-[terminal] default, unchanged from before this ticket.
func dispatchCommandCmd(m *manifest.Manifest, width, height int, name string, env []string, args ...string) tea.Cmd {
	return func() tea.Msg {
		producer := &kinds.CommandProducer{
			Env: env,
			Launch: func(mf *manifest.Manifest, n string, a []string) error {
				return startHostedRun(mf, n, a, height, width, env)
			},
		}
		err := kinds.RunCommand(m, kinds.Default, name, args, producer)
		return workDispatchStartedMsg{name: name, err: err}
	}
}

// activateCommand handles enter on a command row: a query-sourced workflow
// resolves its query and opens the confirm overlay rather than dispatching
// directly (admin-3rgp.26 — see workflowconfirm.go); an already-live command
// attaches to its pane instead of dispatching a second run on top of the one
// that owns its log file and registry entry; a command whose previous enter
// is still dispatching (wd.pending) queues the attach instead of starting a
// second run on top of the one that hasn't registered yet (admin-qk77); a
// multi-target command has no single run to dispatch — its targets already
// render below it (admin-0kxh), so enter on the parent row is a status-only
// no-op pointing at them; anything else dispatches.
func (m Model) activateCommand(wd *workRowData) (tea.Model, tea.Cmd) {
	if wd.workflow != nil {
		return m.startWorkflowConfirm(wd)
	}
	if wd.live != nil {
		return m.attachEntry(wd.live)
	}
	if wd.pending {
		// The dispatch this row's own previous enter started hasn't
		// registered a livework.Entry yet (or already failed and the
		// pendingDispatch flag just hasn't been cleared by this frame) — do
		// NOT dispatch again. Record the intent to attach once it lands;
		// workDispatchStartedMsg's handler resolves and attaches it.
		m.pendingAttach[wd.cmdName] = true
		m.setStatus(wd.cmdName+" starting…", cCyan)
		return m, nil
	}
	if len(wd.targets) > 0 {
		// A command with more than one target can't dispatch from this row —
		// there's no target argument to run with. Its targets are already
		// listed beneath it; enter on one of those dispatches it.
		m.setStatus(wd.cmdName+": pick a target below", cCyan)
		return m, nil
	}
	return m.startDispatch(wd.cmdName)
}

// startDispatch marks name pending (so its row renders "starting" and a
// second enter during the window queues an attach instead of dispatching
// again — activateCommand/activateCommandTarget above) and returns the same
// tea.Cmd dispatchCommandCmd always returned. The single entry point both
// dispatch sites route through, so pendingDispatch can never go stale by one
// of them forgetting to set it.
func (m Model) startDispatch(name string, args ...string) (tea.Model, tea.Cmd) {
	m.pendingDispatch[name] = true
	return m, dispatchCommandCmd(m.manifest, m.width, m.height, name, nil, args...)
}

// liveEntryForCommand resolves the livework.Entry now backing name, once its
// dispatch has registered one — the workDispatchStartedMsg handler's queued-
// attach path uses this rather than re-deriving the registry ID
// (multiTargetLiveID per detach.go/hostedpty.go) itself, reusing
// commandRowItem's/targetRows' own lookups via the same workRows() scan
// every other row-driven action already does.
//
// pendingDispatch/pendingAttach stay keyed by command name only, not target
// — there is at most one in-flight dispatch per command name (the existing
// pending guard), so name alone remains unambiguous — but the entry itself
// may now be recorded under a target row's qualified name rather than the
// parent's bare one, so both rowCommand and rowCommandTarget rows are
// scanned for a match.
func (m Model) liveEntryForCommand(name string) *livework.Entry {
	for _, r := range m.workRows() {
		if r.kind != rowCommand && r.kind != rowCommandTarget {
			continue
		}
		if r.work == nil || r.work.cmdName != name {
			continue
		}
		if r.work.live != nil {
			return r.work.live
		}
	}
	return nil
}

// activateCommandTarget handles enter on one target of a command's target
// group: it dispatches the parent command with that target as its argument,
// which is exactly what `admin <cmd> <target>` does from a shell — makeMultiTarget
// takes args[0] as the picked target and never raises its own picker. Until
// the dispatch registers a livework.Entry there is no target-qualified name
// to key on yet (the entry doesn't exist), so pending stays keyed on
// wd.cmdName here too, matching how the parent row itself already reads it,
// and the same "don't dispatch a second run into the one still starting"
// guard applies (admin-qk77). Once the entry lands it's keyed on
// "<cmdName>:<targetKey>" (multiTargetLiveID) and targetRows resolves it
// per target from then on.
func (m Model) activateCommandTarget(wd *workRowData) (tea.Model, tea.Cmd) {
	if wd.pending {
		m.pendingAttach[wd.cmdName] = true
		m.setStatus(wd.cmdName+" starting…", cCyan)
		return m, nil
	}
	return m.startDispatch(wd.cmdName, wd.targetKey)
}

// attachEntry resolves e's attach through the registry — the terminal view
// for an entry on the dashboard's own hosted pty (Via empty), the project's
// declared [terminal] attach command for one dispatched through a launcher
// (Via set) — see livework.Registry.Attach. A nil e is a silent no-op,
// matching every other action here that requires a live row to act on.
//
// A hosted-pty entry's Attach call reaches installTerminalOpener's
// callback synchronously (terminalview.go), which can only record the
// requested handle — it cannot mutate this value-type Model. Draining that
// recording right after Attach succeeds is what completes the transition
// into the terminal view; a launcher-dispatched entry's Attach shells out
// instead and records nothing, so takePendingTerminal reports false and
// this falls through to the same no-op return it always had.
func (m Model) attachEntry(e *livework.Entry) (tea.Model, tea.Cmd) {
	if e == nil {
		return m, nil
	}
	if err := livework.Default.Attach(e.ID); err != nil {
		m.setStatus("attach failed: "+err.Error(), cRed)
		return m, nil
	}
	if _, ok := takePendingTerminal(); ok {
		return m.openTerminal(e), termTickCmd()
	}
	return m, nil
}

// currentLiveEntry resolves the row under the cursor to the livework.Entry
// its a/k actions operate on: a worker row's own entry, or a command
// row's currently-running entry (nil for an idle command, or any other row
// — a and k are then no-ops).
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

// workTickMsg fires on a repeating timer to repaint the Work tab so a
// livework.Entry another goroutine mutated (a herdr pane settling, an agent
// finishing) shows up without needing a keypress — see Init's own comment.
type workTickMsg struct{}

func workTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return workTickMsg{} })
}

// refreshDetailProbes kicks off (non-blocking) reachability probes for the
// urls the row under the cursor is currently showing — and only while that
// row is live: an idle command's urls are never probed, so the pane renders
// them with no reachability indicator at all rather than a stale one. Called
// from Update (workTickMsg and workDispatchStartedMsg), never from View —
// starting a probe is a side effect, and rendering must stay pure.
func (m Model) refreshDetailProbes() {
	r, ok := m.currentRow()
	if !ok || r.work == nil || r.work.live == nil || len(r.work.urls) == 0 {
		return
	}
	urls := make([]string, 0, len(r.work.urls))
	for _, u := range r.work.urls {
		urls = append(urls, u.URL)
	}
	urlProbes.Refresh(urls)
}
