// Package prompt holds the one confirm() implementation every yes/no
// terminal prompt in this tool shares. It used to live in cmd/prompt.go,
// private to the cmd package — but internal/kinds needs the same seam for a
// bare-CLI workflow's dispatch confirmation (admin-spjp.2), and internal/kinds
// cannot import cmd (cmd already imports internal/kinds), so the body moved
// here. cmd/prompt.go's confirm is now a thin delegation to Confirm so its
// two existing callers (cmd/worktree.go, cmd/prune.go) are unaffected.
package prompt

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/charmbracelet/x/term"
)

// Confirm prints prompt to stderr and waits for a single keypress. y/Y/Enter
// accepts; n/N/Esc/any other key declines. Everything goes to stderr so
// stdout stays clean for the cd sentinel the repo() shell function reads.
//
// When stdin is not a terminal (piped, redirected from /dev/null, a
// non-interactive CI run), TTY raw-mode reads are unavailable, so this falls
// back to a single line read. That fallback ONLY accepts an explicit "y" or
// "yes" (case-insensitive) — an empty line, EOF (what `</dev/null` yields),
// or anything else declines. A prior version of this function treated an
// empty non-TTY line as yes, which meant a workflow run with stdin closed
// (`admin implement </dev/null`) silently dispatched real agents into real
// worktrees with nobody having confirmed anything; a piped decline is the
// safer default and is announced on stderr so it is never silent.
func Confirm(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)

	if term.IsTerminal(os.Stdin.Fd()) {
		old, err := term.MakeRaw(os.Stdin.Fd())
		if err == nil {
			defer term.Restore(os.Stdin.Fd(), old)
			b := make([]byte, 1)
			if _, err := os.Stdin.Read(b); err != nil {
				fmt.Fprintln(os.Stderr)
				return false
			}
			fd := int(os.Stdin.Fd())
			syscall.SetNonblock(fd, true)
			defer syscall.SetNonblock(fd, false)
			drain := make([]byte, 16)
			os.Stdin.Read(drain)
			fmt.Fprintln(os.Stderr)
			return b[0] == 'y' || b[0] == 'Y' || b[0] == '\r'
		}
	}

	fmt.Fprintln(os.Stderr, "\nstdin is not a terminal — declining unless it reads an explicit \"y\"/\"yes\"")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
