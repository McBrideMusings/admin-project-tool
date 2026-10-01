// rowkeys.go is admin-l6kb.2's dashboard side of ADR-0018: turning the
// validated [keys.<row-kind>] table (admin-l6kb.1's validate.RowKeys) into
// an actual (key, row kind) -> command lookup, and dispatching a HIT
// through the same command-activation path a Commands-tab row already
// uses (work.go's dispatchCommandCmd), with the row's identity exported to
// the dispatched process as ADMIN_ROW_KIND/ADMIN_ROW_ID/ADMIN_ROW_URL. A
// MISS returns ok=false and leaves the built-in key switch in
// Model.handleKey completely untouched — that is what keeps every existing
// project's keymap identical to today's with no [keys] table declared.
package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/keymap"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// buildRowKeys resolves m's top-level [keys.<row-kind>] table into the
// (kind, key) -> command lookup rowKeyDispatch reads, via validate.RowKeys
// over m.CommandNames() — the same interpreter and the same command set
// `admin check` validates against, so the dashboard never runs a second
// parser. Shadows and a validation error are both ignored here: `admin
// check` is the reporting surface for a malformed [keys] table (per
// ADR-0018), and RowKeys already returns only the bindings it accepted
// even when it also reports problems — so a manifest that fails validation
// still gets whatever bindings were well-formed rather than none at all.
func buildRowKeys(m *manifest.Manifest) map[keymap.RowKind]map[string]string {
	bindings, _, _ := validate.RowKeys(m.Keys, m.CommandNames())
	if len(bindings) == 0 {
		return nil
	}
	out := make(map[keymap.RowKind]map[string]string, len(bindings))
	for _, b := range bindings {
		if out[b.Kind] == nil {
			out[b.Kind] = map[string]string{}
		}
		out[b.Kind][b.Key] = b.Command
	}
	return out
}

// rowKindOf maps a rowItem to the keymap.RowKind a [keys.<kind>] table
// could bind against, and reports false for every non-selectable row
// (header/rule/text/center) — none of those are in keymap.Bindable, so none
// of them can collide with a project's own key bindings.
func rowKindOf(r rowItem) (keymap.RowKind, bool) {
	switch r.kind {
	case rowCommand:
		return keymap.Command, r.work != nil
	case rowCommandTarget:
		return keymap.CommandTarget, r.work != nil
	case rowWorker:
		return keymap.Worker, r.work != nil && r.work.worker != nil
	case rowBead:
		return keymap.Bead, true
	case rowIssue:
		return keymap.Issue, true
	case rowPR:
		return keymap.PR, true
	case rowBranch:
		return keymap.Branch, true
	case rowCommit:
		return keymap.Commit, true
	case rowStash:
		return keymap.Stash, true
	default:
		return "", false
	}
}

// rowEnv builds the three ADMIN_ROW_* vars a bound command's dispatch gets,
// per row kind — a thin delegation to kinds.RowEnv, the single construction
// this package's key-binding dispatch and internal/kinds' launcher dispatch
// (ADR-0020's ADMIN_ROW_* spawn context) both use, so the two cannot drift
// apart.
func rowEnv(r rowItem, kind keymap.RowKind) []string {
	return kinds.RowEnv(string(kind), rowID(r, kind), r.url)
}

// rowID picks the row's own identity field for ADMIN_ROW_ID, per kind. A
// Work row's id is the live-work id its command's run is keyed on
// (kinds.multiTargetLiveID): the command name, "<command>:<target>" for a
// target row, or the worker's own entry id — the id a reload loop records
// as its owner (internal/reload.Record.Live).
func rowID(r rowItem, kind keymap.RowKind) string {
	switch kind {
	case keymap.Command:
		return r.work.cmdName
	case keymap.CommandTarget:
		return kinds.TargetLiveKey(r.work.cmdName, r.work.targetKey)
	case keymap.Worker:
		return r.work.worker.ID
	case keymap.Bead:
		return r.beadID
	case keymap.Issue:
		return strconv.Itoa(r.issueNum)
	case keymap.PR:
		return strconv.Itoa(r.prNum)
	case keymap.Branch:
		return r.branch
	case keymap.Commit:
		return r.label // the commit SHA (model.go's localCurrent)
	case keymap.Stash:
		// A stash row's label is "<ref>  <subject>" (two spaces,
		// model.go's localCurrent) — there is no dedicated ref field, so
		// the ref is the part before that separator. A label with no
		// separator (defensive; localCurrent always writes one) falls
		// back to the whole label rather than an empty id.
		if ref, _, ok := strings.Cut(r.label, "  "); ok {
			return ref
		}
		return r.label
	default:
		return ""
	}
}

