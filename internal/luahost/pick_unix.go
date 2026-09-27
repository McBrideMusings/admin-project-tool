//go:build !windows

package luahost

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	xterm "github.com/charmbracelet/x/term"

	"github.com/McBrideMusings/admin-project-tool/internal/reload"
)

// pickerEscTimeout is how long the picker waits for the rest of an
// arrow-key escape sequence (or for nothing, meaning a bare Esc) after
// seeing \x1b.
const pickerEscTimeout = 100 * time.Millisecond

// runArrowPicker is PickTarget's interactive body once the TTY gate has
// passed: an in-place arrow-key picker over items, redrawn after every
// keystroke rather than scrolling a fresh copy of the list down the
// terminal. It puts stdin into cbreak mode via internal/reload's
// StartKeyReader — the same raw-mode helper internal/keybindings' own key
// loop uses — rather than opening a second termios raw-mode implementation
// here; StartKeyReader always reads the process's real os.Stdin, which is
// what h.Stdin is set to on every production path (PickTarget's own TTY
// gate above already only succeeds when h.Stdin is a real *os.File
// terminal, and the only such file this process ever wires up is
// os.Stdin).
func (h *Host) runArrowPicker(items []TargetEntry, prompt string) (string, PickOutcome) {
	keys, restore := reload.StartKeyReader()
	defer restore()
	if keys == nil {
		// StartKeyReader declined (stdin isn't actually a terminal by its own
		// check, or the cbreak ioctl failed) even though PickTarget's isTTY
		// gate passed — degrade exactly like the non-TTY path rather than
		// hanging on a read that will never see a byte.
		h.Err(fmt.Sprintf("%s: could not enter raw terminal mode — pass the target as an argument", prompt))
		return "", PickUnavailable
	}

	cursor := 0
	linesDrawn := 0

	keyWidth := maxKeyWidth(items)

	redraw := func() {
		if linesDrawn > 0 {
			// Move up by the physical-row count computed for the previous
			// frame, then erase everything below the cursor to the end of
			// the screen. linesDrawn is now the true wrapped height (see
			// below), so the cursor-up reaches the previous frame's real top
			// before \x1b[0J wipes the rest — \x1b[0J stays only as a
			// defensive clear of anything left below that point.
			fmt.Fprintf(h.Stdout, "\x1b[%dA\x1b[0J", linesDrawn)
		}

		rows := make([]string, 0, len(items)+2)
		rows = append(rows, fmt.Sprintf("%s%s%s", ansiBold, prompt, ansiReset))
		for i, e := range items {
			marker := "    "
			style := ansiReset
			if i == cursor {
				marker = "  > "
				style = ansiCyan + ansiBold
			}
			rows = append(rows, fmt.Sprintf("%s%s%-*s%s  %s%s%s", marker, style, keyWidth, e.Key, ansiReset, ansiDim, e.Description, ansiReset))
		}
		rows = append(rows, fmt.Sprintf("%s[up/down/j/k] move  [enter] select  [esc/q] cancel%s", ansiDim, ansiReset))

		width := pickerTermWidth(h.Stdout)
		var b strings.Builder
		physicalRows := 0
		for _, row := range rows {
			fmt.Fprintf(&b, "\x1b[2K%s\n", row)
			physicalRows += pickerPhysicalRows(row, width)
		}
		fmt.Fprint(h.Stdout, b.String())
		linesDrawn = physicalRows
	}

	for {
		redraw()
		switch readPickerKey(keys, pickerEscTimeout) {
		case pickerKeyUp:
			cursor = wrapCursor(cursor, -1, len(items))
		case pickerKeyDown:
			cursor = wrapCursor(cursor, 1, len(items))
		case pickerKeyEnter:
			// Erase the picker's own frame so the picked command's output
			// starts where the prompt was; nothing above it is touched.
			fmt.Fprintf(h.Stdout, "\x1b[%dA\x1b[0J", linesDrawn)
			return items[cursor].Key, PickChosen
		case pickerKeyCancel:
			fmt.Fprintln(h.Stdout)
			return "", PickCancelled
		}
	}
}

// pickerTermWidth returns the real terminal width behind w, falling back to
// 80 columns (matching internal/frame.termSize's fallback) when w isn't a
// real *os.File or the ioctl fails — e.g. under a non-terminal Stdout, or a
// platform where the syscall isn't wired up.
func pickerTermWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if width, _, err := xterm.GetSize(f.Fd()); err == nil && width > 0 {
			return width
		}
	}
	return 80
}

// pickerPhysicalRows returns how many physical terminal rows row wraps to at
// the given terminal width, measuring only the cells it actually occupies
// (ansi.StringWidth strips the row's ANSI/SGR styling first). Always at
// least 1, so a blank row or an undetectable width never collapses the
// count back toward the pre-fix undercount.
func pickerPhysicalRows(row string, width int) int {
	if width <= 0 {
		width = 80
	}
	w := ansi.StringWidth(row)
	if w <= 0 {
		return 1
	}
	rows := (w + width - 1) / width
	if rows < 1 {
		rows = 1
	}
	return rows
}
