package frame

import xterm "github.com/charmbracelet/x/term"

// termGetSize and termIsTerminal wrap github.com/charmbracelet/x/term (a
// direct, already-vendored dependency — internal/tui already pulls in the
// bubbletea/lipgloss stack it belongs to) rather than hand-rolling a second
// ioctl-based size probe next to the one that package already provides.

func termGetSize(fd uintptr) (width, height int, err error) {
	return xterm.GetSize(fd)
}

func termIsTerminal(fd uintptr) bool {
	return xterm.IsTerminal(fd)
}
