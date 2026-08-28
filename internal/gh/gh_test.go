package gh

import (
	"encoding/json"
	"testing"
)

// mustInput builds a reviewClassInput from JSON so tests exercise the same shape
// the GraphQL responses unmarshal into, rather than hand-spelling the anonymous
// node structs (tags and all).
func mustInput(t *testing.T, j string) reviewClassInput {
	t.Helper()
	var n reviewClassInput
	if err := json.Unmarshal([]byte(j), &n); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return n
}

func TestClassifyInboxState(t *testing.T) {
	const me = "pierce"
	// t1 < commit < t3 < t4, so a review at t1 predates the commit, one at t3
	// covers it, and anything at t4 lands after the viewer's last word.
	const (
		t1     = "2026-01-01T00:00:00Z"
		commit = "2026-01-02T00:00:00Z"
		t3     = "2026-01-03T00:00:00Z"
		t4     = "2026-01-04T00:00:00Z"
	)
	commits := `"commits":{"nodes":[{"commit":{"committedDate":"` + commit + `"}}]}`

	tests := []struct {
		name string
		json string
		want ReviewState
	}{
		{
			name: "no reviews",
			json: `{` + commits + `}`,
			want: NotReviewed,
		},
		{
			name: "someone else reviewed, not me",
			json: `{"reviews":{"nodes":[{"author":{"login":"alice"},"state":"APPROVED","submittedAt":"` + t3 + `"}]},` + commits + `}`,
			want: NotReviewed,
		},
		{
			name: "approved and covers latest commit",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + t3 + `"}]},` + commits + `}`,
			want: ReviewedApproved,
		},
		{
			name: "changes requested and covers latest commit",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"CHANGES_REQUESTED","submittedAt":"` + t3 + `"}]},` + commits + `}`,
			want: ReviewedChangesRequested,
		},
		{
			name: "only commented",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"COMMENTED","submittedAt":"` + t3 + `"}]},` + commits + `}`,
			want: ReviewedCommented,
		},
		{
			name: "reviewed before a newer commit landed",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + t1 + `"}]},` + commits + `}`,
			want: UpdatedSinceReview,
		},
		{
			name: "newest verdict wins over an earlier one",
			json: `{"reviews":{"nodes":[` +
				`{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + t1 + `"},` +
				`{"author":{"login":"pierce"},"state":"CHANGES_REQUESTED","submittedAt":"` + t3 + `"}]},` + commits + `}`,
			want: ReviewedChangesRequested,
		},
		{
			name: "later bare comment does not overturn the verdict",
			json: `{"reviews":{"nodes":[` +
				`{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + t3 + `"},` +
				`{"author":{"login":"pierce"},"state":"COMMENTED","submittedAt":"` + t3 + `"}]},` + commits + `}`,
			want: ReviewedApproved,
		},
		// A comment with no submitted review is still having had your say: GitHub's
		// reviews list can't tell a long unsubmitted review from a throwaway remark.
		{
			name: "conversation comment with no review counts as taking part",
			json: `{"comments":{"nodes":[{"author":{"login":"pierce"},"createdAt":"` + t3 + `"}]},` + commits + `}`,
			want: ReviewedCommented,
		},
		{
			name: "review-thread comment with no review counts as taking part",
			json: `{"reviewThreads":{"nodes":[{"isResolved":false,"comments":{"nodes":[` +
				`{"author":{"login":"pierce"},"createdAt":"` + t3 + `"}]}}]},` + commits + `}`,
			want: ReviewedCommented,
		},
		{
			name: "someone else's comment is not me taking part",
			json: `{"comments":{"nodes":[{"author":{"login":"alice"},"createdAt":"` + t3 + `"}]},` + commits + `}`,
			want: NotReviewed,
		},
		{
			name: "commented before a newer commit landed",
			json: `{"comments":{"nodes":[{"author":{"login":"pierce"},"createdAt":"` + t1 + `"}]},` + commits + `}`,
			want: UpdatedSinceReview,
		},
		{
			name: "reply on an unresolved thread I'm in puts it back on me",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"CHANGES_REQUESTED","submittedAt":"` + t3 + `"}]},` +
				`"reviewThreads":{"nodes":[{"isResolved":false,"comments":{"nodes":[` +
				`{"author":{"login":"pierce"},"createdAt":"` + t3 + `"},` +
				`{"author":{"login":"alice"},"createdAt":"` + t4 + `"}]}}]},` + commits + `}`,
			want: RepliedSinceReview,
		},
		{
			name: "reply on a resolved thread is settled, not on me",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"CHANGES_REQUESTED","submittedAt":"` + t3 + `"}]},` +
				`"reviewThreads":{"nodes":[{"isResolved":true,"comments":{"nodes":[` +
				`{"author":{"login":"pierce"},"createdAt":"` + t3 + `"},` +
				`{"author":{"login":"alice"},"createdAt":"` + t4 + `"}]}}]},` + commits + `}`,
			want: ReviewedChangesRequested,
		},
		{
			name: "reply on an unresolved thread I never spoke in is not mine",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + t3 + `"}]},` +
				`"reviewThreads":{"nodes":[{"isResolved":false,"comments":{"nodes":[` +
				`{"author":{"login":"bob"},"createdAt":"` + t3 + `"},` +
				`{"author":{"login":"alice"},"createdAt":"` + t4 + `"}]}}]},` + commits + `}`,
			want: ReviewedApproved,
		},
		// Conversation comments carry no parent link, so only an @-mention marks one
		// as aimed at the viewer; others talking among themselves doesn't summon them.
		{
			name: "conversation comment mentioning me puts it back on me",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + t3 + `"}]},` +
				`"comments":{"nodes":[{"author":{"login":"alice"},"createdAt":"` + t4 + `","body":"good point @Pierce, but"}]},` + commits + `}`,
			want: RepliedSinceReview,
		},
		{
			name: "conversation comment not mentioning me leaves it settled",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + t3 + `"}]},` +
				`"comments":{"nodes":[{"author":{"login":"alice"},"createdAt":"` + t4 + `","body":"@bob can you look"}]},` + commits + `}`,
			want: ReviewedApproved,
		},
		{
			name: "my handle inside an email address is not a mention",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + t3 + `"}]},` +
				`"comments":{"nodes":[{"author":{"login":"alice"},"createdAt":"` + t4 + `","body":"mail bob@pierce.dev about it"}]},` + commits + `}`,
			want: ReviewedApproved,
		},
		{
			name: "mention at the very start of a comment counts",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + t3 + `"}]},` +
				`"comments":{"nodes":[{"author":{"login":"alice"},"createdAt":"` + t4 + `","body":"@pierce ping"}]},` + commits + `}`,
			want: RepliedSinceReview,
		},
		{
			name: "mention of a longer handle is not a mention of me",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + t3 + `"}]},` +
				`"comments":{"nodes":[{"author":{"login":"alice"},"createdAt":"` + t4 + `","body":"cc @pierce-bot"}]},` + commits + `}`,
			want: ReviewedApproved,
		},
		{
			name: "a bot mentioning me does not put it back on me",
			json: `{"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + t3 + `"}]},` +
				`"comments":{"nodes":[{"author":{"login":"ci[bot]","__typename":"Bot"},"createdAt":"` + t4 + `","body":"@pierce build failed"}]},` + commits + `}`,
			want: ReviewedApproved,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyInboxState(me, mustInput(t, tt.json))
			if got != tt.want {
				t.Errorf("classifyInboxState = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBucket pins the rule that decides which PRs section a row lands in: the
// viewer's own activity, not GitHub's review-request list. GitHub deletes a
// review request the instant that reviewer submits a review, so bucketing on
// requests alone ejected every PR from the viewer's section the moment they
// reviewed it — and then relabelled its verdict with the author's POV, so their
// own "changes requested" read as work they owed. Each case below is a real PR
// shape that produced that bug.
func TestBucket(t *testing.T) {
	const me = "pierce"
	const (
		commit = "2026-01-02T00:00:00Z"
		after  = "2026-01-03T00:00:00Z"
		later  = "2026-01-04T00:00:00Z"
	)
	commits := `"commits":{"nodes":[{"commit":{"committedDate":"` + commit + `"}}]}`

	tests := []struct {
		name string
		json string
		want PRBucket
	}{
		{
			name: "I authored it",
			json: `{"author":{"login":"pierce"},` + commits + `}`,
			want: BucketMine,
		},
		{
			name: "requested and untouched",
			json: `{"author":{"login":"alice"},` +
				`"reviewRequests":{"nodes":[{"requestedReviewer":{"login":"pierce"}}]},` + commits + `}`,
			want: BucketAssigned,
		},
		{
			name: "assigned and untouched",
			json: `{"author":{"login":"alice"},` +
				`"assignees":{"nodes":[{"login":"pierce"}]},` + commits + `}`,
			want: BucketAssigned,
		},
		{
			name: "no relationship at all",
			json: `{"author":{"login":"alice"},` + commits + `}`,
			want: BucketOther,
		},
		// I approved, so GitHub dropped me from reviewRequests. It is waiting on the
		// other reviewer, not on me.
		{
			name: "approved, request cleared",
			json: `{"author":{"login":"alice"},` +
				`"reviewRequests":{"nodes":[{"requestedReviewer":{"login":"bob"}}]},` +
				`"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + after + `"}]},` + commits + `}`,
			want: BucketReviewed,
		},
		// My own changes-requested: the author owes the work, so this must not sit
		// in the section of things needing me.
		{
			name: "I requested changes, request cleared",
			json: `{"author":{"login":"alice"},` +
				`"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"CHANGES_REQUESTED","submittedAt":"` + after + `"}]},` + commits + `}`,
			want: BucketReviewed,
		},
		// Commented without ever submitting a review, and still formally requested:
		// having had my say wins over GitHub's standing request.
		{
			name: "commented while still requested",
			json: `{"author":{"login":"alice"},` +
				`"reviewRequests":{"nodes":[{"requestedReviewer":{"login":"pierce"}}]},` +
				`"comments":{"nodes":[{"author":{"login":"pierce"},"createdAt":"` + after + `"}]},` + commits + `}`,
			want: BucketReviewed,
		},
		// Assignment is a claim on my time that a comment doesn't discharge, and
		// GitHub never clears it on my behalf the way it clears a review request.
		{
			name: "assigned, and I commented",
			json: `{"author":{"login":"alice"},` +
				`"assignees":{"nodes":[{"login":"pierce"}]},` +
				`"comments":{"nodes":[{"author":{"login":"pierce"},"createdAt":"` + after + `"}]},` + commits + `}`,
			want: BucketAssigned,
		},
		// A verdict is where assignment stops mattering: I have ruled on the PR, so
		// the next move is the author's however I came to be on it.
		{
			name: "assigned, and I approved",
			json: `{"author":{"login":"alice"},` +
				`"assignees":{"nodes":[{"login":"pierce"}]},` +
				`"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + after + `"}]},` + commits + `}`,
			want: BucketReviewed,
		},
		{
			name: "assigned, and I requested changes",
			json: `{"author":{"login":"alice"},` +
				`"assignees":{"nodes":[{"login":"pierce"}]},` +
				`"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"CHANGES_REQUESTED","submittedAt":"` + after + `"}]},` + commits + `}`,
			want: BucketReviewed,
		},
		// A new commit landed after my approval, so the approval no longer covers the
		// PR: the assignment is live again.
		{
			name: "assigned, approved, then a new commit landed",
			json: `{"author":{"login":"alice"},` +
				`"assignees":{"nodes":[{"login":"pierce"}]},` +
				`"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"2026-01-01T00:00:00Z"}]},` + commits + `}`,
			want: BucketAssigned,
		},
		{
			name: "reviewed, then a new commit landed",
			json: `{"author":{"login":"alice"},` +
				`"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"2026-01-01T00:00:00Z"}]},` + commits + `}`,
			want: BucketAssigned,
		},
		{
			name: "reviewed, then someone replied to me",
			json: `{"author":{"login":"alice"},` +
				`"reviews":{"nodes":[{"author":{"login":"pierce"},"state":"APPROVED","submittedAt":"` + after + `"}]},` +
				`"comments":{"nodes":[{"author":{"login":"alice"},"createdAt":"` + later + `","body":"@pierce thoughts?"}]},` + commits + `}`,
			want: BucketAssigned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var n openPRNode
			if err := json.Unmarshal([]byte(tt.json), &n); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			got := n.bucket(me, classifyInboxState(me, n.reviewClassInput))
			if got != tt.want {
				t.Errorf("bucket = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNumberPayloadRef pins the issue-vs-PR call the whole bare-#N path turns on:
// the REST issues endpoint serves both kinds, and only the pull_request field
// tells them apart — in every state, which is the point of resolving #N instead
// of trying one type and falling back to the other.
func TestNumberPayloadRef(t *testing.T) {
	tests := []struct {
		name string
		json string
		want NumberRef
	}{
		{
			name: "open issue",
			json: `{"number":42,"title":"Fix the thing","state":"open"}`,
			want: NumberRef{Number: 42, IsPR: false, Title: "Fix the thing", State: "open"},
		},
		{
			name: "closed issue",
			json: `{"number":42,"title":"Fix the thing","state":"closed"}`,
			want: NumberRef{Number: 42, IsPR: false, Title: "Fix the thing", State: "closed"},
		},
		{
			name: "explicit null pull_request is still an issue",
			json: `{"number":42,"title":"Fix the thing","state":"open","pull_request":null}`,
			want: NumberRef{Number: 42, IsPR: false, Title: "Fix the thing", State: "open"},
		},
		{
			name: "open pr",
			json: `{"number":7,"title":"Add the thing","state":"open","pull_request":{"url":"https://api.github.com/repos/o/r/pulls/7"}}`,
			want: NumberRef{Number: 7, IsPR: true, Title: "Add the thing", State: "open"},
		},
		{
			name: "merged pr is reported as merged, not closed",
			json: `{"number":7,"title":"Add the thing","state":"closed","pull_request":{"merged_at":"2026-01-01T00:00:00Z"}}`,
			want: NumberRef{Number: 7, IsPR: true, Title: "Add the thing", State: "merged"},
		},
		{
			name: "abandoned pr stays closed",
			json: `{"number":7,"title":"Add the thing","state":"closed","pull_request":{"merged_at":null}}`,
			want: NumberRef{Number: 7, IsPR: true, Title: "Add the thing", State: "closed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p numberPayload
			if err := json.Unmarshal([]byte(tt.json), &p); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			if got := p.ref(); got != tt.want {
				t.Errorf("ref() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBranchURL(t *testing.T) {
	tests := []struct {
		name   string
		nwo    string
		branch string
		want   string
	}{
		{"plain name", "o/r", "main", "https://github.com/o/r/tree/main"},
		{"slash stays a slash", "o/r", "feat/thing", "https://github.com/o/r/tree/feat/thing"},
		{"space is escaped", "o/r", "my branch", "https://github.com/o/r/tree/my%20branch"},
		{"hash is escaped", "o/r", "fix-#12", "https://github.com/o/r/tree/fix-%2312"},
		{"no remote, no url", "", "main", ""},
		{"no branch, no url", "o/r", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BranchURL(tt.nwo, tt.branch); got != tt.want {
				t.Errorf("BranchURL(%q, %q) = %q, want %q", tt.nwo, tt.branch, got, tt.want)
			}
		})
	}
}

// A reviewer counts as present either way GitHub records one — a still-open
// request, or a review already submitted. Counting only the request flagged
// every PR whose reviewers had already ruled, since GitHub deletes the request
// the moment a review lands.
func TestOpenPRNodeGaps(t *testing.T) {
	withReviewer := func() openPRNode {
		var n openPRNode
		n.Author.Login = "me"
		n.Reviews.Nodes = append(n.Reviews.Nodes, struct {
			Author      reviewActor `json:"author"`
			State       string      `json:"state"`
			SubmittedAt string      `json:"submittedAt"`
		}{Author: reviewActor{Login: "alice", Typename: "User"}, State: "APPROVED"})
		return n
	}

	bare := openPRNode{}
	if got := bare.gaps(); got != GapAssignee|GapReviewer|GapLabels {
		t.Errorf("empty PR reports gaps %q, want ARL", got.Letters())
	}

	n := withReviewer()
	if n.gaps()&GapReviewer != 0 {
		t.Error("PR with a submitted review still reports a missing reviewer")
	}

	self := openPRNode{}
	self.Author.Login = "me"
	self.Reviews.Nodes = withReviewer().Reviews.Nodes[:0]
	self.Reviews.Nodes = append(self.Reviews.Nodes, struct {
		Author      reviewActor `json:"author"`
		State       string      `json:"state"`
		SubmittedAt string      `json:"submittedAt"`
	}{Author: reviewActor{Login: "me", Typename: "User"}, State: "COMMENTED"})
	if self.gaps()&GapReviewer == 0 {
		t.Error("author reviewing their own PR counts as a reviewer")
	}

	full := withReviewer()
	full.Assignees.Nodes = append(full.Assignees.Nodes, struct {
		Login string `json:"login"`
	}{Login: "me"})
	full.Labels.Nodes = append(full.Labels.Nodes, struct {
		Name string `json:"name"`
	}{Name: "bug"})
	if got := full.gaps(); got != 0 {
		t.Errorf("fully triaged PR reports gaps %q, want none", got.Letters())
	}
}
