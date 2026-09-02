// Package gh wraps the gh CLI for the lookups the dashboard needs. The bulk of
// this file is carried over from wtree's internal/gh; MyOpenPRs and PRChecks are
// new for the PRs view.
package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RepoURL returns the GitHub web URL for an "owner/repo" string.
func RepoURL(nwo string) string {
	if nwo == "" {
		return ""
	}
	return "https://github.com/" + nwo
}

// PRURL returns the web URL for PR #n in nwo.
func PRURL(nwo string, n int) string {
	if nwo == "" || n <= 0 {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/pull/%d", nwo, n)
}

// IssueURL returns the web URL for issue #n in nwo.
func IssueURL(nwo string, n int) string {
	if nwo == "" || n <= 0 {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/issues/%d", nwo, n)
}

// BranchURL returns the web URL for a branch's page in nwo. The name is escaped
// one path segment at a time so "feat/thing" keeps its slash — GitHub carries the
// branch name as a path under /tree/, not as a single escaped component.
func BranchURL(nwo, branch string) string {
	if nwo == "" || branch == "" {
		return ""
	}
	parts := strings.Split(branch, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "https://github.com/" + nwo + "/tree/" + strings.Join(parts, "/")
}

// NumberRef is what a bare repo number (#N) turned out to be. GitHub numbers
// issues and PRs from one per-repo sequence, so #N names exactly one object —
// this records which kind it is, in any state.
type NumberRef struct {
	Number     int
	IsPR       bool
	Title      string
	State      string // "open", "closed", or — for a PR that landed — "merged"
	HeadBranch string // the PR's head branch; empty for issues
}

// numberPayload is the REST issues-endpoint response. That endpoint serves both
// issues and PRs — a PR is distinguished by carrying a non-null pull_request
// object — which is what makes it the one call that disambiguates #N. That same
// object carries merged_at, so a merged PR is told apart from an abandoned one
// without a second lookup.
type numberPayload struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	PullRequest *struct {
		MergedAt *string `json:"merged_at"`
	} `json:"pull_request"`
}

// ref converts a decoded issues-endpoint payload into a NumberRef. HeadBranch is
// left empty: the issues endpoint doesn't carry a PR's head ref, so ResolveNumber
// fills it from the pulls endpoint.
func (p numberPayload) ref() NumberRef {
	ref := NumberRef{
		Number: p.Number,
		IsPR:   p.PullRequest != nil,
		Title:  p.Title,
		State:  p.State,
	}
	// GitHub reports a merged PR as "closed", which reads the same as one that was
	// abandoned — worth distinguishing, since only one of them landed.
	if p.PullRequest != nil && p.PullRequest.MergedAt != nil {
		ref.State = "merged"
	}
	return ref
}

// ResolveNumber answers "what is #N in this repo?" authoritatively, instead of
// guessing by trying one type then the other. The REST issues endpoint returns
// the object whether it's an issue or a PR, in any state; when it's a PR, its
// head branch is fetched from the pulls endpoint (also any state, so a merged or
// closed PR resolves just as well as an open one).
func ResolveNumber(ctx context.Context, nwo string, number int) (NumberRef, error) {
	if nwo == "" {
		return NumberRef{}, fmt.Errorf("no repo to resolve #%d in", number)
	}
	out, err := runner(ctx, "api", fmt.Sprintf("repos/%s/issues/%d", nwo, number))
	if err != nil {
		return NumberRef{}, fmt.Errorf("#%d is not an issue or PR in %s: %w", number, nwo, err)
	}
	var payload numberPayload
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return NumberRef{}, err
	}
	ref := payload.ref()
	if ref.Number == 0 {
		return NumberRef{}, fmt.Errorf("#%d is not an issue or PR in %s", number, nwo)
	}
	if !ref.IsPR {
		return ref, nil
	}
	head, err := PRHeadBranch(ctx, nwo, number)
	if err != nil {
		return NumberRef{}, err
	}
	ref.HeadBranch = head
	return ref, nil
}

// PRHeadBranch returns a PR's head branch name in any state — open, closed, or
// merged. It asks for the one field the branch-resolution path needs, rather
// than reusing OpenPRByNumber, whose whole payload (checks, review threads,
// bucketing) exists to render a PRs-tab row and is wasted work here.
func PRHeadBranch(ctx context.Context, nwo string, number int) (string, error) {
	if nwo == "" {
		return "", fmt.Errorf("no repo to look up PR #%d in", number)
	}
	out, err := runner(ctx, "api", fmt.Sprintf("repos/%s/pulls/%d", nwo, number), "-q", ".head.ref")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(out)
	if head == "" {
		return "", fmt.Errorf("pr #%d has no head branch", number)
	}
	return head, nil
}

// LinkedIssue returns the first issue number a PR closes. Returns (0, false, nil)
// when the PR closes nothing.
func LinkedIssue(ctx context.Context, prNumber int) (int, bool, error) {
	out, err := runner(ctx, "pr", "view", strconv.Itoa(prNumber), "--json", "closingIssuesReferences", "-q", `.closingIssuesReferences[0].number // empty`)
	if err != nil {
		return 0, false, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
}

// MergedPR is one recently-merged PR: its head branch, author login, and the
// numbers of the issues it closed (closingIssuesReferences). The author and
// closing issues are what let the classifier see a PR *someone else* landed for
// the issue a local branch is still sitting on — the "superseded" signal — which
// the bare head-branch set cannot, carrying neither.
type MergedPR struct {
	HeadBranch string
	Author     string
	Issues     []int
	// HeadOID is the commit GitHub merged this PR from. It is the difference
	// between "a PR with this branch name merged" and "these commits landed": a
	// squash merge leaves no trace of them on the default branch, and a branch name
	// reused after the merge carries different commits under the same name.
	HeadOID string
}

// MergedPRs returns the most recent merged PRs (up to limit), each with its head
// branch, author, and closing-issue numbers. Catches squash/rebase merges that
// git's own --merged check misses because the branch commits never land on the
// default branch.
//
// nwo is required and pins the query with --repo. Without it gh resolves the repo
// from the process's working directory and any default-repo override saved there —
// in a fork that resolves to upstream, and the answer describes a different repo's
// merged PRs than the one the dashboard is showing.
func MergedPRs(ctx context.Context, nwo string, limit int) ([]MergedPR, error) {
	out, err := runner(ctx, "pr", "list", "--repo", nwo, "--state", "merged", "--limit", strconv.Itoa(limit), "--json", "headRefName,headRefOid,author,closingIssuesReferences")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		HeadRefName string `json:"headRefName"`
		HeadRefOid  string `json:"headRefOid"`
		Author      struct {
			Login string `json:"login"`
		} `json:"author"`
		ClosingIssuesReferences []struct {
			Number int `json:"number"`
		} `json:"closingIssuesReferences"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, err
	}
	prs := make([]MergedPR, 0, len(raw))
	for _, r := range raw {
		pr := MergedPR{HeadBranch: r.HeadRefName, Author: r.Author.Login, HeadOID: r.HeadRefOid}
		for _, c := range r.ClosingIssuesReferences {
			pr.Issues = append(pr.Issues, c.Number)
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

// ClosedPRHeads returns the head-branch names of the most recent PRs (up to
// limit) that were closed WITHOUT merging. It is the counterpart to MergedPRs:
// that set says work landed, this one says a human looked at these commits and
// declined them.
//
// `gh pr list --state closed` returns merged PRs too — GitHub models a merge as a
// close — so the query is narrowed with `is:unmerged` and the mergedAt check is
// kept as a second line of defence. The narrowing is what makes the window worth
// spending at all: on a real repo 40 closed PRs held only 4 unmerged ones, so
// filtering client-side threw away nine rows in ten and learned almost nothing
// per fetch. With the search term the same 40 rows are all unmerged.
//
// Without the mergedAt check at all, a merged branch would be recorded as
// declined and the classifier would weigh "abandoned" over work that shipped.
//
// nwo pins the query with --repo, for the same reason as MergedPRs.
func ClosedPRHeads(ctx context.Context, nwo string, limit int) (map[string]bool, error) {
	out, err := runner(ctx, "pr", "list", "--repo", nwo, "--state", "closed", "--search", "is:unmerged", "--limit", strconv.Itoa(limit), "--json", "headRefName,mergedAt")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		HeadRefName string  `json:"headRefName"`
		MergedAt    *string `json:"mergedAt"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, err
	}
	heads := map[string]bool{}
	for _, r := range raw {
		if r.MergedAt == nil && r.HeadRefName != "" {
			heads[r.HeadRefName] = true
		}
	}
	return heads, nil
}

// CheckState is the rollup of a PR's CI checks.
type CheckState int

const (
	// ChecksNone means no checks are configured / reported for the PR.
	ChecksNone CheckState = iota
	// ChecksPending means at least one check is still running and none failed.
	ChecksPending
	// ChecksPassing means every check completed successfully.
	ChecksPassing
	// ChecksFailing means at least one check failed.
	ChecksFailing
)

// commitRollup is the shared "last commit's CI rollup" shape queried on PRs
// (MyOpenPRs, IssuePRStatuses, MyOpenPR all fetch the same one-commit rollup).
type commitRollup struct {
	Nodes []struct {
		Commit struct {
			StatusCheckRollup struct {
				State string `json:"state"`
			} `json:"statusCheckRollup"`
		} `json:"commit"`
	} `json:"nodes"`
}

// checksFromRollup maps a commitRollup's raw GitHub state string to a CheckState.
func checksFromRollup(c commitRollup) CheckState {
	if len(c.Nodes) == 0 {
		return ChecksNone
	}
	return checkStateFromString(c.Nodes[0].Commit.StatusCheckRollup.State)
}

// checkStateFromString maps a raw GitHub statusCheckRollup state to a CheckState.
func checkStateFromString(s string) CheckState {
	switch s {
	case "SUCCESS":
		return ChecksPassing
	case "PENDING", "EXPECTED":
		return ChecksPending
	case "FAILURE", "ERROR":
		return ChecksFailing
	default:
		return ChecksNone
	}
}

// PRBucket groups an open PR by the viewer's relationship to it — which of the
// four sections on the PRs tab it belongs in.
type PRBucket int

const (
	// BucketMine means the viewer authored the PR.
	BucketMine PRBucket = iota
	// BucketAssigned means the ball is in the viewer's court: they've been asked
	// for a review (or assigned) and haven't touched it, or they have touched it
	// and something has happened since that needs another look.
	BucketAssigned
	// BucketReviewed means the viewer has already had their say — a review or a
	// comment — and nothing has happened since. The PR is waiting on someone
	// else; the viewer owes it nothing.
	BucketReviewed
	// BucketOther means every other open PR — the viewer isn't the author, hasn't
	// been asked, and hasn't taken part.
	BucketOther
)

// PRGap is one piece of triage metadata an open PR is missing: no assignee, no
// reviewer, no labels. All three can be true at once, so the set is a bitmask
// rather than a single state, and Letters prints it in the fixed priority order
// the constants declare — assignee first, then reviewer, then labels.
type PRGap uint8

const (
	// GapAssignee means nobody is assigned to the PR.
	GapAssignee PRGap = 1 << iota
	// GapReviewer means no reviewer has been requested and none has reviewed.
	GapReviewer
	// GapLabels means the PR carries no labels.
	GapLabels
)

// Letters spells the missing pieces as one initial each, in priority order:
// A assignee, R reviewer, L labels. Empty when nothing is missing. The PRs table
// is where these land, and there the cell is scanned for presence rather than
// read as a sentence — "assignee/reviewer/labels" spelled out cost 32 cells on
// every row of the table to say what three letters say in three.
func (g PRGap) Letters() string {
	var b strings.Builder
	if g&GapAssignee != 0 {
		b.WriteByte('A')
	}
	if g&GapReviewer != 0 {
		b.WriteByte('R')
	}
	if g&GapLabels != 0 {
		b.WriteByte('L')
	}
	return b.String()
}

// OpenPR is one open PR in the repo, with everything the PRs view shows. Every
// open PR is fetched (not just the viewer's own and their review inbox), so each
// carries its Bucket and — for every PR the viewer didn't author — MyReview, the
// viewer's own standing on it.
type OpenPR struct {
	Number     int
	Title      string
	Author     string
	HeadBranch string
	IsDraft    bool
	Mergeable  string // "MERGEABLE", "CONFLICTING", "UNKNOWN"
	Checks     CheckState
	Review     ReviewClass // status from the PR author's POV (needs-response / changes-requested / approved / in-review)
	Bucket     PRBucket
	MyReview   ReviewState // the viewer's own standing; meaningful on every bucket except BucketMine
	Gaps       PRGap       // triage metadata the PR is missing (assignee / reviewer / labels)
	Updated    time.Time
}

// openPRNode is one PullRequest node from the all-open-PRs search — every field
// the three buckets need: identity, checks, mergeability, the author-POV review
// classification (via the embedded reviewClassInput), plus assignees and
// review-requests for bucketing and the viewer's own review standing.
type openPRNode struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	IsDraft     bool   `json:"isDraft"`
	Mergeable   string `json:"mergeable"`
	UpdatedAt   string `json:"updatedAt"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer struct {
				Login string `json:"login"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	reviewClassInput
}

