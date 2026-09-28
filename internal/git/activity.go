package git

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Activity is what git itself knows about a branch's life: when its ref last
// moved, and whether it has ever held a commit of its own.
//
// It exists because "does deleting this lose commits?" — the question the dead
// signals answer — is too narrow for the action they authorize. A branch created
// minutes ago off the default branch loses no commits when deleted, and is also
// the branch someone is most likely working in right now. The commit graph cannot
// tell those apart: both are zero commits ahead. The reflog can, because it
// records when the branch came into being and everything that happened to it since.
type Activity struct {
	// LastTouched is when the branch ref last moved — its creation for a branch
	// that has never been committed to. Zero when there is no reflog to read
	// (reflogs disabled, or the entries expired), which classifies as long-stale.
	LastTouched time.Time
	// NeverCommitted is whether nothing has happened to the branch since it was
	// created: no commit has ever landed on it. Such a branch is not "merged"
	// however reachable it is from the default branch — nothing of it merged.
	NeverCommitted bool
}

// BranchActivity reports, per branch, when its ref last moved and whether it ever
// held a commit. Reflogs are read as files rather than through `git reflog` so the
// common case costs one small read per branch instead of one subprocess.
//
// NeverCommitted needs both of two independent checks to pass:
//
//   - the branch's reflog ends with the segment that created it — nothing has been
//     appended since (readReflog);
//   - the branch is zero commits ahead of the ref new branches fork from.
//
// Neither alone is enough. The commit count cannot separate empty from merged —
// a branch whose work landed is also zero ahead — which is the whole reason the
// reflog is read at all. The reflog alone cannot survive `git branch -D`, which
// leaves `.git/logs/refs/heads/<name>` on disk for the next branch of that name to
// append to. Reading the creation *segment* handles the recreated name; the commit
// count is a second, cheaper-to-trust confirmation that nothing landed.
func BranchActivity(ctx context.Context, dir string, branches []BranchInfo) map[string]Activity {
	out := map[string]Activity{}
	common, err := runner(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common == "" {
		return out
	}
	var candidates []string
	for _, bi := range branches {
		a, ok := readReflog(filepath.Join(common, "logs", "refs", "heads", bi.Name))
		if !ok {
			continue
		}
		if a.NeverCommitted {
			candidates = append(candidates, bi.Name)
		}
		out[bi.Name] = a
	}
	// Only branches the reflog already calls empty need confirming, so the base is
	// resolved (which fetches) and rev-list run only when there is something to
	// confirm — usually a handful of branches, often none.
	if len(candidates) == 0 {
		return out
	}
	// No default branch means there is no fork ref to count against; the reflog
	// segment stands alone rather than disqualifying every branch in the repo.
	base := OriginBase(ctx, dir)
	if base == "" {
		return out
	}
	for _, name := range candidates {
		if !zeroAhead(ctx, dir, base, name) {
			a := out[name]
			a.NeverCommitted = false
			out[name] = a
		}
	}
	return out
}

// zeroAhead reports whether branch holds no commit that base does not already
// have. An unreadable count is not a yes: the caller uses this to confirm a branch
// is empty, and a branch that cannot be confirmed empty must not be called empty.
func zeroAhead(ctx context.Context, dir, base, branch string) bool {
	out, err := runner(ctx, dir, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	return err == nil && n == 0
}

// readReflog parses one reflog file. Each line is
//
//	<old> <new> <name> <email> <unix-ts> <tz>\t<message>
//
// so the last line's timestamp is when the ref last moved, and a branch nothing
// has ever happened to is one whose final `branch: Created from …` line is the
// last line in the file. Scanning back to the *last* creation entry rather than
// testing the file for a single line is what makes a name that was deleted and
// recreated read as empty: `git branch -D` leaves the old lines behind, and the
// new branch appends its own creation entry after them.
func readReflog(path string) (Activity, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Activity{}, false
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return Activity{}, false
	}
	a := Activity{LastTouched: reflogTime(lines[len(lines)-1])}
	for i := len(lines) - 1; i >= 0; i-- {
		_, msg, ok := strings.Cut(lines[i], "\t")
		if ok && strings.HasPrefix(msg, "branch: Created from") {
			a.NeverCommitted = i == len(lines)-1
			break
		}
	}
	return a, true
}

// reflogTime pulls the unix timestamp out of one reflog line. The message follows
// a tab, and the timestamp is the second-to-last field before it.
func reflogTime(line string) time.Time {
	head, _, _ := strings.Cut(line, "\t")
	fields := strings.Fields(head)
	if len(fields) < 2 {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(fields[len(fields)-2], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}

// WorktreeTouched is when anything last happened in a worktree: the newest of its
// private index (rewritten by any `git status`, `add`, or `checkout` run in that
// tree — so an agent working there bumps it constantly) and the checkout directory
// itself (which moves when a build drops files at its top level).
//
// Deliberately not a walk of the tree: on a large repo that costs more than every
// other signal combined, and both of these move for the same reason a walk would.
func WorktreeTouched(ctx context.Context, path string) time.Time {
	var newest time.Time
	if fi, err := os.Stat(path); err == nil {
		newest = fi.ModTime()
	}
	gitDir, err := runner(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil || gitDir == "" {
		return newest
	}
	if fi, err := os.Stat(filepath.Join(gitDir, "index")); err == nil && fi.ModTime().After(newest) {
		newest = fi.ModTime()
	}
	return newest
}
