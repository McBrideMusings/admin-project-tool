package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
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

// -- checkUnknownKinds -------------------------------------------------------

// TestCheckUnknownKindsCleanManifest proves a manifest with only valid kinds
// passes.
func TestCheckUnknownKindsCleanManifest(t *testing.T) {
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"build": {"kind": "shell", "run": "echo build"},
			"test":  {"kind": "npm", "run": "npm test"},
		},
	}
	if err := checkUnknownKinds(m); err != nil {
		t.Fatalf("checkUnknownKinds = %v, want nil", err)
	}
}

// TestCheckUnknownKindsRejectsUnknownKind is the ticket's acceptance
// criterion: an action with an undeclared kind fails check, and the error
// names both the action and the unknown kind.
//
// MUTATION EVIDENCE: with checkUnknownKinds' condition flipped from
// `if !declared` to `if declared`, this test fails with:
//
//	checkUnknownKinds = <nil>, want an error naming "nosuchkind"
func TestCheckUnknownKindsRejectsUnknownKind(t *testing.T) {
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"build": {"kind": "nosuchkind", "run": "echo build"},
		},
	}
	err := checkUnknownKinds(m)
	if err == nil {
		t.Fatal("checkUnknownKinds(nosuchkind) = nil, want an error naming the action and kind")
	}
	got := err.Error()
	if !strings.Contains(got, "build") {
		t.Fatalf("error %q does not name the action", got)
	}
	if !strings.Contains(got, "nosuchkind") {
		t.Fatalf("error %q does not name the unknown kind", got)
	}
}

// TestCheckUnknownKindsDoesNotFlagValidKind proves a valid kind is not
// flagged even when an unknown kind is present in the same manifest — the
// false-positive guard. Multiple actions are tested: one unknown, one valid.
func TestCheckUnknownKindsDoesNotFlagValidKind(t *testing.T) {
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"build": {"kind": "shell", "run": "echo build"},
			"test":  {"kind": "nosuchkind", "run": "echo test"},
		},
	}
	err := checkUnknownKinds(m)
	if err == nil {
		t.Fatal("checkUnknownKinds should report the unknown kind")
	}
	got := err.Error()
	// The error must name the unknown action.
	if !strings.Contains(got, "test") || !strings.Contains(got, "nosuchkind") {
		t.Fatalf("error %q does not name the unknown action or kind", got)
	}
	// The error must NOT name the valid action.
	if strings.Contains(got, "build") {
		t.Fatalf("error %q incorrectly flags the valid kind", got)
	}
}

// TestCheckUnknownKindsReportsMultipleUnknownKinds proves multiple unknown
// kinds are all reported, not just the first, consistent with the
// errors.Join pattern the other checks use.
func TestCheckUnknownKindsReportsMultipleUnknownKinds(t *testing.T) {
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"build":  {"kind": "nosuchkind", "run": "echo build"},
			"deploy": {"kind": "another-fake-kind", "run": "echo deploy"},
			"test":   {"kind": "shell", "run": "npm test"},
		},
	}
	err := checkUnknownKinds(m)
	if err == nil {
		t.Fatal("checkUnknownKinds should report unknown kinds")
	}
	got := err.Error()
	// Both unknown actions and kinds must be named.
	for _, want := range []string{"build", "nosuchkind", "deploy", "another-fake-kind"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q does not name %q", got, want)
		}
	}
	// The valid action must NOT be named.
	if strings.Contains(got, "test") && !strings.Contains(got, "another-fake-kind") {
		// Note: only fail if "test" appears without being part of "another-fake-kind"
		// A simple check: "test" should not appear as a standalone action name
		lines := strings.Split(got, "\n")
		for _, line := range lines {
			if strings.Contains(line, "action") && strings.Contains(line, "test") {
				t.Fatalf("error %q incorrectly flags the valid action", got)
			}
		}
	}
}

// -- kinds.ValidateWorkflows, wired into runCheck ---------------------------

