//go:build !windows

package reload

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestCbreakKeepsOutputPostProcessing is the regression test for the
// staircased dev-server log. startKeyReader used term.MakeRaw, which clears
// OPOST — with output post-processing off the line discipline stops
// translating "\n" into "\r\n", so every line admin wrote moved down a row
// without returning to column 0 and the log walked off the right edge of
// the screen. Cbreak must leave OPOST/ONLCR exactly as it found them.
func TestCbreakKeepsOutputPostProcessing(t *testing.T) {
	before := unix.Termios{
		Iflag: unix.ICRNL | unix.IXON,
		Oflag: unix.OPOST | unix.ONLCR,
		Lflag: unix.ECHO | unix.ECHOE | unix.ICANON | unix.ISIG | unix.IEXTEN,
	}
	after := cbreakTermios(before)

	if after.Oflag != before.Oflag {
		t.Errorf("cbreak changed oflag %#x -> %#x; OPOST/ONLCR must survive", before.Oflag, after.Oflag)
	}
	if after.Iflag != before.Iflag {
		t.Errorf("cbreak changed iflag %#x -> %#x", before.Iflag, after.Iflag)
	}
	if after.Lflag&unix.ISIG == 0 {
		t.Error("cbreak cleared ISIG — Ctrl-C would stop generating SIGINT")
	}
	if after.Lflag&unix.ICANON != 0 {
		t.Error("cbreak left ICANON on — a keypress would not be readable until Enter")
	}
	if after.Lflag&unix.ECHO != 0 {
		t.Error("cbreak left ECHO on — keypresses would be echoed to the screen")
	}
	if after.Cc[unix.VMIN] != 1 || after.Cc[unix.VTIME] != 0 {
		t.Errorf("cbreak VMIN/VTIME = %d/%d, want 1/0 — a single byte must return immediately",
			after.Cc[unix.VMIN], after.Cc[unix.VTIME])
	}
}

// TestCbreakDoesNotMutateInput guards the saved state makeCbreak restores:
// the copy it hands to the ioctl must not be the same memory it kept.
func TestCbreakDoesNotMutateInput(t *testing.T) {
	before := unix.Termios{Oflag: unix.OPOST, Lflag: unix.ECHO | unix.ICANON}
	saved := before
	cbreakTermios(before)
	if before != saved {
		t.Error("cbreakTermios mutated its argument")
	}
}

// TestRestoreTermNilIsNoop covers the "cbreak was never entered" path every
// caller relies on — restore is always safe to call.
func TestRestoreTermNilIsNoop(t *testing.T) {
	if err := restoreTerm(0, nil); err != nil {
		t.Fatalf("restoreTerm(nil) = %v, want nil", err)
	}
}
