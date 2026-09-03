package timing

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}

func TestOffEmitsNothing(t *testing.T) {
	reset()
	defer reset()

	out := captureStderr(t, func() {
		start := Start()
		if !start.IsZero() {
			t.Fatalf("Start() returned a non-zero time while off: %v", start)
		}
		Phase("parse manifest", start)
		Mark("first byte")
		Total()
	})

	if out != "" {
		t.Fatalf("expected no stderr output while timing is off, got %q", out)
	}
}

func TestPhaseWritesToStderr(t *testing.T) {
	reset()
	defer reset()

	Enable()
	out := captureStderr(t, func() {
		start := time.Now().Add(-5 * time.Millisecond)
		Phase("parse manifest", start)
	})

	if !strings.Contains(out, "parse manifest") {
		t.Fatalf("expected output to name the phase, got %q", out)
	}
	if !strings.Contains(out, "ms") {
		t.Fatalf("expected output to carry a millisecond value, got %q", out)
	}
}

func TestMarkIsOneShot(t *testing.T) {
	reset()
	defer reset()

	Enable()
	out := captureStderr(t, func() {
		Mark("tui first frame")
		Mark("tui first frame")
	})

	if n := strings.Count(out, "tui first frame"); n != 1 {
		t.Fatalf("expected Mark to fire once, got %d occurrences in %q", n, out)
	}
}

func TestNothingWrittenToStdout(t *testing.T) {
	reset()
	defer reset()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	Enable()
	start := Start()
	Phase("parse manifest", start)
	Total()

	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected no stdout output, got %q", string(out))
	}
}

// TestBufferedRunFlushesOnEarlyReturn models runDashboard's shape: buffer,
// defer Flush, emit a phase, then return early on an error before any
// explicit Flush call. It asserts against the writer (stderr), not a live
// dashboard, that the deferred Flush still empties the buffer — including
// the "total" line a deferred Total() would print after it — rather than
// dropping every buffered phase line on the one path that most needs them.
func TestBufferedRunFlushesOnEarlyReturn(t *testing.T) {
	reset()
	defer reset()

	Enable()

	run := func() error {
		BufferUntilFlush()
		defer Flush()

		start := Start()
		Phase("git repo root", start)
		return errFakeEarlyReturn
	}

	out := captureStderr(t, func() {
		_ = run()
		Total()
	})

	if !strings.Contains(out, "git repo root") {
		t.Fatalf("expected buffered phase line to survive an early return, got %q", out)
	}
	if !strings.Contains(out, "total") {
		t.Fatalf("expected total line after the early return, got %q", out)
	}
	if strings.Index(out, "git repo root") > strings.Index(out, "total") {
		t.Fatalf("expected phase line before total, got %q", out)
	}
}

var errFakeEarlyReturn = fmt.Errorf("fake early return")

// TestMarkEachEmitsPerChild stands in for two children dispatched in one
// long-lived TUI session: each should get its own "first byte from child"
// line rather than the second being swallowed by Mark's process-wide dedup.
func TestMarkEachEmitsPerChild(t *testing.T) {
	reset()
	defer reset()

	Enable()
	out := captureStderr(t, func() {
		MarkEach("first byte from child")
		MarkEach("first byte from child")
	})

	if n := strings.Count(out, "first byte from child"); n != 2 {
		t.Fatalf("expected MarkEach to fire for each child, got %d occurrences in %q", n, out)
	}
}

func TestBufferFlush(t *testing.T) {
	reset()
	defer reset()

	Enable()
	BufferUntilFlush()

	out := captureStderr(t, func() {
		Phase("a", time.Now().Add(-1*time.Millisecond))
		Phase("b", time.Now().Add(-1*time.Millisecond))
	})
	if out != "" {
		t.Fatalf("expected buffered output to withhold writes until Flush, got %q", out)
	}

	out = captureStderr(t, func() {
		Flush()
	})
	idxA := strings.Index(out, "a  ")
	idxB := strings.Index(out, "b  ")
	if idxA == -1 || idxB == -1 {
		t.Fatalf("expected both buffered lines after Flush, got %q", out)
	}
	if idxA > idxB {
		t.Fatalf("expected buffered lines to flush in order, got %q", out)
	}
}
