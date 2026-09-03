// Package keymap holds the built-in key -> label bindings for every
// bindable row kind (bead, issue, branch, PR, commit, stash), keyed by
// RowKind rather than by TUI tab/pane. It is a leaf package (imports no
// other internal package) so both consumers can depend on it without
// creating a cycle:
//
//   - internal/tui's Model.keymap() reads it to build the footer help line
//     (ADR-0018's "the built-in key table moves to internal/keymap").
//   - internal/validate's row-key-binding check (admin-l6kb.1) reads it to
//     detect when a user's [keys.<row-kind>] entry shadows a built-in.
//
// Before this package existed, the table lived only in internal/tui/view.go
// as literals, and internal/validate (a leaf validate couldn't import the
// heaviest package in the repo) had nothing to check shadowing against.
// Duplicating the table into internal/validate instead of extracting it
// would have produced two lists nothing keeps in sync — a key relabelled in
// the footer would silently make `admin check` report a shadow that no
// longer exists (or miss one that does).
//
// The tables here are transcribed verbatim from Model.keymap() and must stay
// byte-identical to it: internal/tui/model_test.go and
// internal/tui/design_parity_test.go assert the footer's exact contents.
//
// This package also owns Reserved, the always-on global key set a
// [keys.<row-kind>] table may never bind — read by internal/validate (admin
// check rejects a binding for one) and internal/tui's rowKeyDispatch
// (refuses one before consulting the bindings), so an incomplete list can
// no longer let a binding silently pre-empt a global key (admin-gz7g).
// Model.handleKey's own case labels remain a deliberately unshared third
// copy — a switch statement cannot read a set.
package keymap

// RowKind identifies one of the six row kinds a manifest's [keys.<kind>]
// table may bind — the tab/pane pairs of internal/tui that show a list of
// real items, as opposed to the Work tab's own rows (command, commandTarget,
// worker) or the Config tab (nothing to bind at all).
type RowKind string

const (
	Bead   RowKind = "bead"
	Issue  RowKind = "issue"
	Branch RowKind = "branch"
	PR     RowKind = "pr"
	Commit RowKind = "commit"
	Stash  RowKind = "stash"
)

// Bindable lists every RowKind a manifest may bind, in a stable order used
// to build "the kinds that are" error text.
var Bindable = []RowKind{Bead, Issue, Branch, PR, Commit, Stash}

// NotBindable names the Work tab's own row kinds — command, commandTarget,
// worker. Their Enter key already means "run this command" / "attach to
// this worker", so a manifest may not rebind them; internal/validate uses
// this to give those three words a more specific error than "unknown kind".
var NotBindable = []string{"command", "commandTarget", "worker"}

// Reserved is the always-on global key set — navigation and escape a config
// file cannot take away. It is the single source of truth for both readers
// that must agree on it: internal/validate/keys.go's RowKeys rejects a
// [keys.<row-kind>] entry naming one of these, and internal/tui's
// rowKeyDispatch refuses one before consulting m.rowKeys, so even a binding
// that somehow got installed cannot pre-empt the global it collides with.
// Transcribed verbatim from Model.handleKey's always-on case labels
// (internal/tui/model.go) — treat as read-only; do not add or remove
// members without updating handleKey to match.
var Reserved = map[string]bool{
	"q": true, "ctrl+c": true, "esc": true, "tab": true, "shift+tab": true,
	"up": true, "down": true, "left": true, "right": true,
	"j": true, "k": true, "1": true, "2": true, "3": true, "4": true, "/": true,
}

// Binding is one built-in key/label pair for a row kind's footer.
//
// Key is the config-vocabulary key name a manifest author would write in
// [keys.<kind>] (e.g. "enter", "d", "o") — empty when the pair has no
// bindable key name at all, such as `↑↓ move` (a range of keys, not one)
// or `⌥↵/W worktree` (its primary chord, ⌥↵, isn't in the key vocabulary;
// `w` is a distinct binding of its own). Display is the glyph the footer
// shows (e.g. "↵", "⇥", "↑↓"). Label is the human-readable action name
// (e.g. "start work", "detail"). A Binding with an empty Key is excluded
// from shadow matching — it names an action a manifest cannot bind a key to
// in the first place.
type Binding struct {
	Key     string
	Display string
	Label   string
}