// gaps reports which pieces of triage metadata this PR is missing. A reviewer
// counts as present either way GitHub can record one: a still-open review
// request, or a review already submitted by a human other than the author.
// Counting only the request would flag every PR whose reviewers have already
// ruled, since GitHub deletes the request when a review lands.
func (n openPRNode) gaps() PRGap {
	var g PRGap
	if len(n.Assignees.Nodes) == 0 {
		g |= GapAssignee
	}
	if !n.hasReviewer() {
		g |= GapReviewer
	}
	if len(n.Labels.Nodes) == 0 {
		g |= GapLabels
	}
	return g
}

func (n openPRNode) hasReviewer() bool {
	for _, rr := range n.ReviewRequests.Nodes {
		if rr.RequestedReviewer.Login != "" {
			return true
		}
	}
	for _, r := range n.Reviews.Nodes {
		if isBotActor(r.Author.Login, r.Author.Typename) {
			continue
		}
		if !strings.EqualFold(r.Author.Login, n.Author.Login) {
			return true
		}
	}
	return false
}

// assigned reports whether the viewer is an assignee on this PR. Assignment is a
// standing claim on their time — they own work on it — and unlike a review
// request GitHub never clears it on their behalf, so it outranks their activity
// in bucket(): having commented is not having finished the thing you were
// assigned. Only the person who assigned it can take it back.
func (n openPRNode) assigned(login string) bool {
	for _, a := range n.Assignees.Nodes {
		if strings.EqualFold(a.Login, login) {
			return true
		}
	}
	return false
}

