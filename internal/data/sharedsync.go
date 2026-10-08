package data

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// sharedFresh is how recent another dashboard's sync of a repo must be for a
// poll to reuse it instead of calling GitHub. It is half the poll, so with any
// number of dashboards open on one repo, whichever tick lands first past it
// syncs and every other tick in the following half-interval reuses that
// result: the repo syncs once per PollInterval, not once per dashboard. A var
// so a test can make every sync go to GitHub.
var sharedFresh = PollInterval / 2

// leaderWait bounds how long a dashboard waits on another dashboard's sync of
// the same repo. Past it, the waiter reuses whatever the cache holds rather
// than hang behind a stuck gh call. A var so a test can shorten it.
var leaderWait = 45 * time.Second

// claimSync elects one syncing dashboard per repo. It takes the repo's sync
// lock (<cache>.sync.lock) and reads the cache: when another process synced
// within sharedFresh, it returns that result with reuse=true and releases the
// lock at once. Otherwise this process is the syncer and holds the lock until
// it calls release, after saving, so a dashboard that was waiting reads the
// new result rather than fetching it a second time.
//
// The lock is a flock, so a syncer that dies mid-fetch frees it with its file
// descriptor. A lock that cannot be opened at all elects this process, since a
// broken lock must never stop a dashboard from syncing.
func claimSync(nwo string) (cached Remote, reuse bool, release func()) {
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
		if !errors.Is(err, unix.EWOULDBLOCK) {
			f.Close()
			return Remote{}, false, release
		}
		if time.Since(start) > leaderWait {
			f.Close()
			waited := time.Since(start).Round(time.Millisecond)
			c, ok := Load(nwo)
			if !ok {
				// Nothing cached to show: fetch rather than leave the dashboard empty.
				LogSync(nwo, "shared", 0, fmt.Sprintf("fetching reason=syncer-busy-no-cache waited=%s", waited), nil)
				return Remote{}, false, release
			}
			LogSync(nwo, "shared", 0, fmt.Sprintf("reused reason=syncer-busy waited=%s cache_age=%s", waited, age(c.PRsFetchedAt)), nil)
			return c, true, release
		}
		time.Sleep(200 * time.Millisecond)
	}
	unlock := func() { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }
	c, ok := Load(nwo)
	if ok && !c.PRsFetchedAt.IsZero() && time.Since(c.PRsFetchedAt) < sharedFresh {
		unlock()
		LogSync(nwo, "shared", 0, fmt.Sprintf("reused reason=fresh cache_age=%s synced_by=%d waited=%s", age(c.PRsFetchedAt), c.SyncedBy, time.Since(start).Round(time.Millisecond)), nil)
		return c, true, release
	}
	LogSync(nwo, "shared", 0, fmt.Sprintf("syncer pid=%d cache_age=%s", os.Getpid(), age(c.PRsFetchedAt)), nil)
	return Remote{}, false, unlock
}

// age renders how old a cache stamp is for the sync log; "none" when the
// section was never fetched.
func age(at time.Time) string {
	if at.IsZero() {
		return "none"
	}
	return time.Since(at).Round(time.Second).String()
}
