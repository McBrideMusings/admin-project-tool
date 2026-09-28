package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitCT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFileCT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPruneDeadBranchesExcludesCurrentAndDefault pins pruneDeadBranches's
// candidate selection: whatever ClassifyDead would say about them, the
// branch checked out right now and the repo's default branch must never be
// offered — deleting either is not "cleanup", it is breaking the checkout out
// from under the user. dry-run only prints, so this needs no confirm prompt.
func TestPruneDeadBranchesExcludesCurrentAndDefault(t *testing.T) {
	origin := t.TempDir()
	gitCT(t, origin, "init", "-q", "--bare", "-b", "main")

	dir := t.TempDir()
	gitCT(t, dir, "init", "-q", "-b", "main")
	writeFileCT(t, filepath.Join(dir, "f.txt"), "base\n")
	gitCT(t, dir, "add", "-A")
	gitCT(t, dir, "commit", "-qm", "init")
	gitCT(t, dir, "remote", "add", "origin", origin)
	gitCT(t, dir, "push", "-q", "-u", "origin", "main")

	// A branch that is fully pushed (so ClassifyDead alone would call it dead
	// "on origin") but is ALSO the branch checked out right now.
	gitCT(t, dir, "switch", "-q", "-c", "current-work")
	writeFileCT(t, filepath.Join(dir, "g.txt"), "g\n")
	gitCT(t, dir, "add", "-A")
	gitCT(t, dir, "commit", "-qm", "work")
	gitCT(t, dir, "push", "-q", "-u", "origin", "current-work")

	var buf bytes.Buffer
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	err := pruneDeadBranches(context.Background(), dir, map[string]bool{}, true /* dryRun */, false)
	w.Close()
	os.Stderr = old
	buf.ReadFrom(r)
	if err != nil {
		t.Fatalf("pruneDeadBranches: %v", err)
	}
	out := buf.String()
	if bytes.Contains(buf.Bytes(), []byte("current-work")) {
		t.Fatalf("pruneDeadBranches offered the currently checked-out branch:\n%s", out)
	}
	if bytes.Contains(buf.Bytes(), []byte("main")) {
		t.Fatalf("pruneDeadBranches offered the default branch:\n%s", out)
	}
}
