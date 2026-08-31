package guards

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (dir=%s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// initRepoWithCommit creates a git repo at dir with one commit on branch.
func initRepoWithCommit(t *testing.T, dir, branch string) {
	t.Helper()
	runGit(t, dir, "init", "--quiet", "--initial-branch="+branch)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "--quiet", "-m", "initial")
}

// TestGitCleanGuardPassesOnCleanTree proves a freshly-committed tree passes.
func TestGitCleanGuardPassesOnCleanTree(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	initRepoWithCommit(t, dir, "main")

	if ok, msg := gitCleanGuard(); !ok {
		t.Fatalf("gitCleanGuard() = (false, %q), want (true, _)", msg)
	}
}

// TestGitCleanGuardFailsOnDirtyTree proves an uncommitted change is caught,
// matching the acceptance criterion "a failing guard blocks the action,
// prints its diagnostic".
func TestGitCleanGuardFailsOnDirtyTree(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	initRepoWithCommit(t, dir, "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ok, msg := gitCleanGuard()
	if ok {
		t.Fatal("gitCleanGuard() = true, want false for a dirty tree")
	}
	if !strings.Contains(msg, "git-clean") {
		t.Errorf("message %q should be prefixed with the guard name", msg)
	}
}

// TestGitPushedGuardUsesConfiguredUpstreamNotOriginBranch is the regression
// test for admin-c8lc: a branch whose upstream is NOT "origin/<same name>"
// (here: remote "upstream", remote branch "trunk") must still pass when
// fully pushed, rather than a hard-coded origin/<branch> lookup falsely
// reporting "not on origin".
func TestGitPushedGuardUsesConfiguredUpstreamNotOriginBranch(t *testing.T) {
	remoteDir := t.TempDir()
	runGit(t, remoteDir, "init", "--quiet", "--bare")

	workDir := t.TempDir()
	t.Chdir(workDir)
	initRepoWithCommit(t, workDir, "feature")
	runGit(t, workDir, "remote", "add", "upstream", remoteDir)
	runGit(t, workDir, "push", "--quiet", "upstream", "feature:trunk")
	runGit(t, workDir, "branch", "--set-upstream-to=upstream/trunk", "feature")

	ok, msg := gitPushedGuard()
	if !ok {
		t.Fatalf("gitPushedGuard() = (false, %q), want (true, _) — the branch IS fully "+
			"pushed to its configured (non-origin, differently-named) upstream", msg)
	}
}

// TestGitPushedGuardFailsWhenLocalAheadOfRemote proves a local commit not
// yet pushed is caught.
func TestGitPushedGuardFailsWhenLocalAheadOfRemote(t *testing.T) {
	remoteDir := t.TempDir()
	runGit(t, remoteDir, "init", "--quiet", "--bare")

	workDir := t.TempDir()
	t.Chdir(workDir)
	initRepoWithCommit(t, workDir, "main")
	runGit(t, workDir, "remote", "add", "origin", remoteDir)
	runGit(t, workDir, "push", "--quiet", "-u", "origin", "main")

	// A second local commit that is never pushed.
	if err := os.WriteFile(filepath.Join(workDir, "g.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", "g.txt")
	runGit(t, workDir, "commit", "--quiet", "-m", "second")

	ok, msg := gitPushedGuard()
	if ok {
		t.Fatal("gitPushedGuard() = true, want false: HEAD has a commit not on origin")
	}
	if !strings.Contains(msg, "git-pushed") {
		t.Errorf("message %q should be prefixed with the guard name", msg)
	}
}

// TestGitPushedGuardDetectsRemoteRewoundSinceLastFetch is the regression
// test for admin-d03k: the guard must not trust a stale local
// remote-tracking ref. Sequence:
//
//  1. workDir pushes commits A, B to origin/main. Git updates workDir's
//     local origin/main tracking ref to B as a side effect of the push.
//  2. A second clone of the same remote force-pushes origin/main back to A
//     — simulating history being rewound by someone else — without workDir
//     ever running `git fetch`, so workDir's local origin/main ref is now
//     stale (still reads B).
//  3. gitPushedGuard runs in workDir, at commit B. A guard that trusts the
//     stale local ref computes rev-list B..B = 0 and wrongly passes. The
//     fixed guard fetches live before comparing, sees the remote is
//     actually at A, computes rev-list A..B = 1, and correctly fails.
func TestGitPushedGuardDetectsRemoteRewoundSinceLastFetch(t *testing.T) {
	remoteDir := t.TempDir()
	runGit(t, remoteDir, "init", "--quiet", "--bare")

	workDir := t.TempDir()
	t.Chdir(workDir)
	initRepoWithCommit(t, workDir, "main") // commit A
	runGit(t, workDir, "remote", "add", "origin", remoteDir)
	runGit(t, workDir, "push", "--quiet", "-u", "origin", "main")

	if err := os.WriteFile(filepath.Join(workDir, "g.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", "g.txt")
	runGit(t, workDir, "commit", "--quiet", "-m", "second") // commit B
	runGit(t, workDir, "push", "--quiet", "origin", "main")
	// At this point workDir's local origin/main tracking ref reads B (git
	// updates it locally after a successful push), matching HEAD.

	otherDir := t.TempDir()
	runGit(t, otherDir, "clone", "--quiet", remoteDir, ".")
	runGit(t, otherDir, "reset", "--hard", "HEAD~1") // back to commit A
	runGit(t, otherDir, "push", "--quiet", "--force", "origin", "main")
	// The real remote is now at A. workDir has NOT fetched, so its local
	// origin/main tracking ref is stale and still reads B.

	ok, msg := gitPushedGuard()
	if ok {
		t.Fatal("gitPushedGuard() = true, want false: the remote was rewound behind HEAD " +
			"since the last fetch, so HEAD is no longer actually on origin")
	}
	if !strings.Contains(msg, "git-pushed") {
		t.Errorf("message %q should be prefixed with the guard name", msg)
	}
}