// reviewRequested reports whether the viewer has a still-open review request on
// this PR. It answers only "has GitHub asked" — deliberately not "is this the
// viewer's move", because GitHub deletes the request the moment that reviewer
// submits a review. Reading it as the whole story dropped every PR out of the
// viewer's section the instant they reviewed it, which is the bug bucket() and
// classifyInboxState exist to fix, so it decides PRs they've never touched only.
func (n openPRNode) reviewRequested(login string) bool {
	for _, rr := range n.ReviewRequests.Nodes {
		if strings.EqualFold(rr.RequestedReviewer.Login, login) {
			return true
		}
	}
	return false
}

// bucket classifies the viewer's relationship to this PR from their own standing
// on it (my, already computed by classifyInboxState) rather than from GitHub's
// mutable review-request list. login is the viewer.
//
// Once the viewer has taken part at all — a submitted review or a comment — their
// activity is what decides: if nothing has happened since their last word the PR
// is waiting on someone else (BucketReviewed), and it only comes back to them
// when a new commit lands or someone replies to them directly. A still-open
// review request does not override that; having said their piece is the point.
// GitHub's own request list only decides PRs the viewer has never touched.
//
// Assignment (see assigned) outranks a review request but not a verdict. Being
// assigned asks for work, and a comment doesn't finish it, so assignment survives
// everything an open review request wouldn't. A submitted APPROVED or
// CHANGES_REQUESTED is where it stops: the viewer has ruled on the PR, so whatever
// the assignment was asking for, it is now someone else's move — the author's or
// the other reviewers'. Leaving it in BucketAssigned puts a PR the viewer has
// already signed off under a heading that says they still owe it something.
func (n openPRNode) bucket(login string, my ReviewState) PRBucket {
	if strings.EqualFold(n.Author.Login, login) {
		return BucketMine
	}
	if my == ReviewedApproved || my == ReviewedChangesRequested {
		return BucketReviewed
	}
	if n.assigned(login) {
		return BucketAssigned
	}
	switch my {
	case NotReviewed:
		if n.reviewRequested(login) {
			return BucketAssigned
		}
		return BucketOther
	case UpdatedSinceReview, RepliedSinceReview:
		return BucketAssigned
	default:
		return BucketReviewed
	}
}

// toOpenPR builds the rendered OpenPR from a node, running both classifiers: the
// author-POV review class for every PR, and — for every PR the viewer didn't
// author — their own standing on it, which is also what decides the bucket.
func (n openPRNode) toOpenPR(login string) OpenPR {
	updated, _ := time.Parse(time.RFC3339, n.UpdatedAt)
	my := classifyInboxState(login, n.reviewClassInput)
	bucket := n.bucket(login, my)
	pr := OpenPR{
		Number:     n.Number,
		Title:      n.Title,
		Author:     n.Author.Login,
		HeadBranch: n.HeadRefName,
		IsDraft:    n.IsDraft,
		Mergeable:  n.Mergeable,
		Checks:     n.reviewClassInput.checks(),
		Review:     classifyReviewClass(n.Author.Login, n.reviewClassInput),
		Bucket:     bucket,
		Gaps:       n.gaps(),
		Updated:    updated,
	}
	if bucket != BucketMine {
		pr.MyReview = my
	}
	return pr
}

const openPRsQuery = `
query($q: String!, $after: String) {
  viewer { login }
  search(query: $q, type: ISSUE, first: 30, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number
        title
        headRefName
        isDraft
        mergeable
        updatedAt
        author { login }
        assignees(first: 20) { nodes { login } }
        reviewRequests(first: 20) { nodes { requestedReviewer { ... on User { login } } } }
        labels(first: 20) { nodes { name } }
        reviewDecision
        reviews(last: 50) { nodes { author { login __typename } state submittedAt } }
        comments(last: 50) { nodes { author { login __typename } createdAt body } }
        reviewThreads(last: 50) { nodes { isResolved comments(first: 50) { nodes { author { login __typename } createdAt } } } }
        commits(last: 1) { nodes { commit { committedDate statusCheckRollup { state } } } }
      }
    }
  }
}`

