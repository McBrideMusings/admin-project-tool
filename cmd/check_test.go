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
// cmd/'s own verbs (branch, show, populate), not only the five tool verbs.
func TestCheckReservedCollisionsRejectsCmdVerb(t *testing.T) {
	m := &manifest.Manifest{
		Overrides: map[string]manifest.Command{
			"show": {Steps: []string{"noop"}},
		},
	}
	err := checkReservedCollisions(m)
	if err == nil {
		t.Fatal("checkReservedCollisions(show) = nil, want an error naming \"show\"")
	}
	if !strings.Contains(err.Error(), "show") {
		t.Fatalf("error %q does not name the colliding command", err.Error())
	}
}

// TestCheckReservedCollisionsRejectsEveryRegisteredVerb proves the reserved
// set covers every verb registered on rootCmd, including ones no literal list
// named (archetypes, env) — a manifest command by either name would be
// shadowed at dispatch and never run.
func TestCheckReservedCollisionsRejectsEveryRegisteredVerb(t *testing.T) {
	for _, name := range []string{"archetypes", "env"} {
		m := &manifest.Manifest{
			Custom: map[string]manifest.Command{name: {Steps: []string{"noop"}}},
		}
		err := checkReservedCollisions(m)
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("checkReservedCollisions(%s) = %v, want an error naming %q", name, err, name)
		}
	}
}

