package tui

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func TestManifestConfigLinesNoManifest(t *testing.T) {
	m := Model{manifestOK: false}
	lines := m.manifestConfigLines(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "no admin.toml") {
		t.Errorf("manifestConfigLines(no manifest) = %q, want it to say no admin.toml", joined)
	}
}

// TestManifestConfigLinesTrackerDeclaredAndDefaulted is admin-3rgp.21's core
// acceptance criterion for the manifest half of the tab: a declared key
// shows its value and origin, an undeclared one shows the "not declared"
// placeholder rather than a guessed value.
func TestManifestConfigLinesTrackerDeclaredAndDefaulted(t *testing.T) {
	m := Model{
		manifestOK: true,
		manifest:   &manifest.Manifest{Tracker: "beads", HasTracker: true},
		keyOrigin:  map[string]string{"tracker": "(manifest)"},
	}
	lines := m.manifestConfigLines(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "beads") || !strings.Contains(joined, "(manifest)") {
		t.Errorf("manifestConfigLines(tracker declared) = %q, want it to show \"beads\" and its \"(manifest)\" origin", joined)
	}

	m2 := Model{manifestOK: true, manifest: &manifest.Manifest{}}
	lines2 := m2.manifestConfigLines(80)
	joined2 := strings.Join(lines2, "\n")
	if !strings.Contains(joined2, "no tracker declared") {
		t.Errorf("manifestConfigLines(no tracker) = %q, want \"no tracker declared\" per the AC (never guess one)", joined2)
	}
	// uses_pull_requests has a real bool default (false), so it renders that
	// value marked defaulted, unlike tracker which has no default value at
	// all — the AC draws this exact distinction.
	if !strings.Contains(joined2, "false") || !strings.Contains(joined2, "defaulted") {
		t.Errorf("manifestConfigLines(uses_pull_requests undeclared) = %q, want \"false\" marked defaulted", joined2)
	}
}

func TestManifestConfigLinesUsesArchetypeOrigin(t *testing.T) {
	m := Model{
		manifestOK: true,
		manifest:   &manifest.Manifest{Tracker: "github", HasTracker: true},
		keyOrigin:  map[string]string{"tracker": "beads-tracker"},
	}
	lines := m.manifestConfigLines(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "beads-tracker") {
		t.Errorf("manifestConfigLines = %q, want it to name the declaring archetype \"beads-tracker\"", joined)
	}
}

func TestEmailOriginNamesTheDeclaringFile(t *testing.T) {
	global := config.Config{Identity: config.Identity{Emails: []string{"me@Global.com"}}}
	local := config.Config{Identity: config.Identity{Emails: []string{"me@local.com"}}}

	if got := emailOrigin("me@global.com", global, local); got != "global" {
		t.Errorf(`emailOrigin(global address) = %q, want "global" (case-insensitive match)`, got)
	}
	if got := emailOrigin("me@local.com", global, local); got != "local" {
		t.Errorf(`emailOrigin(local address) = %q, want "local"`, got)
	}
	if got := emailOrigin("nowhere@x.com", global, local); got != "?" {
		t.Errorf(`emailOrigin(unknown address) = %q, want "?"`, got)
	}
}

// TestDotfileConfigLinesReportsFileOrigin exercises the identity/worktree
// half of the tab: a value declared in one file only names that file, not
// "defaulted" and not the other file.
func TestDotfileConfigLinesReportsFileOrigin(t *testing.T) {
	m := Model{
		cfgGlobal: config.Config{Identity: config.Identity{Login: "pierce"}},
		cfgLocal:  config.Config{Worktrees: config.Worktrees{Root: "/tmp/wt"}},
		cfgMerged: config.Config{
			Identity:  config.Identity{Login: "pierce"},
			Worktrees: config.Worktrees{Root: "/tmp/wt"},
		},
	}
	lines := m.dotfileConfigLines(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "pierce") || !strings.Contains(joined, "global config") {
		t.Errorf("dotfileConfigLines(login) = %q, want \"pierce\" attributed to global config", joined)
	}
	if !strings.Contains(joined, "/tmp/wt") || !strings.Contains(joined, "local config") {
		t.Errorf("dotfileConfigLines(worktree root) = %q, want \"/tmp/wt\" attributed to local config", joined)
	}
	// Nothing declared the default agent anywhere.
	if !strings.Contains(joined, "default agent") {
		t.Errorf("dotfileConfigLines missing the \"default agent\" row entirely: %q", joined)
	}
}
