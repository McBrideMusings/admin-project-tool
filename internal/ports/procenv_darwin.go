//go:build darwin

package ports

import (
	"bytes"
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

var errProcArgs = errors.New("malformed KERN_PROCARGS2 buffer")

// readProcEnv returns pid's environment as KEY=VALUE entries, read from
// sysctl KERN_PROCARGS2 — the same data `ps -E` prints, without a process
// per pid. A process that is gone or belongs to another user yields no
// environment and no error; a buffer that cannot be decoded is an error.
func readProcEnv(pid int) ([]string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, nil
	}
	return parseProcArgs2(raw)
}

// parseProcArgs2 decodes a KERN_PROCARGS2 buffer: an int32 argc, the
// executable path, NUL padding, exactly argc argv strings (any of which may
// be empty), then the environment strings up to the first empty one.
func parseProcArgs2(raw []byte) ([]string, error) {
	if len(raw) < 4 {
		return nil, errProcArgs
	}
	argc := int(int32(binary.NativeEndian.Uint32(raw[:4])))
	if argc < 0 {
		return nil, errProcArgs
	}
	rest := raw[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil, errProcArgs
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	for n := 0; n < argc; n++ {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return nil, errProcArgs
		}
		rest = rest[i+1:]
	}
	var env []string
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			i = len(rest)
		}
		if i == 0 {
			break
		}
		env = append(env, string(rest[:i]))
		if i == len(rest) {
			break
		}
		rest = rest[i+1:]
	}
	return env, nil
}
