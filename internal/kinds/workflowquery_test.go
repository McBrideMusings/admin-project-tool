package kinds

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// stubBeads swaps listBeads for the duration of one test, matching the
// startAgent/runAgent/agentStatus seam pattern workflowdispatch_test.go
// already uses.
func stubBeads(t *testing.T, beads []bd.Bead, err error) {
	t.Helper()
	orig := listBeads
	listBeads = func(context.Context, string) ([]bd.Bead, error) { return beads, err }
	t.Cleanup(func() { listBeads = orig })
}

// stubGitHubIssues swaps listGitHubIssues for the duration of one test,
// mirroring stubBeads — a unit test proving the label-filtering logic has no
// business needing a real `gh` binary or the network.
func stubGitHubIssues(t *testing.T, issues []gh.IssueItem, err error) {
	t.Helper()
	orig := listGitHubIssues
	listGitHubIssues = func(context.Context, string, string) ([]gh.IssueItem, error) { return issues, err }
	t.Cleanup(func() { listGitHubIssues = orig })
}

// TestResolveQueryGitHubLabelResolvesToOpenIssues proves label:<name> on a
// github-tracked project resolves to one Target per matching issue, keyed by
// the bare issue number — the id form internal/tui's old tracker dispatch
// trackerRowID/trackerTargets already dispatch GitHub issues under.
func TestResolveQueryGitHubLabelResolvesToOpenIssues(t *testing.T) {
	stubGitHubIssues(t, []gh.IssueItem{{Number: 12}, {Number: 34}}, nil)

	targets, err := ResolveQuery(context.Background(), nil, "/repo", "github", "o/r", "label:review-me")
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	got := map[string]bool{}
	for _, tg := range targets {
		got[tg.Name] = true
	}
	if len(got) != 2 || !got["12"] || !got["34"] {
		t.Fatalf("want targets 12 and 34, got %+v", targets)
	}
}

// TestMatchQueryGitHubFiltersHeldIssuesByLabel proves MatchQuery on a
// github tracker keeps only the held open issues carrying the label, keyed
// by issue number, with no gh read of its own.
func TestMatchQueryGitHubFiltersHeldIssuesByLabel(t *testing.T) {
	stubGitHubIssues(t, nil, errors.New("MatchQuery must not read gh"))
	issues := []gh.IssueItem{
		{Number: 12, Labels: []string{"review-me"}},
		{Number: 13, Labels: []string{"other"}},
		{Number: 34, Labels: []string{"bug", "review-me"}},
	}

	targets, err := MatchQuery(nil, "github", "label:review-me", nil, issues)
	if err != nil {
		t.Fatalf("MatchQuery: %v", err)
	}
	if len(targets) != 2 || targets[0].Name != "12" || targets[1].Name != "34" {
		t.Fatalf("want targets 12 and 34, got %+v", targets)
	}
}

// TestResolveQueryGitHubReadyErrorsNamingTheTracker proves "ready" on a
// github-tracked project is REJECTED rather than synthesized from prose
// references — the ticket's headline decision — and the error names the
// tracker so the manifest can be fixed from the message alone.
func TestResolveQueryGitHubReadyErrorsNamingTheTracker(t *testing.T) {
	stubGitHubIssues(t, nil, errors.New("listGitHubIssues must not be called for \"ready\""))

	_, err := ResolveQuery(context.Background(), nil, "/repo", "github", "o/r", "ready")
	if err == nil {
		t.Fatal("want an error for \"ready\" on a github tracker, got nil")
	}
	if !strings.Contains(err.Error(), "github") {
		t.Fatalf("error should name the tracker, got: %v", err)
	}
	if !strings.Contains(err.Error(), "beads-only") {
		t.Fatalf("error should say \"ready\" is beads-only, got: %v", err)
	}
}

// TestResolveQueryGitHubEmptyLabelIsAnError proves "label:" with nothing
// after the colon is a malformed query on github too, matching the beads
// path's own behaviour.
func TestResolveQueryGitHubEmptyLabelIsAnError(t *testing.T) {
	stubGitHubIssues(t, nil, errors.New("listGitHubIssues must not be called for an empty label"))

	_, err := ResolveQuery(context.Background(), nil, "/repo", "github", "o/r", "label:")
	if err == nil {
		t.Fatal("want an error for an empty label name, got nil")
	}
}

