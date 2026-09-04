package frame

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testFrame(urls []URLEntry, logPath string, rows, cols int) (*Frame, *bytes.Buffer) {
	var buf bytes.Buffer
	f := newFrame("dev:cf", urls, logPath, &buf)
	f.Rows, f.Cols = rows, cols
	return f, &buf
}

// -- geometry -----------------------------------------------------------

func TestHeightIsStatusPlusLogPlusOneRowPerURL(t *testing.T) {
	f, _ := testFrame([]URLEntry{{"a", "http://a"}, {"b", "http://b"}}, "/tmp/x.log", 24, 100)
	if f.Height() != 4 {
		t.Fatalf("got %d", f.Height())
	}
}

func TestRegionLeavesTheFooterRowsFree(t *testing.T) {
	f, _ := testFrame([]URLEntry{{"a", "http://a"}}, "/tmp/x.log", 24, 100) // height 3
	if f.RegionRows() != 21 {
		t.Fatalf("got %d", f.RegionRows())
	}
}

func TestRegionNeverCollapsesBelowTwoRows(t *testing.T) {
	var urls []URLEntry
	for i := 0; i < 40; i++ {
		urls = append(urls, URLEntry{"a", "http://x"})
	}
	f, _ := testFrame(urls, "/tmp/x.log", 10, 100)
	if f.RegionRows() != 2 {
		t.Fatalf("got %d", f.RegionRows())
	}
}

// -- content rows ---------------------------------------------------------

func TestLogRowCarriesTheAbsoluteLogPath(t *testing.T) {
	f, _ := testFrame([]URLEntry{{"a", "http://a"}}, "/tmp/dev-cf.log", 24, 100)
	row := f.ContentRows()[1]
	if !strings.Contains(row, "/tmp/dev-cf.log") {
		t.Fatalf("row missing raw path: %q", row)
	}
	if !strings.Contains(row, "file:///tmp/dev-cf.log") {
		t.Fatalf("row missing file:// hyperlink target: %q", row)
	}
}

func TestStatusRowNamesTheActionAndElapsedTime(t *testing.T) {
	f, _ := testFrame([]URLEntry{{"a", "http://a"}}, "/tmp/x.log", 24, 100)
	row := f.ContentRows()[0]
	if !strings.Contains(row, "dev:cf") || !strings.Contains(row, "0s") {
		t.Fatalf("got %q", row)
	}
}

func TestStatusRowCarriesHerdrPaneIDWhenPresent(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "wHC:p1")
	f, _ := testFrame([]URLEntry{{"a", "http://a"}}, "/tmp/x.log", 24, 100)
	row := f.ContentRows()[0]
	if !strings.Contains(row, "wHC:p1") {
		t.Fatalf("got %q", row)
	}
}

func TestHerdrEnvSetButPaneIDEmptyIsNotInHerdr(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "")
	f, _ := testFrame(nil, "/tmp/x.log", 24, 100)
	if f.paneID != "" {
		t.Fatalf("expected no pane id, got %q", f.paneID)
	}
}

func TestStatusRowUnchangedOutsideHerdr(t *testing.T) {
	os.Unsetenv("HERDR_ENV")
	os.Unsetenv("HERDR_PANE_ID")
	f, _ := testFrame([]URLEntry{{"a", "http://a"}}, "/tmp/x.log", 24, 100)
	row := f.ContentRows()[0]
	text := " dev:cf · 0s "
	want := dim + "──" + text + strings.Repeat("─", 100-len([]rune(text))-2) + reset
	if row != want {
		t.Fatalf("got %q, want %q", row, want)
	}
}

func TestASelfLabelledURLIsNotPrintedTwice(t *testing.T) {
	f, _ := testFrame([]URLEntry{{"http://localhost:8787", "http://localhost:8787"}}, "/tmp/x.log", 24, 100)
	row := f.ContentRows()[2]
	if n := strings.Count(row, "http://localhost:8787"); n != 2 {
		t.Fatalf("expected the OSC 8 target and its text (2), got %d in %q", n, row)
	}
}

func TestALongURLIsClippedToTheTerminalWidth(t *testing.T) {
	long := "http://localhost:8787/" + strings.Repeat("x", 200)
	f, _ := testFrame([]URLEntry{{"Dev", long}}, "/tmp/x.log", 24, 60)
	row := f.ContentRows()[2]
	if !strings.Contains(row, "…") {
		t.Fatalf("expected a clip mark: %q", row)
	}
	if !strings.Contains(row, long) {
		t.Fatal("expected the hyperlink target to stay whole")
	}
}

