// panetail_test.go covers panetail.go and the band's launcher-entry text: the
// read command's stdout lands in the band as the snapshot, and a launcher
// with no read line keeps the attach hint.
package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// launcherBandModel is buildRunningWorkModelAt's shape with a
// launcher-dispatched (Via "terminal", no Output) entry under the cursor.
func launcherBandModel(t *testing.T) Model {
	t.Helper()
	freshLivework(t)
	mm := workTestModel(&manifest.Manifest{
		Custom:  map[string]manifest.Command{"dev": {Desc: "dev", Steps: []string{"dev"}}},
		Actions: map[string]manifest.Action{"dev": {"kind": "shell"}},
	})
	mm.active, mm.focus = tabWork, focusBody
	mm.width, mm.height = 96, 30
	if err := livework.Default.Add(&livework.Entry{
		ID: "dev", Name: "dev", Handle: "pane-7", Via: "terminal",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for i, r := range mm.workRows() {
		if r.kind == rowCommand && r.work.cmdName == "dev" {
			mm.cursor[tabWork] = i
		}
	}
	return mm
}

func renderedBand(mm Model) string {
	out, _ := mm.renderWorkBody(96, 30)
	return strings.Join(out, "\n")
}

func TestBandShowsLauncherPaneSnapshot(t *testing.T) {
	mm := launcherBandModel(t)
	var gotHandle string
	old := paneReadFn
	paneReadFn = func(_ context.Context, run, handle string) (string, error) {
		gotHandle = handle
		return "compiling...\nlistening on :3000\n", nil
	}
	t.Cleanup(func() { paneReadFn = old })

	msg := mm.pollPaneTailCmd("read-cmd")()
	applyPaneTail(msg.(paneTailMsg))

	if gotHandle != "pane-7" {
		t.Errorf("read got handle %q, want pane-7", gotHandle)
	}
	band := renderedBand(mm)
	if !strings.Contains(band, "listening on :3000") {
		t.Errorf("band lacks the snapshot:\n%s", band)
	}
	if strings.Contains(band, "no local output") {
		t.Errorf("band still shows the fallback:\n%s", band)
	}
}

func TestBandWithoutReadKeepsAttachHint(t *testing.T) {
	mm := launcherBandModel(t)
	if cmd := mm.pollPaneTailCmd(""); cmd != nil {
		t.Error("pollPaneTailCmd with no read command returned a cmd")
	}
	if !strings.Contains(renderedBand(mm), "no local output - attach to see the real session") {
		t.Errorf("band lacks the fallback:\n%s", renderedBand(mm))
	}
}

// A hosted agent session repaints in place, so the band reads its screen grid
// and skips blank and rule-only rows.
func TestScreenTailLinesKeepsWordRows(t *testing.T) {
	screen := vt.NewSafeEmulator(40, 8)
	_, _ = screen.Write([]byte("editing local.go\r\n\r\n────────\r\nrunning tests\r\n"))
	got := screenTailLines(screen, 40, 24)
	want := []string{"editing local.go", "running tests"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("screenTailLines = %q, want %q", got, want)
	}
}

// A frame that sees fewer Work rows than the stored cursor (a filter typed
// while the cursor sits low) draws the last row instead of panicking.
func TestRenderWorkBodyCursorPastRows(t *testing.T) {
	m := launcherBandModel(t)
	m.cursor[m.active] = 999
	m.renderWorkBody(80, 24)
}
