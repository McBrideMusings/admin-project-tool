//go:build windows

package frame

// notifyWinch is a no-op on Windows: there is no SIGWINCH there. Kept as an
// explicit stub, the same pattern winsize_windows.go uses, so `go build`
// on Windows never trips over a missing symbol or a syscall.SIGWINCH
// reference that doesn't exist on this platform.
func notifyWinch(ch chan<- struct{}) (stop func()) {
	return func() {}
}
