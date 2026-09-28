package git

import (
	"context"
	"path/filepath"
	"testing"
)

// TestHasUnpushedCommitsFailsSafe pins the direction HasUnpushedCommits errs in
// when it cannot count. The flag quarantines a branch out of the batch sweep, and
// for a branch the evidence tier only *guesses* is dead it is the sole guard —
// nothing proved that one dead. Reading an unreadable count as "nothing unpushed"
// would let a transient git failure hand a branch full of local-only commits to a
// sweep, so it must answer true instead.
func TestHasUnpushedCommitsFailsSafe(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")
	commitOn(t, dir, "local-only")

	ctx := context.Background()

	bi := BranchInfo{Name: "local-only"}
	if !HasUnpushedCommits(ctx, dir, bi, "main") {
		t.Error("a branch with commits beyond main reported nothing unpushed")
	}

	// A default branch that does not exist makes the rev-list fail. That is the
	// unreadable case, and it must not read as clean.
	if !HasUnpushedCommits(ctx, dir, bi, "no-such-branch") {
		t.Error("an unreadable count reported nothing unpushed; a sweep could take the branch")
	}

	// CountAheadOf reports the same failure to any other caller.
	if _, ok := CountAheadOf(ctx, dir, "no-such-branch", "local-only"); ok {
		t.Error("CountAheadOf reported ok on a ref that does not exist")
	}
}
