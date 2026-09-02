// workflow.go is kind="workflow"'s definition (admin-3rgp.24): the manifest
// shape, its validation, and the catalog/factory wiring that makes it a real
// declared kind. A Workflow expresses "run this same thing over many
// targets" and declares WHERE those targets
// come from via its `over` key — that declaration is the whole reason this
// file exists, per the epic's settled decision:
//
//	over = "selection" is row-only; over = { query = ... } appears in the
//	Commands tab and runs bare after confirming the resolved set.
//
// Actually RUNNING a workflow — resolving a query, confirming the set with
// the user, fanning targets out into worktrees under a concurrency ceiling —
// is explicitly out of scope here and belongs to the follow-on tickets this
// one blocks: admin-3rgp.27 (selection-sourced dispatch from a dashboard
// row), admin-3rgp.26 (query-sourced dispatch, bare-run, from the Commands
// tab) and admin-3rgp.25 (the worktree/concurrency machinery both share).
// makeWorkflow below validates `over` at factory-build time (so a malformed
// workflow fails the same way any other malformed action does, before a
// Runner is ever invoked) and returns a Runner that reports NOT IMPLEMENTED
// — the same notImplemented() stub pattern factories.go already uses for
// apple, apple-dev-loop, interactive-shell and bridge-listen ahead of their
// own port tickets.
package kinds

import (
	"fmt"
	"sort"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// WorkflowSource is a workflow action's declared target source — the value
// this whole ticket exists to define. The zero value, WorkflowSourceNone,
// is never a valid parse result; ParseWorkflowOver always pairs it with a
// non-nil error, matching the acceptance criterion "a workflow with no
// target source fails check with an error naming the missing key" rather
// than silently defaulting to one interpretation or the other.
type WorkflowSource int

const (
	// WorkflowSourceNone marks a parse failure — never returned alongside a
	// nil error.
	WorkflowSourceNone WorkflowSource = iota

	// WorkflowSourceSelection is `over = "selection"`: row-only. The user
	// picks targets on a row in the dashboard; the workflow does not run
	// bare because with no selection there is nothing to act on, and for
	// that reason it must never appear in the Commands tab (a command that
	// cannot say what it will act on should not be offered).
	WorkflowSourceSelection

	// WorkflowSourceQuery is `over = { query = "..." }`: the tool resolves
	// its own targets from Query. It appears in the Commands tab as a
	// runnable row and can run bare, but only after the resolved set is
	// confirmed with the user (admin-3rgp.26) — resolving a query to a set
	// and acting on it without showing what was resolved is the failure
	// mode this design exists to prevent, since a query can match more than
	// the user expects and dispatch puts real agents in real worktrees.
	WorkflowSourceQuery
)

func (s WorkflowSource) String() string {
	switch s {
	case WorkflowSourceSelection:
		return "selection"
	case WorkflowSourceQuery:
		return "query"
	default:
		return "none"
	}
}

// WorkflowTarget is a workflow action's parsed `over` declaration.
type WorkflowTarget struct {
	Source WorkflowSource
	// Query is the raw query string, set only when Source ==
	// WorkflowSourceQuery. Its syntax/semantics belong to admin-3rgp.26,
	// which resolves it against the tracker/repo state — this ticket only
	// carries it through unexamined beyond "present and non-empty".
	Query string
	// Tracker is the raw `over.tracker` value (admin-j00d) — which of the
	// manifest's declared trackers a query-sourced workflow resolves
	// against. "" when the key was omitted; ResolveTracker turns that into
	// a real tracker name (or an error) against the manifest's declared
	// list.
	Tracker string
}

// AppearsInCommandList reports whether a workflow with this target source
// should be offered in the Commands tab — the epic's rule that a
// query-sourced Workflow appears in the Command list while a
// selection-sourced one does not, in one place so the later TUI
// ticket (admin-3rgp.20/.26) has a single source of truth to consult rather
// than re-deriving the rule from WorkflowSource itself.
func (t WorkflowTarget) AppearsInCommandList() bool {
	return t.Source == WorkflowSourceQuery
}

// ResolveTracker resolves this target's tracker against the manifest's
// declared list (admin-j00d) — the one place that turns an `over.tracker`
// key (or its absence) into a real tracker name:
//
//   - t.Tracker is set and NOT in declared: error naming the value and the
//     declared list (an undeclared tracker is a check error, never a
//     silent guess).
//   - t.Tracker is set and IS in declared: that name.
//   - t.Tracker is "" and declared is empty: "beads" — preserves the
//     pre-list default for a manifest with no tracker declared at all.
//   - t.Tracker is "" and declared has exactly one member: that member.
//   - t.Tracker is "" and declared has more than one member: error naming
//     every declared tracker and telling the author to set over.tracker —
//     there is no union-both option; a query resolves into one concrete
//     target set.
//
// name is the action's own name, for an error message that can point at it.
func (t WorkflowTarget) ResolveTracker(name string, declared []string) (string, error) {
	if t.Tracker != "" {
		for _, d := range declared {
			if d == t.Tracker {
				return t.Tracker, nil
			}
		}
		return "", fmt.Errorf(
			`action %q: 'over.tracker' names %q, which the manifest does not declare (declared: %s)`,
			name, t.Tracker, joinOrNone(declared))
	}
	switch len(declared) {
	case 0:
		return "beads", nil
	case 1:
		return declared[0], nil
	default:
		return "", fmt.Errorf(
			`action %q: this manifest declares more than one tracker (%s) — set 'over.tracker' to say which one this query resolves against`,
			name, joinOrNone(declared))
	}
}

func joinOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	out := list[0]
	for _, s := range list[1:] {
		out += ", " + s
	}
	return out
}

