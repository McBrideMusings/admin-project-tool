package tui

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
)

// The row gives the title every cell the id and age leave; the strip is where a
// title longer than that is read whole, with the columns the row no longer draws.
func TestBeadStripShowsTheWholeTitleAndTheDroppedColumns(t *testing.T) {
	const cols = 80
	long := "Verify the Apple device paths on real hardware: dev_ios, dev_tv, dev_device, deploy_ios, deploy_tv"
	m := seededModel()
	m.width, m.height = cols, 30
	m.haveBeads, m.hasBeads = true, true
	m.beadPrefix = "bdtest"
	m.beads = []bd.Bead{{ID: "bdtest-47", Title: long, Status: "open", Type: "task", Priority: 2, Labels: []string{"area:infra", "human"}}}
	m.active = tabBeads
	for i, r := range m.visibleRows() {
		if r.kind == rowBead {
			m.cursor[tabBeads] = i
		}
	}

	frame := strings.Join(m.beadStrip(cols), "\n")
	for _, want := range []string{"Verify the Apple device paths on real hardware:", "deploy_tv", "bdtest-47", "task", "P2", "area:infra, human"} {
		if !strings.Contains(frame, want) {
			t.Errorf("strip is missing %q:\n%s", want, frame)
		}
	}
	if got := len(m.beadStrip(cols)); got != beadStripRows {
		t.Errorf("strip is %d rows, want %d", got, beadStripRows)
	}

	m.beads[0].Labels = []string{"area:infra", "area:reliability", "area:data", "area:api", "area:ui", "human"}
	for _, l := range m.beadStrip(cols) {
		if vlen(l) > cols {
			t.Errorf("strip row is %d cells wide, want at most %d: %q", vlen(l), cols, l)
		}
	}

	if !m.showBeadStrip(beadStripMinBody) || m.showBeadStrip(beadStripMinBody-1) {
		t.Errorf("strip must show at a body of %d rows and not below", beadStripMinBody)
	}
	empty := m
	empty.beads = nil
	if empty.showBeadStrip(30) {
		t.Errorf("strip drawn with no beads to describe")
	}

	m.beads[0].Labels = []string{"area:infra", "human"}
	rows := rowsOfKind(m.beadRows(), rowBead)
	row := segsText(composeLR(rows[0].leftSegs, rows[0].rightSegs, cols))
	if strings.Contains(row, "area:infra") || strings.Contains(row, "human") {
		t.Errorf("row draws the raw labels; the strip has those, the row shows the short form: %q", row)
	}
	if !strings.Contains(row, "task") || !strings.Contains(row, "infra") {
		t.Errorf("row is missing the type word or the short label: %q", row)
	}
}

func TestShortBeadLabels(t *testing.T) {
	for _, c := range []struct {
		labels []string
		w      int
		want   string
	}{
		{[]string{"area:tui", "area:work"}, 18, "tui, work"},
		{[]string{"area:infra", "human"}, 18, "infra"},
		{[]string{"human"}, 18, ""},
		{[]string{"area:data", "area:reliability"}, 18, "data, reliability"},
		{[]string{"area:data", "area:reliability"}, 10, "data +1"},
		{[]string{"area:reliability"}, 6, "relia…"},
		{nil, 18, ""},
	} {
		if got := shortBeadLabels(c.labels, c.w); got != c.want {
			t.Errorf("shortBeadLabels(%v, %d) = %q, want %q", c.labels, c.w, got, c.want)
		}
	}
}
