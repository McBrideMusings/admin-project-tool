//go:build !windows

package reload

import (
	"os"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestSharedStdinReaderNestedRestoreDoesNotStealBytes is the pty-driven
// regression test for admin-byva: two sequential startKeyReader calls, the
// first started and fully restored before the second begins — exactly the
// command-picker-then-target-picker sequence in cmd/cmdpicker.go and
// internal/kinds/multitarget.go. Before the shared refcounted reader, the
// first call's goroutine leaked blocked in Read(os.Stdin) forever (restore
// only restored termios), so it kept competing with the second reader for
// every byte the tty delivered afterward — dropping some outright and
// splitting a multi-byte escape sequence like an arrow key across both
// listeners. With a single process-wide reader this cannot happen: the first
// restore (the last, and only, subscriber at that point) tears the shared
// reader all the way down, and the second startKeyReader call starts a
// fresh one from scratch.
//
// os.Stdin is swapped for a real pty slave for the duration of the test
// (restored via t.Cleanup) rather than re-exec'ing the test binary under a
// pty — pty.Open returns a real tty device in-process, and every function
// under test reads the current os.Stdin at call time, so no subprocess is
// needed to get a genuine terminal fd.
func TestSharedStdinReaderNestedRestoreDoesNotStealBytes(t *testing.T) {
	ptmx, pts, err := pty.Open()
	if err != nil {
		t.Skipf("pty.Open: %v (no pty available in this sandbox)", err)
	}
	t.Cleanup(func() { _ = ptmx.Close() })
	t.Cleanup(func() { _ = pts.Close() })

	origStdin := os.Stdin
	os.Stdin = pts
	t.Cleanup(func() { os.Stdin = origStdin })

	// Outer reader: started and immediately restored, mimicking the command
	// picker's runArrowPicker returning right after Enter.
	keys1, restore1 := startKeyReader()
	if keys1 == nil {
		t.Fatal("expected a non-nil channel from the first reader — pts is a real terminal")
	}
	restore1()

	// Nested reader: the target picker starting immediately after, on the
	// same (now-restored) stdin.
	keys2, restore2 := startKeyReader()
	if keys2 == nil {
		t.Fatal("expected a non-nil channel from the second reader")
	}
	t.Cleanup(restore2)

	// Five 'j' keys plus a full down-arrow escape sequence (ESC '[' 'B'),
	// matching the two shapes reproduced in the ticket.
	want := []byte{'j', 'j', 'j', 'j', 'j', 0x1b, '[', 'B'}
	if _, err := ptmx.Write(want); err != nil {
		t.Fatalf("write to pty: %v", err)
	}

	got := make([]byte, 0, len(want))
	deadline := time.After(2 * time.Second)
	for len(got) < len(want) {
		select {
		case b := <-keys2:
			got = append(got, b)
		case <-deadline:
			t.Fatalf("timed out waiting for bytes; got %v so far, want %v", got, want)
		}
	}

	if string(got) != string(want) {
		t.Fatalf("second reader saw %v, want %v — bytes were stolen or reordered by a leaked first reader", got, want)
	}

	// The fully-restored first reader must not still be receiving bytes.
	select {
	case b, ok := <-keys1:
		if ok {
			t.Fatalf("first reader received a byte (%q) after being restored — it should be fully unsubscribed", b)
		}
	default:
	}
}
