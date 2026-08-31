package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
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

// -- checkCommandRouting -----------------------------------------------------

// TestCheckCommandRoutingCleanManifestPasses proves a manifest whose
// commands only carry desc/steps validates clean.
func TestCheckCommandRoutingCleanManifestPasses(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"deploy": {Steps: []string{"noop"}},
		},
	}
	if err := checkCommandRouting(m); err != nil {
		t.Fatalf("checkCommandRouting = %v, want nil", err)
	}
}

// TestCheckCommandRoutingRejectsKindOnACommand is the ticket's acceptance
// criterion: "A command containing kind, run or targets is rejected."
//
// MUTATION EVIDENCE: with checkCommandRouting's loop over m.Overrides
// deleted (only m.Custom checked), this test — which puts the offending
// command in m.Overrides — failed with:
//
//	checkCommandRouting = <nil>, want an error naming "kind"
//
// (reverted after confirming the failure).
func TestCheckCommandRoutingRejectsKindOnACommand(t *testing.T) {
	m := &manifest.Manifest{
		Overrides: map[string]manifest.Command{
			"deploy": {Steps: []string{"noop"}, Raw: map[string]any{"kind": "shell", "run": "echo hi"}},
		},
	}
	err := checkCommandRouting(m)
	if err == nil {
		t.Fatal(`checkCommandRouting = <nil>, want an error naming "kind"`)
	}
	got := err.Error()
	for _, want := range []string{"kind", "run", "deploy", "ADR-0003"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q does not mention %q", got, want)
		}
	}
	var ve *validate.Error
	if !errors.As(err, &ve) {
		t.Fatal("checkCommandRouting's error is not a *validate.Error")
	}
}

// TestCheckCommandRoutingReportsBothOverridesAndCustom proves a violation
// in commands.custom and one in [commands] are both reported together, not
// only the first table checked.
func TestCheckCommandRoutingReportsBothOverridesAndCustom(t *testing.T) {
	m := &manifest.Manifest{
		Overrides: map[string]manifest.Command{
			"build": {Steps: []string{"noop"}, Raw: map[string]any{"targets": map[string]any{}}},
		},
		Custom: map[string]manifest.Command{
			"empty": {},
		},
	}
	err := checkCommandRouting(m)
	if err == nil {
		t.Fatal("expected an error")
	}
	got := err.Error()
	if !strings.Contains(got, "build") || !strings.Contains(got, "targets") {
		t.Fatalf("error %q is missing the overrides violation", got)
	}
	if !strings.Contains(got, "empty") || !strings.Contains(got, "steps") {
		t.Fatalf("error %q is missing the custom-command violation", got)
	}
}

// -- checkTargetDir -----------------------------------------------------------

func TestCheckTargetDirDefaultsToCwd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := checkTargetDir(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != wd {
		t.Fatalf("checkTargetDir(nil) = %q, want cwd %q", got, wd)
	}
}

func TestCheckTargetDirUsesTheGivenArg(t *testing.T) {
	got, err := checkTargetDir([]string{"/some/project"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/some/project" {
		t.Fatalf("checkTargetDir([DIR]) = %q, want %q", got, "/some/project")
	}
}

// -- end to end: admin check DIR reports every distinct problem -------------

// TestRunCheckReportsEveryDistinctProblem is the ticket's headline
// acceptance criterion end to end: a manifest with several unrelated
// violations (a reserved-verb collision, a pure-routing violation, a bad
// urls ref, and a non-bool footer) reports every one, each naming its key,
// in one `admin check` pass — not just the first found.
func TestRunCheckReportsEveryDistinctProblem(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[urls]
local = "http://localhost:8787"

[commands.custom.deploy]
desc = "deploy it"
steps = ["noop"]
kind = "shell"
run = "echo hi"

[commands.custom.worktree]
desc = "colliding name"
steps = ["noop"]

[actions.noop]
kind = "shell"
run = "true"
urls = ["nowhere", "local"]
footer = "true"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	m, ok, err := resolveProjectManifest(context.Background(), dir)
	if err != nil {
		t.Fatalf("resolveProjectManifest = %v, want nil", err)
	}
	if !ok {
		t.Fatal("resolveProjectManifest ok=false, want true")
	}

	var problems []error
	if err := checkReservedCollisions(m); err != nil {
		problems = append(problems, err)
	}
	if err := checkCommandRouting(m); err != nil {
		problems = append(problems, err)
	}
	if err := frame.ValidateURLRefs(m.Actions, m.Urls); err != nil {
		problems = append(problems, err)
	}
	if err := frame.ValidateFooterRefs(m.Actions); err != nil {
		problems = append(problems, err)
	}

	combined := errors.Join(problems...)
	if combined == nil {
		t.Fatal("expected problems, got none")
	}
	got := combined.Error()
	for _, want := range []string{
		"worktree", "ADR-0011",
		"deploy", "kind, run", "ADR-0003",
		"nowhere", "does not name an entry",
		"'footer' must be true or false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("combined error %q does not mention %q", got, want)
		}
	}
}

// TestRunCheckOnAValidManifestPasses is the ticket's acceptance criterion:
// "check on a valid manifest exits zero."
func TestRunCheckOnAValidManifestPasses(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[urls]
local = "http://localhost:8787"

[commands.custom.deploy]
desc = "deploy it"
steps = ["noop"]

[actions.noop]
kind = "shell"
run = "true"
urls = ["local"]
footer = true
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	m, ok, err := resolveProjectManifest(context.Background(), dir)
	if err != nil {
		t.Fatalf("resolveProjectManifest = %v, want nil", err)
	}
	if !ok {
		t.Fatal("resolveProjectManifest ok=false, want true")
	}
	if err := checkReservedCollisions(m); err != nil {
		t.Fatalf("checkReservedCollisions = %v, want nil", err)
	}
	if err := checkCommandRouting(m); err != nil {
		t.Fatalf("checkCommandRouting = %v, want nil", err)
	}
	if err := frame.ValidateURLRefs(m.Actions, m.Urls); err != nil {
		t.Fatalf("frame.ValidateURLRefs = %v, want nil", err)
	}
	if err := frame.ValidateFooterRefs(m.Actions); err != nil {
		t.Fatalf("frame.ValidateFooterRefs = %v, want nil", err)
	}
}