// TestResolveQueryGitHubUnknownFormIsAnError proves a query string that is
// neither "ready" nor "label:..." is rejected by name on github too.
func TestResolveQueryGitHubUnknownFormIsAnError(t *testing.T) {
	stubGitHubIssues(t, nil, errors.New("listGitHubIssues must not be called"))

	_, err := ResolveQuery(context.Background(), nil, "/repo", "github", "o/r", "everything")
	if err == nil {
		t.Fatal("want an error for an unrecognized query form, got nil")
	}
	if !strings.Contains(err.Error(), "everything") {
		t.Fatalf("error should name the bad query, got: %v", err)
	}
	if strings.Contains(err.Error(), `"ready"`) {
		t.Fatalf("error must not advertise \"ready\" as a supported github form, got: %v", err)
	}
	if !strings.Contains(err.Error(), "label:<name>") {
		t.Fatalf("error should name \"label:<name>\" as the supported form, got: %v", err)
	}
}

// TestClassifyQuery proves ClassifyQuery is the single source of truth for
// which of the four syntactic forms a query string is — the classification
// cmd/check.go's queryTrackerProblem and this file's resolveGitHubQuery/
// resolveBeadsQuery all defer to instead of each re-deriving it.
func TestClassifyQuery(t *testing.T) {
	cases := []struct {
		query     string
		wantForm  QueryForm
		wantLabel string
	}{
		{"ready", QueryFormReady, ""},
		{" ready ", QueryFormReady, ""},
		{"label:", QueryFormEmptyLabel, ""},
		{"label:review-me", QueryFormLabel, "review-me"},
		{"everything", QueryFormUnrecognized, ""},
		{"", QueryFormUnrecognized, ""},
	}
	for _, c := range cases {
		form, label := ClassifyQuery(c.query)
		if form != c.wantForm || label != c.wantLabel {
			t.Errorf("ClassifyQuery(%q) = (%v, %q), want (%v, %q)",
				c.query, form, label, c.wantForm, c.wantLabel)
		}
	}
}

// TestResolveQueryGitHubNoMatchIsNotAnError proves a github label query
// resolving to nothing comes back as an empty, non-nil slice with a nil
// error — matching the beads path's own acceptance criterion.
func TestResolveQueryGitHubNoMatchIsNotAnError(t *testing.T) {
	stubGitHubIssues(t, []gh.IssueItem{}, nil)

	targets, err := ResolveQuery(context.Background(), nil, "/repo", "github", "o/r", "label:nothing-matches-this")
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if targets == nil {
		t.Fatal("want a non-nil empty slice, got nil")
	}
	if len(targets) != 0 {
		t.Fatalf("want zero targets, got %+v", targets)
	}
}

// TestResolveQueryUnknownTrackerIsAnError proves a tracker that is neither
// beads nor github (nor undeclared) errors by name rather than silently
// falling back to a resolver it was never configured for.
func TestResolveQueryUnknownTrackerIsAnError(t *testing.T) {
	_, err := ResolveQuery(context.Background(), nil, "/repo", "jira", "", "label:x")
	if err == nil {
		t.Fatal("want an error for an unsupported tracker, got nil")
	}
	if !strings.Contains(err.Error(), "jira") {
		t.Fatalf("error should name the tracker, got: %v", err)
	}
}

// TestResolveQueryReadyExcludesBlocked proves "ready" matches only beads with
// no still-open blocker — bd's own "available to start" definition.
func TestResolveQueryReadyExcludesBlocked(t *testing.T) {
	stubBeads(t, []bd.Bead{
		{ID: "a-1"},
		{ID: "a-2", Blockers: []string{"a-1"}},
	}, nil)

	targets, err := ResolveQuery(context.Background(), nil, "/repo", "beads", "", "ready")
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if len(targets) != 1 || targets[0].Name != "a-1" {
		t.Fatalf("want only a-1 (unblocked), got %+v", targets)
	}
}