// OpenPRs lists every open PR in nwo in one paginated GraphQL search, classifying
// each into a bucket (mine / assigned to me / everyone else) and computing full
// status for all of them. Pages are fetched until GitHub reports no more. The
// page is deliberately small (30): GitHub's GraphQL cost is charged on the
// declared page size times the per-PR reviewThreads window, so a smaller page
// is cheaper per call and, with pagination, cheaper overall than one big page.
// Sorted most-recently-updated first.
//
// mergeable commonly comes back "UNKNOWN" right after a push — GitHub computes it
// asynchronously — so callers wanting a resolved value should follow up with
// PRMergeStates rather than treating UNKNOWN as final.
func OpenPRs(ctx context.Context, nwo string) ([]OpenPR, error) {
	if nwo == "" {
		return nil, fmt.Errorf("no repo for open PRs")
	}
	var prs []OpenPR
	var cursor string
	for {
		args := []string{"api", "graphql",
			"-f", "query=" + openPRsQuery,
			"-f", "q=repo:" + nwo + " is:pr is:open"}
		if cursor != "" {
			args = append(args, "-f", "after="+cursor)
		}
		out, err := runner(ctx, args...)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Data struct {
				Viewer struct {
					Login string `json:"login"`
				} `json:"viewer"`
				Search struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []openPRNode `json:"nodes"`
				} `json:"search"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return nil, err
		}
		login := resp.Data.Viewer.Login
		for _, n := range resp.Data.Search.Nodes {
			if n.Number == 0 {
				continue
			}
			prs = append(prs, n.toOpenPR(login))
		}
		if !resp.Data.Search.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Search.PageInfo.EndCursor
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Updated.After(prs[j].Updated) })
	return prs, nil
}

// aliasedPRQuery builds a GraphQL query requesting fields on each of numbers'
// pullRequest via a per-number alias (pr<N>: pullRequest(number: N) {...}) —
// the shared batch-query shape used by both PRMergeStates and
// IssuePRStatuses. Callers must not pass duplicate numbers: a repeated alias
// in the same query is rejected outright, failing the whole batch.
func aliasedPRQuery(numbers []int, fields string) string {
	var b strings.Builder
	for _, n := range numbers {
		fmt.Fprintf(&b, "pr%d: pullRequest(number: %d) { %s }\n", n, n, fields)
	}
	return fmt.Sprintf(`
query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
%s
  }
}`, b.String())
}

// PRMergeStates looks up just the mergeable state for a specific set of PR
// numbers in nwo, via one aliased GraphQL query per pullRequest(number:) — used
// to re-poll the handful of PRs still "UNKNOWN" after OpenPRs without re-fetching
// every open PR's title/checks/etc. Returns a map of PR number to its mergeable
// string ("MERGEABLE", "CONFLICTING", "UNKNOWN").
func PRMergeStates(ctx context.Context, nwo string, numbers []int) (map[int]string, error) {
	if len(numbers) == 0 {
		return nil, nil
	}
	owner, name, ok := strings.Cut(nwo, "/")
	if !ok {
		return nil, fmt.Errorf("invalid repo %q", nwo)
	}
	query := aliasedPRQuery(numbers, "number mergeable")
	out, err := runner(ctx, "api", "graphql",
		"-f", "query="+query,
		"-f", "owner="+owner,
		"-f", "name="+name)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Repository map[string]struct {
				Number    int    `json:"number"`
				Mergeable string `json:"mergeable"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, err
	}
	states := make(map[int]string, len(resp.Data.Repository))
	for _, v := range resp.Data.Repository {
		states[v.Number] = v.Mergeable
	}
	return states, nil
}

// ReviewState classifies where the current user stands on a PR they're involved in.
type ReviewState int

const (
	// NotReviewed means the user has not taken part at all — no review, no comment.
	NotReviewed ReviewState = iota
	// UpdatedSinceReview means the user took part, but a newer commit landed after.
	UpdatedSinceReview
	// RepliedSinceReview means the user took part and no new commit landed, but
	// someone answered them directly afterwards — a reply on a thread they're in,
	// or a conversation comment that @-mentions them.
	RepliedSinceReview
	// ReviewedApproved means the user's latest verdict approved the PR and covers
	// the latest commit.
	ReviewedApproved
	// ReviewedChangesRequested means the user's latest verdict requested changes
	// and covers the latest commit.
	ReviewedChangesRequested
	// ReviewedCommented means the user took part and covers the latest commit but
	// left no approve/changes verdict — only comments, whether as a COMMENTED
	// review or as plain conversation/thread comments.
	ReviewedCommented
)

