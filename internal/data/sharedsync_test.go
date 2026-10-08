package data

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// everySyncFetches turns off reuse of another sync's result for t, for a test
// that runs several syncs back to back and asserts on what each one fetched.
func everySyncFetches(t *testing.T) {
	t.Helper()
	prev := sharedFresh
	sharedFresh = 0
	t.Cleanup(func() { sharedFresh = prev })
}

// countingFetches stubs every section fetch for t and returns how many PR
// fetches ran, the call every sync makes.
func countingFetches(t *testing.T) *atomic.Int32 {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	prevPRs, prevIssues, prevMerged, prevRemote := fetchPRsFn, fetchIssuesFn, fetchMergedFn, remoteSigFn
	t.Cleanup(func() {
		fetchPRsFn, fetchIssuesFn, fetchMergedFn, remoteSigFn = prevPRs, prevIssues, prevMerged, prevRemote
	})
	var prs atomic.Int32
	fetchPRsFn = func(context.Context, string) Remote {
		prs.Add(1)
		return Remote{PRsFetchedAt: time.Now()}
	}
	fetchIssuesFn = func(context.Context, string) Remote { return Remote{IssuesFetchedAt: time.Now()} }
	fetchMergedFn = func(context.Context, string, int) Remote { return Remote{MergedFetchedAt: time.Now()} }
	remoteSigFn = func(context.Context, string) string { return "remote" }
	return &prs
}

// A second dashboard's poll inside sharedFresh reuses the first one's result
// and names who synced; a forced sync always fetches; a poll past sharedFresh
// fetches again.
func TestFetchAllReusesAnotherDashboardsFreshSync(t *testing.T) {
	logPath := t.TempDir() + "/sync.log"
	t.Cleanup(UseSyncLogPath(logPath))
	prs := countingFetches(t)
	o := SyncOpts{MergedLimit: 10, Dir: t.TempDir()}

	first := FetchAll(context.Background(), "o/r", o)
	second := FetchAll(context.Background(), "o/r", o)
	if n := prs.Load(); n != 1 {
		t.Fatalf("two polls inside sharedFresh made %d PR fetches, want 1", n)
	}
	if !second.PRsFetchedAt.Equal(first.PRsFetchedAt) || second.SyncedBy != os.Getpid() {
		t.Errorf("reused result = %v by %d, want the first sync's %v by %d", second.PRsFetchedAt, second.SyncedBy, first.PRsFetchedAt, os.Getpid())
	}

	FetchAll(context.Background(), "o/r", SyncOpts{MergedLimit: 10, Dir: o.Dir, Force: true})
	if n := prs.Load(); n != 2 {
		t.Fatalf("forced sync made %d PR fetches in total, want 2", n)
	}

	everySyncFetches(t)
	FetchAll(context.Background(), "o/r", o)
	if n := prs.Load(); n != 3 {
		t.Fatalf("poll past sharedFresh made %d PR fetches in total, want 3", n)
	}

	b, _ := os.ReadFile(logPath)
	for _, want := range []string{"shared ok syncer pid=", "shared ok reused cache_age="} {
		if !strings.Contains(string(b), want) {
			t.Errorf("sync log is missing %q:\n%s", want, b)
		}
	}
}

// A reused result must cover every section the reusing dashboard shows: a
// dashboard that tracks GitHub issues does not reuse a sync that skipped them,
// and a section that failed in another dashboard's sync is retried, not reused.
func TestFetchAllReusesOnlyWhatCoversThisDashboard(t *testing.T) {
	t.Cleanup(UseSyncLogPath(t.TempDir() + "/sync.log"))
	prs := countingFetches(t)
	dir := t.TempDir()

	FetchAll(context.Background(), "o/r", SyncOpts{MergedLimit: 10, Dir: dir})
	FetchAll(context.Background(), "o/r", SyncOpts{MergedLimit: 10, Dir: dir, WithIssues: true})
	if n := prs.Load(); n != 2 {
		t.Fatalf("an issues dashboard after a sync without issues made %d PR fetches in total, want 2", n)
	}

	// A sync only runs once the cache is past sharedFresh, so a section it
	// fails to refresh keeps a stamp that old.
	stale := time.Now().Add(-time.Hour)
	save("p/q", Remote{PRsFetchedAt: stale, IssuesFetchedAt: stale, MergedFetchedAt: stale})
	fetchPRsFn = func(context.Context, string) Remote {
		prs.Add(1)
		return Remote{PRsFetchError: "gh: boom"}
	}
	FetchAll(context.Background(), "p/q", SyncOpts{MergedLimit: 10, Dir: dir, WithIssues: true})
	got := FetchAll(context.Background(), "p/q", SyncOpts{MergedLimit: 10, Dir: dir, WithIssues: true})
	if n := prs.Load(); n != 4 {
		t.Fatalf("a poll after another sync's PR fetch failed made %d PR fetches in total, want 4", n)
	}
	if got.PRsFetchError == "" {
		t.Error("the retried sync hid the PR failure")
	}
}

// A dashboard whose repo is being synced by another process waits at most
// leaderWait, then fetches for itself rather than stall behind it or pass old
// data off as a fresh sync.
func TestFetchAllWaitsOutABusySyncer(t *testing.T) {
	logPath := t.TempDir() + "/sync.log"
	t.Cleanup(UseSyncLogPath(logPath))
	prs := countingFetches(t)
	prev := leaderWait
	leaderWait = 300 * time.Millisecond
	t.Cleanup(func() { leaderWait = prev })
	stale := time.Now().Add(-time.Hour)
	save("o/r", Remote{PRsFetchedAt: stale, MergedFetchedAt: stale})

	// Another syncer: a separate open of the lock file holds it, the way
	// another dashboard process would.
	if err := os.MkdirAll(filepath.Dir(cachePath("o/r")), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(cachePath("o/r")+".sync.lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	got := FetchAll(context.Background(), "o/r", SyncOpts{MergedLimit: 10, Dir: t.TempDir()})
	if waited := time.Since(start); waited < leaderWait || waited > leaderWait+2*time.Second {
		t.Errorf("waited %s on a busy syncer, want about %s", waited, leaderWait)
	}
	if n := prs.Load(); n != 1 || got.PRsFetchedAt.Equal(stale) {
		t.Errorf("after the wait: %d PR fetches, result stamped %v; want its own fetch, not the hour-old cache", n, got.PRsFetchedAt)
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "fetching reason=syncer-busy") {
		t.Errorf("sync log is missing the busy-syncer line:\n%s", b)
	}
}

// A cancelled sync stops waiting on another dashboard's sync at once.
func TestFetchAllStopsWaitingWhenCancelled(t *testing.T) {
	t.Cleanup(UseSyncLogPath(t.TempDir() + "/sync.log"))
	countingFetches(t)
	if err := os.MkdirAll(filepath.Dir(cachePath("o/r")), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(cachePath("o/r")+".sync.lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	FetchAll(ctx, "o/r", SyncOpts{MergedLimit: 10, Dir: t.TempDir()})
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("a cancelled sync waited %s on a busy syncer, want no wait", waited)
	}
}
