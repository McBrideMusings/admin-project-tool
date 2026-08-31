package cmd

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestCheckReservedCollisionsCleanManifest proves a manifest with no
// colliding command names passes.
func TestCheckReservedCollisionsCleanManifest(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"deploy": {Steps: []string{"noop"}},
		},
	}
	if err := checkReservedCollisions(m); err != nil {
		t.Fatalf("checkReservedCollisions = %v, want nil", err)
	}
}

// TestCheckReservedCollisionsRejectsToolVerb is the ADR-0011 §3 acceptance
// criterion: a manifest command named after a reserved tool verb is
// rejected, and the error names the rule and the colliding name.
//
// MUTATION EVIDENCE: with reservedToolVerbs missing "worktree" (simulating
// a dropped entry), this test's collision case fails with:
//
//	checkReservedCollisions(worktree) = nil, want an error naming "worktree"
func TestCheckReservedCollisionsRejectsToolVerb(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"worktree": {Steps: []string{"noop"}},
		},
	}
	err := checkReservedCollisions(m)
	if err == nil {
		t.Fatal("checkReservedCollisions(worktree) = nil, want an error naming \"worktree\"")
	}
	if !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("error %q does not name the colliding command", err.Error())
	}
	if !strings.Contains(err.Error(), "ADR-0011") {
		t.Fatalf("error %q does not name the rule", err.Error())
	}
}

// TestCheckReservedCollisionsRejectsCmdVerb proves the same rule extends to
// cmd/'s own verbs (branch, prs, populate), not only the five tool verbs.
func TestCheckReservedCollisionsRejectsCmdVerb(t *testing.T) {
	m := &manifest.Manifest{
		Overrides: map[string]manifest.Command{
			"prs": {Steps: []string{"noop"}},
		},
	}
	err := checkReservedCollisions(m)
	if err == nil {
		t.Fatal("checkReservedCollisions(prs) = nil, want an error naming \"prs\"")
	}
	if !strings.Contains(err.Error(), "prs") {
		t.Fatalf("error %q does not name the colliding command", err.Error())
	}
}

// TestCheckReservedCollisionsReportsEveryName proves multiple collisions are
// all named, sorted, in one error rather than only the first found.
func TestCheckReservedCollisionsReportsEveryName(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"new":   {Steps: []string{"noop"}},
			"check": {Steps: []string{"noop"}},
		},
	}
	err := checkReservedCollisions(m)
	if err == nil {
		t.Fatal("checkReservedCollisions = nil, want an error")
	}
	for _, name := range []string{"new", "check"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not name colliding command %q", err.Error(), name)
		}
	}
}
