package data

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
)

func TestDecideMerged(t *testing.T) {
	now := time.Now()
	prev := Remote{
		PRs:             []gh.OpenPR{{Number: 1}, {Number: 2}},
		MergedFetchedAt: now,
		MergedLocalSig:  "sig",
		MergedRemoteSig: "rsig",
	}
	same := Remote{PRs: []gh.OpenPR{{Number: 1}, {Number: 2}, {Number: 3}}, PRsFetchedAt: now}
	cases := []struct {
		name       string
		prev, prs  Remote
		sig, rsig  string
		force      bool
		wantRun    bool
		wantReason string
	}{
		{"nothing changed", prev, same, "sig", "rsig", false, false, "unchanged"},
		{"forced", prev, same, "sig", "rsig", true, true, "forced"},
		{"no cached result", Remote{}, same, "sig", "rsig", false, true, "no-cached-result"},
		{"branch set or head changed", prev, same, "other", "rsig", false, true, "local-branches-changed"},
		{"local git unreadable", prev, same, "", "rsig", false, true, "local-branches-unknown"},
		{"previous merged fetch failed", Remote{PRs: prev.PRs, MergedFetchedAt: now}, same, "sig", "rsig", false, true, "local-branches-unknown"},
		{"a PR merged on origin", prev, same, "sig", "moved", false, true, "remote-branches-moved"},
		{"origin unreachable", prev, same, "sig", "", false, true, "remote-branches-unknown"},
		{"a PR merged on GitHub", prev, Remote{PRs: []gh.OpenPR{{Number: 1}}}, "sig", "rsig", false, true, "pr-left-open-list=#2"},
		{"open-PR fetch failed", prev, Remote{PRsFetchError: "boom"}, "sig", "rsig", false, true, "open-prs-unknown"},
	}
	for _, c := range cases {
		run, reason := decideMerged(c.prev, c.prs, c.sig, c.rsig, c.force)
		if run != c.wantRun || reason != c.wantReason {
			t.Errorf("%s: got run=%t reason=%q, want run=%t reason=%q", c.name, run, reason, c.wantRun, c.wantReason)
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestFetchAllSkipsTheMergedQueryUntilSomethingMoved runs real syncs against a
// real repo: the second tick makes no merged call and keeps the cached section;
// a new local branch, a PR merging on origin that this
// dashboard never saw open, a PR leaving the open list, and a forced sync each
// bring the call back.
func TestFetchAllSkipsTheMergedQueryUntilSomethingMoved(t *testing.T) {
	everySyncFetches(t)
	logPath := t.TempDir() + "/sync.log"
	t.Cleanup(UseSyncLogPath(logPath))
	t.Setenv("HOME", t.TempDir())
	prevPRs, prevIssues, prevMerged, prevRemote := fetchPRsFn, fetchIssuesFn, fetchMergedFn, remoteSigFn
	t.Cleanup(func() {
		fetchPRsFn, fetchIssuesFn, fetchMergedFn, remoteSigFn = prevPRs, prevIssues, prevMerged, prevRemote
	})
	remoteHead := "r1"
	remoteSigFn = func(context.Context, string) string { return remoteHead }

	open := []gh.OpenPR{{Number: 1}, {Number: 2}}
	fetchPRsFn = func(context.Context, string) Remote { return Remote{PRs: open, PRsFetchedAt: time.Now()} }
	fetchIssuesFn = func(context.Context, string) Remote { return Remote{} }
	mergedCalls := 0
	fetchMergedFn = func(context.Context, string, int) Remote {
		mergedCalls++
		return Remote{MergedHeads: map[string]string{"feat": "oid"}, MergedFetchedAt: time.Now()}
	}

	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	sync := func(force bool) Remote {
		return FetchAll(context.Background(), "owner/skip", SyncOpts{MergedLimit: 10, Dir: repo, Force: force})
	}

	sync(false)
	if mergedCalls != 1 {
		t.Fatalf("first sync: merged calls = %d, want 1", mergedCalls)
	}
	first, _ := Load("owner/skip")

	r := sync(false)
	if mergedCalls != 1 {
		t.Errorf("unchanged tick ran the merged query (calls=%d)", mergedCalls)
	}
	if !r.MergedSkipped {
		t.Errorf("skipped tick did not mark its fragment skipped")
	}
	cached, _ := Load("owner/skip")
	if cached.MergedHeads["feat"] != "oid" {
		t.Errorf("skipped tick altered the cached merged data: %+v", cached)
	}
	if !cached.MergedFetchedAt.After(first.MergedFetchedAt) {
		t.Errorf("skipped tick left MergedFetchedAt at %v; a skip is a check and must refresh it", cached.MergedFetchedAt)
	}
	if cached.Stale(false) {
		t.Errorf("cache reads stale right after a skipped tick")
	}

	gitIn(t, repo, "branch", "feat")
	sync(false)
	if mergedCalls != 2 {
		t.Errorf("new local branch: merged calls = %d, want 2", mergedCalls)
	}

	sync(false)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "more")
	sync(false)
	if mergedCalls != 3 {
		t.Errorf("commit to a local branch: merged calls = %d, want 3", mergedCalls)
	}

	remoteHead = "r2" // a PR opened and squash-merged between two ticks
	sync(false)
	if mergedCalls != 4 {
		t.Errorf("remote branches moved: merged calls = %d, want 4", mergedCalls)
	}

	open = []gh.OpenPR{{Number: 1}}
	sync(false)
	if mergedCalls != 5 {
		t.Errorf("PR left the open list: merged calls = %d, want 5", mergedCalls)
	}

	sync(false)
	if mergedCalls != 5 {
		t.Errorf("settled tick: merged calls = %d, want 5", mergedCalls)
	}
	sync(true)
	if mergedCalls != 6 {
		t.Errorf("forced sync: merged calls = %d, want 6", mergedCalls)
	}

	b, _ := os.ReadFile(logPath)
	for _, want := range []string{
		"merged ok skipped reason=unchanged",
		"ran reason=no-cached-result",
		"ran reason=local-branches-changed",
		"ran reason=remote-branches-moved",
		"ran reason=pr-left-open-list=#2",
		"ran reason=forced",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("sync log is missing %q:\n%s", want, b)
		}
	}
}

// TestFailedMergedFetchIsRetriedNextTick pins that a failure clears the stored
// fingerprint, so an unchanged repo does not skip past a fetch that never landed.
func TestFailedMergedFetchIsRetriedNextTick(t *testing.T) {
	var cached Remote
	cached.MergedLocalSig, cached.MergedRemoteSig = "sig", "rsig"
	Remote{MergedFetchError: "boom"}.applyMergedTo(&cached)
	if cached.MergedLocalSig != "" || cached.MergedRemoteSig != "" {
		t.Errorf("failed fetch left the fingerprints %q / %q", cached.MergedLocalSig, cached.MergedRemoteSig)
	}
}

// TestFailedClosedQueryIsRetriedNextSync pins that a merged fetch whose closed-PR
// query failed keeps its merged data but is not stamped with fingerprints, so an
// unchanged repo runs the query again on the next sync instead of skipping past it.
func TestFailedClosedQueryIsRetriedNextSync(t *testing.T) {
	everySyncFetches(t)
	t.Cleanup(UseSyncLogPath(t.TempDir() + "/sync.log"))
	t.Setenv("HOME", t.TempDir())
	prevPRs, prevIssues, prevMerged, prevRemote := fetchPRsFn, fetchIssuesFn, fetchMergedFn, remoteSigFn
	t.Cleanup(func() {
		fetchPRsFn, fetchIssuesFn, fetchMergedFn, remoteSigFn = prevPRs, prevIssues, prevMerged, prevRemote
	})
	fetchPRsFn = func(context.Context, string) Remote { return Remote{PRsFetchedAt: time.Now()} }
	fetchIssuesFn = func(context.Context, string) Remote { return Remote{} }
	remoteSigFn = func(context.Context, string) string { return "r1" }
	calls := 0
	fetchMergedFn = func(context.Context, string, int) Remote {
		calls++
		r := Remote{MergedHeads: map[string]string{"feat": "oid"}, MergedFetchedAt: time.Now()}
		if calls == 1 {
			r.ClosedFetchError = "rate limited"
		}
		return r
	}
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	sync := func() { FetchAll(context.Background(), "owner/closed", SyncOpts{MergedLimit: 10, Dir: repo}) }

	sync()
	if cached, _ := Load("owner/closed"); cached.MergedHeads["feat"] != "oid" {
		t.Fatalf("a closed-query failure dropped the merged data: %+v", cached)
	}
	sync()
	if calls != 2 {
		t.Errorf("sync after a closed-query failure: merged calls = %d, want 2", calls)
	}
	sync()
	if calls != 2 {
		t.Errorf("sync after the retry succeeded: merged calls = %d, want 2", calls)
	}
}
