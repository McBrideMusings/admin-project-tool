// work.go is the Work tab's data layer (admin-3rgp.20): resolving the
// project's admin.toml, building the combined command+worker row list, and
// dispatching a command. workview.go is the rendering half.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/keymap"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/viewstate"
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
	// (a row-sourced one never reaches this list at all — see
	// declaredCommands), on its group row and on each matched item's target
	// row. workflowAction is that same step's raw action body, which ↵ on a
	// target hands to kinds.DispatchTargets with that one item
	// (dispatchWorkflowTarget). workflowNote is what the group shows in place
	// of targets when its query matched nothing yet: still loading, no
	// match, or why the query cannot resolve.
	workflow       *kinds.WorkflowTarget
	workflowAction manifest.Action
	workflowNote   string
	// live is the livework.Entry currently backing this command, if any —
	// set only for a process-kind entry (Worker == nil); a genuine agent
	// worker never folds into a command row, it gets its own row below.
	live    *livework.Entry
	logPath string
	// pending is true while this command's dispatch tea.Cmd is in flight but
	// has not yet returned a workDispatchStartedMsg — the window between the
	// keypress and the point a livework.Entry (or a dispatch error) exists.
	// Mirrors commandsTab.pendingDispatch[cmdName]; kept as its own field (rather
	// than read live from the Model during render) for the same reason live
	// is: rows() rebuilds every rowItem from scratch each frame, so the
	// value is resolved once at build time and read by both the child-row
	// builder (targetRows) and the renderer.
	pending bool
	// pendingHere narrows pending to "the in-flight dispatch is THIS row",
	// not just this row's command — set only by targetRows (admin-2y6p), a
	// target row's own renderer reads this instead of pending. pending alone
	// stays command-wide on purpose (activateCommand/activateCommandTarget's
	// second-enter guard is deliberately whole-command: at most one dispatch
	// in flight per command name, whichever target it was for), so
	// commandRowItem's plain rowCommand row — which has no sibling targets
	// to distinguish from — never needs this field and leaves it false; only
	// targetRows sets it, from commandsTab.pendingTarget[cmdName] matching this
	// row's own targetKey.
	pendingHere bool
	// targets is a kind="multi-target" sole step's target list, in the
	// order kinds.TargetKeys gives (the same order the CLI picker offers).
	// Non-empty is exactly the condition that makes this row a group header
	// rather than something enter dispatches directly: its targets always
	// render beneath it, and enter on one of those dispatches it.
	targets []string
	// targetDescs maps each target key to what it does — its own desc, the
	// delegated action's desc, or the shell command it runs — so the target
	// group says what each row will do rather than restating its own name.
	targetDescs map[string]string
	// targetURLs maps each target key to its own resolved urls (the same
	// per-target inheritance the pinned footer applies — kinds.FrameURLs),
	// so targetRows can hand each child row its own list without a second
	// Model-aware resolver.
	targetURLs map[string][]frame.URLEntry
	// closed marks a group row whose targets collapse mode is hiding, so
	// renderGroupRow can say so: "▸" in the marker cell and the hidden count
	// after the name. Without it a closed group looks like an empty one.
	closed bool
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
	// targetDesc is what that target does, shown beside its name.
	targetDesc string
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
// plus "worktrees", "beads", "workflow" and "populate" per the
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
func soleAction(mf *manifest.Manifest, c manifest.Command) (manifest.Action, bool) {
	if mf == nil || len(c.Steps) != 1 {
		return manifest.Action{}, false
	}
	a, ok := mf.Actions[c.Steps[0]]
	return a, ok
}

// multiStepURLs collects the urls a multi-step command's row should show:
// every step's own action urls, in step order, de-duplicated by URL — the
// CLI footer resolves this same list per step as each one runs (enterFrame,
// internal/kinds/frame.go), so the Work tab's row mirrors it up front rather
// than showing only the last step's list or nothing at all. A step naming an
// undeclared action is skipped, matching commandSteps' own "?" handling. A
// step whose action is a single-target multi-target resolves that one
// target's inherited list, same as soleAction's single-step path.
func multiStepURLs(mf *manifest.Manifest, c manifest.Command) []frame.URLEntry {
	var out []frame.URLEntry
	seen := map[string]bool{}
	for _, s := range c.Steps {
		a, ok := mf.Actions[s]
		if !ok {
			continue
		}
		target := ""
		if actionKind(a) == "multi-target" {
			if keys := kinds.TargetKeys(a); len(keys) == 1 {
				target = keys[0]
			}
		}
		u, err := kinds.FrameURLs(a, target, mf)
		if err != nil {
			continue
		}
		for _, e := range u {
			if seen[e.URL] {
				continue
			}
			seen[e.URL] = true
			out = append(out, e)
		}
	}
	return out
}

