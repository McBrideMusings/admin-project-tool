//go:build windows

package reload

import (
	"errors"
	"io"
	"time"
)

// errUnsupported is returned by every reload primitive that depends on
// SIGUSR1 (SetupReload, RunWithReload, Signal, PollForReload) — that
// signal does not exist on Windows.
var errUnsupported = errors.New("reload: SIGUSR1-based reload is not supported on Windows")

// IsRebuildRequested is always false on Windows — there is no signal path
// that could ever set it.
func IsRebuildRequested() bool { return false }

// ClearRebuildRequest is a no-op on Windows.
func ClearRebuildRequest() {}

// SetupReload always fails on Windows.
func SetupReload(target, dir string) (cleanup func(), err error) {
	return func() {}, errUnsupported
}

// PollForReload always returns false (treated as "watched process is gone")
// on Windows, since it can never observe a reload request.
func PollForReload(isRunning func() bool, timeout time.Duration) bool { return false }

// Options mirrors the Unix Options shape so callers compile unchanged;
// RunWithReload ignores every field and fails immediately.
type Options struct {
	Dir       string
	Stdout    io.Writer
	Formatter func(lineWithNewline string) string
	LogFn     func(text string)
	Info      func(msg string)
	Warn      func(msg string)
}

// RunWithReload always fails on Windows: no SIGUSR1, no process-group
// SIGTERM/SIGKILL semantics to build a reload loop on top of.
func RunWithReload(cmd, target string, opts Options) int {
	if opts.Warn != nil {
		opts.Warn(errUnsupported.Error())
	}
	return 1
}

// Signal always fails on Windows.
func Signal(prefix string, match func(Record) bool, r Reporter) (int, error) {
	if r != nil {
		r.Err(errUnsupported.Error())
	}
	return 0, errUnsupported
}