// classifyInboxState reports where the viewer stands on a PR they didn't author,
// which is also what buckets it (see openPRNode.bucket).
//
// Taking part means a submitted review *or* a comment — a conversation comment or
// a review-thread reply. A long unsubmitted stream of comments is a review in
// every way that matters to the reader, and GitHub's reviews list can't tell one
// from a throwaway remark, so both count the same: the viewer has had their say.
//
// From there the question is only whether anything has landed since their last
// word. A newer commit is UpdatedSinceReview; a direct answer to them (see
// repliedTo) is RepliedSinceReview — both put the PR back in their court.
// Otherwise it's settled and carries whatever verdict their covering review
// held: the newest APPROVED or CHANGES_REQUESTED review (a later bare COMMENTED
// review doesn't overturn it, matching GitHub's own review decision), falling
// through to ReviewedCommented when they only ever commented.
func classifyInboxState(login string, n reviewClassInput) ReviewState {
	var myLatest, myVerdictAt time.Time
	var myVerdict string
	// bumpMine folds one of my timestamps into myLatest and hands it back, so the
	// reviews loop can go on to weigh the same parsed time as a verdict. An
	// unparseable stamp comes back zero and is skipped by every caller.
	bumpMine := func(ts string) time.Time {
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return time.Time{}
		}
		if t.After(myLatest) {
			myLatest = t
		}
		return t
	}
	for _, r := range n.Reviews.Nodes {
		if !strings.EqualFold(r.Author.Login, login) {
			continue
		}
		t := bumpMine(r.SubmittedAt)
		if t.IsZero() {
			continue
		}
		if (r.State == "APPROVED" || r.State == "CHANGES_REQUESTED") && t.After(myVerdictAt) {
			myVerdictAt = t
			myVerdict = r.State
		}
	}
	for _, c := range n.Comments.Nodes {
		if strings.EqualFold(c.Author.Login, login) {
			bumpMine(c.CreatedAt)
		}
	}
	for _, th := range n.ReviewThreads.Nodes {
		for _, c := range th.Comments.Nodes {
			if strings.EqualFold(c.Author.Login, login) {
				bumpMine(c.CreatedAt)
			}
		}
	}
	if myLatest.IsZero() {
		return NotReviewed
	}
	var lastCommit time.Time
	if len(n.Commits.Nodes) > 0 {
		lastCommit, _ = time.Parse(time.RFC3339, n.Commits.Nodes[0].Commit.CommittedDate)
	}
	if lastCommit.After(myLatest) {
		return UpdatedSinceReview
	}
	if repliedTo(login, n, myLatest) {
		return RepliedSinceReview
	}
	switch myVerdict {
	case "APPROVED":
		return ReviewedApproved
	case "CHANGES_REQUESTED":
		return ReviewedChangesRequested
	default:
		return ReviewedCommented
	}
}

// repliedTo reports whether another human answered the viewer directly after
// since. Two shapes count, and only these two: a later comment by someone else on
// an unresolved review thread the viewer has spoken in (a resolved thread is
// settled, so a reply buried in one isn't waiting on them), and a conversation
// comment that @-mentions them. GitHub's conversation comments carry no parent
// link, so the @-mention is the only structural evidence there that a comment is
// aimed at the viewer rather than at each other — a third party's unrelated
// comment deliberately doesn't drag the PR back into the viewer's court.
func repliedTo(login string, n reviewClassInput, since time.Time) bool {
	after := func(ts string) bool {
		t, err := time.Parse(time.RFC3339, ts)
		return err == nil && t.After(since)
	}
	for _, th := range n.ReviewThreads.Nodes {
		if th.IsResolved {
			continue
		}
		mine := false
		for _, c := range th.Comments.Nodes {
			if strings.EqualFold(c.Author.Login, login) {
				mine = true
			}
		}
		if !mine {
			continue
		}
		for _, c := range th.Comments.Nodes {
			if strings.EqualFold(c.Author.Login, login) || isBotActor(c.Author.Login, c.Author.Typename) {
				continue
			}
			if after(c.CreatedAt) {
				return true
			}
		}
	}
	for _, c := range n.Comments.Nodes {
		if strings.EqualFold(c.Author.Login, login) || isBotActor(c.Author.Login, c.Author.Typename) {
			continue
		}
		if after(c.CreatedAt) && mentions(c.Body, login) {
			return true
		}
	}
	return false
}

// mentions reports whether body @-mentions login, matched case-insensitively and
// only on a whole handle standing on its own: "@pierce-bot" is a longer handle,
// and "bob@pierce.dev" is an address, so neither is a mention of "@pierce". Both
// sides have to be checked — an unguarded trailing test alone reads every email
// address in a comment as a summons.
func mentions(body, login string) bool {
	if login == "" {
		return false
	}
	at := "@" + strings.ToLower(login)
	hay := strings.ToLower(body)
	for i := 0; ; {
		j := strings.Index(hay[i:], at)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(at)
		lead := start == 0 || !isHandleByte(hay[start-1])
		tail := end == len(hay) || !isHandleByte(hay[end])
		if lead && tail {
			return true
		}
		i = end
	}
}

// isHandleByte reports whether c can continue a GitHub handle, so mentions can
// tell the end of one from the start of a longer name.
func isHandleByte(c byte) bool {
	return c == '-' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

const openPRByNumberQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  viewer { login }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number
      title
      headRefName
      isDraft
      mergeable
      updatedAt
      author { login }
      assignees(first: 20) { nodes { login } }
      reviewRequests(first: 20) { nodes { requestedReviewer { ... on User { login } } } }
      labels(first: 20) { nodes { name } }
      reviewDecision
      reviews(last: 50) { nodes { author { login __typename } state submittedAt } }
      comments(last: 50) { nodes { author { login __typename } createdAt body } }
      reviewThreads(first: 50) { nodes { isResolved comments(first: 50) { nodes { author { login __typename } createdAt } } } }
      commits(last: 1) { nodes { commit { committedDate statusCheckRollup { state } } } }
    }
  }
}`

// OpenPRByNumber re-fetches a single open PR's full status and bucket — the
// row-scoped `r` refresh on the PRs tab, cheaper than re-running OpenPRs for the
// whole repo. It recomputes the bucket too, so a PR newly assigned to (or
// un-assigned from) the viewer moves to the right section on refresh.
func OpenPRByNumber(ctx context.Context, nwo string, number int) (OpenPR, error) {
	owner, name, ok := strings.Cut(nwo, "/")
	if !ok {
		return OpenPR{}, fmt.Errorf("invalid repo %q", nwo)
	}
	out, err := runner(ctx, "api", "graphql",
		"-f", "query="+openPRByNumberQuery,
		"-f", "owner="+owner,
		"-f", "name="+name,
		"-F", "number="+strconv.Itoa(number))
	if err != nil {
		return OpenPR{}, err
	}
	var resp struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
			Repository struct {
				PullRequest openPRNode `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return OpenPR{}, err
	}
	n := resp.Data.Repository.PullRequest
	if n.Number == 0 {
		return OpenPR{}, fmt.Errorf("pr #%d not found", number)
	}
	return n.toOpenPR(resp.Data.Viewer.Login), nil
}

// ReviewClass is where one of the user's own open PRs stands, from their POV.
type ReviewClass int

const (
	// ReviewInProgress means the PR is out for review with nothing for the author to do.
	ReviewInProgress ReviewClass = iota
	// ReviewNeedsResponse means a human left a comment (conversation, a COMMENTED
	// review, or an unresolved inline thread) newer than the author's last action,
	// without formally requesting changes — the author owes a reply or a fix.
	ReviewNeedsResponse
	// ReviewChangesRequested means an outstanding change request the author hasn't answered.
	ReviewChangesRequested
	// ReviewApproved means the PR is approved and ready to merge.
	ReviewApproved
)

