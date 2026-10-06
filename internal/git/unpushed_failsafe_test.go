package git

import (
	"context"
	"path/filepath"
	"testing"
)

// TestCountUnpushedFailsSafe pins that CountUnpushed reports an unreadable count
// as not ok rather than as zero. The caller turns not-ok into "unpushed", which
// quarantines a branch out of the batch sweep; for a branch the evidence tier only
// *guesses* is dead that is the sole guard, so a transient git failure must never
// read as "nothing unpushed".
func TestCountUnpushedFailsSafe(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")
	commitOn(t, dir, "local-only")

	ctx := context.Background()

	if n, ok := CountUnpushed(ctx, dir, "local-only", "main"); !ok || n != 1 {
		t.Errorf("CountUnpushed = %d, %v; want 1, true for one commit beyond main with no origin", n, ok)
	}

	// A default branch that does not exist makes the rev-list fail. That is the
	// unreadable case, and it must not read as clean.
	if _, ok := CountUnpushed(ctx, dir, "local-only", "no-such-branch"); ok {
		t.Error("an unreadable count reported ok; a sweep could take the branch")
	}

	// CountAheadOf reports the same failure to any other caller.
	if _, ok := CountAheadOf(ctx, dir, "no-such-branch", "local-only"); ok {
		t.Error("CountAheadOf reported ok on a ref that does not exist")
	}
}
