package kinds

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// -- ParseWorkflowOver --------------------------------------------------

// TestParseWorkflowOverSelection is the ticket's first acceptance
// criterion: a selection-sourced workflow parses.
func TestParseWorkflowOverSelection(t *testing.T) {
	action := manifest.Action{"kind": "workflow", "over": "selection"}
	target, err := ParseWorkflowOver("retire-merged", action)
	if err != nil {
		t.Fatalf("ParseWorkflowOver = %v, want nil", err)
	}
	if target.Source != WorkflowSourceSelection {
		t.Fatalf("Source = %v, want WorkflowSourceSelection", target.Source)
	}
	if target.AppearsInCommandList() {
		t.Fatal("a selection-sourced workflow must NOT appear in the Command list")
	}
}

// TestParseWorkflowOverQuery is the ticket's second acceptance criterion: a
// query-sourced workflow parses and carries its query through.
func TestParseWorkflowOverQuery(t *testing.T) {
	action := manifest.Action{"kind": "workflow", "over": map[string]any{"query": "label:review-me"}}
	target, err := ParseWorkflowOver("review-waiting", action)
	if err != nil {
		t.Fatalf("ParseWorkflowOver = %v, want nil", err)
	}
	if target.Source != WorkflowSourceQuery {
		t.Fatalf("Source = %v, want WorkflowSourceQuery", target.Source)
	}
	if target.Query != "label:review-me" {
		t.Fatalf("Query = %q, want %q", target.Query, "label:review-me")
	}
	if !target.AppearsInCommandList() {
		t.Fatal("a query-sourced workflow must appear in the Command list")
	}
}

// TestParseWorkflowOverMissingNamesTheKey is the ticket's third acceptance
// criterion: a workflow with no target source fails, naming the missing
// key.
//
// MUTATION EVIDENCE: with the `if !present` branch in ParseWorkflowOver
// deleted (falling through to the `default` case of the type switch, since
// a nil `any` matches neither `string` nor `map[string]any`), this test
// failed with:
//
//	action "no-over": 'over' must be "selection" or a table with 'query' (got <nil>)
//
// — which still errors (the test only checks for an error and for "over" in
// the message) but no longer names the key as MISSING versus malformed,
// which is the distinction this test is pinned to catch: re-running with
// the present check restored, the assertion on "needs 'over'" is what
// actually discriminates the two paths, and it failed under the mutation
// with:
//
//	error "action \"no-over\": 'over' must be \"selection\" or a table with 'query' (got <nil>)" does not mention "needs 'over'"
//
// (reverted after confirming the failure).
func TestParseWorkflowOverMissingNamesTheKey(t *testing.T) {
	action := manifest.Action{"kind": "workflow"}
	_, err := ParseWorkflowOver("no-over", action)
	if err == nil {
		t.Fatal("ParseWorkflowOver = nil, want an error naming the missing key")
	}
	if !strings.Contains(err.Error(), "over") {
		t.Fatalf("error %q does not name the 'over' key", err.Error())
	}
	if !strings.Contains(err.Error(), "needs 'over'") {
		t.Fatalf("error %q does not say the key is missing", err.Error())
	}
	if !strings.Contains(err.Error(), "no-over") {
		t.Fatalf("error %q does not name the action", err.Error())
	}
}

// TestParseWorkflowOverRejectsBadString proves an `over` string other than
// "selection" is rejected rather than silently treated as one source or the
// other.
func TestParseWorkflowOverRejectsBadString(t *testing.T) {
	action := manifest.Action{"kind": "workflow", "over": "everything"}
	_, err := ParseWorkflowOver("bad", action)
	if err == nil {
		t.Fatal("expected an error for over = \"everything\"")
	}
	if !strings.Contains(err.Error(), `"selection"`) {
		t.Fatalf("error %q does not explain the valid string value", err.Error())
	}
}

