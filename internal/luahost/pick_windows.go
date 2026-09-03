//go:build windows

package luahost

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// runArrowPicker is PickTarget's interactive body on Windows, where this
// repo has no raw-mode terminal support (internal/reload's Windows stub
// exposes no StartKeyReader — process-group signal handling and termios
// cbreak mode are both unix-only). It falls back to a numbered list read
// from a line of stdin: still no subprocess, still one process, and it
// degrades the same way PickTarget's non-TTY path does.
func (h *Host) runArrowPicker(items []TargetEntry, prompt string) (string, PickOutcome) {
	stdinFile, _ := h.Stdin.(*os.File)

	keyWidth := maxKeyWidth(items)

	fmt.Fprintf(h.Stdout, "%s%s%s\n", ansiBold, prompt, ansiReset)
	for i, e := range items {
		fmt.Fprintf(h.Stdout, "  %d) %-*s  %s%s%s\n", i+1, keyWidth, e.Key, ansiDim, e.Description, ansiReset)
	}
	fmt.Fprintf(h.Stdout, "%sEnter a number, or q to cancel:%s ", ansiDim, ansiReset)

	reader := bufio.NewReader(stdinFile)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(h.Stdout)
		return "", PickCancelled
	}
	line = strings.TrimSpace(line)
	if line == "" || strings.EqualFold(line, "q") {
		return "", PickCancelled
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(items) {
		h.Err(fmt.Sprintf("%s: %q is not a valid choice", prompt, line))
		return "", PickUnavailable
	}
	return items[n-1].Key, PickChosen
}
