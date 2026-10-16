//go:build linux

package ports

import (
	"fmt"
	"os"
	"strings"
)

// readProcEnv returns pid's environment as KEY=VALUE entries from
// /proc/<pid>/environ; nil when it can't be read.
func readProcEnv(pid int) ([]string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, nil // gone, or another user's process: not a parse failure
	}
	var env []string
	for _, e := range strings.Split(string(raw), "\x00") {
		if e != "" {
			env = append(env, e)
		}
	}
	return env, nil
}
