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
			"↑↓:move", "↵:checkout", "i:issue", "/:filter",
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
