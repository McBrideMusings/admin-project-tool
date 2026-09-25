package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	rows, err := ShowRows(m, "prs", FrameOptions{Settle: 500 * time.Millisecond})
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
	if _, err := ShowRows(m, "nope", FrameOptions{Settle: time.Millisecond}); err == nil || !strings.Contains(err.Error(), "unknown tab") {
		t.Errorf("unknown tab err = %v, want \"unknown tab\"", err)
	}
	if _, err := ShowRows(m, "prs", FrameOptions{Settle: time.Millisecond}); err == nil || !strings.Contains(err.Error(), "not shown") {
		t.Errorf("absent tab err = %v, want \"not shown\"", err)
	}
}

// TestShowRowsRepoListsBranches runs ShowRows against a real git repo: the Repo
// tab's rows must hold the branches it draws, not the empty list a model that
// never received its local load would give.
func TestShowRowsRepoListsBranches(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "first"},
		{"branch", "topic"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, dir, "", "main", "main")
	rows, err := ShowRows(m, "repo", FrameOptions{Settle: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, r := range rows {
		if r.Kind == "branch" {
			names[r.Branch] = true
		}
	}
	if !names["main"] || !names["topic"] {
		t.Errorf("repo rows hold branches %v, want main and topic", names)
	}
}

// TestShowRowsBeadsWaitsForData puts a fake bd on PATH: the Beads tab needs two
// hops (bd where, then bd list) and ShowRows must return the bead, never the
// "loading…" placeholder.
func TestShowRowsBeadsWaitsForData(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"where) echo '{\"path\":\"/x/.beads\",\"prefix\":\"t\",\"schema_version\":1}';;\n" +
		"list) echo '[{\"id\":\"t-1\",\"title\":\"first bead\",\"status\":\"open\",\"priority\":1,\"issue_type\":\"task\"}]';;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, t.TempDir(), "", "main", "main")
	m.manifest = &manifest.Manifest{HasTracker: true, Trackers: []string{"beads"}}
	m.manifestOK = true
	rows, err := ShowRows(m, "beads", FrameOptions{Settle: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Kind != "bead" || rows[0].ID != "t-1" || rows[0].Title != "first bead" {
		t.Errorf("beads rows = %+v, want the one bead t-1", rows)
	}
}

// TestShowRowsBeadsNeverPrintsPlaceholder: when the bead list is slower than the
// settle bound, ShowRows reports the tab as not loaded instead of returning the
// tab's "loading…" row as if it were data.
func TestShowRowsBeadsNeverPrintsPlaceholder(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"where) echo '{\"path\":\"/x/.beads\",\"prefix\":\"t\",\"schema_version\":1}';;\n" +
		"list) sleep 3; echo '[]';;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, t.TempDir(), "", "main", "main")
	m.manifest = &manifest.Manifest{HasTracker: true, Trackers: []string{"beads"}}
	m.manifestOK = true
	rows, err := ShowRows(m, "beads", FrameOptions{Settle: 400 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "had not loaded") {
		t.Errorf("rows = %+v, err = %v; want a \"had not loaded\" error, not a placeholder row", rows, err)
	}
}