// TestRunCheckRejectsAWorkflowWithNoTargetSource is admin-3rgp.24's
// acceptance criterion at the `admin check` level: a workflow declaring
// neither `over = "selection"` nor `over = { query = ... }` fails check,
// naming the missing key — caught here rather than surfacing only at
// dispatch.
func TestRunCheckRejectsAWorkflowWithNoTargetSource(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[commands.custom.retire-merged]
desc = "retire merged worktrees"
steps = ["retire-merged"]

[actions.retire-merged]
kind = "workflow"
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
	err = kinds.ValidateWorkflows(m.Actions)
	if err == nil {
		t.Fatal("kinds.ValidateWorkflows = nil, want an error naming the missing 'over' key")
	}
	got := err.Error()
	if !strings.Contains(got, "retire-merged") || !strings.Contains(got, "over") {
		t.Fatalf("error %q does not name the action and the missing key", got)
	}
}

// TestRunCheckAcceptsBothWorkflowTargetSources proves a selection-sourced
// and a query-sourced workflow both pass `admin check` and parse to the
// right source.
func TestRunCheckAcceptsBothWorkflowTargetSources(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[actions.retire-merged]
kind = "workflow"
over = "selection"

[actions.review-waiting]
kind = "workflow"
over.query = "label:review-me"
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
	if err := kinds.ValidateWorkflows(m.Actions); err != nil {
		t.Fatalf("kinds.ValidateWorkflows = %v, want nil", err)
	}

	selTarget, err := kinds.ParseWorkflowOver("retire-merged", m.Actions["retire-merged"])
	if err != nil {
		t.Fatalf("ParseWorkflowOver(retire-merged) = %v, want nil", err)
	}
	if selTarget.Source != kinds.WorkflowSourceSelection || selTarget.AppearsInCommandList() {
		t.Fatalf("retire-merged: got %+v, want selection-sourced and absent from the Command list", selTarget)
	}

	qTarget, err := kinds.ParseWorkflowOver("review-waiting", m.Actions["review-waiting"])
	if err != nil {
		t.Fatalf("ParseWorkflowOver(review-waiting) = %v, want nil", err)
	}
	if qTarget.Source != kinds.WorkflowSourceQuery || !qTarget.AppearsInCommandList() || qTarget.Query != "label:review-me" {
		t.Fatalf("review-waiting: got %+v, want query-sourced, present in the Command list, query %q", qTarget, "label:review-me")
	}
}

// -- checkQueryWorkflowTracker (admin-3rgp.53) ------------------------------

// TestCheckQueryWorkflowTrackerNoTrackerDeclaredPasses proves a project that
// declares no tracker at all is untouched by this check — HasTracker false
// must not be conflated with "declared non-beads".
func TestCheckQueryWorkflowTrackerNoTrackerDeclaredPasses(t *testing.T) {
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"review-waiting": {"kind": "workflow", "over": map[string]any{"query": "label:review-me"}},
		},
	}
	if err := checkQueryWorkflowTracker(m); err != nil {
		t.Fatalf("checkQueryWorkflowTracker = %v, want nil (no tracker declared)", err)
	}
}

// TestCheckQueryWorkflowTrackerBeadsPasses proves an explicitly-declared
// beads tracker also passes — ResolveQuery's own resolver.
func TestCheckQueryWorkflowTrackerBeadsPasses(t *testing.T) {
	m := &manifest.Manifest{
		Tracker:    "beads",
		HasTracker: true,
		Actions: map[string]manifest.Action{
			"review-waiting": {"kind": "workflow", "over": map[string]any{"query": "label:review-me"}},
		},
	}
	if err := checkQueryWorkflowTracker(m); err != nil {
		t.Fatalf("checkQueryWorkflowTracker = %v, want nil (tracker = beads)", err)
	}
}

// TestCheckQueryWorkflowTrackerSelectionSourcedPasses proves a
// selection-sourced workflow is never flagged, whatever the tracker: it
// never resolves a query at all.
func TestCheckQueryWorkflowTrackerSelectionSourcedPasses(t *testing.T) {
	m := &manifest.Manifest{
		Tracker:    "github",
		HasTracker: true,
		Actions: map[string]manifest.Action{
			"retire-merged": {"kind": "workflow", "over": "selection"},
		},
	}
	if err := checkQueryWorkflowTracker(m); err != nil {
		t.Fatalf("checkQueryWorkflowTracker = %v, want nil (selection-sourced)", err)
	}
}

