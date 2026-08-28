package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/charmbracelet/x/term"
)

// confirm prints prompt to stderr and waits for a single keypress. y/Y/Enter
// accepts; n/N/Esc/any other key declines. Falls back to a line read when stdin
// is not a TTY. Everything goes to stderr so stdout stays clean for the cd
// sentinel the repo() shell function reads.
func confirm(prompt string) bool {
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

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true
	}
	return false
}
