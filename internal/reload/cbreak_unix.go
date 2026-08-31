//go:build !windows

package reload

import (
	"golang.org/x/sys/unix"
)

// termState is a saved terminal mode, restored by restoreTerm.
type termState struct {
	termios unix.Termios
}

// makeCbreak puts fd into cbreak mode — canonical input and echo off, so a
// single keypress is readable immediately, with EVERYTHING ELSE left alone.
// This is Python's tty.setcbreak, which is what admin_lib/reload.py and
// admin_lib/keybindings.py both used.
//
// It is deliberately NOT term.MakeRaw. MakeRaw additionally clears OPOST,
// which turns off the line discipline's ONLCR translation for the whole
// terminal — every "\n" admin then writes moves down a row without
// returning to column 0, so a dev server's log staircases off the right
// edge of the screen after the first line. MakeRaw also clears ISIG, which
// is why the raw version had to read 0x03 by hand; with ISIG left on,
// Ctrl-C arrives as a real SIGINT on the signal channel like any other.
func makeCbreak(fd uintptr) (*termState, error) {
	old, err := unix.IoctlGetTermios(int(fd), ioctlReadTermios)
	if err != nil {
		return nil, err
	}
	saved := termState{termios: *old}

	newState := cbreakTermios(*old)
	if err := unix.IoctlSetTermios(int(fd), ioctlWriteTermios, &newState); err != nil {
		return nil, err
	}
	return &saved, nil
}

// restoreTerm puts fd back into the mode makeCbreak saved.
func restoreTerm(fd uintptr, state *termState) error {
	if state == nil {
		return nil
	}
	return unix.IoctlSetTermios(int(fd), ioctlWriteTermios, &state.termios)
}

// cbreakTermios is the pure half of makeCbreak: the exact bit changes that
// turn a saved terminal mode into cbreak. Split out from the ioctls so the
// mode itself is testable without a real terminal.
func cbreakTermios(old unix.Termios) unix.Termios {
	s := old
	s.Lflag &^= unix.ECHO | unix.ICANON
	s.Cc[unix.VMIN] = 1
	s.Cc[unix.VTIME] = 0
	return s
}