// reviewActor is a GitHub actor (login + type), used to filter out bot noise
// when classifying review/comment activity.
type reviewActor struct {
	Login    string `json:"login"`
	Typename string `json:"__typename"`
}

// reviewClassInput is the shared review/comment/commit shape queried for every
// open PR (OpenPRs and OpenPRByNumber). It feeds classifyReviewClass (the author-
// POV status), classifyInboxState (the viewer's own review standing), and checks()
// (the last commit's CI rollup).
type reviewClassInput struct {
	ReviewDecision string `json:"reviewDecision"`
	Reviews        struct {
		Nodes []struct {
			Author      reviewActor `json:"author"`
			State       string      `json:"state"`
			SubmittedAt string      `json:"submittedAt"`
		} `json:"nodes"`
	} `json:"reviews"`
	Comments struct {
		Nodes []struct {
			Author    reviewActor `json:"author"`
			CreatedAt string      `json:"createdAt"`
			// Body is read only to spot an @-mention of the viewer, the one
			// structural clue that a conversation comment answers them (see repliedTo).
			// Bodies are the one bulky field in this query — on a 17-open-PR repo they
			// take a page from 7.6 KB to 170 KB — but GitHub charges them no GraphQL
			// points, and the request measures within noise of the same one without
			// them (~0.66 s vs ~0.60 s, three runs each), so they ride the existing
			// connection. Pulling them on a narrower aliased connection was measured
			// and is strictly worse: identical bytes, more nodes, one point dearer.
			Body string `json:"body"`
		} `json:"nodes"`
	} `json:"comments"`
	ReviewThreads struct {
		Nodes []struct {
			IsResolved bool `json:"isResolved"`
			Comments   struct {
				Nodes []struct {
					Author    reviewActor `json:"author"`
					CreatedAt string      `json:"createdAt"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				CommittedDate     string `json:"committedDate"`
				StatusCheckRollup struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// checks maps the last commit's CI rollup to a CheckState.
func (n reviewClassInput) checks() CheckState {
	if len(n.Commits.Nodes) == 0 {
		return ChecksNone
	}
	return checkStateFromString(n.Commits.Nodes[0].Commit.StatusCheckRollup.State)
}

// classifyReviewClass runs the "what does the author need to do next"
// classification, shared across every open PR by OpenPRs/OpenPRByNumber. Run with
// the PR's own author as me, it yields that PR's status from the author's POV.
//
// lastMine is the author's newest action (commit or comment/reply) — it clears
// feedback. lastFormal is the newest unanswered CHANGES_REQUESTED; lastInformal
// is the newest unanswered human comment short of that. Bot comments never
// count as feedback.
//
// The query fetches the 50 newest review threads (reviewThreads returns
// oldest-first, so we take the last 50). It was 15 while this classifier was the
// only reader and a miss merely softened a "needs response"; repliedTo now reads
// the same threads to decide whether a PR is back in the viewer's court, where a
// miss strands it in the wrong section. Measured on a 17-open-PR repo, the widening
// costs 6 → 16 GraphQL points per page against a 5,000/hour budget.
func classifyReviewClass(me string, n reviewClassInput) ReviewClass {
	var lastMine, lastFormal, lastInformal time.Time
	bump := func(t *time.Time, ts string) {
		if v, err := time.Parse(time.RFC3339, ts); err == nil && v.After(*t) {
			*t = v
		}
	}
	for _, r := range n.Reviews.Nodes {
		switch {
		case strings.EqualFold(r.Author.Login, me):
			bump(&lastMine, r.SubmittedAt)
		case isBotActor(r.Author.Login, r.Author.Typename):
			// skip bot reviews
		case r.State == "CHANGES_REQUESTED":
			bump(&lastFormal, r.SubmittedAt)
		case r.State == "COMMENTED":
			bump(&lastInformal, r.SubmittedAt)
		}
	}
	for _, c := range n.Comments.Nodes {
		switch {
		case strings.EqualFold(c.Author.Login, me):
			bump(&lastMine, c.CreatedAt)
		case !isBotActor(c.Author.Login, c.Author.Typename):
			bump(&lastInformal, c.CreatedAt)
		}
	}
	for _, th := range n.ReviewThreads.Nodes {
		if th.IsResolved {
			continue
		}
		for _, c := range th.Comments.Nodes {
			switch {
			case strings.EqualFold(c.Author.Login, me):
				bump(&lastMine, c.CreatedAt)
			case !isBotActor(c.Author.Login, c.Author.Typename):
				bump(&lastInformal, c.CreatedAt)
			}
		}
	}
	if len(n.Commits.Nodes) > 0 {
		bump(&lastMine, n.Commits.Nodes[0].Commit.CommittedDate)
	}
	switch {
	case !lastFormal.IsZero() && lastFormal.After(lastMine):
		return ReviewChangesRequested
	case !lastInformal.IsZero() && lastInformal.After(lastMine):
		return ReviewNeedsResponse
	case n.ReviewDecision == "APPROVED":
		return ReviewApproved
	default:
		return ReviewInProgress
	}
}

// isBotActor reports whether a GitHub actor is an automation account rather than
// a human — either a Bot-typed actor or a login with the conventional [bot] suffix.
func isBotActor(login, typename string) bool {
	return typename == "Bot" || strings.HasSuffix(login, "[bot]")
}

// IssuePR is the linked PR's status for an issue's "PR open" stage — checks,
// draft, and mergeability only. Review classification (needs-response /
// changes-requested / approved) is specific to PRs the viewer authored (see
// ReviewClass) and doesn't generalize to a linked PR someone else opened, so
// it's left out here.
type IssuePR struct {
	Number    int
	IsDraft   bool
	Mergeable string
	Checks    CheckState
}

// IssueItem is one open issue in the repo.
type IssueItem struct {
	Number    int
	Title     string
	Labels    []string
	Updated   time.Time
	Assignees []string // GitHub logins currently assigned; empty if unassigned
	PR        *IssuePR // the linked open PR's status, nil if none
}

// timelineItemsField is the shared "what PR is connected to this issue" shape
// queried for issues (AllOpenIssues and IssueByNumber).
type timelineItemsField struct {
	Nodes []struct {
		Typename string `json:"__typename"`
		Subject  struct {
			Number int    `json:"number"`
			State  string `json:"state"`
		} `json:"subject"`
		Source struct {
			Number int    `json:"number"`
			State  string `json:"state"`
		} `json:"source"`
	} `json:"nodes"`
}

// openPR returns the number of the most recently linked OPEN PR connected to
// or cross-referencing this issue, or 0 if none. A closed/merged reference
// doesn't count — it's no longer "in progress" from this issue's POV.
func (t timelineItemsField) openPR() int {
	var linkedPR int
	for _, n := range t.Nodes {
		var num int
		var state string
		switch n.Typename {
		case "ConnectedEvent":
			num, state = n.Subject.Number, n.Subject.State
		case "CrossReferencedEvent":
			num, state = n.Source.Number, n.Source.State
		}
		if num != 0 && state == "OPEN" {
			linkedPR = num // last match wins — timelineItems is chronological
		}
	}
	return linkedPR
}

const issuesQuery = `
query($q: String!, $after: String) {
  viewer { login }
  search(query: $q, type: ISSUE, first: 100, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on Issue {
        number
        title
        updatedAt
        labels(first: 10) { nodes { name } }
        assignees(first: 5) { nodes { login } }
        timelineItems(last: 10, itemTypes: [CONNECTED_EVENT, CROSS_REFERENCED_EVENT]) {
          nodes {
            __typename
            ... on ConnectedEvent { subject { ... on PullRequest { number state } } }
            ... on CrossReferencedEvent { source { ... on PullRequest { number state } } }
          }
        }
      }
    }
  }
}`

// maxIssuePages caps AllOpenIssues' pagination loop as a runaway guard, not a
// coverage limit — 100 pages of 100 issues (10,000 open issues) is far beyond
// any repo this dashboard targets; it exists only so a malformed API response
// (hasNextPage stuck true) can't loop forever.
const maxIssuePages = 100

// AllOpenIssues returns every open issue in nwo, with its assignees and the
// number of any open PR that references/would close it, paging through the
// full result set rather than silently truncating past the first 100. A PR's
// full status (checks/draft/mergeable) is resolved separately by
// IssuePRStatuses so this stays proportional to the number of pages, not the
// number of linked PRs. Also returns the viewer's own login, so callers can
// classify each issue's assignee group (mine / unassigned / someone else's)
// without a second query.
func AllOpenIssues(ctx context.Context, nwo string) ([]IssueItem, string, error) {
	if nwo == "" {
		return nil, "", fmt.Errorf("no repo for issues")
	}
	type node struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		UpdatedAt string `json:"updatedAt"`
		Labels    struct {
			Nodes []struct {
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"labels"`
		Assignees struct {
			Nodes []struct {
				Login string `json:"login"`
			} `json:"nodes"`
		} `json:"assignees"`
		TimelineItems timelineItemsField `json:"timelineItems"`
	}

	var items []IssueItem
	var viewerLogin string
	cursor := ""
	for page := 0; page < maxIssuePages; page++ {
		args := []string{"api", "graphql",
			"-f", "query=" + issuesQuery,
			"-f", "q=repo:" + nwo + " is:issue is:open"}
		if cursor != "" {
			args = append(args, "-f", "after="+cursor)
		}
		out, err := runner(ctx, args...)
		if err != nil {
			return nil, "", err
		}
		var resp struct {
			Data struct {
				Viewer struct {
					Login string `json:"login"`
				} `json:"viewer"`
				Search struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []node `json:"nodes"`
				} `json:"search"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return nil, "", err
		}
		viewerLogin = resp.Data.Viewer.Login
		for _, n := range resp.Data.Search.Nodes {
			if n.Number == 0 {
				continue
			}
			var labels []string
			for _, l := range n.Labels.Nodes {
				labels = append(labels, l.Name)
			}
			var assignees []string
			for _, a := range n.Assignees.Nodes {
				assignees = append(assignees, a.Login)
			}
			updated, _ := time.Parse(time.RFC3339, n.UpdatedAt)
			item := IssueItem{
				Number:    n.Number,
				Title:     n.Title,
				Labels:    labels,
				Updated:   updated,
				Assignees: assignees,
			}
			if linkedPR := n.TimelineItems.openPR(); linkedPR != 0 {
				item.PR = &IssuePR{Number: linkedPR}
			}
			items = append(items, item)
		}
		if !resp.Data.Search.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Search.PageInfo.EndCursor
	}
	return items, viewerLogin, nil
}

// issueLabelListLimit caps how many open issues OpenIssuesByLabel asks gh
// for in one call. Unlike AllOpenIssues' paginated GraphQL search, this is a
// single `gh issue list --label` call, so the cap is passed straight through
// as --limit rather than looped — a runaway guard (matching maxIssuePages'
// role), not a coverage limit any label on the repos this dashboard targets
// would realistically reach.
const issueLabelListLimit = 500

// OpenIssuesByLabel returns the open issues in nwo carrying label, via a
// plain `gh issue list --label` call — the resolver
// internal/kinds/workflowquery.go's ResolveQuery calls through for a
// github-tracked project's `over = { query = "label:<name>" }` workflow.
// Only identity fields are populated (Number/Title/Labels); Assignees and PR
// are left zero, since the query resolver only needs to know which issues
// matched. Returns a non-nil empty slice, not an error, when nothing does.
func OpenIssuesByLabel(ctx context.Context, nwo, label string) ([]IssueItem, error) {
	if nwo == "" {
		return nil, fmt.Errorf("no repo for issues")
	}
	out, err := runner(ctx, "issue", "list",
		"--repo", nwo,
		"--state", "open",
		"--label", label,
		"--json", "number,title,labels",
		"--limit", strconv.Itoa(issueLabelListLimit))
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, err
	}
	items := make([]IssueItem, 0, len(raw))
	for _, r := range raw {
		var labels []string
		for _, l := range r.Labels {
			labels = append(labels, l.Name)
		}
		items = append(items, IssueItem{Number: r.Number, Title: r.Title, Labels: labels})
	}
	return items, nil
}

// issuePRStatusBatchSize caps how many pullRequest aliases go into one
// IssuePRStatuses query — well under GitHub's query-complexity limit, so a
// fetch with many simultaneously-linked PRs can't fail the whole batch.
const issuePRStatusBatchSize = 50

// IssuePRStatuses resolves checks/draft/mergeable for a set of PR numbers
// linked from open issues, chunked into aliased GraphQL calls of at most
// issuePRStatusBatchSize each — avoids one query per issue when filling in
// the "PR open" stage, while keeping any single query's alias count bounded.
func IssuePRStatuses(ctx context.Context, nwo string, numbers []int) (map[int]IssuePR, error) {
	if len(numbers) == 0 {
		return nil, nil
	}
	owner, name, ok := strings.Cut(nwo, "/")
	if !ok {
		return nil, fmt.Errorf("invalid repo %q", nwo)
	}
	// De-dupe: a single PR can close more than one issue, and a duplicate
	// alias in the same GraphQL query is rejected outright, failing the
	// whole batch.
	seen := make(map[int]bool, len(numbers))
	deduped := make([]int, 0, len(numbers))
	for _, n := range numbers {
		if seen[n] {
			continue
		}
		seen[n] = true
		deduped = append(deduped, n)
	}

	states := make(map[int]IssuePR, len(deduped))
	for start := 0; start < len(deduped); start += issuePRStatusBatchSize {
		end := min(start+issuePRStatusBatchSize, len(deduped))
		batch, err := issuePRStatusBatch(ctx, owner, name, deduped[start:end])
		if err != nil {
			return states, err
		}
		for k, v := range batch {
			states[k] = v
		}
	}
	return states, nil
}

func issuePRStatusBatch(ctx context.Context, owner, name string, numbers []int) (map[int]IssuePR, error) {
	query := aliasedPRQuery(numbers, "number isDraft mergeable commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }")
	out, err := runner(ctx, "api", "graphql",
		"-f", "query="+query,
		"-f", "owner="+owner,
		"-f", "name="+name)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Repository map[string]struct {
				Number    int          `json:"number"`
				IsDraft   bool         `json:"isDraft"`
				Mergeable string       `json:"mergeable"`
				Commits   commitRollup `json:"commits"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, err
	}
	states := make(map[int]IssuePR, len(resp.Data.Repository))
	for _, v := range resp.Data.Repository {
		states[v.Number] = IssuePR{
			Number:    v.Number,
			IsDraft:   v.IsDraft,
			Mergeable: v.Mergeable,
			Checks:    checksFromRollup(v.Commits),
		}
	}
	return states, nil
}

const issueByNumberQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) {
      number
      title
      state
      updatedAt
      labels(first: 10) { nodes { name } }
      assignees(first: 5) { nodes { login } }
      timelineItems(last: 10, itemTypes: [CONNECTED_EVENT, CROSS_REFERENCED_EVENT]) {
        nodes {
          __typename
          ... on ConnectedEvent { subject { ... on PullRequest { number state } } }
          ... on CrossReferencedEvent { source { ... on PullRequest { number state } } }
        }
      }
    }
  }
}`

// IssueByNumber re-fetches a single issue's assignees and linked-PR status —
// the row-scoped `r` refresh on the Issues tab, cheaper than re-running
// AllOpenIssues for the whole repo. ok is false when the issue has since
// closed, meaning it no longer belongs in the (open-only) Issues view at
// all — the caller should drop the row instead of updating it.
func IssueByNumber(ctx context.Context, nwo string, number int) (item IssueItem, ok bool, err error) {
	owner, name, valid := strings.Cut(nwo, "/")
	if !valid {
		return IssueItem{}, false, fmt.Errorf("invalid repo %q", nwo)
	}
	out, err := runner(ctx, "api", "graphql",
		"-f", "query="+issueByNumberQuery,
		"-f", "owner="+owner,
		"-f", "name="+name,
		"-F", "number="+strconv.Itoa(number))
	if err != nil {
		return IssueItem{}, false, err
	}
	var resp struct {
		Data struct {
			Repository struct {
				Issue struct {
					Number    int    `json:"number"`
					Title     string `json:"title"`
					State     string `json:"state"`
					UpdatedAt string `json:"updatedAt"`
					Labels    struct {
						Nodes []struct {
							Name string `json:"name"`
						} `json:"nodes"`
					} `json:"labels"`
					Assignees struct {
						Nodes []struct {
							Login string `json:"login"`
						} `json:"nodes"`
					} `json:"assignees"`
					TimelineItems timelineItemsField `json:"timelineItems"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return IssueItem{}, false, err
	}
	iss := resp.Data.Repository.Issue
	if iss.Number == 0 {
		return IssueItem{}, false, fmt.Errorf("issue #%d not found", number)
	}
	if iss.State != "OPEN" {
		return IssueItem{}, false, nil
	}
	var labels []string
	for _, l := range iss.Labels.Nodes {
		labels = append(labels, l.Name)
	}
	var assignees []string
	for _, a := range iss.Assignees.Nodes {
		assignees = append(assignees, a.Login)
	}
	updated, _ := time.Parse(time.RFC3339, iss.UpdatedAt)
	item = IssueItem{
		Number:    iss.Number,
		Title:     iss.Title,
		Labels:    labels,
		Updated:   updated,
		Assignees: assignees,
	}
	if linkedPR := iss.TimelineItems.openPR(); linkedPR != 0 {
		// Set the placeholder immediately, same as AllOpenIssues — a failure to
		// resolve its full status (checks/draft/mergeable) below should degrade
		// to "PR open, status unknown", not silently lose the PR link entirely.
		item.PR = &IssuePR{Number: linkedPR}
		if statuses, err := IssuePRStatuses(ctx, nwo, []int{linkedPR}); err == nil {
			if pr, ok := statuses[linkedPR]; ok {
				item.PR = &pr
			}
		}
	}
	return item, true, nil
}

// runner is the seam every command in this package goes through. It exists so a
// test can record the argv a call builds: a dropped `--repo`, `--json` field or
// `--limit` is otherwise invisible, since the arguments never leave the process
// in a form anything can read. See argv_test.go.
var runner = run

func run(ctx context.Context, args ...string) (string, error) {
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return string(out), nil
}
