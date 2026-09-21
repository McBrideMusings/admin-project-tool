// statustoast_test.go covers admin-dky7's status-as-toast half: footer()
// never grows a second line for a status message — the message overlays the
// last body row as a one-line toast instead (view.go's spliceStatusToast) —
// and Update schedules that toast's expiry itself (model.go's setStatus /
// statusExpiredMsg): any status a case inside update sets, from any call
// site, expires on the same gen-guarded timer without that call site having
// to thread a cmd of its own back out.
package tui

import (
	"errors"
	"strings"
	"testing"
)

// TestFooterStaysOneLineWithStatusSet is the core layout claim: setting a
// status leaves footer() at one line, so body height (frameGeom) is the
// same with or without it.
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

// TestNonErrorStatusExpiresOnGenMatch drives a non-error status through the
// real key path — "x" with a selection made up only of the branch checked
// out right now, which cleanup.go's deleteSelectedBranches filters out and
// then refuses with a plain m.setStatus("nothing selected", ...) call that
// threads no cmd of its own — and proves Update schedules that status's
// expiry: a statusExpiredMsg carrying the resulting toastGen clears it.
func TestNonErrorStatusExpiresOnGenMatch(t *testing.T) {
	m := seededModel()
	m.active, m.pane = tabLocal, paneBranches
	m.selected = map[string]bool{"feature": true} // the checked-out branch — always refused

	updated, cmd := m.Update(keyMsg("x"))
	mm := updated.(Model)
	if mm.status != "nothing selected" {
		t.Fatalf("status = %q after x with only a refused branch selected, want %q", mm.status, "nothing selected")
	}
	if cmd == nil {
		t.Fatal("Update returned a nil cmd for a non-error status, want the scheduled expiry tick")
	}
	gen := mm.toastGen

	updated2, _ := mm.Update(statusExpiredMsg{gen: gen})
	mm2 := updated2.(Model)
	if mm2.status != "" {
		t.Errorf("status = %q after a matching-gen expiry, want cleared", mm2.status)
	}
}

// TestStaleGenExpiryIsANoOp proves a stale gen — superseded by a later status
// on the same model — is a no-op rather than clearing the newer toast.
func TestStaleGenExpiryIsANoOp(t *testing.T) {
	m := seededModel()
	m.active, m.pane = tabLocal, paneBranches
	m.selected = map[string]bool{"feature": true}

	updated, _ := m.Update(keyMsg("x")) // "nothing selected"
	mm := updated.(Model)
	staleGen := mm.toastGen

	updated2, _ := mm.Update(keyMsg("p")) // "pulling…" — bumps toastGen again
	mm2 := updated2.(Model)

	updated3, _ := mm2.Update(statusExpiredMsg{gen: staleGen})
	mm3 := updated3.(Model)
	if mm3.status != "pulling…" {
		t.Errorf("status = %q after a stale-gen expiry, want it untouched (%q)", mm3.status, "pulling…")
	}
}

// TestErrorStatusSurvivesExpiryClearsOnKey is the error half: an error
// status (fg == cRed) gets no expiry cmd scheduled at all, and instead
// clears the moment any key is pressed — not before.
func TestErrorStatusSurvivesExpiryClearsOnKey(t *testing.T) {
	m := seededModel()

	updated, _ := m.Update(actionMsg{text: "refresh failed", err: errors.New("boom")})
	mm := updated.(Model)
	if mm.statusFg != cRed {
		t.Fatalf("statusFg = %v after an error action, want cRed", mm.statusFg)
	}

	// Even a matching-gen expiry msg (there shouldn't be one scheduled, but
	// prove it anyway) leaves an error status alone.
	updated2, _ := mm.Update(statusExpiredMsg{gen: mm.toastGen})
	mm2 := updated2.(Model)
	if mm2.status == "" {
		t.Fatal("error status cleared on an expiry msg, want it to survive")
	}

	nm, _ := mm2.handleKey(keyMsg("g"))
	nmm := nm.(Model)
	if nmm.status != "" {
		t.Errorf("status = %q after a keypress, want the error cleared", nmm.status)
	}
}