// TestCheckQueryWorkflowTrackerRejectsGithubReadyQueryWorkflow is the
// ticket's headline acceptance criterion: a "ready"-query workflow declared
// in a project whose tracker is github fails, and the error names the
// action, the query, and the tracker so the manifest can be fixed from the
// message alone — "ready" has no GitHub equivalent (no dependency edges to
// derive it from), unlike "label:<name>", which admin-dgv9 gives github its
// own resolver for (see the passes test below).
//
// MUTATION EVIDENCE: with the `m.Tracker == "beads"` early-return condition
// inverted (so it fires when tracker is NOT beads instead), this test
// failed with:
//
//	checkQueryWorkflowTracker = <nil>, want an error naming the action, query, and tracker
//
// (reverted after confirming the failure).
func TestCheckQueryWorkflowTrackerRejectsGithubReadyQueryWorkflow(t *testing.T) {
	m := &manifest.Manifest{
		Tracker:    "github",
		HasTracker: true,
		Actions: map[string]manifest.Action{
			"ready-work": {"kind": "workflow", "over": map[string]any{"query": "ready"}},
		},
	}
	err := checkQueryWorkflowTracker(m)
	if err == nil {
		t.Fatal("checkQueryWorkflowTracker = nil, want an error naming the action, query, and tracker")
	}
	got := err.Error()
	for _, want := range []string{"ready-work", "ready", "github"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q does not mention %q", got, want)
		}
	}
	var ve *validate.Error
	if !errors.As(err, &ve) {
		t.Fatal("checkQueryWorkflowTracker's error is not a *validate.Error")
	}
}

// TestCheckQueryWorkflowTrackerGithubLabelQueryPasses proves
// admin-dgv9's acceptance criterion directly: a github-tracked project
// declaring `over = { query = "label:<name>" }` now passes check, since
// ResolveQuery can actually resolve it via gh.OpenIssuesByLabel.
func TestCheckQueryWorkflowTrackerGithubLabelQueryPasses(t *testing.T) {
	m := &manifest.Manifest{
		Tracker:    "github",
		HasTracker: true,
		Actions: map[string]manifest.Action{
			"review-waiting": {"kind": "workflow", "over": map[string]any{"query": "label:review-me"}},
		},
	}
	if err := checkQueryWorkflowTracker(m); err != nil {
		t.Fatalf("checkQueryWorkflowTracker = %v, want nil (github resolves label: queries)", err)
	}
}

// TestCheckQueryWorkflowTrackerGithubEmptyLabelFails proves "label:" with no
// name after the colon still fails on github — it is not a real query
// gh.OpenIssuesByLabel could ever resolve, unlike a real label name.
func TestCheckQueryWorkflowTrackerGithubEmptyLabelFails(t *testing.T) {
	m := &manifest.Manifest{
		Tracker:    "github",
		HasTracker: true,
		Actions: map[string]manifest.Action{
			"broken": {"kind": "workflow", "over": map[string]any{"query": "label:"}},
		},
	}
	err := checkQueryWorkflowTracker(m)
	if err == nil {
		t.Fatal("checkQueryWorkflowTracker = nil, want an error for an empty label")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error %q does not name the action", err.Error())
	}
}

