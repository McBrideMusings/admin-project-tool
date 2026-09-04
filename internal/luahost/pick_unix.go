//go:build !windows

package luahost

import (
	"fmt"
	"strings"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/reload"
)

// pickerEscTimeout is how long the picker waits for the rest of an
// arrow-key escape sequence (or for nothing, meaning a bare Esc) after
// seeing \x1b — matches the 0.1s admin_lib/core.py's read_key() waits via
// select().
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
			// Move up by the line count assumed for the previous frame, then
			// erase everything below the cursor to the end of the screen. A
			// long Description can wrap onto 2+ physical terminal rows even
			// though linesDrawn only counts one row per item, so the plain
			// cursor-up alone can undercount and leave stale wrapped remnants
			// of the previous frame on screen below the new one (looks like
			// garbled/doubled text). \x1b[0J tolerates that undercount by
			// wiping everything past the repositioned cursor every frame.
			fmt.Fprintf(h.Stdout, "\x1b[%dA\x1b[0J", linesDrawn)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "\x1b[2K%s%s%s\n", ansiBold, prompt, ansiReset)
		for i, e := range items {
			marker := "    "
			style := ansiReset
			if i == cursor {
				marker = "  > "
				style = ansiCyan + ansiBold
			}
			fmt.Fprintf(&b, "\x1b[2K%s%s%-*s%s  %s%s%s\n", marker, style, keyWidth, e.Key, ansiReset, ansiDim, e.Description, ansiReset)
		}
		fmt.Fprintf(&b, "\x1b[2K%s[up/down/j/k] move  [enter] select  [esc/q] cancel%s\n", ansiDim, ansiReset)
		fmt.Fprint(h.Stdout, b.String())
		linesDrawn = len(items) + 2
	}

	for {
		redraw()
		switch readPickerKey(keys, pickerEscTimeout) {
		case pickerKeyUp:
			cursor = wrapCursor(cursor, -1, len(items))
		case pickerKeyDown:
			cursor = wrapCursor(cursor, 1, len(items))
		case pickerKeyEnter:
			fmt.Fprintln(h.Stdout)
			return items[cursor].Key, PickChosen
		case pickerKeyCancel:
			fmt.Fprintln(h.Stdout)
			return "", PickCancelled
		}
	}
}
