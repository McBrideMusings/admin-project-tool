//go:build windows

package logbridge

import (
	"os"
)

// isPidAlive on Windows: os.FindProcess always succeeds, so probe with
// Signal(0)-equivalent by sending no signal and checking the error path via
// Release is not reliable either — the simplest correct check available
// without new deps is a best-effort "assume alive unless the process
// obviously doesn't exist", matching this package's discovery-file sweep
// being a debugging aid, not load-bearing for delivery (see logbridge.go's
// doc comment).
func isPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil || proc == nil {
		return false
	}
	return true
}
