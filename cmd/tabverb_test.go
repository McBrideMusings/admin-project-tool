package cmd

import (
	"slices"
	"testing"
)

// TestTabVerbsAreReserved: every tab verb is a registered command, so a
// manifest command of the same name is reported by admin check, and `cmd`
// stays the picker.
func TestTabVerbsAreReserved(t *testing.T) {
	verbs := reservedCmdVerbs()
	for _, want := range []string{"workers", "repo", "prs", "issues", "beads", "config", "cmd"} {
		if !slices.Contains(verbs, want) {
			t.Errorf("reservedCmdVerbs() lacks %q: %v", want, verbs)
		}
	}
}