// TestParseWorkflowOverRejectsTableWithNoQuery proves `over = {}` (a table
// missing `query`) is rejected, not silently treated as query-sourced with
// an empty query.
func TestParseWorkflowOverRejectsTableWithNoQuery(t *testing.T) {
	action := manifest.Action{"kind": "workflow", "over": map[string]any{}}
	_, err := ParseWorkflowOver("bad", action)
	if err == nil {
		t.Fatal("expected an error for over = {} (no query)")
	}
	if !strings.Contains(err.Error(), "query") {
		t.Fatalf("error %q does not mention 'query'", err.Error())
	}
}

// TestParseWorkflowOverRejectsWrongType proves a non-string, non-table
// `over` (e.g. a bare number or bool) is rejected.
func TestParseWorkflowOverRejectsWrongType(t *testing.T) {
	action := manifest.Action{"kind": "workflow", "over": true}
	_, err := ParseWorkflowOver("bad", action)
	if err == nil {
		t.Fatal("expected an error for over = true")
	}
}

// TestParseWorkflowOverTracker proves `over.tracker` parses into
// WorkflowTarget.Tracker when present and non-empty, and stays "" when
// omitted (admin-j00d).
func TestParseWorkflowOverTracker(t *testing.T) {
	action := manifest.Action{"kind": "workflow", "over": map[string]any{"query": "label:review-me", "tracker": "github"}}
	target, err := ParseWorkflowOver("review", action)
	if err != nil {
		t.Fatalf("ParseWorkflowOver = %v, want nil", err)
	}
	if target.Tracker != "github" {
		t.Fatalf("Tracker = %q, want %q", target.Tracker, "github")
	}

	noTracker := manifest.Action{"kind": "workflow", "over": map[string]any{"query": "label:review-me"}}
	target2, err := ParseWorkflowOver("review", noTracker)
	if err != nil {
		t.Fatalf("ParseWorkflowOver = %v, want nil", err)
	}
	if target2.Tracker != "" {
		t.Fatalf("Tracker = %q, want \"\" when over.tracker is omitted", target2.Tracker)
	}
}

// TestParseWorkflowOverTrackerRejectsEmptyString proves `over.tracker = ""`
// is rejected rather than silently treated as omitted.
func TestParseWorkflowOverTrackerRejectsEmptyString(t *testing.T) {
	action := manifest.Action{"kind": "workflow", "over": map[string]any{"query": "label:x", "tracker": ""}}
	_, err := ParseWorkflowOver("review", action)
	if err == nil {
		t.Fatal("expected an error for over.tracker = \"\"")
	}
	if !strings.Contains(err.Error(), "over.tracker") {
		t.Fatalf("error %q does not name 'over.tracker'", err.Error())
	}
}

// -- WorkflowTarget.ResolveTracker ----------------------------------------

// TestResolveTrackerUndeclaredNameErrors proves an over.tracker naming a
// tracker the manifest doesn't declare is a check error naming both the
// offending value and the declared list.
func TestResolveTrackerUndeclaredNameErrors(t *testing.T) {
	target := WorkflowTarget{Tracker: "github"}
	_, err := target.ResolveTracker("review", []string{"beads"})
	if err == nil {
		t.Fatal("expected an error for an undeclared over.tracker")
	}
	if !strings.Contains(err.Error(), "github") || !strings.Contains(err.Error(), "beads") {
		t.Fatalf("error %q does not name both the offending value and the declared list", err.Error())
	}
}

// TestResolveTrackerDeclaredNameResolves proves an over.tracker naming a
// declared tracker resolves to it.
func TestResolveTrackerDeclaredNameResolves(t *testing.T) {
	target := WorkflowTarget{Tracker: "github"}
	got, err := target.ResolveTracker("review", []string{"beads", "github"})
	if err != nil {
		t.Fatalf("ResolveTracker = %v, want nil", err)
	}
	if got != "github" {
		t.Fatalf("ResolveTracker = %q, want %q", got, "github")
	}
}

// TestResolveTrackerOmittedNoneDeclaredDefaultsBeads proves that with no
// over.tracker and no manifest-declared tracker at all, resolution still
// defaults to "beads" — preserving the tool's pre-list default.
func TestResolveTrackerOmittedNoneDeclaredDefaultsBeads(t *testing.T) {
	target := WorkflowTarget{}
	got, err := target.ResolveTracker("review", nil)
	if err != nil {
		t.Fatalf("ResolveTracker = %v, want nil", err)
	}
	if got != "beads" {
		t.Fatalf("ResolveTracker = %q, want %q", got, "beads")
	}
}

