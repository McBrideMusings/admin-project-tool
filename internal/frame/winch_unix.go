//go:build !windows

package frame

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyWinch registers ch to receive a (non-blocking) signal on every
// SIGWINCH the process gets, and returns a stop func that tears the
// registration down. Mirrors winsize_unix.go's platform-split shape: the
// only place in the package that names syscall.SIGWINCH, so frame.go itself
// never has to.
func notifyWinch(ch chan<- struct{}) (stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sig:
				select {
				case ch <- struct{}{}:
				default:
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(sig)
		close(done)
	}
}
