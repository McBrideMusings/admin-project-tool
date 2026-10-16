//go:build !darwin && !linux

package ports

// readProcEnv has no portable source on this platform, so the tag sweep
// finds nothing and does nothing here.
func readProcEnv(pid int) ([]string, error) { return nil, nil }
