package luahost

import (
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
	"time"
)

const testEscTimeout = 2 * time.Millisecond

// closedChan returns a channel pre-loaded with bs and then closed, so a
// receive past the last byte returns immediately (ok=false) rather than
// blocking — lets tests exercise "nothing more arrives" without waiting out
// readPickerKey's real escTimeout.
func closedChan(bs ...byte) chan byte {
	ch := make(chan byte, len(bs))
	for _, b := range bs {
		ch <- b
	}
	close(ch)
	return ch
}

func TestReadPickerKeyArrows(t *testing.T) {
	if got := readPickerKey(closedChan(0x1b, '[', 'A'), testEscTimeout); got != pickerKeyUp {
		t.Fatalf("up arrow: got %v, want pickerKeyUp", got)
	}
	if got := readPickerKey(closedChan(0x1b, '[', 'B'), testEscTimeout); got != pickerKeyDown {
		t.Fatalf("down arrow: got %v, want pickerKeyDown", got)
	}
}

func TestReadPickerKeyVimStyle(t *testing.T) {
	if got := readPickerKey(closedChan('k'), testEscTimeout); got != pickerKeyUp {
		t.Fatalf("k: got %v, want pickerKeyUp", got)
	}
	if got := readPickerKey(closedChan('K'), testEscTimeout); got != pickerKeyUp {
		t.Fatalf("K: got %v, want pickerKeyUp", got)
	}
	if got := readPickerKey(closedChan('j'), testEscTimeout); got != pickerKeyDown {
		t.Fatalf("j: got %v, want pickerKeyDown", got)
	}
	if got := readPickerKey(closedChan('J'), testEscTimeout); got != pickerKeyDown {
		t.Fatalf("J: got %v, want pickerKeyDown", got)
	}
}

func TestReadPickerKeyEnter(t *testing.T) {
	if got := readPickerKey(closedChan('\r'), testEscTimeout); got != pickerKeyEnter {
		t.Fatalf("\\r: got %v, want pickerKeyEnter", got)
	}
	if got := readPickerKey(closedChan('\n'), testEscTimeout); got != pickerKeyEnter {
		t.Fatalf("\\n: got %v, want pickerKeyEnter", got)
	}
}

func TestReadPickerKeyCancel(t *testing.T) {
	cases := []struct {
		name string
		ch   chan byte
	}{
		{"q", closedChan('q')},
		{"Q", closedChan('Q')},
		{"ctrl-c", closedChan(0x03)},
		{"closed/EOF", closedChan()},
		{"bare esc, channel closed after", closedChan(0x1b)},
	}
	for _, c := range cases {
		if got := readPickerKey(c.ch, testEscTimeout); got != pickerKeyCancel {
			t.Errorf("%s: got %v, want pickerKeyCancel", c.name, got)
		}
	}
}

// TestReadPickerKeyBareEscTimesOut covers the live-terminal shape: a lone
// Esc byte with nothing following it yet (the channel stays open — a real
// terminal doesn't close its stream), which must resolve to cancel after
// escTimeout rather than blocking forever.
func TestReadPickerKeyBareEscTimesOut(t *testing.T) {
	open := make(chan byte, 1) // buffered, stays open with no follow-up byte
	open <- 0x1b
	got := readPickerKey(open, testEscTimeout)
	if got != pickerKeyCancel {
		t.Fatalf("bare esc, open channel: got %v, want pickerKeyCancel", got)
	}
}

func TestReadPickerKeyOther(t *testing.T) {
	if got := readPickerKey(closedChan('x'), testEscTimeout); got != pickerKeyOther {
		t.Fatalf("x: got %v, want pickerKeyOther", got)
	}
	// Esc followed by a non-'[' byte, or '[' followed by a non-arrow byte.
	if got := readPickerKey(closedChan(0x1b, 'x'), testEscTimeout); got != pickerKeyOther {
		t.Fatalf("esc,x: got %v, want pickerKeyOther", got)
	}
	if got := readPickerKey(closedChan(0x1b, '[', 'C'), testEscTimeout); got != pickerKeyOther {
		t.Fatalf("right arrow (unhandled): got %v, want pickerKeyOther", got)
	}
}