// TestCheckReservedCollisionsIgnoresDispatchedManifestCommands proves a
// manifest command dispatchArgs has already registered on rootCmd does not
// become reserved against its own manifest.
func TestCheckReservedCollisionsIgnoresDispatchedManifestCommands(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeManifest(t, dir, "fy95-build")
	dispatchArgs([]string{"fy95-build"})

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{"fy95-build": {Steps: []string{"noop"}}},
	}
	if err := checkReservedCollisions(m); err != nil {
		t.Fatalf("checkReservedCollisions = %v, want nil for a dispatched manifest command", err)
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
// neither `over = "row"` nor `over = { query = ... }` fails check,
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

// TestRunCheckRejectsRemovedSelectionSource proves a manifest still saying
// `over = "selection"` fails check with a message that names the removed
// value and its replacement.
func TestRunCheckRejectsRemovedSelectionSource(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[commands.custom.implement]
desc = "implement"
steps = ["implement"]

[actions.implement]
kind = "workflow"
over = "selection"
run = "go $ADMIN_TARGET"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	m, ok, err := resolveProjectManifest(context.Background(), dir)
	if err != nil || !ok {
		t.Fatalf("resolveProjectManifest = ok %v, err %v", ok, err)
	}
	err = kinds.ValidateWorkflows(m.Actions)
	if err == nil {
		t.Fatal("kinds.ValidateWorkflows = nil, want an error for over = \"selection\"")
	}
	if got := err.Error(); !strings.Contains(got, "implement") || !strings.Contains(got, "no longer supported") {
		t.Fatalf("error %q does not name the action and say selection was removed", got)
	}
}

// TestRunCheckAcceptsBothWorkflowTargetSources proves a row-sourced
// and a query-sourced workflow both pass `admin check` and parse to the
// right source.
func TestRunCheckAcceptsBothWorkflowTargetSources(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[actions.retire-merged]
kind = "workflow"
over = "row"
run = "retire $ADMIN_TARGET"

[actions.review-waiting]
kind = "workflow"
over.query = "label:review-me"
run = "review $ADMIN_TARGET"
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
	if selTarget.Source != kinds.WorkflowSourceRow || selTarget.AppearsInCommandList() {
		t.Fatalf("retire-merged: got %+v, want row-sourced and absent from the Command list", selTarget)
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
		Trackers:   []string{"beads"},
		HasTracker: true,
		Actions: map[string]manifest.Action{
			"review-waiting": {"kind": "workflow", "over": map[string]any{"query": "label:review-me"}},
		},
	}
	if err := checkQueryWorkflowTracker(m); err != nil {
		t.Fatalf("checkQueryWorkflowTracker = %v, want nil (tracker = beads)", err)
	}
}

// TestCheckQueryWorkflowTrackerRowSourcedPasses proves a
// row-sourced workflow is never flagged, whatever the tracker: it
// never resolves a query at all.
func TestCheckQueryWorkflowTrackerRowSourcedPasses(t *testing.T) {
	m := &manifest.Manifest{
		Trackers:   []string{"github"},
		HasTracker: true,
		Actions: map[string]manifest.Action{
			"retire-merged": {"kind": "workflow", "over": "row"},
		},
	}
	if err := checkQueryWorkflowTracker(m); err != nil {
		t.Fatalf("checkQueryWorkflowTracker = %v, want nil (row-sourced)", err)
	}
}

// TestCheckQueryWorkflowTrackerRejectsGithubReadyQueryWorkflow is the
// ticket's headline acceptance criterion: a "ready"-query workflow declared
// in a project whose tracker is github fails, and the error names the
// action, the query, and the tracker so the manifest can be fixed from the
// message alone — "ready" has no GitHub equivalent (no dependency edges to
// derive it from), unlike "label:<name>", which admin-dgv9 gives github its
// own resolver for (see the passes test below).
func TestCheckQueryWorkflowTrackerRejectsGithubReadyQueryWorkflow(t *testing.T) {
	m := &manifest.Manifest{
		Trackers:   []string{"github"},
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
		Trackers:   []string{"github"},
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
		Trackers:   []string{"github"},
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

// TestGithubQueryClassificationAgreesAcrossCallSites is ROUND 2's structural
// guard: queryTrackerProblem (check time) and kinds.ResolveQuery (dispatch
// time) must never classify the same query string into different cases —
// that is exactly how 708afbe's dispatch-time "unrecognized" message came to
// advertise "ready" as supported when the case above it rejected it. Package
// cmd is the only package that can call both call sites, since
// resolveGitHubQuery's gh-backed seam is unexported inside package kinds.
//
// Every row here errors before any gh call (ready, empty-label,
// unrecognized), so this test runs offline; the one row that would reach
// listGitHubIssues (a real label) is asserted via kinds.ClassifyQuery plus
// queryTrackerProblem's "resolvable" verdict — the actual gh-backed
// resolution stays proven by workflowquery_test.go's stubbed test inside
// package kinds.
func TestGithubQueryClassificationAgreesAcrossCallSites(t *testing.T) {
	cases := []struct {
		query string
	}{
		{"ready"},
		{" ready "},
		{"label:"},
		{"label:review-me"},
		{"everything"},
		{""},
	}
	for _, c := range cases {
		checkProblem := queryTrackerProblem(c.query, "github")
		form, _ := kinds.ClassifyQuery(c.query)

		switch form {
		case kinds.QueryFormReady:
			if checkProblem == "" {
				t.Errorf("query %q: check time says resolvable, dispatch time classifies it QueryFormReady (beads-only)", c.query)
			}
			if !strings.Contains(checkProblem, "beads-only") {
				t.Errorf("query %q: check-time message %q does not say beads-only", c.query, checkProblem)
			}
		case kinds.QueryFormEmptyLabel:
			if checkProblem == "" {
				t.Errorf("query %q: check time says resolvable, dispatch time classifies it QueryFormEmptyLabel", c.query)
			}
		case kinds.QueryFormLabel:
			if checkProblem != "" {
				t.Errorf("query %q: check time says %q, dispatch time classifies it QueryFormLabel (resolvable)", c.query, checkProblem)
			}
		case kinds.QueryFormUnrecognized:
			if checkProblem == "" {
				t.Errorf("query %q: check time says resolvable, dispatch time classifies it QueryFormUnrecognized", c.query)
			}
			if !strings.Contains(checkProblem, "label:<name>") {
				t.Errorf("query %q: check-time message %q does not name label:<name>", c.query, checkProblem)
			}
			if strings.Contains(checkProblem, `"ready"`) {
				t.Errorf("query %q: check-time message %q must not advertise \"ready\" as supported", c.query, checkProblem)
			}
		}
	}
}

// TestCheckQueryWorkflowTrackerReportsEveryOffendingAction proves multiple
// query-sourced workflows the tracker cannot resolve are all reported, not
// just the first, and a resolvable label: query alongside them is left
// alone, as is a non-workflow action.
func TestCheckQueryWorkflowTrackerReportsEveryOffendingAction(t *testing.T) {
	m := &manifest.Manifest{
		Trackers:   []string{"github"},
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

// TestCheckQueryWorkflowTrackerRejectsUndeclaredOverTracker proves an
// over.tracker naming a tracker the manifest doesn't declare is a check
// error naming the offending value (admin-j00d).
func TestCheckQueryWorkflowTrackerRejectsUndeclaredOverTracker(t *testing.T) {
	m := &manifest.Manifest{
		Trackers:   []string{"beads"},
		HasTracker: true,
		Actions: map[string]manifest.Action{
			"review-waiting": {"kind": "workflow", "over": map[string]any{"query": "label:review-me", "tracker": "github"}},
		},
	}
	err := checkQueryWorkflowTracker(m)
	if err == nil {
		t.Fatal("checkQueryWorkflowTracker = nil, want an error for an undeclared over.tracker")
	}
	got := err.Error()
	for _, want := range []string{"review-waiting", "github", "beads"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q does not mention %q", got, want)
		}
	}
}

// TestCheckQueryWorkflowTrackerRejectsAmbiguousTwoDeclared proves that with
// two trackers declared and over.tracker omitted, check errors and names
// both declared trackers rather than guessing (admin-j00d) — there is no
// union-both option.
func TestCheckQueryWorkflowTrackerRejectsAmbiguousTwoDeclared(t *testing.T) {
	m := &manifest.Manifest{
		Trackers:   []string{"beads", "github"},
		HasTracker: true,
		Actions: map[string]manifest.Action{
			"review-waiting": {"kind": "workflow", "over": map[string]any{"query": "label:review-me"}},
		},
	}
	err := checkQueryWorkflowTracker(m)
	if err == nil {
		t.Fatal("checkQueryWorkflowTracker = nil, want an error for an omitted over.tracker with two trackers declared")
	}
	got := err.Error()
	for _, want := range []string{"review-waiting", "beads", "github"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q does not mention %q", got, want)
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

[status.local]
kind = "http"
url  = "http://localhost:8787"

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
	if err := frame.ValidateURLRefs(m.Actions, m.Status); err != nil {
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
		"nowhere", "does not name a [status.<label>] entry",
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

[status.local]
kind = "http"
url  = "http://localhost:8787"

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
	if err := frame.ValidateURLRefs(m.Actions, m.Status); err != nil {
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

// TestRunCheckRowKeysNonBindableRowKindFails: a row kind that is not
// bindable fails check, and the message lists the kinds that are.
func TestRunCheckRowKeysNonBindableRowKindFails(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[commands.custom.implement]
desc  = "implement it"
steps = ["noop"]

[keys.nonsense]
o = "implement"
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err == nil {
		t.Fatal("[keys.nonsense]: runCheck = nil, want an error")
	}
	msg := err.Error()
	for _, want := range []string{"bead", "issue", "branch", "pr", "commit", "stash", "command", "commandTarget", "worker"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not list bindable kind %q", msg, want)
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

// TestRunCheckAcceptsAWellFormedTerminalTable is admin-ysxr.1's first
// acceptance criterion: a manifest declaring a well-formed [terminal] table
// (ADR-0020) passes `admin check`.
func TestRunCheckAcceptsAWellFormedTerminalTable(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[terminal]
spawn  = 'id=$(herdr pane split --current --cwd "$ADMIN_CWD" --no-focus) && herdr pane run "$id" "$ADMIN_CMD" && echo "$id"'
attach = 'p=$(herdr pane get "$ADMIN_HANDLE") && herdr workspace focus "$(printf %s "$p" | jq -r .result.pane.workspace_id)" >/dev/null && herdr tab focus "$(printf %s "$p" | jq -r .result.pane.tab_id)" >/dev/null'
kill   = 'herdr pane close "$ADMIN_HANDLE"'
status = 'herdr agent get "$ADMIN_HANDLE"'
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err != nil {
		t.Fatalf("runCheck = %v, want nil", err)
	}
}

// TestRunCheckRejectsATerminalTableMissingAttach is admin-ysxr.1's second
// acceptance criterion: a [terminal] table missing `attach` fails `admin
// check`, and the message names the missing key.
func TestRunCheckRejectsATerminalTableMissingAttach(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[terminal]
spawn = 'id=$(herdr pane split --current --cwd "$ADMIN_CWD" --no-focus) && herdr pane run "$id" "$ADMIN_CMD" && echo "$id"'
kill  = 'herdr pane close "$ADMIN_HANDLE"'
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err == nil {
		t.Fatal("runCheck = nil, want an error naming the missing 'attach' key")
	}
	if !strings.Contains(err.Error(), "attach") {
		t.Fatalf("error %q does not name the missing key 'attach'", err.Error())
	}
}

// TestRunCheckAcceptsTheDogfoodDeclaration is admin-7wy5's acceptance
// criterion 1, carried here because the real declaration lives only in this
// repo's own untracked admin.toml (see CLAUDE.md's "admin.toml is NOT
// committed" section) — nothing else makes it travel with the commit. This
// writes the exact shape admin-7wy5 adds to that file: [commands.implement]
// routing to a kind="workflow" action with over="row" and run/done
// set on the action itself, plus
// [keys.bead] enter = "implement" — and asserts the whole thing passes
// `admin check`, with the expected enter-shadow line reported on stderr but
// never contributing to the exit code (validate/keys.go's shadow contract,
// exercised end-to-end here rather than only at the validate.RowKeys unit
// level).
func TestRunCheckAcceptsTheDogfoodDeclaration(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[commands.implement]
desc     = "Dispatch an agent over the selected bead(s) (ADR-0018 dogfood)"
steps    = ["implement"]
group    = 2
priority = 60

[actions.implement]
kind  = "workflow"
desc  = "Dispatch a claude agent over the selected bead(s)"
over = "row"
run   = "claude '/implement $ADMIN_TARGET auto'"
done  = 'bd show "$ADMIN_TARGET" --json | jq -e ".[0].status == \"closed\""'

[keys.bead]
enter = "implement"
`)
	err, out := runCheckCapturingStderr(t, dir)
	if err != nil {
		t.Fatalf("runCheck = %v, want nil (the dogfood declaration is valid)", err)
	}
	if !strings.Contains(out, "keys.bead.enter") || !strings.Contains(out, "shadows") {
		t.Fatalf("stderr %q does not report the enter shadow", out)
	}

	// checkRowKeys itself: the shadow is reported, never an error.
	m, ok, err := resolveProjectManifest(context.Background(), dir)
	if err != nil {
		t.Fatalf("resolveProjectManifest = %v, want nil", err)
	}
	if !ok {
		t.Fatal("resolveProjectManifest ok=false, want true")
	}
	shadows, rkErr := checkRowKeys(m)
	if rkErr != nil {
		t.Fatalf("checkRowKeys = %v, want nil", rkErr)
	}
	found := false
	for _, s := range shadows {
		if strings.Contains(s, "keys.bead.enter") && strings.Contains(s, "shadows") {
			found = true
		}
	}
	if !found {
		t.Fatalf("checkRowKeys shadows = %v, want one naming keys.bead.enter", shadows)
	}

	// The action itself resolves to a row-sourced workflow carrying its
	// own run string.
	target, err := kinds.ParseWorkflowOver("implement", m.Actions["implement"])
	if err != nil {
		t.Fatalf("ParseWorkflowOver(implement) = %v, want nil", err)
	}
	if target.Source != kinds.WorkflowSourceRow {
		t.Fatalf("implement: Source = %v, want WorkflowSourceRow", target.Source)
	}
	if run := kinds.ActionRun(m.Actions["implement"]); !strings.Contains(run, "/implement $ADMIN_TARGET auto") {
		t.Fatalf("implement action's run = %q, want the authored command", run)
	}
}

// TestRunCheckRejectsWorkflowWithoutRun: a workflow action declaring no run
// fails check, naming the action.
func TestRunCheckRejectsWorkflowWithoutRun(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[actions.implement]
kind = "workflow"
over = "row"
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err == nil || !strings.Contains(err.Error(), `action "implement"`) || !strings.Contains(err.Error(), "'run'") {
		t.Fatalf("runCheck = %v, want an error naming the action and 'run'", err)
	}
}

// TestRunCheckRejectsRemovedWorkflowAgentKey: agent on a workflow action is
// rejected, naming the key.
func TestRunCheckRejectsRemovedWorkflowAgentKey(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[actions.implement]
kind  = "workflow"
over = "row"
run   = "claude"
agent = "claude"
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err == nil || !strings.Contains(err.Error(), "'agent' is not a workflow key") {
		t.Fatalf("runCheck = %v, want an error naming the agent key", err)
	}
}

// TestRunCheckPreKillRefUnknownFails is admin-tlhw.3's acceptance criterion
// "admin check reports a declared reference that names nothing": a
// [clean_dev].pre_kill naming a command/action the manifest never declares
// must fail check, naming both the table and the missing reference.
func TestRunCheckPreKillRefUnknownFails(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[clean_dev]
pre_kill = "does-not-exist"
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err == nil {
		t.Fatal("runCheck = nil, want an error: pre_kill references an undeclared command/action")
	}
	got := err.Error()
	for _, want := range []string{"clean_dev", "pre_kill", `"does-not-exist"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("runCheck error %q does not mention %q", got, want)
		}
	}
}

// TestRunCheckPreKillRefResolvingCommandPasses is the sibling acceptance
// criterion: a project can still declare something to run before a dev loop
// starts, by naming a real command in `pre_kill`.
func TestRunCheckPreKillRefResolvingCommandPasses(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[clean_dev]
pre_kill = "kill"

[commands.custom.kill]
desc = "kill stray dev processes"
steps = ["kill-step"]

[actions.kill-step]
kind = "shell"
run = "true"
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err != nil {
		t.Fatalf("runCheck = %v, want nil — pre_kill names a declared command", err)
	}
}

// TestRunCheckPreKillRefResolvingActionPasses covers the other declared-name
// shape: pre_kill naming a bare [actions.X] rather than a [commands.X].
func TestRunCheckPreKillRefResolvingActionPasses(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[vite_dev]
pre_kill = "stop-stale"

[actions.stop-stale]
kind = "shell"
run = "true"
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err != nil {
		t.Fatalf("runCheck = %v, want nil — pre_kill names a declared action", err)
	}
}

// TestRunCheckPreKillRefNonStringFails: pre_kill must be a string naming a
// reference now that a bare bool no longer means "look for a command called
// kill" (admin-tlhw.3).
func TestRunCheckPreKillRefNonStringFails(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[docker_dev]
pre_kill = true
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err == nil {
		t.Fatal("runCheck = nil, want an error: pre_kill = true is no longer a valid declaration")
	}
	if !strings.Contains(err.Error(), "docker_dev") || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("runCheck error %q does not report docker_dev.pre_kill's type", err.Error())
	}
}

// TestRunCheckCommandPreflightFalsePasses proves a command can declare
// itself exempt from the global preflight without the tool matching its
// name against a hardcoded list (admin-tlhw.3).
func TestRunCheckCommandPreflightFalsePasses(t *testing.T) {
	dir := writeCheckManifest(t, `project_name = "checktest"

[commands.custom.logs]
desc      = "tail logs"
steps     = ["noop"]
preflight = false

[actions.noop]
kind = "shell"
run = "true"
`)
	err, _ := runCheckCapturingStderr(t, dir)
	if err != nil {
		t.Fatalf("runCheck = %v, want nil — preflight = false is a valid declaration", err)
	}
}

// checkStatus reports a pull expression that does not compile and a show
// placeholder no pull entry produces, every one in a single pass.
func TestCheckStatusReportsBadExpressionAndUnknownPlaceholder(t *testing.T) {
	dir := t.TempDir()
	toml := `project_name = "checktest"

[status.jobs]
kind = "json"
url  = "http://localhost:1/api"
pull = { n = "length([?status==" }
show = "{n} and {missing}"

[status.ok]
kind = "json"
url  = "http://localhost:1/api"
pull = { n = "length(@)" }
show = "{n}"
`
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	m, ok, err := resolveProjectManifest(context.Background(), dir)
	if err != nil || !ok {
		t.Fatalf("resolveProjectManifest = %v, %v", ok, err)
	}
	err = checkStatus(m)
	if err == nil {
		t.Fatal("checkStatus = nil, want errors")
	}
	got := err.Error()
	for _, want := range []string{"[status.jobs] pull.n", "{missing}"} {
		if !strings.Contains(got, want) {
			t.Errorf("error %q does not mention %q", got, want)
		}
	}
	if strings.Contains(got, "[status.ok]") {
		t.Errorf("error %q blames the valid entry", got)
	}
}
