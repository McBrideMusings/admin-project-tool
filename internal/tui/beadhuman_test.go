package tui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// fakeBdScript is a minimal `bd` stand-in: just enough of `where --json` and
// `list --json` for Init's detect-then-load sequence to succeed against a real
// subprocess, since Detect/List go through internal/bd's unexported runner
// seam and can't be swapped from this package. schema_version must match
// bd.SchemaVersion or Detect refuses the workspace outright.
const fakeBdScript = `#!/bin/sh
case "$1" in
  where)
    echo '{"path":"/fake","database_path":"/fake/db","prefix":"bdtest","schema_version":1}'
    ;;
  list)
    cat <<'JSON'
[
  {"id":"bdtest-1","title":"Waiting on a person","status":"open","priority":1,"issue_type":"task","owner":"","labels":["human"],"updated_at":"2020-01-01T00:00:00Z","dependencies":[]},
  {"id":"bdtest-2","title":"Someone is on this","status":"in_progress","priority":1,"issue_type":"task","owner":"","labels":[],"updated_at":"2020-01-01T00:00:00Z","dependencies":[]},
  {"id":"bdtest-3","title":"Nothing special here","status":"open","priority":2,"issue_type":"task","owner":"","labels":[],"updated_at":"2020-01-01T00:00:00Z","dependencies":[]}
]
JSON
    ;;
esac
`

// fakeBdScriptNoHuman is the same shape with no human-labelled bead at all —
// the fixture for the "with no human beads, neither mode shows an empty
// section or a stray column" acceptance criterion.
const fakeBdScriptNoHuman = `#!/bin/sh
case "$1" in
  where)
    echo '{"path":"/fake","database_path":"/fake/db","prefix":"bdtest","schema_version":1}'
    ;;
  list)
    cat <<'JSON'
[
  {"id":"bdtest-2","title":"Someone is on this","status":"in_progress","priority":1,"issue_type":"task","owner":"","labels":[],"updated_at":"2020-01-01T00:00:00Z","dependencies":[]},
  {"id":"bdtest-3","title":"Nothing special here","status":"open","priority":2,"issue_type":"task","owner":"","labels":[],"updated_at":"2020-01-01T00:00:00Z","dependencies":[]}
]
JSON
    ;;
esac
`

// putFakeBdOnPath writes script as an executable `bd` in a temp bin dir and
// prepends it to PATH for the duration of the test. t.Setenv forbids
// t.Parallel in this file, which is fine — nothing here needs it.
func putFakeBdOnPath(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake bd script is a /bin/sh script; no windows path in this suite")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bd")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake bd: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// beadsModel builds a Model with tracker = ["beads"] declared, positioned on
// the Beads tab, against whatever fake `bd` is currently on PATH.
func beadsModel(t *testing.T) Model {
	t.Helper()
	ctx := context.Background()
	m := New(ctx, t.TempDir(), "owner/repo", "main", "main")
	m.herdr = false
	m.manifest = &manifest.Manifest{Trackers: []string{"beads"}, HasTracker: true}
	return m
}

// frameOn renders m through the headless renderer, driving it with keys
// first (["right"] lands on the Beads tab; append "g" to toggle tree mode),
// and returns the ANSI-stripped output.
func frameOn(t *testing.T, m Model, keys ...string) string {
	t.Helper()
	ctx := context.Background()
	out := Frame(ctx, m, FrameOptions{Width: 140, Height: 30, Settle: 500 * time.Millisecond, Keys: keys})
	return stripANSI(out)
}

// TestBeadsPaneSurfacesHumanRowsFlat pins admin-p4pv's flat-mode rendering: a
// human-labelled bead gets its own "Needs you" section, above Ready, and an
// unlabelled bead is unchanged.
func TestBeadsPaneSurfacesHumanRowsFlat(t *testing.T) {
	putFakeBdOnPath(t, fakeBdScript)
	m := beadsModel(t)

	out := frameOn(t, m, "right")

	if !strings.Contains(out, "Needs you") {
		t.Fatalf("expected a \"Needs you\" section heading, got:\n%s", out)
	}
	humanAt := strings.Index(out, "Waiting on a person")
	readyAt := strings.Index(out, "Ready")
	if humanAt < 0 || readyAt < 0 {
		t.Fatalf("expected both the human bead's title and the Ready heading, got:\n%s", out)
	}
	if humanAt >= readyAt {
		t.Errorf("human bead did not float above the Ready section: human at %d, Ready at %d", humanAt, readyAt)
	}
	if !strings.Contains(out, "Nothing special here") {
		t.Errorf("expected the plain bead's title to render unchanged, got:\n%s", out)
	}
	if !strings.Contains(out, "human") {
		t.Errorf("expected the \"human\" label to still render in the labels column, got:\n%s", out)
	}
}

// TestBeadsPaneSurfacesHumanRowsTree pins tree mode's glyph: a human bead
// renders "?" and an in-progress bead keeps its existing "▶", distinct cells
// in the glyph column.
func TestBeadsPaneSurfacesHumanRowsTree(t *testing.T) {
	putFakeBdOnPath(t, fakeBdScript)
	m := beadsModel(t)

	out := frameOn(t, m, "right", "g")

	if !strings.Contains(out, "?") {
		t.Errorf("expected the human bead's row to carry the \"?\" glyph, got:\n%s", out)
	}
	if !strings.Contains(out, "▶") {
		t.Errorf("expected the in-progress bead's row to still carry \"▶\", got:\n%s", out)
	}
	if strings.Contains(out, "Needs you") {
		t.Errorf("tree mode has no section headings; \"Needs you\" should not appear, got:\n%s", out)
	}
}

// TestBeadsPaneWithNoHumanBeadsShowsNoSection covers the "with no human beads
// at all, neither mode shows an empty section or a stray column" criterion.
func TestBeadsPaneWithNoHumanBeadsShowsNoSection(t *testing.T) {
	putFakeBdOnPath(t, fakeBdScriptNoHuman)
	m := beadsModel(t)

	flat := frameOn(t, m, "right")
	if strings.Contains(flat, "Needs you") {
		t.Errorf("no human bead in this workspace; flat mode should not show \"Needs you\", got:\n%s", flat)
	}
	if strings.Contains(flat, "?") {
		t.Errorf("no human bead in this workspace; flat mode should not show the \"?\" glyph, got:\n%s", flat)
	}

	tree := frameOn(t, m, "right", "g")
	if strings.Contains(tree, "?") {
		t.Errorf("no human bead in this workspace; tree mode should not show the \"?\" glyph, got:\n%s", tree)
	}
}