// TestResolveTrackerOmittedOneDeclaredResolves proves that with exactly one
// declared tracker and no over.tracker, resolution uses that tracker.
func TestResolveTrackerOmittedOneDeclaredResolves(t *testing.T) {
	target := WorkflowTarget{}
	got, err := target.ResolveTracker("review", []string{"github"})
	if err != nil {
		t.Fatalf("ResolveTracker = %v, want nil", err)
	}
	if got != "github" {
		t.Fatalf("ResolveTracker = %q, want %q", got, "github")
	}
}

// TestResolveTrackerOmittedTwoDeclaredErrorsNamingBoth proves that with two
// declared trackers and no over.tracker, resolution refuses to guess and
// errors naming every declared tracker — there is no union-both option.
func TestResolveTrackerOmittedTwoDeclaredErrorsNamingBoth(t *testing.T) {
	target := WorkflowTarget{}
	_, err := target.ResolveTracker("review", []string{"beads", "github"})
	if err == nil {
		t.Fatal("expected an error when over.tracker is omitted and two trackers are declared")
	}
	if !strings.Contains(err.Error(), "beads") || !strings.Contains(err.Error(), "github") {
		t.Fatalf("error %q does not name both declared trackers", err.Error())
	}
}

// -- ValidateWorkflows ----------------------------------------------------

// TestValidateWorkflowsIgnoresOtherKinds proves an action of a non-workflow
// kind is never inspected for `over` at all, however it's shaped.
func TestValidateWorkflowsIgnoresOtherKinds(t *testing.T) {
	actions := map[string]manifest.Action{
		"build": {"kind": "shell", "run": "go build ./..."},
	}
	if err := ValidateWorkflows(actions); err != nil {
		t.Fatalf("ValidateWorkflows = %v, want nil", err)
	}
}

// TestValidateWorkflowsAcceptsBothSources proves a manifest with one
// selection-sourced and one query-sourced workflow both pass.
func TestValidateWorkflowsAcceptsBothSources(t *testing.T) {
	actions := map[string]manifest.Action{
		"retire-merged": {"kind": "workflow", "over": "selection"},
		"review-waiting": {
			"kind": "workflow",
			"over": map[string]any{"query": "label:review-me"},
		},
	}
	if err := ValidateWorkflows(actions); err != nil {
		t.Fatalf("ValidateWorkflows = %v, want nil", err)
	}
}

// TestValidateWorkflowsFailsCheckNamingTheMissingKey is the ticket's
// acceptance criterion stated at the validation-pipeline level: a workflow
// with no target source fails check with an error naming the missing key.
//
// MUTATION EVIDENCE: with the `if kind != "workflow" { continue }` guard in
// ValidateWorkflows deleted (so every action, of any kind, was parsed as a
// workflow), a manifest with an ordinary shell action alongside the broken
// workflow started failing for the WRONG reason — the shell action itself
// (which carries no `over`) was reported too. Concretely, adding
// `"build": {"kind": "shell", "run": "true"}` to this test's actions map
// under that mutation produced two problem lines instead of one:
//
//	action "build": kind="workflow" needs 'over' — ...
//	action "no-over": kind="workflow" needs 'over' — ...
//
// so the test asserting the error mentions "no-over" still passed (a false
// negative for THIS assertion) but a companion assertion checking the
// message does NOT also mention "build" caught it:
//
//	error mentions "build", which is not a workflow and should never be
//	inspected for 'over'
//
// (reverted after confirming the failure).
func TestValidateWorkflowsFailsCheckNamingTheMissingKey(t *testing.T) {
	actions := map[string]manifest.Action{
		"no-over": {"kind": "workflow"},
		"build":   {"kind": "shell", "run": "true"},
	}
	err := ValidateWorkflows(actions)
	if err == nil {
		t.Fatal("ValidateWorkflows = nil, want an error naming the missing key")
	}
	got := err.Error()
	if !strings.Contains(got, "no-over") || !strings.Contains(got, "needs 'over'") {
		t.Fatalf("error %q does not name the missing key on the offending action", got)
	}
	if strings.Contains(got, "\"build\"") {
		t.Fatalf("error %q mentions \"build\", which is not a workflow and should never be inspected for 'over'", got)
	}
}

