package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestShowRowsPRsClassifiesByBucket asserts on the PRs tab's rows through
// ShowRows — the function `admin show prs` prints — rather than on a frame: each
// PR carries the bucket the tab files it under, by name, and the tab's section.
func TestShowRowsPRsClassifiesByBucket(t *testing.T) {
	restore := UseStaticRemote(data.Remote{
		PRs: []gh.OpenPR{
			{Number: 7, Title: "mine", HeadBranch: "a", Bucket: gh.BucketMine, Checks: gh.ChecksPassing, Updated: time.Now()},
			{Number: 9, Title: "review me", HeadBranch: "b", Bucket: gh.BucketAssigned, Mergeable: "CONFLICTING", MyReview: gh.UpdatedSinceReview, Updated: time.Now()},
		},
		PRsFetchedAt: time.Now(), IssuesFetchedAt: time.Now(), MergedFetchedAt: time.Now(),
	})
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, t.TempDir(), "o/r", "main", "main")
	m.manifest = &manifest.Manifest{HasUsesPullRequests: true, UsesPullRequests: true}
	rows, err := ShowRows(ctx, m, "prs", FrameOptions{Settle: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	got := map[int]ShowRow{}
	for _, r := range rows {
		if r.PR != nil {
			got[r.Number] = r
		}
	}
	if r := got[7]; r.Section != "Mine" || r.PR.Bucket != "mine" || r.PR.Checks != "passing" || r.PR.MyReview != "" {
		t.Errorf("PR 7 = %+v, want Mine/mine/passing with no myReview", r)
	}
	if r := got[9]; r.Section != "Needs me" || r.PR.Bucket != "needs-me" || !r.PR.Conflicts || r.PR.MyReview != "updated-since-review" {
		t.Errorf("PR 9 = %+v, want Needs me/needs-me, conflicting, updated-since-review", r)
	}
	if r := got[9]; !strings.Contains(strings.Join(r.Cells, " "), "review me") {
		t.Errorf("PR 9 cells = %v, want the title", r.Cells)
	}
}

func TestShowRowsRejectsUnknownAndAbsentTab(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, t.TempDir(), "", "main", "main")
	if _, err := ShowRows(ctx, m, "nope", FrameOptions{Settle: time.Millisecond}); err == nil || !strings.Contains(err.Error(), "unknown tab") {
		t.Errorf("unknown tab err = %v, want \"unknown tab\"", err)
	}
	if _, err := ShowRows(ctx, m, "prs", FrameOptions{Settle: time.Millisecond}); err == nil || !strings.Contains(err.Error(), "not shown") {
		t.Errorf("absent tab err = %v, want \"not shown\"", err)
	}
}