// rowKeyDispatch is handleKey's row-scoped-key-binding hook: ok is false on
// any miss (no [keys] table at all, no row under the cursor, an
// unbindable row kind, or no binding for this exact key on this exact
// kind) — leaving the built-in switch in handleKey to run exactly as it
// does today. A HIT dispatches the named command through
// dispatchCommandCmd, the same path activateCommand uses for a
// Commands-tab row, with the row's identity in env — unless that command
// already has a live entry, in which case it attaches instead of racing it
// (admin-c2zl): a second run over a command whose first run still owns the
// log file is exactly what recordHostedPTYWork's own comment
// (internal/kinds/hostedpty.go) keys the live entry on cmd.Steps[0] to
// prevent, and the Commands-tab path already honors that guard.
func (m Model) rowKeyDispatch(key string) (tea.Model, tea.Cmd, bool) {
	if len(m.rowKeys) == 0 || !m.manifestOK {
		return m, nil, false
	}
	// keymap.Reserved is refused before any lookup, not just left out of
	// admin check's accepted bindings: validate.RowKeys is the only thing
	// that keeps a reserved key out of m.rowKeys today, so a binding that
	// somehow got installed anyway (a manifest bypassing validation, a bug
	// in buildRowKeys) would otherwise silently kill a global key with no
	// error and no shadow line. Refusing here degrades that failure to
	// "the binding does nothing" instead (admin-gz7g).
	if keymap.Reserved[key] {
		return m, nil, false
	}
	r, ok := m.currentRow()
	if !ok {
		return m, nil, false
	}
	kind, ok := rowKindOf(r)
	if !ok {
		return m, nil, false
	}
	name, ok := m.rowKeys[kind][key]
	if !ok {
		return m, nil, false
	}
	c, found := m.commandNamed(name)
	if found && m.isRowWorkflow(c) {
		mm, cmd := m.rowKeyWorkflow(name, c, r, kind)
		return mm, cmd, true
	}
	if found {
		// Same filter as workRows' byName build (work.go): only non-worker
		// entries are keyed by command name. Duplicated rather than
		// shared because work.go is under concurrent modification by
		// another swarm — see commandNamed's comment for the same call.
		byName := map[string]*livework.Entry{}
		for _, e := range livework.Default.List() {
			if e.Worker == nil {
				byName[e.Name] = e
			}
		}
		if e := liveEntryFor(name, byName); e != nil {
			mm, cmd := m.attachEntry(e)
			return mm, cmd, true
		}
	}
	return m, dispatchCommandCmd(m.manifest, m.width, m.height, name, rowEnv(r, kind)), true
}

// commandNamed looks up a declared command by name, Custom winning over
// Overrides — the same rule collectCommands' own unexported `lookup`
// closure applies (work.go), duplicated here rather than shared because
// that closure is local to collectCommands and work.go is under
// concurrent modification by another swarm.
func (m Model) commandNamed(name string) (manifest.Command, bool) {
	if c, ok := m.manifest.Custom[name]; ok {
		return c, true
	}
	c, ok := m.manifest.Overrides[name]
	return c, ok
}

// rowKeyWorkflow is rowKeyDispatch's branch for a binding that resolves to
// a single-step kind="workflow" action with over="row" (ADR-0018): it
// dispatches immediately over just the row under the cursor, with no overlay,
// reporting the outcome on the status line instead.
func (m Model) rowKeyWorkflow(name string, c manifest.Command, r rowItem, kind keymap.RowKind) (tea.Model, tea.Cmd) {
	action, ok := m.soleAction(c)
	if !ok {
		// Defensive: isRowWorkflow already proved c is a single-step
		// kind="workflow" action, so this can't happen outside a bug
		// changing that filter without updating this assumption.
		m.setStatus(name+": not a workflow", cRed)
		return m, nil
	}
	id := rowID(r, kind)
	if id == "" {
		m.setStatus(name+": no id on this row to dispatch over", cRed)
		return m, nil
	}
	targets := []kinds.Target{kinds.NewTarget(action, id)}
	if err := kinds.RequireRun(name, targets[0].Run); err != nil {
		m.setStatus(err.Error(), cRed)
		return m, nil
	}

	m.setStatus(fmt.Sprintf("dispatching %s over %s…", name, id), cCyan)
	return m, dispatchWorkflowCmd(m.repoRoot, name, action, targets)
}
