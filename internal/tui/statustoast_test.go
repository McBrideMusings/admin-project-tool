// statustoast_test.go covers admin-dky7's status-as-toast half: footer()
// never grows a second line for a status message anymore — the message
// overlays the last body row as a one-line toast instead (view.go's
// spliceStatusToast) — and the toast's own expiry generalises the
// "copied to clipboard" toast's gen-guarded timer (model.go's setStatus /
// statusExpiredMsg).
package tui

import (
	"strings"
	"testing"
)

// TestFooterStaysOneLineWithStatusSet is the core layout claim: setting a
// status no longer turns footer() into two lines, so body height (frameGeom)
// never has to shrink to make room for it.
func TestFooterStaysOneLineWithStatusSet(t *testing.T) {
	m := seededModel()
	const width = 100

	_, _, bodyHeightBefore := m.frameGeom()

	m.setStatus("refreshed", cDim)
	if strings.Contains(m.footer(width), "\n") {
		t.Errorf("footer() = %q, want a single line even with a status set", m.footer(width))
	}

	_, _, bodyHeightAfter := m.frameGeom()
	if bodyHeightAfter != bodyHeightBefore {
		t.Errorf("bodyHeight changed from %d to %d when a status was set, want it unchanged", bodyHeightBefore, bodyHeightAfter)
	}
}

// TestStatusToastOverlaysLastBodyRow proves the message actually reaches the
// screen: it appears on the LAST row of the rendered body (frameRaw), not
// nowhere and not on a second footer line.
func TestStatusToastOverlaysLastBodyRow(t *testing.T) {
	m := seededModel()
	m.setStatus("something happened", cDim)

	lines, _ := m.frameRaw()
	_, _, bodyHeight := m.frameGeom()
	const topRows = 3
	lastBodyRow := lines[topRows+bodyHeight-1]
	if !strings.Contains(stripANSI(lastBodyRow), "something happened") {
		t.Errorf("last body row = %q, want it to carry the status toast", lastBodyRow)
	}
	// Every other body row is untouched by the toast.
	for i := topRows; i < topRows+bodyHeight-1; i++ {
		if strings.Contains(lines[i], "something happened") {
			t.Errorf("row %d unexpectedly carries the toast text, want only the last body row: %q", i, lines[i])
		}
	}
}

// TestNonErrorStatusExpiresOnGenMatch and its stale-gen sibling below pin the
// generalised toastGen mechanism: a non-error setStatus schedules an expiry
// tick, and only a tick whose gen still matches the model's current toastGen
// actually clears the status — a stale one (superseded by a newer status)
// must be a no-op.
func TestNonErrorStatusExpiresOnGenMatch(t *testing.T) {
	m := seededModel()
	cmd := m.setStatus("refreshed", cDim)
	if cmd == nil {
		t.Fatal("setStatus for a non-error status returned a nil cmd, want the expiry tick")
	}
	gen := m.toastGen

	updated, _ := m.Update(statusExpiredMsg{gen: gen})
	mm := updated.(Model)
	if mm.status != "" {
		t.Errorf("status = %q after a matching-gen expiry, want cleared", mm.status)
	}
}

func TestStaleGenExpiryIsANoOp(t *testing.T) {
	m := seededModel()
	m.setStatus("first", cDim)
	staleGen := m.toastGen
	m.setStatus("second", cDim) // bumps toastGen again — staleGen is now behind

	updated, _ := m.Update(statusExpiredMsg{gen: staleGen})
	mm := updated.(Model)
	if mm.status != "second" {
		t.Errorf("status = %q after a stale-gen expiry, want it untouched (\"second\")", mm.status)
	}
}

// TestErrorStatusSurvivesExpiryClearsOnKey is the error half: an error status
// (fg == cRed) gets no expiry cmd from setStatus at all, and instead clears
// the moment any key is pressed — not before.
func TestErrorStatusSurvivesExpiryClearsOnKey(t *testing.T) {
	m := seededModel()
	cmd := m.setStatus("refresh failed: boom", cRed)
	if cmd != nil {
		t.Error("setStatus for an error status returned a non-nil cmd, want none — errors don't expire on a timer")
	}

	// Even a matching-gen expiry msg (there shouldn't be one in flight, but
	// prove it anyway) leaves an error status alone.
	updated, _ := m.Update(statusExpiredMsg{gen: m.toastGen})
	mm := updated.(Model)
	if mm.status == "" {
		t.Fatal("error status cleared on an expiry msg, want it to survive")
	}

	nm, _ := mm.handleKey(keyMsg("g"))
	nmm := nm.(Model)
	if nmm.status != "" {
		t.Errorf("status = %q after a keypress, want the error cleared", nmm.status)
	}
}
