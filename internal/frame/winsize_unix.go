//go:build !windows

package frame

import "golang.org/x/sys/unix"

// applyWinsize is the unix half of ApplyWinsize: a real TIOCSWINSZ ioctl.
// Errors are swallowed — a pty that doesn't support this (or an fd that
// isn't a pty at all) just keeps its old size, matching
// admin_lib/frame.py's apply_winsize, which does the same under a bare
// `except OSError: pass`.
func applyWinsize(fd uintptr, rows, cols int) {
	_ = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(rows),
		Col: uint16(cols),
	})
}
