package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// sharedFresh is how recent another dashboard's sync of a repo must be for a
// poll to reuse it instead of calling GitHub. It is TTL, half the poll: with any
// number of dashboards open on one repo, whichever tick lands first past it
// syncs and every other tick in the following half-interval reuses that
// result, so the repo syncs once per PollInterval rather than once per
// dashboard, and a reused result never reads as stale. A var so a test can
// make every sync go to GitHub.
var sharedFresh = TTL

// leaderWait bounds how long a dashboard waits on another dashboard's sync of
// the same repo. Past it the waiter fetches for itself: a stuck gh call in one
// dashboard must not stall the others. A var so a test can shorten it.
var leaderWait = 45 * time.Second

// claimSync elects one syncing dashboard per repo. It takes the repo's sync
// lock (<cache>.sync.lock) and reads the cache: when every section this
// dashboard shows was synced within sharedFresh, it returns that result with
// reuse=true and releases the lock at once. Otherwise this process syncs, and
// holds the lock until it calls release after saving, so a dashboard waiting
// on it reads the new result rather than fetching it a second time.
//
// A section that failed in another dashboard's sync keeps its old stamp, so it
// is never fresh and the next dashboard retries it. The lock is a flock, so a
// syncer that dies frees it with its file descriptor. Every way of not getting
// the lock — it cannot be opened, the wait passed leaderWait, ctx ended —
// fetches without it: the lock only saves calls and must never be why a
// dashboard does not sync.
func claimSync(ctx context.Context, nwo string, withIssues bool) (cached Remote, reuse bool, release func()) {
	release = func() {}
	path := cachePath(nwo) + ".sync.lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Remote{}, false, release
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return Remote{}, false, release
	}
	start := time.Now()
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		reason := ""
		switch {
		case !errors.Is(err, unix.EWOULDBLOCK):
			reason = "lock-failed"
		case ctx.Err() != nil:
			reason = "cancelled"
		case time.Since(start) > leaderWait:
			reason = "syncer-busy"
		}
		if reason != "" {
			f.Close()
			LogSync(nwo, "shared", 0, fmt.Sprintf("fetching reason=%s waited=%s", reason, time.Since(start).Round(time.Millisecond)), nil)
			return Remote{}, false, release
		}
		time.Sleep(200 * time.Millisecond)
	}
	unlock := func() { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }
	c, ok := Load(nwo)
	oldest := c.oldestSection(withIssues)
	if ok && !oldest.IsZero() && time.Since(oldest) < sharedFresh {
		unlock()
		LogSync(nwo, "shared", 0, fmt.Sprintf("reused cache_age=%s synced_by=%d waited=%s", age(oldest), c.SyncedBy, time.Since(start).Round(time.Millisecond)), nil)
		return c, true, release
	}
	LogSync(nwo, "shared", 0, fmt.Sprintf("syncer pid=%d cache_age=%s", os.Getpid(), age(oldest)), nil)
	return Remote{}, false, unlock
}

// oldestSection is the stamp of the least recently fetched section a dashboard
// shows — zero when any of them was never fetched.
func (r Remote) oldestSection(withIssues bool) time.Time {
	stamps := []time.Time{r.PRsFetchedAt, r.MergedFetchedAt}
	if withIssues {
		stamps = append(stamps, r.IssuesFetchedAt)
	}
	oldest := stamps[0]
	for _, s := range stamps {
		if s.IsZero() {
			return time.Time{}
		}
		if s.Before(oldest) {
			oldest = s
		}
	}
	return oldest
}

// age renders how old a cache stamp is for the sync log; "none" when a section
// was never fetched.
func age(at time.Time) string {
	if at.IsZero() {
		return "none"
	}
	return time.Since(at).Round(time.Second).String()
}