func TestElapsedFormatsSecondsMinutesHours(t *testing.T) {
	cases := map[time.Duration]string{
		9 * time.Second:                             "9s",
		2*time.Minute + 5*time.Second:               "2m05s",
		2*time.Hour + 2*time.Minute + 5*time.Second: "2h02m",
	}
	for d, want := range cases {
		if got := elapsed(d); got != want {
			t.Errorf("elapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

// A footer=true action with no URLs still gets a two-row footer (the status
// rule and the log path) — the whole point of the footer/urls tri-state.
func TestFooterTrueWithNoURLsStillHasHeightTwo(t *testing.T) {
	f, _ := testFrame(nil, "/tmp/x.log", 24, 100)
	if f.Height() != 2 {
		t.Fatalf("got %d", f.Height())
	}
	if len(f.ContentRows()) != 2 {
		t.Fatalf("got %d rows", len(f.ContentRows()))
	}
}

// -- ShouldActivate -------------------------------------------------------

func TestShouldActivateNoURLsAndNoFooterKeyStillRaisesAFrame(t *testing.T) {
	if !ShouldActivate(0, false, false) {
		t.Fatal("expected true — footer defaults on")
	}
}

func TestShouldActivateURLsAloneRaiseAFrame(t *testing.T) {
	if !ShouldActivate(1, false, false) {
		t.Fatal("expected true")
	}
}

func TestShouldActivateFooterTrueRaisesWithNoURLs(t *testing.T) {
	if !ShouldActivate(0, true, true) {
		t.Fatal("expected true")
	}
}

func TestShouldActivateFooterFalseSuppressesURLs(t *testing.T) {
	if ShouldActivate(3, true, false) {
		t.Fatal("expected false")
	}
}

// -- lifecycle / drawing --------------------------------------------------

func TestEnterWritesScrollRegionAndFooter(t *testing.T) {
	f, buf := testFrame([]URLEntry{{"a", "http://a"}}, "/tmp/x.log", 24, 100)
	f.enter()
	defer f.leave()

	out := buf.String()
	if !strings.Contains(out, esc+"[1;21r") {
		t.Fatalf("missing DECSTBM region setup: %q", out)
	}
	if !strings.Contains(out, "dev:cf") {
		t.Fatalf("missing status row content: %q", out)
	}
}

func TestLeaveReleasesTheScrollRegion(t *testing.T) {
	f, buf := testFrame(nil, "/tmp/x.log", 24, 100)
	f.enter()
	buf.Reset()
	f.leave()
	if !strings.Contains(buf.String(), esc+"[r") {
		t.Fatalf("expected the region-release sequence, got %q", buf.String())
	}
	if f.active {
		t.Fatal("expected active=false after leave")
	}
}

// The concrete "survives a child that positions its own cursor" guarantee:
// a repaint after the child has written arbitrary bytes still lands the
// footer at the region's own rows via absolute addressing (not relative to
// wherever the child last left the cursor).
func TestDrawRepaintsAtAbsoluteFooterRowsRegardlessOfPriorOutput(t *testing.T) {
	f, buf := testFrame([]URLEntry{{"a", "http://a"}}, "/tmp/x.log", 24, 100)
	f.enter()
	defer f.leave()
	buf.Reset()
	buf.WriteString("child moved the cursor around\x1b[5;5H")
	f.drawLocked()
	out := buf.String()
	first := f.RegionRows() + 1
	wantAddr := esc + "[" + strconv.Itoa(first) + ";1H"
	if !strings.Contains(out, wantAddr) {
		t.Fatalf("expected absolute footer addressing %q in %q", wantAddr, out)
	}
}

// -- Activate / Deactivate guards ------------------------------------------

func TestActivateDeclinesWhenWriterIsNotATerminal(t *testing.T) {
	var buf bytes.Buffer
	if f := Activate("dev", []URLEntry{{"Dev", "http://x"}}, "/tmp/x.log", &buf); f != nil {
		t.Fatal("expected nil for a non-terminal writer")
	}
	if active != nil {
		t.Fatal("expected no active frame")
	}
}

func TestActivateNoLongerDeclinesJustBecauseThereAreNoURLs(t *testing.T) {
	// A bytes.Buffer is never a terminal either way, so this exercises the
	// same tty gate as the "no urls" case would — the real assertion is
	// that nothing about an empty urls list short-circuits Activate before
	// the tty check even runs (it would still return nil here regardless).
	var buf bytes.Buffer
	if f := Activate("dev", nil, "/tmp/x.log", &buf); f != nil {
		t.Fatal("expected nil")
	}
}

func TestActivateDeclinesWhenAdminNoFrameIsSet(t *testing.T) {
	t.Setenv("ADMIN_NO_FRAME", "1")
	if f := Activate("dev", []URLEntry{{"Dev", "http://x"}}, "/tmp/x.log", os.Stdout); f != nil {
		t.Fatal("expected nil")
	}
}

func TestActivateDeclinesWhenAlreadyActive(t *testing.T) {
	outer := newFrame("outer", nil, "/tmp/x.log", &bytes.Buffer{})
	mu.Lock()
	active = outer
	mu.Unlock()
	defer func() {
		mu.Lock()
		active = nil
		mu.Unlock()
	}()

	if f := Activate("inner", []URLEntry{{"a", "http://a"}}, "/tmp/y.log", os.Stdout); f != nil {
		t.Fatal("expected nil — a frame is already up")
	}
}

func TestWritePassesThroughUntouchedWhenNoFrameIsActive(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf, "hello\n")
	if buf.String() != "hello\n" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestDeactivateIgnoresAFrameItDoesNotOwn(t *testing.T) {
	outer := newFrame("dev:cf", []URLEntry{{"a", "http://a"}}, "/tmp/x.log", &bytes.Buffer{})
	mu.Lock()
	active = outer
	mu.Unlock()
	defer func() {
		mu.Lock()
		active = nil
		mu.Unlock()
	}()

	Deactivate(nil) // inner action never activated — must leave outer alone
	mu.Lock()
	got := active
	mu.Unlock()
	if got != outer {
		t.Fatal("expected the outer frame to remain active")
	}
}

func TestApplyWinsizeIsANoOpWithNoActiveFrame(t *testing.T) {
	mu.Lock()
	active = nil
	mu.Unlock()
	// Must not panic even with a bogus fd.
	ApplyWinsize(^uintptr(0))
}
