package tui

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/status"
)

func stubStatus(t *testing.T, fn func(manifest.StatusSpec, string) status.Result) {
	t.Helper()
	orig := statusProbes.setRun(fn)
	statusProbes.reset()
	t.Cleanup(func() {
		statusProbes.setRun(orig)
		statusProbes.reset()
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// TestStatusRowsShowReadoutAfterRefresh: before the first check lands a row
// has no text; after it, the row carries the label and the readout.
func TestStatusRowsShowReadoutAfterRefresh(t *testing.T) {
	stubStatus(t, func(spec manifest.StatusSpec, _ string) status.Result {
		return status.Result{State: status.Up, Text: "2 running · 1 failed"}
	})
	entries := map[string]manifest.StatusSpec{"jobs": {Kind: manifest.StatusJSON}}

	rows := statusRows(entries)
	if len(rows) != 2 || rows[0].label != "Status" {
		t.Fatalf("rows = %+v, want header and one entry", rows)
	}

	statusProbes.Refresh(entries, "")
	waitFor(t, func() bool { return statusProbes.Get("jobs").Text != "" })

	line := renderSegs(statusRows(entries)[1].leftSegs, nil)
	if !strings.Contains(line, "jobs") || !strings.Contains(line, "2 running · 1 failed") {
		t.Errorf("row = %q", line)
	}
}

// TestStatusRefreshHonoursEachEntrysInterval: an entry is not re-checked
// before its own `every`, and an in-flight check is never doubled.
func TestStatusRefreshHonoursEachEntrysInterval(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	stubStatus(t, func(spec manifest.StatusSpec, _ string) status.Result {
		mu.Lock()
		calls++
		mu.Unlock()
		return status.Result{State: status.Up, Text: "ok"}
	})
	entries := map[string]manifest.StatusSpec{"a": {Kind: manifest.StatusCommand, Every: time.Hour}}

	statusProbes.Refresh(entries, "")
	waitFor(t, func() bool { return statusProbes.Get("a").Text == "ok" })
	statusProbes.Refresh(entries, "")
	statusProbes.Refresh(entries, "")
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("checker ran %d times inside a 1h interval, want 1", calls)
	}
}

func TestStatusRowsAbsentWithoutEntries(t *testing.T) {
	if rows := statusRows(nil); rows != nil {
		t.Errorf("rows = %+v, want none", rows)
	}
}

// The strip takes its rows from the list, never the band: the band's title
// rule sits on the same body row with [status] entries as without them
// (docs/spikes/admin-tui-desktop/commands-status.txt against commands.txt),
// including when more entries are declared than the strip can show.
func TestStatusStripLeavesTheBandInPlace(t *testing.T) {
	stubStatus(t, func(manifest.StatusSpec, string) status.Result { return status.Result{} })
	freshLivework(t)
	titleRow := func(st map[string]manifest.StatusSpec, h int) int {
		m := &manifest.Manifest{
			Status:  st,
			Custom:  map[string]manifest.Command{"dev": {Desc: "start dev server", Steps: []string{"dev"}}},
			Actions: map[string]manifest.Action{"dev": {"kind": "shell", "run": "run-dev"}},
		}
		mm := workTestModel(m)
		mm.cursor[mm.active] = 1 // the "dev" row, under the Commands header
		lines, _ := mm.renderWorkBody(96, h)
		for i, l := range lines {
			if strings.HasPrefix(stripANSI(l), "── ") {
				return i
			}
		}
		t.Fatalf("no band title rule in:\n%s", strings.Join(lines, "\n"))
		return -1
	}
	entries := func(n int) map[string]manifest.StatusSpec {
		st := map[string]manifest.StatusSpec{}
		for i := range n {
			st[string(rune('a'+i))] = manifest.StatusSpec{Kind: manifest.StatusHTTP, URL: "http://x"}
		}
		return st
	}
	// Body heights for 26-32 terminal rows (5 rows of chrome) and the band
	// title's body row the spike draws at each: terminal rows 15/16/17/18.
	for _, tc := range []struct{ h, want int }{{21, 11}, {23, 12}, {25, 13}, {27, 14}} {
		for _, n := range []int{0, 2, 12} {
			if got := titleRow(entries(n), tc.h); got != tc.want {
				t.Errorf("body %d rows, %d [status] entries: band title row = %d, want %d", tc.h, n, got, tc.want)
			}
		}
	}
}

// More entries than fit: the strip says how many it hid instead of dropping
// them silently.
func TestStatusStripSaysHowManyEntriesItHid(t *testing.T) {
	stubStatus(t, func(manifest.StatusSpec, string) status.Result { return status.Result{} })
	entries := map[string]manifest.StatusSpec{}
	for _, l := range []string{"a", "b", "c", "d", "e"} {
		entries[l] = manifest.StatusSpec{Kind: manifest.StatusHTTP, URL: "http://x"}
	}
	lines, _ := statusStrip(entries, 80, 4)
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4", len(lines))
	}
	if last := lines[3]; !strings.Contains(last, "3 more status") {
		t.Errorf("last line = %q, want a '3 more status' marker (6 rows, 3 shown)", last)
	}
}