// TestCheckQueryWorkflowTrackerReportsEveryOffendingAction proves multiple
// query-sourced workflows the tracker cannot resolve are all reported, not
// just the first, and a resolvable label: query alongside them is left
// alone, as is a non-workflow action.
func TestCheckQueryWorkflowTrackerReportsEveryOffendingAction(t *testing.T) {
	m := &manifest.Manifest{
		Tracker:    "github",
		HasTracker: true,
		Actions: map[string]manifest.Action{
			"resolvable": {"kind": "workflow", "over": map[string]any{"query": "label:review-me"}},
			"ready-work": {"kind": "workflow", "over": map[string]any{"query": "ready"}},
			"unknown-fm": {"kind": "workflow", "over": map[string]any{"query": "everything"}},
			"build":      {"kind": "shell", "run": "echo build"},
		},
	}
	err := checkQueryWorkflowTracker(m)
	if err == nil {
		t.Fatal("expected an error")
	}
	got := err.Error()
	for _, want := range []string{"ready-work", "unknown-fm"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q does not name %q", got, want)
		}
	}
	for _, notWant := range []string{"resolvable", "build"} {
		if strings.Contains(got, notWant) {
			t.Fatalf("error %q incorrectly flags %q", got, notWant)
		}
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

// -- checkAntiPatterns (admin-3rgp.9, ADR-0009) -----------------------------

// TestCheckAntiPatternsFlagsAMultilineRun is the ticket's first acceptance
// criterion: a project-declared action whose run is a multi-line string is
// reported, naming the action and the ADR.
//
// MUTATION EVIDENCE: with the `strings.Contains(ref.run, "\n")` check in
// checkAntiPatterns replaced by `false` (simulating the rule being silently
// dropped), this test fails with:
//
//	checkAntiPatterns found 0 anti-patterns, want 1 naming "deploy" and rule "multiline-run"
func TestCheckAntiPatternsFlagsAMultilineRun(t *testing.T) {
	dir := t.TempDir()
	toml := "project_name = \"checktest\"\n\n" +
		"[actions.deploy]\n" +
		"kind = \"shell\"\n" +
		"run = \"\"\"\n" +
		"set -euo pipefail\n" +
		"echo one\n" +
		"echo two\n" +
		"\"\"\"\n"
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	project, err := manifest.Load(dir)
	if err != nil {
		t.Fatalf("manifest.Load = %v, want nil", err)
	}
	found := checkAntiPatterns(project)
	if len(found) != 1 || found[0].Action != "deploy" || found[0].Rule != AntiPatternMultilineRun {
		t.Fatalf("checkAntiPatterns = %+v, want 1 naming \"deploy\" and rule %q", found, AntiPatternMultilineRun)
	}
	if !strings.Contains(found[0].Message, "deploy") || !strings.Contains(found[0].Message, "ADR-0009") {
		t.Fatalf("message %q does not name the action and the ADR", found[0].Message)
	}
}

// TestCheckAntiPatternsFlagsASidecarScript is the ticket's second acceptance
// criterion: a project-declared action whose run invokes a script the
// project keeps is reported, naming the action, the script, and the ADR.
//
// MUTATION EVIDENCE: with sidecarScriptRe replaced by a pattern that never
// matches (`regexp.MustCompile("$^")`), this test fails with:
//
//	checkAntiPatterns found 0 anti-patterns, want 1 naming "deploy" and rule "sidecar-script"
func TestCheckAntiPatternsFlagsASidecarScript(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[actions.deploy]
kind = "shell"
run  = "./tools/dev-run.sh"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	project, err := manifest.Load(dir)
	if err != nil {
		t.Fatalf("manifest.Load = %v, want nil", err)
	}
	found := checkAntiPatterns(project)
	if len(found) != 1 || found[0].Action != "deploy" || found[0].Rule != AntiPatternSidecarScript {
		t.Fatalf("checkAntiPatterns = %+v, want 1 naming \"deploy\" and rule %q", found, AntiPatternSidecarScript)
	}
	if !strings.Contains(found[0].Message, "./tools/dev-run.sh") {
		t.Fatalf("message %q does not name the script", found[0].Message)
	}
}

// TestCheckAntiPatternsCleanManifestReportsNothing is the ticket's third
// acceptance criterion: a manifest with neither anti-pattern reports
// nothing — a single-line run naming no script.
func TestCheckAntiPatternsCleanManifestReportsNothing(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[actions.deploy]
kind = "shell"
run  = "bun run deploy"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	project, err := manifest.Load(dir)
	if err != nil {
		t.Fatalf("manifest.Load = %v, want nil", err)
	}
	if found := checkAntiPatterns(project); len(found) != 0 {
		t.Fatalf("checkAntiPatterns = %+v, want none", found)
	}
}

// TestCheckAntiPatternsIgnoresLuaKind proves kind="lua" is out of this
// ticket's scope: ADR-0010's makeLua comment is explicit that a lua body's
// `run` is always inline source, never a script-path reference, so neither
// ADR-0009 rule fires on one — even a multi-line body, which is simply what
// a lua action always looks like.
func TestCheckAntiPatternsIgnoresLuaKind(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[actions.deploy]
kind = "lua"
run  = "info('a')\ninfo('b')\n"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	project, err := manifest.Load(dir)
	if err != nil {
		t.Fatalf("manifest.Load = %v, want nil", err)
	}
	if found := checkAntiPatterns(project); len(found) != 0 {
		t.Fatalf("checkAntiPatterns = %+v, want none (kind=\"lua\" run is always inline source, out of this ticket's scope)", found)
	}
}

// TestCheckAntiPatternsFlagsAMultiTargetTarget proves the check descends
// into an inline multi-target subtable's own `run`, reporting it under its
// dotted path ("dev.cf") rather than missing it because the parent action
// has no top-level `run` of its own.
func TestCheckAntiPatternsFlagsAMultiTargetTarget(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[actions.dev]
kind = "multi-target"

[actions.dev.cf]
run = "./tools/dev-run.sh"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	project, err := manifest.Load(dir)
	if err != nil {
		t.Fatalf("manifest.Load = %v, want nil", err)
	}
	found := checkAntiPatterns(project)
	if len(found) != 1 || found[0].Action != "dev.cf" || found[0].Rule != AntiPatternSidecarScript {
		t.Fatalf("checkAntiPatterns = %+v, want 1 naming \"dev.cf\" and rule %q", found, AntiPatternSidecarScript)
	}
}

// TestRunCheckReportsAntiPatternsAndExitsZero is the ticket's fourth
// acceptance criterion end to end: `admin check` on a manifest with an
// ADR-0009 anti-pattern still exits zero, and the finding reaches stderr.
func TestRunCheckReportsAntiPatternsAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[commands.custom.deploy]
desc  = "deploy"
steps = ["deploy"]

[actions.deploy]
kind = "shell"
run  = "./tools/dev-run.sh"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	err := runCheck(cmd, []string{dir})
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	buf.ReadFrom(r)

	if err != nil {
		t.Fatalf("runCheck = %v, want nil (an ADR-0009 anti-pattern reports, it does not fail)", err)
	}
	out := buf.String()
	if !strings.Contains(out, "deploy") || !strings.Contains(out, "ADR-0009") {
		t.Fatalf("runCheck stderr %q does not report the anti-pattern", out)
	}
}

