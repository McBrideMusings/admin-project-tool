package luahost

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// TestVerifyRedrawSurvivesWrappedDescriptions drives runArrowPicker over a
// REAL pty (github.com/creack/pty), exactly the mechanism a person types
// into: os.Stdin is swapped for the pty slave so internal/reload's
// StartKeyReader passes its isatty/cbreak gate for real, keystrokes are
// injected by writing to the pty master (what a real keyboard would do),
// the picker's Stdout is the same pty slave (so the kernel's ONLCR output
// processing turns its "\n" into "\r\n" exactly like a real terminal
// driver), and the resulting bytes are read off the master into a real
// VT100 emulator (github.com/charmbracelet/x/vt, already vendored for the
// hosted-pty terminal view) sized to a narrow column count so a long
// Description autowraps exactly like a real terminal would. This is the
// one thing pick_test.go says no test in this repo can reach (see its
// TestPickTargetNonTTYNamesEveryTarget comment and cmd/cmdpicker.go's
// handlePickOutcome doc).
//
// It first reproduces the pre-fix bug shape (plain cursor-up, no \x1b[0J)
// through the same real-terminal pipeline to confirm the harness itself
// can detect the symptom, then drives the real (post-fix) runArrowPicker
// over the same wrapped content and asserts the symptom is gone.
//
// pick_unix.go's runArrowPicker already computes linesDrawn as the summed
// wrap-aware physical-row count of every row it writes (pickerPhysicalRows),
// not a flat `len(items)+2`, so the cursor-up distance already reaches the
// previous frame's true top before \x1b[0J erases the rest.
//
// The prompt below is "choose", not "select": runArrowPicker's own static
// footer text ("[enter] select") already contains the word "select", so
// using that as the prompt would make hasStalePrompt count two occurrences
// on every correctly-drawn single frame (title + footer) regardless of
// whether a stale remnant is actually on screen.
func TestVerifyRedrawSurvivesWrappedDescriptions(t *testing.T) {
	const cols, rows = 40, 20

	items := []TargetEntry{
		{Key: "a", Description: "a description just one word too long to fit"},
		{Key: "bb", Description: "short"},
	}

	// --- Old (buggy) redraw, through a real pty + real VT100 emulator ---
	oldTerm := renderThroughRealPTY(t, cols, rows, func(w io.Writer) {
		linesDrawn := 0
		redraw := func(cursor int) {
			if linesDrawn > 0 {
				fmt.Fprintf(w, "\x1b[%dA", linesDrawn) // the pre-fix line: no \x1b[0J
			}
			fmt.Fprint(w, renderFrame(items, cursor))
			linesDrawn = len(items) + 2
		}
		redraw(0)
		time.Sleep(20 * time.Millisecond)
		redraw(1) // cursor moves onto the short item -> old cursor-up undercounts the wrapped frame it's leaving
	})
	if !hasStalePrompt(oldTerm, rows, "choose") {
		t.Fatalf("expected the OLD redraw (no \\x1b[0J) to leave a stale remnant on a %dx%d screen with wrapped content; screen:\n%s", cols, rows, screenText(oldTerm, rows))
	}

	// --- Real fix, driven through the actual production code path -----
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		t.Fatalf("pty.Setsize: %v", err)
	}

	origStdin := os.Stdin
	os.Stdin = slave
	defer func() { os.Stdin = origStdin }()

	h := &Host{Stdout: slave, Stderr: slave}

	type result struct {
		key     string
		outcome PickOutcome
	}
	done := make(chan result, 1)
	go func() {
		k, o := h.runArrowPicker(items, "choose")
		done <- result{k, o}
	}()

	// newTerm is fed by a background reader that never gets to observe EOF:
	// internal/reload's StartKeyReader spawns its own goroutine that holds a
	// permanent blocking read on the same slave fd for the rest of the
	// process's life (by design — restore() only resets termios, it never
	// stops that reader), which keeps the underlying pty's read side "in
	// use" forever from the kernel's point of view. So this test cannot
	// synchronize on Close-triggered EOF (unlike renderThroughRealPTY below,
	// which has no such second reader) or on a read deadline (unsupported on
	// this platform's pty master — confirmed separately: SetReadDeadline
	// returns nil but never actually bounds the read). It synchronizes on
	// fixed, generous sleeps instead, matching the real keystroke cadence a
	// person's fingers produce; the reader goroutine below is intentionally
	// left running past the end of the test (it can never observe EOF here
	// either) rather than joined.
	newTerm := vt.NewEmulator(cols, rows)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				newTerm.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// Give the picker time to draw its first frame, then send Down then
	// Enter over the real pty master — exactly what a person's keyboard
	// produces — pausing after each so the reader goroutine above has
	// processed one frame before the next keystroke goes in.
	time.Sleep(150 * time.Millisecond)
	if _, err := master.Write([]byte{0x1b, '[', 'B'}); err != nil { // Down arrow
		t.Fatalf("write down arrow: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := master.Write([]byte{'\r'}); err != nil { // Enter
		t.Fatalf("write enter: %v", err)
	}

	select {
	case res := <-done:
		if res.outcome != PickChosen || res.key != "bb" {
			t.Fatalf("runArrowPicker returned (%q, %v), want (\"bb\", PickChosen)", res.key, res.outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runArrowPicker did not return within 2s — picker likely hung reading the real pty")
	}
	time.Sleep(150 * time.Millisecond) // let the reader goroutine drain the picker's trailing Fprintln("\n")

	if hasStalePrompt(newTerm, rows, "choose") {
		t.Fatalf("fixed runArrowPicker left a stale remnant on a %dx%d screen with wrapped content; screen:\n%s", cols, rows, screenText(newTerm, rows))
	}
}

// renderThroughRealPTY opens a real pty sized cols x rows, calls write with
// a writer that goes to the slave (so kernel ONLCR output processing turns
// "\n" into "\r\n" exactly like a real terminal driver would for any real
// picker output), reads everything back off the master, feeds it into a
// real VT100 emulator, and returns the emulator holding the final screen.
func renderThroughRealPTY(t *testing.T, cols, rows int, write func(w io.Writer)) *vt.Emulator {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer master.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		t.Fatalf("pty.Setsize: %v", err)
	}

	term := vt.NewEmulator(cols, rows)
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				term.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	write(slave)
	// Closing the slave races the background reader: on macOS, closing the
	// last open reference to a pty's slave side can flush the driver's
	// output queue rather than draining it, discarding whatever the reader
	// goroutine above hasn't pulled off the master yet. write() returning
	// only means the bytes reached the kernel's tty queue, not that the
	// reader has drained them — so give it a moment before the close that
	// can blow that queue away.
	time.Sleep(50 * time.Millisecond)
	slave.Close()
	<-copyDone
	return term
}

// renderFrame reproduces exactly the per-frame body runArrowPicker writes
// (prompt + rows + footer), without the cursor-repositioning prefix, so
// the old redraw strategy under test can prefix it its own (buggy) way.
func renderFrame(items []TargetEntry, cursor int) string {
	var b strings.Builder
	kw := maxKeyWidth(items)
	fmt.Fprintf(&b, "\x1b[2Kchoose\n")
	for i, e := range items {
		marker := "    "
		if i == cursor {
			marker = "  > "
		}
		fmt.Fprintf(&b, "\x1b[2K%s%-*s  %s\n", marker, kw, e.Key, e.Description)
	}
	fmt.Fprintf(&b, "\x1b[2K[up/down/j/k] move  [enter] select  [esc/q] cancel\n")
	return b.String()
}

// screenText renders every row of term as plain text (trailing spaces
// trimmed) for failure messages only — human-readable, not what the
// assertion checks (autowrap can split a description across the newlines
// this inserts, so it is not safe to substring-match against).
func screenText(term *vt.Emulator, rows int) string {
	var b strings.Builder
	for y := 0; y < rows; y++ {
		b.WriteString(strings.TrimRight(rawRow(term, y), " ") + "\n")
	}
	return b.String()
}

// rawRow returns row y's cells concatenated with no separator, at full
// width — reconstructing exactly what autowrap continues writing into the
// next physical row with no inserted boundary, so concatenating every raw
// row (no newlines) reproduces the original written text verbatim.
func rawRow(term *vt.Emulator, y int) string {
	var b strings.Builder
	for x := 0; x < term.Width(); x++ {
		c := term.CellAt(x, y)
		if c == nil || c.String() == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.String())
		}
	}
	return b.String()
}

// hasStalePrompt reports whether prompt (a short string, guaranteed not to
// autowrap and written verbatim as the very first line of every redraw)
// appears more than once on screen — the direct, layout-independent
// signature of the bug: a correct redraw always repositions the cursor to
// the true top of the previous frame before writing a fresh one, so the
// prompt line is rewritten in place and appears exactly once. If the
// cursor-up undercounts a wrapped previous frame's real physical height,
// the new frame starts partway down the old one, leaving the old frame's
// own prompt line (and everything above where the new frame starts)
// sitting untouched above it.
func hasStalePrompt(term *vt.Emulator, rows int, prompt string) bool {
	var flat strings.Builder
	for y := 0; y < rows; y++ {
		flat.WriteString(rawRow(term, y))
	}
	return strings.Count(flat.String(), prompt) > 1
}
