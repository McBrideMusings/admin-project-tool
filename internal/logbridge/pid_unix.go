//go:build !windows

package logbridge

import "syscall"

// isPidAlive reports whether pid is still running, via signal 0. A
// permission error (the pid exists but belongs to another user) reads as
// alive.
func isPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return err != syscall.ESRCH
}