func TestWrapCursor(t *testing.T) {
	cases := []struct {
		cur, delta, n, want int
	}{
		{0, 1, 3, 1},
		{1, 1, 3, 2},
		{2, 1, 3, 0},  // wraps past the end
		{0, -1, 3, 2}, // wraps past the start
		{1, -1, 3, 0},
		{0, 1, 1, 0}, // single item: stays put either direction
		{0, -1, 1, 0},
	}
	for _, c := range cases {
		if got := wrapCursor(c.cur, c.delta, c.n); got != c.want {
			t.Errorf("wrapCursor(%d, %d, %d) = %d, want %d", c.cur, c.delta, c.n, got, c.want)
		}
	}
}

func TestWrapCursorZeroItems(t *testing.T) {
	if got := wrapCursor(0, 1, 0); got != 0 {
		t.Fatalf("wrapCursor with n=0: got %d, want 0 (total, not a panic)", got)
	}
}

// TestPickTargetNonTTYNamesEveryTarget covers PickTarget's shared,
// platform-independent gate: no *os.File stdin (or a *os.File that isn't a
// terminal) must produce the exact "no target given" diagnostic and never
// reach either platform's interactive runArrowPicker, regardless of how
// many real (non-spacer) entries are offered.
func TestPickTargetNonTTYNamesEveryTarget(t *testing.T) {
	h, _, errOut := newTestHost(t)
	picked, outcome := h.PickTarget([]TargetEntry{
		{Key: "ios", Description: "iPhone"},
		{Key: "", Description: "spacer, filtered out before the TTY check"},
		{Key: "mac", Description: "Mac"},
	}, "Select")
	if outcome != PickUnavailable || picked != "" {
		t.Fatalf("expected PickUnavailable without a TTY, got (%q, %v)", picked, outcome)
	}
	got := errOut.String()
	for _, want := range []string{"no target given", "not a TTY", "ios", "mac"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr %q missing %q", got, want)
		}
	}
	if strings.Contains(got, ", , ") {
		t.Errorf("spacer leaked into the expected-values list: %q", got)
	}
}

// TestPickTargetEmptyIsUnavailable covers the "nothing to pick from" case —
// every entry a spacer — which must short-circuit before the TTY check
// even runs.
func TestPickTargetEmptyIsUnavailable(t *testing.T) {
	h, _, errOut := newTestHost(t)
	picked, outcome := h.PickTarget([]TargetEntry{{Key: "", Description: "spacer"}}, "Select")
	if outcome != PickUnavailable || picked != "" {
		t.Fatalf("expected PickUnavailable with zero real entries, got (%q, %v)", picked, outcome)
	}
	if errOut.String() != "" {
		t.Fatalf("expected no diagnostic for zero real entries, got %q", errOut.String())
	}
}

func TestPickerRowTruncatesDescriptionToWidth(t *testing.T) {
	e := TargetEntry{Key: "dev", Description: strings.Repeat("x", 200)}
	row := pickerRow("  > ", "", 3, e, 40)
	if w := ansi.StringWidth(row); w >= 40 || !strings.Contains(row, "…") {
		t.Errorf("width %d, row %q: want under 40 cells ending in an ellipsis", w, row)
	}
	flat := pickerRow("    ", "", 3, TargetEntry{Key: "dev", Description: "a\nb"}, 40)
	if strings.Contains(flat, "\n") {
		t.Errorf("row has a newline: %q", flat)
	}
	if bare := pickerRow("    ", "", 3, TargetEntry{Key: "dev"}, 40); strings.Contains(bare, "\x1b[2m") {
		t.Errorf("empty description still printed a dim column: %q", bare)
	}
}
