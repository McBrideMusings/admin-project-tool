package luahost

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// TargetEntry is one row of a pick_target menu: a stable key and its
// display description. Order is carried separately (see PickTarget) since Go
// maps don't preserve insertion order.
type TargetEntry struct {
	Key         string
	Description string
}

// isTTY reports whether f looks like an interactive terminal. A minimal,
// dependency-free stand-in for isatty: good enough to gate the picker
// without pulling in golang.org/x/term for one check.
func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// PickOutcome distinguishes the three ways PickTarget can end. Cancelling
// and failing to prompt are both "no key" but they are not the same event:
// a cancel is the user answering the question, so a caller must not report
// it as the "no target given — pass one as an argument" failure that a
// non-TTY stdin earns.
type PickOutcome int

const (
	// PickChosen — the returned key is the user's selection.
	PickChosen PickOutcome = iota
	// PickCancelled — the user dismissed the picker (Esc/q/Ctrl-C, or EOF).
	// Nothing is wrong; the caller should exit quietly.
	PickCancelled
	// PickUnavailable — the picker could not run (no entries, stdin isn't a
	// terminal, raw mode refused). PickTarget has already printed why.
	PickUnavailable
)

// PickTarget prompts for one of order (in order — "" entries render as a
// blank spacer row and are filtered out of items below so the picker's
// cursor only ever lands on a real target) and returns the chosen key with
// PickChosen, or "" with PickCancelled / PickUnavailable.
//
// This runs entirely in-process against h.Stdin/h.Stdout/h.Stderr — the
// acceptance criterion ADR-0010 exists to satisfy ("pick_target prompts
// from the same process, with no subprocess and no wire protocol"). On a
// real terminal (unix) it is an in-place arrow-key picker: Up/Down (and
// k/j) move a highlighted cursor, Enter selects, Esc/q/Ctrl-C cancel, and
// each keystroke redraws the same block of lines in place, driven by a
// read_key()-style keystroke loop applied to pick_target's flat list. On
// Windows, where this repo has no raw-mode terminal support
// (see internal/reload's Windows stub), it falls back to the original
// numbered-list prompt read from a line of stdin. Both platforms degrade
// identically off a TTY: no target given → an explicit error naming the
// expected values, never a hang.
// Host function: pick_target(targets_table[, prompt]) — targets_table is an
// ordered array of {key=..., description=...} rows.
func (h *Host) PickTarget(entries []TargetEntry, prompt string) (string, PickOutcome) {
	items := make([]TargetEntry, 0, len(entries))
	for _, e := range entries {
		if e.Key != "" {
			items = append(items, e)
		}
	}
	if len(items) == 0 {
		return "", PickUnavailable
	}

	stdinFile, isFile := h.Stdin.(*os.File)
	if !isFile || !isTTY(stdinFile) {
		keys := make([]string, len(items))
		for i, e := range items {
			keys[i] = e.Key
		}
		expected := ""
		if len(keys) > 0 {
			expected = " (expected one of: " + strings.Join(keys, ", ") + ")"
		}
		h.Err(fmt.Sprintf("%s: no target given%s — stdin is not a TTY, pass the target as an argument", prompt, expected))
		return "", PickUnavailable
	}

	return h.runArrowPicker(items, prompt)
}

// pickerKey is one decoded keystroke from the arrow-key picker's raw byte
// stream — the parsed shape readPickerKey and the picker's render loop
// operate on, kept separate from raw bytes so the decoding logic is
// testable without a real terminal.
type pickerKey int

const (
	pickerKeyNone pickerKey = iota
	pickerKeyUp
	pickerKeyDown
	pickerKeyEnter
	pickerKeyCancel
	pickerKeyOther
)

// readPickerKey pulls one decoded keystroke off ch, which streams raw bytes
// from a terminal in cbreak mode (see internal/reload.StartKeyReader — the
// same byte-at-a-time reader internal/keybindings already reuses for its
// own key loop rather than each caller opening the terminal in raw mode a
// second time). Ctrl-C and a
// closed channel (EOF) both cancel; a bare Esc byte followed by nothing
// else within escTimeout is a cancel; "\x1b[A"/"\x1b[B" are up/down; 'k'/'j'
// are up/down; Enter (\r or \n) selects; 'q'/'Q' cancels; anything else is
// pickerKeyOther and is ignored by the render loop.
//
// escTimeout is the wait for the rest of an arrow-key escape sequence —
// tests pass a short duration since a closed or already-populated channel
// never actually blocks for it.
func readPickerKey(ch <-chan byte, escTimeout time.Duration) pickerKey {
	b, ok := <-ch
	if !ok {
		return pickerKeyCancel
	}
	switch b {
	case 0x03: // Ctrl-C
		return pickerKeyCancel
	case 0x1b: // Esc, or the start of an arrow-key escape sequence
		var b2 byte
		select {
		case v, ok := <-ch:
			if !ok {
				return pickerKeyCancel
			}
			b2 = v
		case <-time.After(escTimeout):
			return pickerKeyCancel
		}
		if b2 != '[' {
			return pickerKeyOther
		}
		select {
		case b3, ok := <-ch:
			if !ok {
				return pickerKeyOther
			}
			switch b3 {
			case 'A':
				return pickerKeyUp
			case 'B':
				return pickerKeyDown
			default:
				return pickerKeyOther
			}
		case <-time.After(escTimeout):
			return pickerKeyOther
		}
	case '\r', '\n':
		return pickerKeyEnter
	case 'q', 'Q':
		return pickerKeyCancel
	case 'k', 'K':
		return pickerKeyUp
	case 'j', 'J':
		return pickerKeyDown
	default:
		return pickerKeyOther
	}
}

// maxKeyWidth returns the length of the longest Key across items, so both
// picker renderers (pick_unix.go's arrow picker, pick_windows.go's numbered
// fallback) can pad every key to one shared column width instead of each
// re-walking items with its own copy of this loop.
func maxKeyWidth(items []TargetEntry) int {
	width := 0
	for _, e := range items {
		if n := len(e.Key); n > width {
			width = n
		}
	}
	return width
}

// wrapCursor moves cur by delta (+1 down, -1 up) within a list of n items,
// wrapping past either end — the pure arithmetic behind the picker's
// Up/Down handling, split out so it's testable without a render loop.
// n <= 0 returns 0 (PickTarget never calls the picker with zero items, but
// this stays total rather than panicking).
func wrapCursor(cur, delta, n int) int {
	if n <= 0 {
		return 0
	}
	return ((cur+delta)%n + n) % n
}

// OnlyTarget returns the sole entry in choices when there is exactly one,
// announcing it with Info so a no-prompt run is never mistaken for a picker
// that silently failed to appear, using the "cmdName: key is this
// project's only target" phrasing. Returns ("", false) when choices has
// zero or more than one entry.
// Host function: only_target(choices_table, cmd_name).
func (h *Host) OnlyTarget(choices []TargetEntry, cmdName string) (string, bool) {
	if len(choices) != 1 {
		return "", false
	}
	target := choices[0].Key
	h.Info(cmdName + ": " + target + " is this project's only target")
	return target, true
}
