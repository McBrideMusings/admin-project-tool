package gh

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/argvscan"
)

// recorder stands in for the runner var: it records the argv of every call and
// replays canned stdout, so a test can assert the exact arguments a command
// function built without a `gh` binary, a network, or a repo.
type recorder struct {
	calls [][]string
	out   []string // stdout for call i; calls past the end get "{}"
}

func (r *recorder) run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if i := len(r.calls) - 1; i < len(r.out) {
		return r.out[i], nil
	}
	return "{}", nil
}

// issuePRFields is the field set issuePRStatusBatch asks for per aliased PR.
// Spelled out here rather than referenced so dropping one from the query is a
// test failure, not a silently thinner payload.
const issuePRFields = "number isDraft mergeable commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }"

// TestCommandArgv pins the argv of every exported command in this package.
// #18 was a missing `--repo` on `gh pr list --state merged`: gh then resolved
// the repo from the working directory, so in a fork MergedPRs described the
// upstream's merged PRs and the Cleanup box struck a live branch through as
// merged. That one was fixed by making the repo a required parameter, which a
// dropped `--json` field, `--limit` or `--search` cannot be. This is what
// covers those.
//
// The returned values and errors are deliberately ignored — the canned stdout
// only has to get each function as far as its command call.
func TestCommandArgv(t *testing.T) {
	tests := []struct {
		name string
		out  []string
		call func(ctx context.Context)
		want [][]string
	}{
		{
			name: "ResolveNumber issue",
			out:  []string{`{"number":7,"state":"open","title":"t"}`},
			call: func(ctx context.Context) { ResolveNumber(ctx, "o/r", 7) },
			want: [][]string{{"api", "repos/o/r/issues/7"}},
		},
		{
			name: "ResolveNumber PR also fetches the head branch",
			out: []string{
				`{"number":7,"state":"closed","pull_request":{"merged_at":"2026-01-01T00:00:00Z"}}`,
				"feature-x\n",
			},
			call: func(ctx context.Context) { ResolveNumber(ctx, "o/r", 7) },
			want: [][]string{
				{"api", "repos/o/r/issues/7"},
				{"api", "repos/o/r/pulls/7", "-q", ".head.ref"},
			},
		},
		{
			name: "PRHeadBranch",
			out:  []string{"feature-x\n"},
			call: func(ctx context.Context) { PRHeadBranch(ctx, "o/r", 7) },
			want: [][]string{{"api", "repos/o/r/pulls/7", "-q", ".head.ref"}},
		},
		{
			name: "LinkedIssue",
			out:  []string{"12\n"},
			call: func(ctx context.Context) { LinkedIssue(ctx, 7) },
			want: [][]string{{
				"pr", "view", "7",
				"--json", "closingIssuesReferences",
				"-q", `.closingIssuesReferences[0].number // empty`,
			}},
		},
		{
			name: "Viewer",
			out:  []string{`{"login":"p","email":null,"id":7}`},
			call: func(ctx context.Context) { Viewer(ctx, "github.com") },
			want: [][]string{{"api", "--hostname", "github.com", "user"}},
		},
		{
			name: "MergedPRs",
			out:  []string{"[]"},
			call: func(ctx context.Context) { MergedPRs(ctx, "o/r", 30) },
			want: [][]string{{
				"pr", "list",
				"--repo", "o/r",
				"--state", "merged",
				"--limit", "30",
				"--json", "headRefName,headRefOid,author,closingIssuesReferences,mergedAt",
			}},
		},
		{
			name: "ClosedPRHeads",
			out:  []string{"[]"},
			call: func(ctx context.Context) { ClosedPRHeads(ctx, "o/r", 40) },
			want: [][]string{{
				"pr", "list",
				"--repo", "o/r",
				"--state", "closed",
				"--search", "is:unmerged",
				"--limit", "40",
				"--json", "headRefName,headRefOid,mergedAt",
			}},
		},
		{
			name: "OpenPRs",
			call: func(ctx context.Context) { OpenPRs(ctx, "o/r") },
			want: [][]string{{
				"api", "graphql",
				"-f", "query=" + openPRsQuery,
				"-f", "q=repo:o/r is:pr is:open",
			}},
		},
		{
			name: "OpenPRs pages with the end cursor",
			out:  []string{`{"data":{"search":{"pageInfo":{"hasNextPage":true,"endCursor":"CUR"}}}}`},
			call: func(ctx context.Context) { OpenPRs(ctx, "o/r") },
			want: [][]string{
				{"api", "graphql", "-f", "query=" + openPRsQuery, "-f", "q=repo:o/r is:pr is:open"},
				{"api", "graphql", "-f", "query=" + openPRsQuery, "-f", "q=repo:o/r is:pr is:open", "-f", "after=CUR"},
			},
		},
		{
			name: "PRMergeStates",
			call: func(ctx context.Context) { PRMergeStates(ctx, "o/r", []int{4, 9}) },
			want: [][]string{{
				"api", "graphql",
				"-f", "query=" + aliasedPRQuery([]int{4, 9}, "number mergeable"),
				"-f", "owner=o",
				"-f", "name=r",
			}},
		},
		{
			name: "OpenPRByNumber",
			call: func(ctx context.Context) { OpenPRByNumber(ctx, "o/r", 7) },
			want: [][]string{{
				"api", "graphql",
				"-f", "query=" + openPRByNumberQuery,
				"-f", "owner=o",
				"-f", "name=r",
				"-F", "number=7",
			}},
		},
		{
			name: "AllOpenIssues",
			call: func(ctx context.Context) { AllOpenIssues(ctx, "o/r") },
			want: [][]string{{
				"api", "graphql",
				"-f", "query=" + issuesQuery,
				"-f", "q=repo:o/r is:issue is:open",
			}},
		},
		{
			name: "AllOpenIssues pages with the end cursor",
			out:  []string{`{"data":{"search":{"pageInfo":{"hasNextPage":true,"endCursor":"CUR"}}}}`},
			call: func(ctx context.Context) { AllOpenIssues(ctx, "o/r") },
			want: [][]string{
				{"api", "graphql", "-f", "query=" + issuesQuery, "-f", "q=repo:o/r is:issue is:open"},
				{"api", "graphql", "-f", "query=" + issuesQuery, "-f", "q=repo:o/r is:issue is:open", "-f", "after=CUR"},
			},
		},
		{
			name: "IssuePRStatuses de-dupes numbers into one aliased query",
			call: func(ctx context.Context) { IssuePRStatuses(ctx, "o/r", []int{4, 9, 4}) },
			want: [][]string{{
				"api", "graphql",
				"-f", "query=" + aliasedPRQuery([]int{4, 9}, issuePRFields),
				"-f", "owner=o",
				"-f", "name=r",
			}},
		},
		{
			name: "IssuePRStatuses chunks past the batch size",
			call: func(ctx context.Context) { IssuePRStatuses(ctx, "o/r", numbersUpTo(issuePRStatusBatchSize+1)) },
			want: [][]string{
				{
					"api", "graphql",
					"-f", "query=" + aliasedPRQuery(numbersUpTo(issuePRStatusBatchSize), issuePRFields),
					"-f", "owner=o",
					"-f", "name=r",
				},
				{
					"api", "graphql",
					"-f", "query=" + aliasedPRQuery([]int{issuePRStatusBatchSize + 1}, issuePRFields),
					"-f", "owner=o",
					"-f", "name=r",
				},
			},
		},
		{
			name: "OpenIssuesByLabel",
			out:  []string{"[]"},
			call: func(ctx context.Context) { OpenIssuesByLabel(ctx, "o/r", "review-me") },
			want: [][]string{{
				"issue", "list",
				"--repo", "o/r",
				"--state", "open",
				"--label", "review-me",
				"--json", "number,title,labels",
				"--limit", "500",
			}},
		},
		{
			name: "IssueByNumber",
			call: func(ctx context.Context) { IssueByNumber(ctx, "o/r", 7) },
			want: [][]string{{
				"api", "graphql",
				"-f", "query=" + issueByNumberQuery,
				"-f", "owner=o",
				"-f", "name=r",
				"-F", "number=7",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{out: tt.out}
			restore := runner
			runner = rec.run
			defer func() { runner = restore }()

			tt.call(context.Background())

			if len(rec.calls) != len(tt.want) {
				t.Fatalf("made %d gh calls, want %d:\ngot  %v\nwant %v", len(rec.calls), len(tt.want), rec.calls, tt.want)
			}
			for i := range tt.want {
				if !reflect.DeepEqual(rec.calls[i], tt.want[i]) {
					t.Errorf("call %d argv mismatch:\ngot  %q\nwant %q", i, rec.calls[i], tt.want[i])
				}
			}
		})
	}
}

func numbersUpTo(n int) []int {
	out := make([]int, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, i)
	}
	return out
}

