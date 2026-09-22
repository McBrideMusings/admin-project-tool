package git

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWorkingStatusLeavesTheIndexAlone proves the dashboard's status read
// never refreshes the index. A plain `git status` rewrites a stale index
// under index.lock, which collides with a commit in the same checkout and,
// when the poll is killed mid-write, leaves the lock file behind.
func TestWorkingStatusLeavesTheIndexAlone(t *testing.T) {
	repo := t.TempDir()
	gitT(t, repo, "init", "-q")
	file := filepath.Join(repo, "a.txt")
	writeFile(t, file, "a\n")
	gitT(t, repo, "add", "a.txt")
	gitT(t, repo, "commit", "-qm", "init")

	// A new mtime with the same content makes the index's cached stat data
	// stale, which is what a refresh would rewrite.
	later := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	index := filepath.Join(repo, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("reading index: %v", err)
	}

	if _, err := WorkingStatus(context.Background(), repo); err != nil {
		t.Fatalf("WorkingStatus: %v", err)
	}

	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("reading index: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("WorkingStatus rewrote the index, so it took index.lock")
	}
}