// ParseWorkflowOver reads and validates action's `over` key — the Go home
// for every rule this ticket's acceptance criteria name:
//
//   - `over = "selection"` parses to WorkflowSourceSelection.
//   - `over = { query = "..." }` (a non-empty string) parses to
//     WorkflowSourceQuery, carrying Query.
//   - anything else — no `over` key at all, a string that isn't
//     "selection", a table with no (or an empty/non-string) `query` — is a
//     manifest error naming both the action and the missing/malformed key,
//     matching the project CLAUDE.md's "a malformed over... is a manifest
//     error naming the key and the rule, not a runtime surprise."
//
// name is the action's own name, for an error message that can point at it.
func ParseWorkflowOver(name string, action manifest.Action) (WorkflowTarget, error) {
	raw, present := action["over"]
	if !present {
		return WorkflowTarget{}, fmt.Errorf(
			`action %q: kind="workflow" needs 'over' — "selection" (row-only, never in the Command list) `+
				`or { query = "..." } (appears in the Command list and can run bare)`, name)
	}
	switch v := raw.(type) {
	case string:
		if v == "selection" {
			return WorkflowTarget{Source: WorkflowSourceSelection}, nil
		}
		return WorkflowTarget{}, fmt.Errorf(
			`action %q: 'over' as a string must be exactly "selection" (got %q) — `+
				`a query source needs a table: over = { query = "..." }`, name, v)
	case map[string]any:
		q, ok := v["query"].(string)
		if !ok || q == "" {
			return WorkflowTarget{}, fmt.Errorf(
				`action %q: 'over' table needs a non-empty 'query' string`, name)
		}
		var tracker string
		if raw, present := v["tracker"]; present {
			t, ok := raw.(string)
			if !ok || t == "" {
				return WorkflowTarget{}, fmt.Errorf(
					`action %q: 'over.tracker' must be a non-empty string`, name)
			}
			tracker = t
		}
		return WorkflowTarget{Source: WorkflowSourceQuery, Query: q, Tracker: tracker}, nil
	default:
		return WorkflowTarget{}, fmt.Errorf(
			`action %q: 'over' must be "selection" or a table with 'query' (got %#v)`, name, raw)
	}
}

// ValidateWorkflows checks every declared kind="workflow" action's `over`
// key, reporting every problem found rather than stopping at the first —
// matching frame.ValidateURLRefs/ValidateFooterRefs' own all-problems style,
// which cmd/check.go's runCheck already relies on to report every distinct
// problem in one pass. Actions of any other kind are untouched.
func ValidateWorkflows(actions map[string]manifest.Action) error {
	names := make([]string, 0, len(actions))
	for n := range actions {
		names = append(names, n)
	}
	sort.Strings(names)

	var problems []string
	for _, name := range names {
		action := actions[name]
		kind, _ := actionString(action, "kind")
		if kind != "workflow" {
			continue
		}
		if _, err := ParseWorkflowOver(name, action); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) == 0 {
		return nil
	}
	msg := problems[0]
	for _, p := range problems[1:] {
		msg += "\n" + p
	}
	return fmt.Errorf("%s", msg)
}

// makeWorkflow is kind="workflow"'s factory. It validates `over` up front —
// the same "malformed config fails at build time, not at Runner-invocation
// time" contract every other kind factory in this package follows — and
// then returns a Runner that reports NOT IMPLEMENTED, naming the resolved
// target source so the message is useful rather than generic: dispatch,
// worktrees and concurrency are admin-3rgp.25/.26/.27's job, not this
// ticket's.
func makeWorkflow(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	target, err := ParseWorkflowOver(name, action)
	if err != nil {
		return nil, err
	}
	return func(args []string) error {
		return fmt.Errorf(
			"action %q: kind=\"workflow\" (over: %s) is not implemented in the Go runtime yet — "+
				"dispatch lands in admin-3rgp.25/.26/.27", name, target.Source)
	}, nil
}