// coveredCommands is every exported function in this package that runs a gh
// command. TestArgvCoverage fails when gh.go grows one that isn't listed, so a
// new command can't ship without its argv pinned above.
var coveredCommands = map[string]bool{
	"ResolveNumber":     true,
	"PRHeadBranch":      true,
	"LinkedIssue":       true,
	"MergedPRs":         true,
	"ClosedPRHeads":     true,
	"OpenPRs":           true,
	"PRMergeStates":     true,
	"OpenPRByNumber":    true,
	"AllOpenIssues":     true,
	"IssuePRStatuses":   true,
	"IssueByNumber":     true,
	"OpenIssuesByLabel": true,
	"Viewer":            true,
}

func TestArgvCoverage(t *testing.T) {
	argvscan.Check(t, argvscan.Config{
		Tool:    "gh",
		Files:   []string{"gh.go"},
		Seams:   []string{"runner("},
		Covered: coveredCommands,
	})
}

// TestRealRunnerRefusesUnderTest pins the fail-closed default: a test that
// reaches the real runner panics with the argv instead of calling GitHub.
func TestRealRunnerRefusesUnderTest(t *testing.T) {
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "gh api repos/o/r/issues/7") {
			t.Fatalf("panic = %q, want it to name the argv", msg)
		}
	}()
	_, _ = run(context.Background(), "api", "repos/o/r/issues/7")
	t.Fatal("run returned; want a panic")
}