// commandKind is a command's displayed kind: a single step's own declared
// kind, or "sequence" for more than one ("deploy" with three steps shows
// "sequence", "build" with one shows "shell"). "?" marks a step
// naming an undeclared action, which a broken manifest can produce and
// which check (not this tab) is the place to reject.
func commandKind(mf *manifest.Manifest, c manifest.Command) string {
	switch len(c.Steps) {
	case 0:
		return "?"
	case 1:
		if a, ok := soleAction(mf, c); ok {
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
func (t *commandsTab) commandSteps(c manifest.Command) []workStepInfo {
	out := make([]workStepInfo, 0, len(c.Steps))
	for i, s := range c.Steps {
		kind := "?"
		if a, ok := t.cfg.manifest().Actions[s]; ok {
			kind = actionKind(a)
		}
		out = append(out, workStepInfo{idx: i + 1, name: s, kind: kind, archetype: t.cfg.actionOrigin(s)})
	}
	return out
}

// commandLogDir is the log directory a command's log file lands under — the
// same luahost.ResolveLogDir rule dispatch.go's nameCommandLog configures the
// process context with at run time.
func commandLogDir(repoRoot string, m *manifest.Manifest) string {
	return luahost.ResolveLogDir(repoRoot, m.Logging)
}

// logStemTimestamp extracts the "<stem>.<TIMESTAMP>.log" suffix's timestamp
// segment, which sorts correctly as a plain string (the format is
// YYYY-MM-DD_HHMMSS_NNNNNNNNN — seconds plus a zero-padded nanosecond
// disambiguator, underscore-joined so it stays part of the SAME
// dot-delimited segment rather than adding a second dot) — used so the
// newest-of-several-matches pick doesn't need a filesystem stat call, since
// a "which file is really newest" tie only needs to compare the name
// luahost itself chose.
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
// declaredCommands orders and commandsTab.rows renders.
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
// is "row" never appears here at all — WorkflowTarget.
// AppearsInCommandList (internal/kinds/workflow.go) is the single source of
// truth for that rule, applied by workTabInclude below.
func declaredCommands(mf *manifest.Manifest) []workCmdRow {
	return collectCommands(mf, func(c manifest.Command) bool { return workTabInclude(mf, c) })
}

// workTabInclude is declaredCommands' filter: every command except a
// row-sourced kind="workflow" one, which never appears in the Work
// tab's command list — see AppearsInCommandList's own doc comment for why.
func workTabInclude(mf *manifest.Manifest, c manifest.Command) bool {
	if a, ok := soleAction(mf, c); ok && actionKind(a) == "workflow" {
		if target, err := kinds.ParseWorkflowOver(c.Steps[0], a); err == nil && !target.AppearsInCommandList() {
			return false
		}
	}
	return true
}

// isRowWorkflow reports whether c is a kind="workflow" action whose `over`
// is "row" — the one predicate rowKeyDispatch needs beyond what
// soleAction/actionKind/ParseWorkflowOver already give declaredCommands.
func isRowWorkflow(mf *manifest.Manifest, c manifest.Command) bool {
	a, ok := soleAction(mf, c)
	if !ok || actionKind(a) != "workflow" {
		return false
	}
	target, err := kinds.ParseWorkflowOver(c.Steps[0], a)
	return err == nil && target.Source == kinds.WorkflowSourceRow
}

// collectCommands walks every declared command (manifest order when set,
// else group/priority/name) exactly as declaredCommands always has, handing
// each one to include so the two lists above can share every rule except
// which side of AppearsInCommandList they want: hide_commands, dedup against
// Custom/Overrides, and the manifest-order-vs-grouped-fallback ordering.
func collectCommands(mf *manifest.Manifest, include func(manifest.Command) bool) []workCmdRow {
	if mf == nil {
		return nil
	}
	hidden := map[string]bool{}
	for _, h := range mf.HideCommands {
		hidden[h] = true
	}
	lookup := func(name string) (manifest.Command, bool) { return commandNamed(mf, name) }
	seen := map[string]bool{}
	var out []workCmdRow
	add := func(name string, c manifest.Command) {
		if hidden[name] || seen[name] || !include(c) {
			return
		}
		seen[name] = true
		out = append(out, workCmdRow{name: name, cmd: c})
	}

	if len(mf.Order) > 0 {
		for _, name := range mf.Order {
			if name == manifest.Spacer {
				continue
			}
			if c, ok := lookup(name); ok {
				add(name, c)
			}
		}
		rest := unseenCommandNames(mf, seen)
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
	for name, c := range mf.Custom {
		items = append(items, item{name, c})
	}
	for name, c := range mf.Overrides {
		if _, exists := mf.Custom[name]; !exists {
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

// liveCommandEntries maps every non-worker livework.Entry by its registry ID —
// the key TargetLiveKey and liveEntryFor look it up by. Entry.Name is not
// used: it names the dispatched action, and nothing makes it equal the ID.
// A genuine agent worker (Worker != nil) never folds into a command row.
func liveCommandEntries() map[string]*livework.Entry {
	byID := map[string]*livework.Entry{}
	for _, e := range livework.Default.List() {
		if e.Worker == nil {
			byID[e.ID] = e
		}
	}
	return byID
}

// liveEntryFor returns the process-kind livework.Entry currently backing
// the command named name, if any. Both producers (internal/kinds'
// StartHostedPTY and launchCommand) register an entry under the id of the
// command that was run, so this is a lookup by that id alone: a command that
// merely shares a step with the running one never shows it. byID is already
// filtered to Worker == nil.
// For a genuine multi-target menu the registry keys the entry on
// "<command>:<target>" once a target is picked, so this returns nil for the
// parent row while targetRows resolves each target's own entry below.
func liveEntryFor(name string, byID map[string]*livework.Entry) *livework.Entry {
	return byID[name]
}

// liveWorkers is every registry entry that IS a genuine agent worker
// (Worker != nil) whose session is still running or waiting — the Workers
// tab's rows, in the registry's own insertion order (livework.Registry.List
// already guarantees that), which is what keeps the tab from reordering
// itself as entries change state. A worker is its live session, so an ended
// one is not a row; the reap's outcome reaches the status line instead.
func liveWorkers() []*livework.Entry {
	var out []*livework.Entry
	for _, e := range livework.Default.List() {
		if e.Worker == nil {
			continue
		}
		if st, ok := livework.Default.State(e.ID); ok && st.Reapable() {
			out = append(out, e)
		}
	}
	return out
}

// commandInvocationCount is how many resolved invocations cr resolves to: the
// same count commandRowItem uses to decide whether a multi-target action gets
// a target group (len(keys) > 1) rather than a plain single dispatch row — 1
// for a flat command or a single-target multi-target, len(keys) for a
// multi-target action with more than one target. It reads only cr's action,
// never a rendered row, so both rows' "Commands  N" header and
// count can total invocations without depending on whether collapse
// mode currently hides any target rows. commandRowItem calls this too, so the
// two can never disagree about which multi-target actions get a group. A
// query workflow counts one invocation per tracker item its query matches
// (workflowMatches), zero when it matches none.
func (t *commandsTab) commandInvocationCount(cr workCmdRow) int {
	a, ok := soleAction(t.cfg.manifest(), cr.cmd)
	if !ok {
		return 1
	}
	switch actionKind(a) {
	case "multi-target":
		if keys := kinds.TargetKeys(a); len(keys) > 1 {
			return len(keys)
		}
	case "workflow":
		if target, err := kinds.ParseWorkflowOver(cr.cmd.Steps[0], a); err == nil {
			ids, _ := t.workflowMatches(cr.name, target, a)
			return len(ids)
		}
	}
	return 1
}

// commandRowItem builds one Work-tab row for a declared command, resolving
// its live status (if any), its workflow target (if it is one), and its
// best-effort last-run/log.
func (t *commandsTab) commandRowItem(s *shell, cr workCmdRow, byID map[string]*livework.Entry) rowItem {
	mf := t.cfg.manifest()
	wd := &workRowData{
		cmdName:      cr.name,
		cmdDesc:      cr.cmd.Desc,
		cmdKind:      commandKind(mf, cr.cmd),
		cmdArchetype: t.cfg.cmdOrigin(cr.name),
		steps:        t.commandSteps(cr.cmd),
		live:         liveEntryFor(cr.name, byID),
		pending:      t.pendingDispatch[cr.name],
	}
	if a, ok := soleAction(mf, cr.cmd); ok {
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
				ids, note := t.workflowMatches(cr.name, target, a)
				wd.workflowNote = note
				wd.targets = ids
				wd.targetDescs = map[string]string{}
				agent := "agent"
				if f := strings.Fields(kinds.ActionRun(a)); len(f) > 0 {
					agent = f[0]
				}
				for _, id := range ids {
					wd.targetDescs[id] = "dispatch " + agent + " over " + id
				}
			}
		case "multi-target":
			// A single-target action isn't a menu — the CLI picks that
			// target without prompting (makeMultiTarget's "is this
			// project's only target"), so the row stays a plain dispatch
			// rather than a group with a target list under it.
			// commandInvocationCount makes exactly this same call so the two
			// can never disagree about which actions get a target group.
			if t.commandInvocationCount(cr) > 1 {
				wd.targets = keys
				wd.targetDescs = map[string]string{}
				wd.targetURLs = map[string][]frame.URLEntry{}
				for _, k := range keys {
					wd.targetDescs[k] = kinds.TargetDescription(mf, a, k)
					if u, err := kinds.FrameURLs(a, k, mf); err == nil {
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
			if u, err := kinds.FrameURLs(a, target, mf); err == nil {
				wd.urls = u
			}
		}
	} else {
		wd.urls = multiStepURLs(mf, cr.cmd)
	}
	var livePath string
	if wd.live != nil {
		livePath, _ = livework.Default.LogPath(wd.live.ID)
	}
	if livePath != "" {
		wd.logPath = livePath
	} else {
		wd.logPath = commandLogPath(s.manifestDir(), mf, cr.name, cr.cmd)
	}
	// A multi-target parent with more than one target is a group: it holds
	// targets and runs nothing itself, so the cursor must never land on it
	// (admin-3rgp.72.11). group rides on the rowItem, not workRowData, since
	// selectable() reads it directly with no need to reach into work. A
	// query workflow is a group whatever its query matched: its targets are
	// the tracker items, and with none there is nothing for ↵ to dispatch.
	group := len(wd.targets) > 0 || wd.workflow != nil
	return rowItem{kind: rowCommand, label: cr.name + " " + cr.cmd.Desc, group: group, work: wd}
}

// workflowMatches is the tracker item ids a query workflow's query matches
// right now, read from the beads and open issues the dashboard already
// polls (kinds.MatchQuery), so drawing the list never reads the tracker
// itself. With no ids, note says why: the tracker has not answered yet, the
// query matched nothing, or the query cannot resolve.
func (t *commandsTab) workflowMatches(name string, target kinds.WorkflowTarget, action manifest.Action) (ids []string, note string) {
	tracker, err := target.ResolveTracker(name, t.cfg.trackers())
	if err != nil {
		return nil, err.Error()
	}
	switch tracker {
	case "", "beads":
		if len(t.beads.list()) == 0 && t.beads.err() != "" {
			return nil, t.beads.err()
		}
		if !t.beads.loaded() {
			return nil, "loading beads…"
		}
	case "github":
		if t.sync.issuesFetchedAt().IsZero() {
			if e := t.sync.issuesFetchError(); e != "" {
				return nil, e
			}
			return nil, "loading issues…"
		}
	}
	matched, err := kinds.MatchQuery(action, tracker, target.Query, t.beads.list(), t.sync.issues())
	if err != nil {
		return nil, err.Error()
	}
	if len(matched) == 0 {
		return nil, "no items match " + target.Query
	}
	for _, t := range matched {
		ids = append(ids, t.Name)
	}
	return ids, ""
}

// targetRows is a command's target group: one rowCommandTarget per target of
// parent, or nothing at all when parent has no targets. It always builds the
// full set for parent — whether that set actually reaches the row list is
// rows' call, gated on t.collapsed and t.openCmd (`c` toggles
// between the two; see moveCursorWork for how the open command tracks the
// cursor). Target rows sit directly under their parent in the same flat row
// list, so every cursor, filter, window and hit-test rule already written
// for the Work tab applies to them with no special case.
//
// byID is the same registry-id -> entry map rows() built for the
// parent rows: once a target is actually dispatched, its entry is keyed on
// "<command>:<targetKey>" (internal/kinds.TargetLiveKey, as written by
// internal/kinds.multiTargetLiveID), so each target row resolves its OWN
// live entry here rather than inheriting whatever the parent happened to
// find.
func targetRows(parent rowItem, byID map[string]*livework.Entry, pendingTargetKey string) []rowItem {
	wd := parent.work
	if wd == nil {
		return nil
	}
	if wd.workflow != nil && len(wd.targets) == 0 {
		// The group's one line under it, in the tree column, so a query that
		// matched nothing reads as that rather than as a header with no rows.
		return []rowItem{{kind: rowText, label: "  └ " + wd.workflowNote}}
	}
	out := make([]rowItem, 0, len(wd.targets))
	for i, key := range wd.targets {
		live := byID[kinds.TargetLiveKey(wd.cmdName, key)]
		logPath := wd.logPath
		if live != nil {
			if p, _ := livework.Default.LogPath(live.ID); p != "" {
				logPath = p
			}
		}
		out = append(out, rowItem{
			kind:  rowCommandTarget,
			label: wd.cmdName + " " + key,
			work: &workRowData{
				cmdName:        wd.cmdName,
				cmdDesc:        wd.cmdDesc,
				cmdKind:        wd.cmdKind,
				cmdArchetype:   wd.cmdArchetype,
				steps:          wd.steps,
				workflow:       wd.workflow,
				workflowAction: wd.workflowAction,
				logPath:        logPath,
				targetKey:      key,
				targetDesc:     wd.targetDescs[key],
				targetLast:     i == len(wd.targets)-1,
				urls:           wd.targetURLs[key],
				// This target's own qualified entry, if one is running —
				// see the byID doc comment above. renderTargetRow now
				// renders state/activity/where from this exactly as
				// renderWorkRow does for a plain command row.
				live: live,
				// Same reasoning as live just above: a multi-target
				// dispatch is pending under the parent action's own name
				// until a livework.Entry registers (there is no target
				// identity to key on during that transient window — see
				// dispatchCommandCmd/activateCommandTarget), so this is
				// what activateCommandTarget's second-enter guard reads to
				// turn a second enter on a starting command's target into a
				// queued attach instead of a second run (admin-qk77).
				pending:     wd.pending,
				pendingHere: wd.pending && pendingTargetKey == key,
			},
		})
	}
	return out
}

// rows builds the Commands tab's list: every declared command, each
// multi-target or query-workflow command followed by its target rows. A
// process-kind entry (a LongRunning command actually running) never gets a row
// of its own; it folds into its command's row via commandRowItem's live lookup
// instead, and a genuine agent worker lives on the Workers tab.
func (t *commandsTab) rows(s *shell) []rowItem {
	if !t.cfg.ok() {
		return nil
	}
	cmdRows := declaredCommands(t.cfg.manifest())
	byID := liveCommandEntries()

	var cmdList []rowItem
	if len(cmdRows) == 0 {
		cmdList = append(cmdList, rowItem{kind: rowText, label: "admin.toml declares no commands"})
	} else {
		lastGroup := 0
		haveLast := false
		for _, cr := range cmdRows {
			// Blank row on every Group change — collectCommands already
			// sorted cmdRows by Group then Priority; this is just the
			// visual half of the admin skill's Phase 2c layout (group 1
			// build/dev/deploy, an automatic spacer, then group 2) that
			// this tab was missing even though the order itself was
			// already correct.
			if haveLast && cr.cmd.Group != lastGroup {
				cmdList = append(cmdList, blankRow())
			}
			lastGroup = cr.cmd.Group
			haveLast = true
			item := t.commandRowItem(s, cr, byID)
			// showTargets is the collapse gate (admin-4zz9): expanded mode
			// (t.collapsed false, the default and the launch state) shows
			// every command's targets exactly as before this existed;
			// collapsed mode shows them only for the one command currently
			// "open" (t.openCmd), which moveCursorWork keeps pointed at
			// whichever command the cursor is inside. A group it hides is
			// marked closed so its row says so.
			showTargets := !t.collapsed || cr.name == t.openCmd
			item.work.closed = item.group && !showTargets
			cmdList = append(cmdList, item)
			// No trailing ruleRow() between one group's targets and the next
			// command: the reference frames show consecutive groups adjoining
			// with no rule between them (commands.txt:13 -> :14).
			if showTargets {
				if targets := targetRows(item, byID, t.pendingTarget[cr.name]); len(targets) > 0 {
					cmdList = append(cmdList, targets...)
				}
			}
		}
	}

	// The Commands header's count is every resolved invocation — a target or
	// a flat command, never a group parent ("Commands  47") — computed from
	// cmdRows directly rather than from cmdList, so it stays the same whether
	// or not collapse mode hid any target rows out of cmdList (admin-4zz9).
	invocations := 0
	for _, cr := range cmdRows {
		invocations += t.commandInvocationCount(cr)
	}

	var rows []rowItem
	rows = append(rows, headerRow("Commands", seg{t: "  " + strconv.Itoa(invocations)}))
	rows = append(rows, cmdList...)
	return rows
}

// workersEmptyHint is the empty Workers tab's one line: where a worker can be
// started from in THIS project. A worker comes from a query-sourced
// kind="workflow" command on the Commands tab, or from a [keys.<kind>] binding
// naming a row-sourced one; the hint names each, so it never points at a
// dispatch the manifest does not declare.
func (m Model) workersEmptyHint() string {
	var sources []string
	for _, cr := range declaredCommands(m.manifest) {
		a, ok := soleAction(m.manifest, cr.cmd)
		if !ok || actionKind(a) != "workflow" {
			continue
		}
		if t, err := kinds.ParseWorkflowOver(cr.cmd.Steps[0], a); err == nil && t.Source == kinds.WorkflowSourceQuery {
			sources = append(sources, cr.name+" ("+tabDisplay(tabWork)+")")
		}
	}
	for _, kind := range keymap.Bindable {
		t, noun, ok := rowKindSource(kind)
		if !ok || m.tabIndex(t) < 0 {
			continue
		}
		keys := make([]string, 0, len(m.rowKeys[kind]))
		for k := range m.rowKeys[kind] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if c, found := commandNamed(m.manifest, m.rowKeys[kind][k]); found && isRowWorkflow(m.manifest, c) {
				sources = append(sources, keyGlyph(k)+" on "+noun+" ("+tabDisplay(t)+")")
			}
		}
	}
	if len(sources) == 0 {
		return "no workers running — this project declares no workflow"
	}
	return "no workers running — start one: " + strings.Join(sources, ", ")
}

// rowKindSource is the tab a row kind's rows live on and the noun the empty
// Workers hint calls one of them. A worker row cannot start a worker when
// there are none, so it has no source.
func rowKindSource(k keymap.RowKind) (tab, string, bool) {
	switch k {
	case keymap.Bead:
		return tabBeads, "a bead", true
	case keymap.Issue:
		return tabIssues, "an issue", true
	case keymap.PR:
		return tabPRs, "a PR", true
	case keymap.Branch:
		return tabLocal, "a branch", true
	case keymap.Commit:
		return tabLocal, "a commit", true
	case keymap.Stash:
		return tabLocal, "a stash", true
	case keymap.Command:
		return tabWork, "a command", true
	case keymap.CommandTarget:
		return tabWork, "a target", true
	}
	return 0, "", false
}

// keyGlyph is how the footer writes a bound key: enter as ↵, anything else
// as typed.
func keyGlyph(k string) string {
	if k == "enter" {
		return "↵"
	}
	return k
}

// ownerCmdName is the command a row belongs to, for collapse purposes: a
// command row's own name, or a target row's parent's name. Empty for
// anything else (a worker row, a header, a rule) — those rows carry no
// command to keep open.
func ownerCmdName(r rowItem) string {
	if r.work == nil {
		return ""
	}
	switch r.kind {
	case rowCommand, rowCommandTarget:
		return r.work.cmdName
	}
	return ""
}

// sameRowIdentity reports whether a and b are the same logical row — not the
// same rowItem value, since collapsing/expanding rebuilds every row from
// scratch, but the same command, the same command's same target, or the
// same worker entry. toggleCollapse uses this to relocate the cursor
// after a rebuild changes how many rows come before it.
func sameRowIdentity(a, b rowItem) bool {
	if a.kind != b.kind || a.work == nil || b.work == nil {
		return false
	}
	switch a.kind {
	case rowCommand:
		return a.work.cmdName == b.work.cmdName
	case rowCommandTarget:
		return a.work.cmdName == b.work.cmdName && a.work.targetKey == b.work.targetKey
	case rowWorker:
		return a.work.worker != nil && b.work.worker != nil && a.work.worker.ID == b.work.worker.ID
	}
	return false
}

// indexOfCommandRow finds name's own rowCommand row (never one of its
// targets) in rows, or -1. moveCursorWork uses this right after opening a
// command to find where its header landed in the freshly rebuilt list —
// rebuilding shifts every row after the point of change, so the header's old
// index is no longer trustworthy.
func indexOfCommandRow(rows []rowItem, name string) int {
	for i, r := range rows {
		if r.kind == rowCommand && r.work != nil && r.work.cmdName == name {
			return i
		}
	}
	return -1
}

// toggleCollapse flips the Commands tab's display mode (key `c`): expanded
// (every command's targets render — this dashboard's original behavior)
// versus collapsed (only the command the cursor is inside shows its targets
// — see rows' showTargets gate and moveCursorWork). before is read with
// the OLD mode still in effect (the expanded list always has every row, so
// ownerCmdName always resolves correctly there) — reading it after flipping
// the bool would rebuild the rows against an openCmd that hasn't been set
// yet, hiding the very command the cursor is sitting on.
// Once the mode (and, when turning collapse on, openCmd) is set, the
// cursor is relocated onto that same row's identity in the freshly built
// list, since collapsing removes rows that may sit ahead of it. The new mode
// is persisted to view.toml's work.collapsed (admin-y5lc.1) — a write
// failure is reported on the status line but never undoes the in-memory
// flip.
func (t *commandsTab) toggleCollapse(s *shell) {
	before, ok := t.currentRow(s)
	t.collapsed = !t.collapsed
	if t.collapsed && ok {
		t.openCmd = ownerCmdName(before)
	}
	if err := viewstate.SaveWorkCollapsed(s.manifestDir(), t.collapsed); err != nil {
		s.setStatus("view.toml: "+err.Error(), cRed)
	}
	if !ok {
		return
	}
	for i, r := range t.visibleRows(s) {
		if sameRowIdentity(r, before) {
			t.cursor = i
			return
		}
	}
	t.clamp(s)
}

// count is the tab bar's badge count for Commands — every resolved
// invocation (a flat command, or one entry per target of a multi-target
// command), matching how the PRs/Issues/Beads badges
// count what exists rather than what's currently painted. It totals
// commandInvocationCount over declaredCommands() directly rather than
// counting rendered rows, so the number is the same in collapsed and
// expanded mode and at every cursor position — collapsing the Work tab's
// `c` toggle only changes which target rows are drawn, never how many
// commands or targets exist.
func (t *commandsTab) count() int {
	if !t.cfg.ok() {
		return 0
	}
	n := 0
	for _, cr := range declaredCommands(t.cfg.manifest()) {
		n += t.commandInvocationCount(cr)
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
	args []string
	err  error
}

// resolvedInvocation joins name and args into the fully-resolved dispatch
// string workusage.Record wants — "deploy grafana", never the bare "deploy"
// a multi-target command is a no-op without its target. Args is empty for a
// flat command, so this returns bare name in that case.
func resolvedInvocation(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + strings.Join(args, " ")
}

// startHostedRun is kinds.StartHostedPTY behind a package-level var so a
// test can stub it without spawning a real pty — mirrors pane.go's
// selfExecutable seam.
var startHostedRun = kinds.StartHostedPTY

// dispatchCommandCmd starts name and returns immediately: unlike the
// tea.Exec dispatch it replaces, this is an ordinary asynchronous tea.Cmd.
// It routes through kinds.RunCommand with a dashboard CommandProducer —
// admin-ysxr.4's collapsed entry point — rather than calling StartHostedPTY
// directly, so a command whose first step declares `locale = "window"` against
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
		return workDispatchStartedMsg{name: name, args: args, err: err}
	}
}

// rowAction is what ↵ does on a row — resolved once, in resolveRowAction
// below, from the row's own state rather than re-derived per caller.
//
// admin-3rgp.72.18: activateCommandTarget carried its own copy of
// activateCommand's ladder, and only the activateCommand copy ever got the
// "already live → attach" branch (admin-3rgp.72.12 added it there and
// nowhere else) — so ↵ on a running target row (e.g. command-test's
// "counter") dispatched a SECOND run while the footer, which already
// read row state independently and got it right, advertised "↵ attach". The
// fix is not a second copy of the missing branch on the target ladder; it is
// this one function, read by activateCommand, activateCommandTarget, the
// workersTab.activate, and workSurface.bindings — so the
// four can no longer drift the way the two ladders just did.
type rowAction int

const (
	actionNone rowAction = iota
	actionDispatch
	actionAttach
	actionTranscript
)

// resolveRowAction decides ↵'s primary action for kind+wd. wd may be nil (a
// row with no work payload, or one whose kind carries no primary action at
// all) — actionNone then, same as any kind resolveRowAction doesn't
// recognize (rowHeader, rowConfigValue, …).
func resolveRowAction(kind rowKind, wd *workRowData) rowAction {
	if wd == nil {
		return actionNone
	}
	switch kind {
	case rowCommand:
		if entryIsLive(wd.live) {
			return actionAttach
		}
		return actionDispatch
	case rowCommandTarget:
		if entryIsLive(wd.live) {
			return actionAttach
		}
		return actionDispatch
	case rowWorker:
		if entryIsLive(wd.worker) {
			return actionAttach
		}
		return actionTranscript
	}
	return actionNone
}

// activateCommand handles enter on a command row: an already-live command
// attaches to its pane instead of dispatching a second run on top of the one
// that owns its log file and registry entry; a command whose previous enter
// is still dispatching (wd.pending) queues the attach instead of starting a
// second run on top of the one that hasn't registered yet (admin-qk77);
// anything else dispatches. A multi-target command with more than one target,
// or a query workflow, is a group row (rowItem.group) the cursor can never
// land on, so this is never reached for one (admin-3rgp.72.11 removed the
// "pick a target below" branch that used to guard that case here — its
// targets already render below it, and enter on one of those dispatches it).
func (t *commandsTab) activateCommand(s *shell, wd *workRowData) tea.Cmd {
	if resolveRowAction(rowCommand, wd) == actionAttach {
		return s.attach(wd.live)
	}
	if wd.pending {
		// The dispatch this row's own previous enter started hasn't
		// registered a livework.Entry yet (or already failed and the
		// pendingDispatch flag just hasn't been cleared by this frame) — do
		// NOT dispatch again. Record the intent to attach once it lands;
		// dispatchStarted resolves and attaches it.
		t.pendingAttach[wd.cmdName] = true
		s.setStatus(wd.cmdName+" starting…", cCyan)
		return nil
	}
	return t.startDispatch(s, wd.cmdName)
}

// startDispatch marks name pending (so its row renders "starting" and a
// second enter during the window queues an attach instead of dispatching
// again — activateCommand/activateCommandTarget above) and returns the same
// tea.Cmd dispatchCommandCmd always returned. The single entry point both
// dispatch sites route through, so pendingDispatch can never go stale by one
// of them forgetting to set it.
func (t *commandsTab) startDispatch(s *shell, name string, args ...string) tea.Cmd {
	t.pendingDispatch[name] = true
	// args[0], when present, is the target key a multi-target dispatch was
	// made with (activateCommandTarget) — recorded so targetRows can light up
	// only the ONE target row actually being dispatched (admin-2y6p) rather
	// than every sibling sharing the same pendingDispatch[name] flag. Cleared
	// (not left stale) on a flat dispatch so a later target dispatch of the
	// same command name never inherits a leftover key from a previous run.
	if len(args) > 0 {
		t.pendingTarget[name] = args[0]
	} else {
		delete(t.pendingTarget, name)
	}
	return dispatchCommandCmd(t.cfg.manifest(), s.width, s.height, name, nil, args...)
}

// dispatchStarted ends name's "starting" window once its dispatch reports —
// on both the error and success paths (admin-qk77): a failed spawn falls back
// to idle, and a successful one either shows running (once the entry exists)
// or idle again if the command already exited. An enter that arrived during
// the window attaches now, to the entry the dispatch registered.
func (t *commandsTab) dispatchStarted(s *shell, msg workDispatchStartedMsg) tea.Cmd {
	delete(t.pendingDispatch, msg.name)
	delete(t.pendingTarget, msg.name)
	queued := t.pendingAttach[msg.name]
	delete(t.pendingAttach, msg.name)
	if msg.err != nil || !queued {
		return nil
	}
	if e := t.liveEntryForCommand(s, msg.name); e != nil {
		return s.attach(e)
	}
	return nil
}

// liveEntryForCommand resolves the livework.Entry now backing name, once its
// dispatch has registered one — dispatchStarted's queued-attach path uses
// this rather than re-deriving the registry ID (multiTargetLiveID per
// locale.go/hostedpty.go) itself, reusing commandRowItem's/targetRows' own
// lookups via the same rows() scan every other row-driven action already
// does.
//
// pendingDispatch/pendingAttach stay keyed by command name only, not target
// — there is at most one in-flight dispatch per command name (the existing
// pending guard), so name alone remains unambiguous — but the entry itself
// may now be recorded under a target row's qualified name rather than the
// parent's bare one, so both rowCommand and rowCommandTarget rows are
// scanned for a match.
func (t *commandsTab) liveEntryForCommand(s *shell, name string) *livework.Entry {
	for _, r := range t.rows(s) {
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
// group: an already-live target attaches to its pane instead of dispatching
// a second run on top of the one that owns its log file and registry entry
// (resolveRowAction above — this used to be a separate copy of the ladder
// that never got that branch, admin-3rgp.72.18); otherwise it dispatches the
// parent command with that target as its argument, which is exactly what
// `admin <cmd> <target>` does from a shell — makeMultiTarget takes args[0]
// as the picked target and never raises its own picker. Until the dispatch
// registers a livework.Entry there is no target-qualified name to key on yet
// (the entry doesn't exist), so pending stays keyed on wd.cmdName here too,
// matching how the parent row itself already reads it, and the same "don't
// dispatch a second run into the one still starting" guard applies
// (admin-qk77). Once the entry lands it's keyed on "<cmdName>:<targetKey>"
// (multiTargetLiveID) and targetRows resolves it per target from then on.
//
// A query workflow's target is one matched tracker item, and ↵ dispatches an
// agent over that item alone (dispatchWorkflowTarget). Its agent registers
// as a worker, which lives on the Workers tab, so the row never attaches.
func (t *commandsTab) activateCommandTarget(s *shell, wd *workRowData) tea.Cmd {
	if wd.workflow != nil {
		return dispatchWorkflowTarget(s, wd)
	}
	if resolveRowAction(rowCommandTarget, wd) == actionAttach {
		return s.attach(wd.live)
	}
	if wd.pending {
		t.pendingAttach[wd.cmdName] = true
		s.setStatus(wd.cmdName+" starting…", cCyan)
		return nil
	}
	return t.startDispatch(s, wd.cmdName, wd.targetKey)
}

// dispatchWorkflowTarget hands one matched tracker item to
// kinds.DispatchTargets through dispatchWorkflowCmd, the path a row-scoped
// workflow binding takes too (rowKeyWorkflow), and reports the outcome on the
// status line.
func dispatchWorkflowTarget(s *shell, wd *workRowData) tea.Cmd {
	target := kinds.NewTarget(wd.workflowAction, wd.targetKey)
	if err := kinds.RequireRun(wd.cmdName, target.Run); err != nil {
		s.setStatus(err.Error(), cRed)
		return nil
	}
	s.setStatus(fmt.Sprintf("dispatching %s over %s…", wd.cmdName, wd.targetKey), cCyan)
	return dispatchWorkflowCmd(s.repoRoot, wd.cmdName, wd.workflowAction, []kinds.Target{target})
}

// entryIsLive reports whether e is still running in the sense that matters to
// the footer and to kill: something a and x can act on. StateExited/
// StateFailed are terminal — nothing to attach-and-watch, nothing to kill —
// even though the row (a finished worker) keeps its own Entry around.
func entryIsLive(e *livework.Entry) bool {
	if e == nil {
		return false
	}
	st, _ := livework.Default.State(e.ID)
	switch st {
	case livework.StateExited, livework.StateFailed:
		return false
	default:
		return true
	}
}

// workTickMsg fires on a repeating timer to repaint the Work tab so a
// livework.Entry another goroutine mutated (a herdr pane settling, an agent
// finishing) shows up without needing a keypress — see Init's own comment.
type workTickMsg struct{}

func (m Model) workTickCmd() tea.Cmd {
	if m.headless {
		return nil
	}
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return workTickMsg{} })
}

// refreshDetailProbes kicks off (non-blocking) reachability probes for the
// urls the row under the cursor is currently showing — and only while that
// row is live: an idle command's urls are never probed, so the pane renders
// them with no reachability indicator at all rather than a stale one. Called
// from Update (workTickMsg and workDispatchStartedMsg), never from View —
// starting a probe is a side effect, and rendering must stay pure.
func (m Model) refreshDetailProbes() {
	if m.manifestOK && m.manifest != nil {
		statusProbes.Refresh(m.manifest.Status, m.manifest.Root)
	}
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