// -- checkRowKeys / [keys.<row-kind>] (admin-l6kb.1, ADR-0018) -------------

// runCheckCapturingStderr runs runCheck against dir and returns its error
// plus everything it wrote to stderr, mirroring
// TestRunCheckReportsAntiPatternsAndExitsZero's capture technique.
func runCheckCapturingStderr(t *testing.T, dir string) (error, string) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	err := runCheck(cmd, []string{dir})
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return err, buf.String()
}

func writeCheckManifest(t *testing.T, toml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestRunCheckRowKeysValidBindingPasses is the ticket's first acceptance
// criterion: a manifest with [keys.bead] enter = "implement", where
// [commands.implement] exists, passes admin check.
func TestRunCheckRowKeysValidBindingPasses(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[commands.custom.implement]
desc  = "implement it"
steps = ["noop"]

[keys.bead]
enter = "implement"
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err != nil {
		t.Fatalf("runCheck = %v, want nil", err)
	}
}

// TestRunCheckRowKeysMissingCommandFails is the ticket's second acceptance
// criterion: naming a command that does not exist fails check, and the
// message names both the binding and the missing command.
func TestRunCheckRowKeysMissingCommandFails(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[keys.bead]
o = "does-not-exist"
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err == nil {
		t.Fatal("runCheck = nil, want an error naming the missing command")
	}
	msg := err.Error()
	if !strings.Contains(msg, "keys.bead.o") || !strings.Contains(msg, "does-not-exist") {
		t.Fatalf("error %q does not name both the binding and the missing command", msg)
	}
}

// TestRunCheckRowKeysReservedKeyFails is the ticket's third acceptance
// criterion: a reserved key fails check, and the message says the key is
// reserved.
func TestRunCheckRowKeysReservedKeyFails(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[commands.custom.implement]
desc  = "implement it"
steps = ["noop"]

[keys.bead]
q = "implement"
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err == nil {
		t.Fatal("runCheck = nil, want an error naming the reserved key")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("error %q does not say the key is reserved", err.Error())
	}
}

// TestRunCheckRowKeysNonBindableRowKindFails is the ticket's fourth
// acceptance criterion: a row kind that is not bindable — command,
// commandTarget, worker, or an unknown word — fails check, and the message
// lists the kinds that are.
func TestRunCheckRowKeysNonBindableRowKindFails(t *testing.T) {
	for _, kind := range []string{"command", "commandTarget", "worker", "nonsense"} {
		dir := writeCheckManifest(t, `project_name = "checktest"

[commands.custom.implement]
desc  = "implement it"
steps = ["noop"]

[keys.`+kind+`]
o = "implement"
`)
		err, _ := runCheckCapturingStderr(t, dir)
		if err == nil {
			t.Fatalf("[keys.%s]: runCheck = nil, want an error", kind)
		}
		msg := err.Error()
		for _, want := range []string{"bead", "issue", "branch", "pr", "commit", "stash"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("[keys.%s]: error %q does not list bindable kind %q", kind, msg, want)
			}
		}
	}
}