// builtins holds each row kind's built-in bindings, transcribed verbatim
// (order included) from Model.keymap() in internal/tui/view.go, minus the
// trailing `←→ tab` pair every view appends on top and shares in common —
// that composition stays in view.go since it isn't a row-kind label.
var builtins = map[RowKind][]Binding{
	Bead: {
		{Key: "", Display: "↑↓", Label: "move"},
		{Key: "enter", Display: "↵", Label: "start work"},
		{Key: "d", Display: "d", Label: "detail"},
		{Key: "g", Display: "g", Label: "tree"},
		{Key: "shift+g", Display: "⇧G", Label: "family"},
		{Key: "s", Display: "s", Label: "sort"},
		{Key: "w", Display: "w", Label: "workflow"},
		{Key: "/", Display: "/", Label: "filter"},
		{Key: "r", Display: "r", Label: "refresh"},
		{Key: "q", Display: "q", Label: "quit"},
	},
	Issue: {
		{Key: "", Display: "↑↓", Label: "move"},
		{Key: "enter", Display: "↵", Label: "start work"},
		{Key: "s", Display: "s", Label: "sort"},
		{Key: "w", Display: "w", Label: "workflow"},
		{Key: "/", Display: "/", Label: "filter"},
		{Key: "r", Display: "r", Label: "refresh row"},
		{Key: "shift+r", Display: "⇧R", Label: "refresh all"},
		{Key: "q", Display: "q", Label: "quit"},
	},
	PR: {
		{Key: "", Display: "↑↓", Label: "move"},
		{Key: "enter", Display: "↵", Label: "checkout"},
		{Key: "i", Display: "i", Label: "issue"},
		{Key: "w", Display: "w", Label: "workflow"},
		{Key: "/", Display: "/", Label: "filter"},
		{Key: "r", Display: "r", Label: "refresh row"},
		{Key: "shift+r", Display: "⇧R", Label: "refresh all"},
		{Key: "q", Display: "q", Label: "quit"},
	},
	Branch: {
		{Key: "", Display: "↑↓", Label: "move"},
		{Key: "enter", Display: "↵", Label: "checkout"},
		{Key: "", Display: "⌥↵/W", Label: "worktree"},
		// "new" is the label with no branch selected. internal/tui's
		// branchPairs (view.go) relabels this pair to "workflow" on the fly
		// whenever the Branches pane's own selection is nonempty — `w` then
		// runs a workflow over the selected branches instead of opening the
		// new-worktree prompt (admin-4pps). This table stays the empty-
		// selection truth; it is not the whole story for what `w` does.
		{Key: "w", Display: "w", Label: "new"},
		{Key: "x", Display: "x", Label: "delete"},
		{Key: "p", Display: "p", Label: "pull"},
		{Key: "tab", Display: "⇥", Label: "pane"},
		{Key: "/", Display: "/", Label: "filter"},
		{Key: "r", Display: "r", Label: "refresh"},
	},
	Commit: {
		{Key: "", Display: "↑↓", Label: "move"},
		{Key: "enter", Display: "↵", Label: "copy sha"},
		{Key: "tab", Display: "⇥", Label: "pane"},
		{Key: "/", Display: "/", Label: "filter"},
		{Key: "r", Display: "r", Label: "refresh"},
	},
	Stash: {
		{Key: "", Display: "↑↓", Label: "move"},
		{Key: "enter", Display: "↵", Label: "details"},
		{Key: "tab", Display: "⇥", Label: "pane"},
		{Key: "/", Display: "/", Label: "filter"},
		{Key: "r", Display: "r", Label: "refresh"},
	},
}

// Builtins returns the built-in key/label pairs for a row kind, in the same
// order Model.keymap() emits them (minus the trailing `←→ tab` pair, which
// stays composed in view.go). Returns nil for a RowKind not in Bindable.
func Builtins(k RowKind) []Binding {
	return builtins[k]
}

// Kind looks up a row-kind word (as it would appear as a [keys.<word>]
// table name in admin.toml) against the bindable set. ok is false for any
// word not in Bindable, including the Work tab's own row kinds in
// NotBindable and any unrecognized word.
func Kind(name string) (RowKind, bool) {
	k := RowKind(name)
	for _, b := range Bindable {
		if b == k {
			return k, true
		}
	}
	return "", false
}
