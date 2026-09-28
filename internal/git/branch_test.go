package git

import (
	"context"
	"path/filepath"
	"testing"
)

// commitOn creates branch off the current HEAD with one commit on it.
func commitOn(t *testing.T, dir, branch string) {
	t.Helper()
	gitT(t, dir, "switch", "-q", "-c", branch)
	writeFile(t, filepath.Join(dir, branch+".txt"), branch+"\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", branch)
}

// TestBranchRemoteStateWithoutUpstreamConfig pins the classification of branches
// whose relationship to origin is not recorded in upstream config. `git push
// origin <branch>` without -u leaves no upstream at all, so reading upstream
// config alone reports such a branch as having no remote — and then neither "gone"
// nor "on origin" can ever fire for it, however completely origin holds its work.
func TestBranchRemoteStateWithoutUpstreamConfig(t *testing.T) {
	origin := t.TempDir()
	gitT(t, origin, "init", "-q", "--bare", "-b", "main")

	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")
	gitT(t, dir, "remote", "add", "origin", origin)
	gitT(t, dir, "push", "-q", "-u", "origin", "main")

	// Pushed without -u: on origin, no upstream config.
	commitOn(t, dir, "untracked-pushed")
	gitT(t, dir, "push", "-q", "origin", "untracked-pushed")

	// Pushed without -u, then given a further local commit origin has not seen.
	gitT(t, dir, "switch", "-q", "main")
	commitOn(t, dir, "untracked-ahead")
	gitT(t, dir, "push", "-q", "origin", "untracked-ahead")
	writeFile(t, filepath.Join(dir, "more.txt"), "more\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "unpushed work")

	// Never pushed at all.
	gitT(t, dir, "switch", "-q", "main")
	commitOn(t, dir, "local-only")

	// Tracked, then deleted on the remote.
	gitT(t, dir, "switch", "-q", "main")
	commitOn(t, dir, "deleted-upstream")
	gitT(t, dir, "push", "-q", "-u", "origin", "deleted-upstream")
	gitT(t, dir, "push", "-q", "origin", "--delete", "deleted-upstream")
	gitT(t, dir, "fetch", "-q", "--prune", "origin")

	gitT(t, dir, "switch", "-q", "main")

	branches, err := LocalBranches(context.Background(), dir, "main", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	byName := map[string]BranchInfo{}
	for _, bi := range branches {
		byName[bi.Name] = bi
	}

	for _, tc := range []struct {
		branch    string
		hasRemote bool
		ahead     int
		gone      bool
	}{
		{"untracked-pushed", true, 0, false},
		{"untracked-ahead", true, 1, false},
		{"local-only", false, 0, false},
		{"deleted-upstream", false, 0, true},
	} {
		bi, ok := byName[tc.branch]
		if !ok {
			t.Fatalf("%q missing from LocalBranches", tc.branch)
		}
		if bi.HasRemote != tc.hasRemote {
			t.Errorf("%q HasRemote = %v, want %v", tc.branch, bi.HasRemote, tc.hasRemote)
		}
		if bi.Ahead != tc.ahead {
			t.Errorf("%q Ahead = %d, want %d", tc.branch, bi.Ahead, tc.ahead)
		}
		if bi.UpstreamGone != tc.gone {
			t.Errorf("%q UpstreamGone = %v, want %v", tc.branch, bi.UpstreamGone, tc.gone)
		}
	}

	gone, fullyPushed := UpstreamState(branches)
	if !fullyPushed["untracked-pushed"] {
		t.Error("a branch pushed without -u is entirely on origin, but did not classify as fully pushed")
	}
	if fullyPushed["untracked-ahead"] {
		t.Error("a branch with a commit origin has not seen classified as fully pushed — deleting it would lose work")
	}
	if fullyPushed["local-only"] || gone["local-only"] {
		t.Error("a never-pushed branch must be neither gone nor fully pushed — nothing on origin holds it")
	}
	if !gone["deleted-upstream"] {
		t.Error("a branch deleted on the remote did not classify as gone")
	}

	// The unsafe check has to follow the same reading of "on origin", or a branch
	// pushed without -u gets quarantined as holding unsaved work it does not hold.
	if HasUnpushedCommits(context.Background(), dir, byName["untracked-pushed"], "main") {
		t.Error("a branch fully pushed without -u read as having unpushed commits")
	}
	if !HasUnpushedCommits(context.Background(), dir, byName["untracked-ahead"], "main") {
		t.Error("a branch with a local-only commit read as fully pushed")
	}
	if !HasUnpushedCommits(context.Background(), dir, byName["local-only"], "main") {
		t.Error("a never-pushed branch read as having no unpushed commits")
	}
}

// TestGoneNeedsOriginToHaveNothing pins that a branch whose configured upstream
// vanished but which origin still carries under its own name is not gone. Its work
// is on the remote; sweeping it as gone would be judging by a stale config entry
// rather than by what origin actually holds.
func TestGoneNeedsOriginToHaveNothing(t *testing.T) {
	origin := t.TempDir()
	gitT(t, origin, "init", "-q", "--bare", "-b", "main")

	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "init")
	gitT(t, dir, "remote", "add", "origin", origin)
	gitT(t, dir, "push", "-q", "-u", "origin", "main")

	// Track an upstream under a different name, delete that remote branch, then
	// push the branch again under its own name — the config still points at the
	// deleted ref.
	commitOn(t, dir, "revived")
	gitT(t, dir, "push", "-q", "-u", "origin", "revived:old-name")
	gitT(t, dir, "push", "-q", "origin", "--delete", "old-name")
	gitT(t, dir, "push", "-q", "origin", "revived")
	gitT(t, dir, "fetch", "-q", "--prune", "origin")
	gitT(t, dir, "switch", "-q", "main")

	branches, err := LocalBranches(context.Background(), dir, "main", nil)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	gone, fullyPushed := UpstreamState(branches)
	if gone["revived"] {
		t.Error("branch classified gone while origin still carries it — deleting it on that signal drops a live branch")
	}
	if !fullyPushed["revived"] {
		t.Error("branch present on origin and not ahead of it did not classify as fully pushed")
	}
}