// TestRunCheckRowKeysShadowPassesAndPrints is the ticket's fifth acceptance
// criterion: a binding whose key already carries a built-in meaning for
// that row kind passes, and check prints a shadow line naming what it
// displaces.
func TestRunCheckRowKeysShadowPassesAndPrints(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[commands.custom.implement]
desc  = "implement it"
steps = ["noop"]

[keys.bead]
d = "implement"
`)
	err, out := runCheckCapturingStderr(t, dir)
	if err != nil {
		t.Fatalf("runCheck = %v, want nil — a shadow passes", err)
	}
	if !strings.Contains(out, "keys.bead.d") || !strings.Contains(out, "shadows") || !strings.Contains(out, "detail") {
		t.Fatalf("stderr %q does not report the shadow naming what it displaces", out)
	}
}

// TestRunCheckRowKeysTwoProblemsBothReported is the ticket's sixth
// acceptance criterion: two distinct problems in one manifest are both
// reported by a single admin check run.
func TestRunCheckRowKeysTwoProblemsBothReported(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[commands.custom.implement]
desc  = "implement it"
steps = ["noop"]

[keys.bead]
q = "implement"

[keys.issue]
o = "does-not-exist"
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err == nil {
		t.Fatal("runCheck = nil, want an error naming both problems")
	}
	msg := err.Error()
	if !strings.Contains(msg, "keys.bead.q") {
		t.Fatalf("error %q missing the reserved-key problem", msg)
	}
	if !strings.Contains(msg, "keys.issue.o") || !strings.Contains(msg, "does-not-exist") {
		t.Fatalf("error %q missing the missing-command problem", msg)
	}
}

// TestRunCheckRowKeysNoTableUnaffected is the ticket's seventh acceptance
// criterion: a manifest with no [keys] table at all is unaffected in every
// respect.
func TestRunCheckRowKeysNoTableUnaffected(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[commands.custom.deploy]
desc  = "deploy it"
steps = ["noop"]
`)
	err, out := runCheckCapturingStderr(t, dir)
	if err != nil {
		t.Fatalf("runCheck = %v, want nil", err)
	}
	if !strings.Contains(out, "admin.toml: valid") {
		t.Fatalf("stderr %q does not report a clean pass", out)
	}
}
