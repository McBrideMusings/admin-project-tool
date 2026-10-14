package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/viewstate"
)

// TestSourcesStartOnlyForMountedTabs pins ADR-0026's source rule: the shell
// starts a data source only when a mounted tab declares it, and bare `admin`
// (nothing narrowed) starts every source the project can feed.
func TestSourcesStartOnlyForMountedTabs(t *testing.T) {
	cases := []struct {
		name    string
		mounted []tab
		want    []source
		sync    bool // whether a forced sync may go out
	}{
		{"bare admin", nil, []source{srcSync, srcBeads, srcWork, srcIdentity}, true},
		{"config alone", []tab{tabConfig}, nil, false},
		{"beads alone", []tab{tabBeads}, []source{srcBeads}, false},
		{"commands alone", []tab{tabWork}, []source{srcWork}, false},
		{"prs alone", []tab{tabPRs}, []source{srcSync}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{
				ctx:        context.Background(),
				nwo:        "owner/repo",
				shell:      shell{mounted: tc.mounted, repoRoot: t.TempDir()},
				manifestOK: true,
				manifest: &manifest.Manifest{
					HasTracker: true, Trackers: []string{"beads"},
					HasUsesPullRequests: true, UsesPullRequests: true,
				},
				headless: true,
			}
			m.mountTabs(viewstate.State{})
			got := m.claimSources()
			if !slices.Equal(got, tc.want) {
				t.Errorf("claimed %v, want %v", got, tc.want)
			}
			if again := m.claimSources(); len(again) != 0 {
				t.Errorf("second claim started %v again, want nothing", again)
			}
			if gotSync := m.startSyncCmd(true) != nil; gotSync != tc.sync {
				t.Errorf("forced sync went out = %v, want %v", gotSync, tc.sync)
			}
		})
	}
}

// TestIssuesTabMountedAloneMatchesBare pins the Issues slice's acceptance: the
// tab declares only the sync, so mounted alone it starts exactly that, and it
// renders the same rows as under bare admin.
func TestIssuesTabMountedAloneMatchesBare(t *testing.T) {
	build := func(mounted []tab) Model {
		m := Model{
			ctx:        context.Background(),
			nwo:        "owner/repo",
			shell:      shell{mounted: mounted, repoRoot: t.TempDir(), width: 120, height: 40},
			manifestOK: true,
			manifest:   &manifest.Manifest{HasTracker: true, Trackers: []string{"github"}},
			headless:   true,
		}
		m.mountTabs(viewstate.State{})
		m.remote.Issues = []gh.IssueItem{{Number: 5, Title: "Fix bug", Labels: []string{"bug"}}}
		m.haveData = true
		return m
	}
	alone, bare := build([]tab{tabIssues}), build(nil)
	if got := alone.claimSources(); !slices.Equal(got, []source{srcSync}) {
		t.Errorf("issues alone claimed %v, want [srcSync]", got)
	}
	aloneRows := alone.issuesTab.rows(&alone.shell)
	bareRows := bare.issuesTab.rows(&bare.shell)
	if len(aloneRows) == 0 || len(aloneRows) != len(bareRows) {
		t.Fatalf("issues alone drew %d rows, bare drew %d", len(aloneRows), len(bareRows))
	}
	for i := range aloneRows {
		if a, b := fmt.Sprint(rowLine(aloneRows[i], 120)), fmt.Sprint(rowLine(bareRows[i], 120)); a != b {
			t.Errorf("row %d: alone %q, bare %q", i, a, b)
		}
	}
}

// TestPRsTabMountedAloneMatchesBare pins the PRs slice's acceptance: the tab
// declares only the sync, so mounted alone it starts exactly that, and it
// renders the same rows as under bare admin.
func TestPRsTabMountedAloneMatchesBare(t *testing.T) {
	build := func(mounted []tab) Model {
		m := Model{
			ctx:        context.Background(),
			nwo:        "owner/repo",
			shell:      shell{mounted: mounted, repoRoot: t.TempDir(), width: 120, height: 40},
			manifestOK: true,
			manifest:   &manifest.Manifest{HasUsesPullRequests: true, UsesPullRequests: true},
			headless:   true,
		}
		m.mountTabs(viewstate.State{})
		m.remote.PRs = []gh.OpenPR{
			{Number: 7, Title: "Add thing", Author: "me", HeadBranch: "add-thing", Bucket: gh.BucketMine},
			{Number: 8, Title: "Fix other", Author: "you", HeadBranch: "fix-other", Bucket: gh.BucketOther},
		}
		m.haveData = true
		return m
	}
	alone, bare := build([]tab{tabPRs}), build(nil)
	if got := alone.claimSources(); !slices.Equal(got, []source{srcSync}) {
		t.Errorf("prs alone claimed %v, want [srcSync]", got)
	}
	aloneRows := alone.prsTab.rows(&alone.shell)
	bareRows := bare.prsTab.rows(&bare.shell)
	if len(aloneRows) == 0 || len(aloneRows) != len(bareRows) {
		t.Fatalf("prs alone drew %d rows, bare drew %d", len(aloneRows), len(bareRows))
	}
	for i := range aloneRows {
		if a, b := fmt.Sprint(rowLine(aloneRows[i], 120)), fmt.Sprint(rowLine(bareRows[i], 120)); a != b {
			t.Errorf("row %d: alone %q, bare %q", i, a, b)
		}
	}
}

// TestRepoTabMountedAloneMatchesBare pins the Repo slice's acceptance: the tab
// declares the sync, beads and identity sources through its constructor's
// handles, so mounted alone it starts exactly those, and it draws the same
// panes as under bare admin.
func TestRepoTabMountedAloneMatchesBare(t *testing.T) {
	build := func(mounted []tab) Model {
		m := Model{
			ctx:        context.Background(),
			nwo:        "owner/repo",
			def:        "main",
			shell:      shell{mounted: mounted, repoRoot: t.TempDir(), width: 120, height: 40, active: tabLocal},
			manifestOK: true,
			manifest:   &manifest.Manifest{HasTracker: true, Trackers: []string{"beads"}},
			headless:   true,
		}
		m.mountTabs(viewstate.State{})
		m.local = localData{
			branches: []git.BranchInfo{{Name: "main", Current: true}, {Name: "feat-x", LastCommit: "1 hour ago"}},
			commits:  []git.Commit{{SHA: "abc123", Subject: "do a thing", When: "1 hour ago"}},
		}
		m.haveLocal = true
		return m
	}
	alone, bare := build([]tab{tabLocal}), build(nil)
	if got := alone.claimSources(); !slices.Equal(got, []source{srcSync, srcBeads, srcIdentity}) {
		t.Errorf("repo alone claimed %v, want [srcSync srcBeads srcIdentity]", got)
	}
	aloneBody, _ := alone.repoTab.body(&alone.shell, 120, 30)
	bareBody, _ := bare.repoTab.body(&bare.shell, 120, 30)
	if len(aloneBody) == 0 || !slices.Equal(aloneBody, bareBody) {
		t.Errorf("repo alone drew %d lines, bare drew %d, or they differ", len(aloneBody), len(bareBody))
	}
	if !strings.Contains(strings.Join(aloneBody, "\n"), "feat-x") {
		t.Errorf("repo tab mounted alone does not list the branch:\n%s", strings.Join(aloneBody, "\n"))
	}
}
