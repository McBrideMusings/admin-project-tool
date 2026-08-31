package luahost

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// TargetEntry is one row of a pick_target menu: a stable key and its
// display description, matching admin_lib/core.py's {name: description}
// dict entries. Order is carried separately (see PickTarget) since Go maps
// don't preserve insertion order.
type TargetEntry struct {
	Key         string
	Description string
}

// isTTY reports whether f looks like an interactive terminal. A minimal,
// dependency-free stand-in for isatty: good enough to gate the picker the
// same way admin_lib/core.py's sys.stdout.isatty() does, without pulling in
// golang.org/x/term for one check.
func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// PickTarget prompts for one of order (in order — matching admin_lib's
// display_order, "" entries render as a blank spacer row exactly as
// Python's pick_target does) and returns the chosen key, or ("", false) if
// there was nothing to pick from, the terminal isn't interactive, or the
// user cancelled.
//
// This runs entirely in-process against h.Stdin/h.Stdout/h.Stderr — the
// acceptance criterion ADR-0010 exists to satisfy ("pick_target prompts
// from the same process, with no subprocess and no wire protocol"). It
// trades the Python original's raw-mode arrow-key picker for a numbered
// list read from a line of stdin: still no subprocess, still one process,
// and it degrades the same way (no TTY → explicit error naming the
// expected values, never a hang). A richer in-terminal picker is tracked as
// a follow-up, not required by this ticket's acceptance criteria.
// Host function: pick_target(targets_table[, prompt]) — targets_table is an
// ordered array of {key=..., description=...} rows.
func (h *Host) PickTarget(entries []TargetEntry, prompt string) (string, bool) {
	items := make([]TargetEntry, 0, len(entries))
	for _, e := range entries {
		if e.Key != "" {
			items = append(items, e)
		}
	}
	if len(items) == 0 {
		return "", false
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
		return "", false
	}

	fmt.Fprintf(h.Stdout, "%s%s%s\n", ansiBold, prompt, ansiReset)
	for i, e := range items {
		fmt.Fprintf(h.Stdout, "  %d) %s  %s%s%s\n", i+1, e.Key, ansiDim, e.Description, ansiReset)
	}
	fmt.Fprintf(h.Stdout, "%sEnter a number, or q to cancel:%s ", ansiDim, ansiReset)

	reader := bufio.NewReader(stdinFile)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(h.Stdout)
		return "", false
	}
	line = strings.TrimSpace(line)
	if line == "" || strings.EqualFold(line, "q") {
		return "", false
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(items) {
		h.Err(fmt.Sprintf("%s: %q is not a valid choice", prompt, line))
		return "", false
	}
	return items[n-1].Key, true
}

// OnlyTarget returns the sole entry in choices when there is exactly one,
// announcing it with Info so a no-prompt run is never mistaken for a picker
// that silently failed to appear — matching admin_lib/apple.py's
// only_target exactly, including its own "cmdName: key is this project's
// only target" phrasing. Returns ("", false) when choices has zero or more
// than one entry.
// Host function: only_target(choices_table, cmd_name).
func (h *Host) OnlyTarget(choices []TargetEntry, cmdName string) (string, bool) {
	if len(choices) != 1 {
		return "", false
	}
	target := choices[0].Key
	h.Info(cmdName + ": " + target + " is this project's only target")
	return target, true
}
