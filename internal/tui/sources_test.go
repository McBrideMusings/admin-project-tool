package tui

import (
	"context"
	"slices"
	"testing"

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
		{"prs alone", []tab{tabPRs}, []source{srcSync, srcIdentity}, true},
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
