// Fixtures for the tracker axis (admin-p55x). Before this file, the tracker
// axis rendered three LIVE checkouts under the developer's home directory —
// this repo plus two unrelated projects — so the suite could not run on a
// fresh clone, could not run with those repos absent, and changed result
// whenever the primary checkout's (untracked) admin.toml changed. Each axis
// now gets a temp-dir repo built here, plus a canned internal/tui.
// UseStaticRemote so the PRs axis needs no `gh` call and no repo that happens
// to have open PRs.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/tui"
)

var (
	fixtureOnce sync.Once
	fixtureDir  string
	fixtures    map[string]repoSrc

	restoreArchetypeDir func()
	restoreRemote       func()
)

// commonManifest is the part every axis shares: a real project_name plus a
// couple of commands/actions so the Work tab has rows to draw, and a
// [worktrees] root pinned inside the fixture tree so config.WorktreeRoot
// never falls through to the machine's home directory (the second home path
// this ticket found, in config.txt/config-local.txt — model.go's
// worktreeRootRow calls config.WorktreeRoot, which defaults to
// ~/.worktrees only when no root is declared).
const commonManifestHead = `project_name = %q
archetypes   = []
%s`

const commonManifestTail = `
[worktrees]
root = "/fixtures/worktrees"

[commands.build]
desc  = "Validate admin.toml"
steps = ["check"]

[commands.test]
desc  = "Run the test suite"
steps = ["test"]

[actions.check]
kind = "shell"
desc = "Parse and resolve admin.toml"
run  = "true"

[actions.test]
kind = "shell"
desc = "Run tests"
run  = "true"
`

// axisManifest inserts the tracker declaration between project_name and the
// first table header ([worktrees]) rather than after commonManifest: TOML
// attributes a bare key = value line to whichever table header precedes it,
// so appending "tracker = ..." after the last [actions.test] table would
// silently set an unknown field on that action instead of the top-level
// manifest.Trackers/UsesPullRequests fields model.go's trackers() and
// wantsPRs() read. %s is "" for the PRs axis (no tracker declared at all —
// trackers() returns empty and visibleTabs() draws no tracker tab of its
// own, leaving PRs as the only one).
func axisManifest(name, trackerLine string, usesPRs bool) string {
	head := fmt.Sprintf(commonManifestHead, name, trackerLine+fmt.Sprintf("uses_pull_requests = %v\n", usesPRs))
	return head + commonManifestTail
}

// writeFixtureManifest makes dir a git repo with one commit on main, then
// writes admin.toml (and, for the beads axis, an empty .beads/ marker
// directory) into it. The Repo tab reads branches, commits and worktrees from
// the repo root the dashboard is handed, so a fixture that is not a repo draws
// an error there instead of the Branches pane.
func writeFixtureManifest(dir, manifestBody string, beads bool) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=fixture", "-c", "user.email=fixture@fixture", "commit", "-q", "--allow-empty", "-m", "fixture"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			panic(fmt.Sprintf("git %v: %v\n%s", args, err, out))
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(manifestBody), 0o644); err != nil {
		panic(err)
	}
	if beads {
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
			panic(err)
		}
	}
}

// cannedRemote is what tui.UseStaticRemote hands back to every remote read:
// one open PR (so the PRs axis has a real row, not an empty section drawn
// for a legitimate reason) and one open issue (so the github axis does too),
// with every FetchedAt stamp set to now so Remote.Stale is false and the
// dashboard never even attempts a sync.
func cannedRemote() data.Remote {
	now := time.Now()
	return data.Remote{
		PRs: []gh.OpenPR{{
			Number: 42, Title: "fixture PR", Author: "fixture",
			HeadBranch: "fixture-branch", Mergeable: "MERGEABLE", Updated: now,
		}},
		Issues: []gh.IssueItem{{
			Number: 7, Title: "fixture issue", Updated: now,
		}},
		ViewerLogin:     "fixture",
		PRsFetchedAt:    now,
		IssuesFetchedAt: now,
		MergedFetchedAt: now,
	}
}

// buildFixtures creates the three tracker-axis repos under one temp dir, and
// installs the two process-global seams every fixture needs: an empty
// installed-archetypes dir (archetype.UseInstalledDir — otherwise a merge
// would silently pull in whatever base archetype happens to be installed on
// the machine running the test, exactly what internal/tui/hermetic_test.go
// guards against for that package's own tests) and a canned remote
// (tui.UseStaticRemote — no `gh`, no `git fetch`, no on-disk cache read).
func buildFixtures() {
	dir, err := os.MkdirTemp("", "admin-tui-desktop-fixtures-")
	if err != nil {
		panic(err)
	}
	fixtureDir = dir

	beadsRoot := filepath.Join(dir, "beads-repo")
	githubRoot := filepath.Join(dir, "github-repo")
	prsRoot := filepath.Join(dir, "prs-repo")

	writeFixtureManifest(beadsRoot, axisManifest("fixture-beads", "tracker = [\"beads\"]\n", false), true)
	writeFixtureManifest(githubRoot, axisManifest("fixture-github", "tracker = [\"github\"]\n", false), false)
	writeFixtureManifest(prsRoot, axisManifest("fixture-prs", "", true), false)

	fixtures = map[string]repoSrc{
		// nwo is a label only here — tui.UseStaticRemote means nothing keyed
		// by it is ever actually fetched.
		"beads":  {root: beadsRoot, nwo: "fixture/beads-repo", branch: "main"},
		"github": {root: githubRoot, nwo: "fixture/github-repo", branch: "main"},
		"prs":    {root: prsRoot, nwo: "fixture/prs-repo", branch: "main"},
	}

	archDir := filepath.Join(dir, "empty-archetypes")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		panic(err)
	}
	restoreArchetypeDir = archetype.UseInstalledDir(archDir)
	restoreRemote = tui.UseStaticRemote(cannedRemote())
}

// trackerRepo returns the fixture repoSrc for axis ("beads", "github", or
// "prs"), building all three (once per process) on first use.
func trackerRepo(axis string) repoSrc {
	fixtureOnce.Do(buildFixtures)
	src, ok := fixtures[axis]
	if !ok {
		return fixtures["beads"]
	}
	return src
}

// cleanupFixtures restores the seams buildFixtures overrode and removes the
// temp dir. Safe to call even when buildFixtures never ran (nothing dumps or
// tests without touching a tracker screen first, but TestMain calls this
// unconditionally on the way out).
func cleanupFixtures() {
	if restoreArchetypeDir != nil {
		restoreArchetypeDir()
	}
	if restoreRemote != nil {
		restoreRemote()
	}
	if fixtureDir != "" {
		os.RemoveAll(fixtureDir)
	}
}
