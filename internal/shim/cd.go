// Package shim emits the CD sentinel that the repo() shell function reads to
// change the parent shell's working directory.
package shim

import (
	"fmt"
	"os"
)

// SentinelPrefix is the contract with the repo() shell function: any line on
// stdout starting with this is a `cd` request. Everything else MUST go to stderr.
const SentinelPrefix = "__REPO_CD__:"

// PrintCD writes a cd request to stdout for the shell function to act on.
func PrintCD(path string) {
	fmt.Fprintln(os.Stdout, SentinelPrefix+path)
}