// TestResolveQueryLabelFiltersByLabel proves "label:<name>" matches only
// beads carrying that exact label — the form admin-3rgp.24's own fixture
// already used (workflow_test.go's `query = "label:review-me"`).
func TestResolveQueryLabelFiltersByLabel(t *testing.T) {
	stubBeads(t, []bd.Bead{
		{ID: "a-1", Labels: []string{"review-me"}},
		{ID: "a-2", Labels: []string{"other"}},
		{ID: "a-3", Labels: []string{"review-me", "other"}},
	}, nil)

	targets, err := ResolveQuery(context.Background(), nil, "/repo", "beads", "", "label:review-me")
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	got := map[string]bool{}
	for _, tg := range targets {
		got[tg.Name] = true
	}
	if len(got) != 2 || !got["a-1"] || !got["a-3"] {
		t.Fatalf("want a-1 and a-3, got %+v", targets)
	}
}

// TestResolveQueryEmptyLabelIsAnError proves "label:" with nothing after the
// colon is a malformed query, not a query that matches every bead.
func TestResolveQueryEmptyLabelIsAnError(t *testing.T) {
	stubBeads(t, []bd.Bead{{ID: "a-1", Labels: []string{"x"}}}, nil)

	_, err := ResolveQuery(context.Background(), nil, "/repo", "beads", "", "label:")
	if err == nil {
		t.Fatal("want an error for an empty label name, got nil")
	}
}

// TestResolveQueryNoMatchIsNotAnError proves a query resolving to nothing
// comes back as an empty, non-nil slice with a nil error — this ticket's own
// acceptance criterion is that this is a reportable outcome ("says so"), not
// a resolution failure.
func TestResolveQueryNoMatchIsNotAnError(t *testing.T) {
	stubBeads(t, []bd.Bead{{ID: "a-1", Labels: []string{"other"}}}, nil)

	targets, err := ResolveQuery(context.Background(), nil, "/repo", "beads", "", "label:nothing-matches-this")
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("want zero targets, got %+v", targets)
	}
}

// TestResolveQueryUnknownFormIsAnError proves a query string that is neither
// "ready" nor "label:..." is rejected by name rather than silently matching
// nothing or everything.
func TestResolveQueryUnknownFormIsAnError(t *testing.T) {
	stubBeads(t, []bd.Bead{{ID: "a-1"}}, nil)

	_, err := ResolveQuery(context.Background(), nil, "/repo", "beads", "", "everything")
	if err == nil {
		t.Fatal("want an error for an unrecognized query form, got nil")
	}
	if !strings.Contains(err.Error(), "everything") {
		t.Fatalf("error should name the bad query, got: %v", err)
	}
}

// TestResolveQueryTrackerErrorPropagates proves an unreadable tracker (no bd
// workspace, a schema mismatch) surfaces as an error rather than as an empty,
// no-op match set — the two have to stay distinguishable so the confirm
// overlay can say which one happened.
func TestResolveQueryTrackerErrorPropagates(t *testing.T) {
	stubBeads(t, nil, errors.New("no beads workspace"))

	_, err := ResolveQuery(context.Background(), nil, "/repo", "beads", "", "ready")
	if err == nil {
		t.Fatal("want the tracker error propagated, got nil")
	}
}

// TestResolveQueryTargetsCarryTheActionsRunAndDone proves every resolved
// target carries the workflow action's own run and done strings, unformatted.
func TestResolveQueryTargetsCarryTheActionsRunAndDone(t *testing.T) {
	stubBeads(t, []bd.Bead{{ID: "proj-42"}}, nil)
	action := manifest.Action{"kind": "workflow", "run": "go-run $ADMIN_TARGET", "done": "is-done"}

	targets, err := ResolveQuery(context.Background(), action, "/repo", "beads", "", "ready")
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("want one target, got %d", len(targets))
	}
	if targets[0].Name != "proj-42" {
		t.Fatalf("want target named proj-42, got %q", targets[0].Name)
	}
	if targets[0].Run != "go-run $ADMIN_TARGET" || targets[0].Done != "is-done" {
		t.Fatalf("want the action's run and done, got %+v", targets[0])
	}
}
