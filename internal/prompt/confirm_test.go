package prompt

import (
	"os"
	"testing"
)

// TestConfirmNonTTYDoesNotAutoAccept pins the acceptance criterion that a
// non-terminal stdin never auto-accepts. stdin here is a pipe (never a
// terminal under `go test`), holding "" — the same shape an EOF from
// `</dev/null` produces — and must decline, not accept.
func TestConfirmNonTTYDoesNotAutoAccept(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })

	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	if got := Confirm("continue? "); got {
		t.Error("Confirm = true, want false — an EOF/empty non-TTY read must decline, not accept")
	}
}

// TestConfirmNonTTYExplicitYesAccepts proves the fallback still accepts an
// explicit "y"/"yes" line — only the previously-implicit empty-line accept
// changed.
func TestConfirmNonTTYExplicitYesAccepts(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		w.WriteString("yes\n")
		w.Close()
	}()
	t.Cleanup(func() { r.Close() })

	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	if got := Confirm("continue? "); !got {
		t.Error("Confirm = false, want true for an explicit \"yes\" line")
	}
}

// TestConfirmNonTTYDeclineLineDeclines proves an explicit "n" line declines,
// same as before.
func TestConfirmNonTTYDeclineLineDeclines(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		w.WriteString("n\n")
		w.Close()
	}()
	t.Cleanup(func() { r.Close() })

	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	if got := Confirm("continue? "); got {
		t.Error("Confirm = true, want false for an explicit \"n\" line")
	}
}