// TestValidateWorkflowsReportsEveryOffender proves several broken workflows
// are all reported, not only the first.
func TestValidateWorkflowsReportsEveryOffender(t *testing.T) {
	actions := map[string]manifest.Action{
		"a": {"kind": "workflow"},
		"b": {"kind": "workflow", "over": "nonsense"},
	}
	err := ValidateWorkflows(actions)
	if err == nil {
		t.Fatal("expected an error")
	}
	got := err.Error()
	for _, want := range []string{"\"a\"", "\"b\""} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q does not mention action %s", got, want)
		}
	}
}

// -- catalog / factory wiring ---------------------------------------------

// TestWorkflowKindIsDeclaredConfigless proves the catalog entry's shape:
// Configless (its body is `over`, not `run`) and not ConsumesTargets. A
// workflow action must never carry `locale = "window"` (admin-spjp.2's fix,
// still true under ADR-0020 — see catalog.go's comment on the "workflow"
// entry), but that is a manifest-authoring rule, not a catalog property, so
// there is nothing left on Kind itself to assert here.
func TestWorkflowKindIsDeclaredConfigless(t *testing.T) {
	k, ok := Declared("workflow")
	if !ok {
		t.Fatal(`Declared("workflow") ok=false, want true`)
	}
	if !k.Configless {
		t.Fatal("workflow kind should be Configless (driven by 'over', not 'run')")
	}
	if k.ConsumesTargets {
		t.Fatal("workflow kind should not be ConsumesTargets — its targets come from 'over', not a 'targets' table")
	}
}

// TestMakeWorkflowRejectsMalformedOverAtBuildTime proves the factory itself
// (not just ParseWorkflowOver in isolation) fails at build time for a
// malformed `over`, the same contract every other kind factory in this
// package follows.
func TestMakeWorkflowRejectsMalformedOverAtBuildTime(t *testing.T) {
	action := manifest.Action{"kind": "workflow"}
	if _, err := makeWorkflow("no-over", action, nil, nil); err == nil {
		t.Fatal("makeWorkflow = nil error, want a build-time rejection of the missing 'over'")
	}
}

// TestMakeWorkflowSelectionRunnerNamesRowGesture proves a selection-sourced
// workflow's real Runner (not a stub — admin-spjp.2 wired makeWorkflow to
// runWorkflowCLI) fails loudly naming both the action and the row gesture
// that does have targets to act on, since there is no selection outside the
// dashboard.
func TestMakeWorkflowSelectionRunnerNamesRowGesture(t *testing.T) {
	action := manifest.Action{"kind": "workflow", "over": "selection"}
	run, err := makeWorkflow("retire-merged", action, nil, nil)
	if err != nil {
		t.Fatalf("makeWorkflow = %v, want nil", err)
	}
	err = run(nil)
	if err == nil {
		t.Fatal("expected the selection-sourced Runner to error")
	}
	if !strings.Contains(err.Error(), "retire-merged") {
		t.Fatalf("error %q does not name the action", err.Error())
	}
	if !strings.Contains(err.Error(), "space") || !strings.Contains(err.Error(), "w") {
		t.Fatalf("error %q does not name the row gesture (space to select, w to pick)", err.Error())
	}
}

// TestWorkflowRegisteredInDefaultRegistry proves "workflow" is actually
// bound in the Default registry — the coverage check factories.go's init()
// runs would panic at package init if it weren't, but this makes the
// invariant an ordinary, readable test failure instead of a package-load
// panic.
func TestWorkflowRegisteredInDefaultRegistry(t *testing.T) {
	if _, ok := Default.Lookup("workflow"); !ok {
		t.Fatal(`Default.Lookup("workflow") ok=false, want true`)
	}
}
