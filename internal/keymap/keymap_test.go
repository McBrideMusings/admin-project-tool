package keymap

import "testing"

func TestBindable(t *testing.T) {
	want := []RowKind{Bead, Issue, Branch, PR, Commit, Stash}
	if len(Bindable) != len(want) {
		t.Fatalf("Bindable = %v, want %v", Bindable, want)
	}
	for i, k := range want {
		if Bindable[i] != k {
			t.Errorf("Bindable[%d] = %q, want %q", i, Bindable[i], k)
		}
	}
}

func TestKind(t *testing.T) {
	for _, k := range Bindable {
		got, ok := Kind(string(k))
		if !ok || got != k {
			t.Errorf("Kind(%q) = (%q, %v), want (%q, true)", k, got, ok, k)
		}
	}
	for _, bad := range append([]string{"unknown", "", "Bead"}, NotBindable...) {
		if _, ok := Kind(bad); ok {
			t.Errorf("Kind(%q) unexpectedly ok", bad)
		}
	}
}

// TestBuiltins pins each row kind's built-in labels and their order —
// transcribed from Model.keymap() in internal/tui/view.go. A relabel there
// must be caught here rather than surfacing only as a footer-test failure.
func TestBuiltins(t *testing.T) {
	cases := []struct {
		kind   RowKind
		labels []string // Display+Label pairs flattened for a readable diff
	}{
		{Bead, []string{
			"↑↓:move", "↵:start work", "d:detail", "g:tree", "s:sort",
			"w:workflow", "/:filter", "r:refresh", "q:quit",
		}},
		{Issue, []string{
			"↑↓:move", "↵:start work", "s:sort", "w:workflow",
			"/:filter", "r:refresh row", "⇧R:refresh all", "q:quit",
		}},
		{PR, []string{
			"↑↓:move", "↵:checkout", "i:issue", "w:workflow", "/:filter",
			"r:refresh row", "⇧R:refresh all", "q:quit",
		}},
		{Branch, []string{
			"↑↓:move", "↵:checkout", "⌥↵/W:worktree", "w:new", "x:delete",
			"p:pull", "⇥:pane", "/:filter", "r:refresh",
		}},
		{Commit, []string{
			"↑↓:move", "↵:copy sha", "⇥:pane", "/:filter", "r:refresh",
		}},
		{Stash, []string{
			"↑↓:move", "↵:details", "⇥:pane", "/:filter", "r:refresh",
		}},
	}
	for _, c := range cases {
		bindings := Builtins(c.kind)
		if len(bindings) != len(c.labels) {
			t.Errorf("Builtins(%q) has %d entries, want %d (%v)", c.kind, len(bindings), len(c.labels), c.labels)
			continue
		}
		for i, b := range bindings {
			got := b.Display + ":" + b.Label
			if got != c.labels[i] {
				t.Errorf("Builtins(%q)[%d] = %q, want %q", c.kind, i, got, c.labels[i])
			}
		}
	}
}

// TestReserved pins Reserved's membership — the always-on global key set
// internal/validate and internal/tui's rowKeyDispatch both read as their
// single source of truth (admin-gz7g). A future thinning of this set must
// fail here rather than silently letting a manifest rebind a global.
func TestReserved(t *testing.T) {
	want := []string{
		"q", "ctrl+c", "esc", "tab", "shift+tab",
		"up", "down", "left", "right",
		"j", "k", "1", "2", "3", "4", "/",
	}
	if len(Reserved) != len(want) {
		t.Fatalf("len(Reserved) = %d, want %d (%v)", len(Reserved), len(want), want)
	}
	for _, k := range want {
		if !Reserved[k] {
			t.Errorf("Reserved[%q] = false, want true", k)
		}
	}
	for _, bindable := range []string{"o", "enter", "d", "g", "s", "w"} {
		if Reserved[bindable] {
			t.Errorf("Reserved[%q] = true, want false — it's a bindable key", bindable)
		}
	}
}
