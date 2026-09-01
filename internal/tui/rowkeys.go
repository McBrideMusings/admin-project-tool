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
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/keymap"
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
// could bind against, and reports false for every row the Work tab owns
// itself (rowCommand/rowCommandTarget/rowWorker) and every non-selectable
// row (header/rule/text/center) — none of those are in keymap.Bindable, so
// none of them can collide with a project's own key bindings.
func rowKindOf(r rowItem) (keymap.RowKind, bool) {
	switch r.kind {
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
// per row kind. ADMIN_ROW_URL is appended only when the row genuinely
// carries a URL — never as an empty value, since the dispatched process
// inherits the dashboard's own os.Environ() as its base and an empty
// ADMIN_ROW_URL="" would still read as "set" to anything that only checks
// presence.
func rowEnv(r rowItem, kind keymap.RowKind) []string {
	env := []string{
		"ADMIN_ROW_KIND=" + string(kind),
		"ADMIN_ROW_ID=" + rowID(r, kind),
	}
	if r.url != "" {
		env = append(env, "ADMIN_ROW_URL="+r.url)
	}
	return env
}

// rowID picks the row's own identity field for ADMIN_ROW_ID, per kind.
func rowID(r rowItem, kind keymap.RowKind) string {
	switch kind {
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
// Commands-tab row, with the row's identity in env.
func (m Model) rowKeyDispatch(key string) (tea.Model, tea.Cmd, bool) {
	if len(m.rowKeys) == 0 || !m.manifestOK {
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
	return m, dispatchCommandCmd(m.manifest, m.width, m.height, name, rowEnv(r, kind)), true
}
