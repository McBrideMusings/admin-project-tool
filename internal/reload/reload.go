// Package reload is the ONE place a rebuild request (SIGUSR1, or an 'r'
// keypress from a live terminal) and its PID-file bookkeeping live.
//
// This package holds two shapes on one piece of state, because dev
// commands come in two shapes:
//
//   - RunWithReload(cmd, target, ...) — this package owns the loop. For a
//     process that stays up on its own (a server, a bundler): it streams the
//     child's output and, on reload, tears the child down and relaunches it.
//     kind="docker-dev" and kind="docker-reload-loop" use this shape.
//
//   - SetupReload(pidFile) + IsRebuildRequested()/ClearRebuildRequest() — the
//     CALLER owns the loop. For a build-install-launch cycle (the Apple dev
//     loops admin-3rgp.12 ports) where "reload" means running the whole cycle
//     again, not restarting one child.
//
// Keeping a separate rebuild-requested flag and SIGUSR1 handler per caller is
// exactly the duplication this repo's CLAUDE.md records as past pain. Both
// shapes live in this one package instead, so a future caller never has to
// choose between adopting this package and writing a second copy of its own.
//
// `admin logs`/`admin kill` and the `server-reload` kind find a running dev
// loop through the PID files this package writes under PIDPrefix.
package reload

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PIDPrefix is the base path for every PID file this package writes. A per-target
// loop's PID file is "<PIDPrefix>-<target>.pid"; SetupReload's caller-owned
// shape defaults to the bare "<PIDPrefix>.pid".
const PIDPrefix = "/tmp/admin-run"

// Reporter is the small slice of luahost.Host's output surface this package
// needs (Warn/Ok/Err) — a structural interface rather than an import of
// internal/luahost, so this package stays free of the Lua VM / project-root /
// log-context machinery that lives there and has nothing to do with reload
// state. *luahost.Host already satisfies this with no adapter needed.
type Reporter interface {
	Warn(msg string)
	Ok(msg string)
	Err(msg string)
}

// WritePIDFile writes the current process's PID to path, creating parent
// directories as needed.
func WritePIDFile(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// CleanupPIDFile removes path if it exists — best-effort, errors swallowed.
func CleanupPIDFile(path string) {
	_ = os.Remove(path)
}

// readPIDFile reads and parses the integer PID stored at path.
func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("malformed pid file %s: %w", path, err)
	}
	return pid, nil
}

// globPIDFiles returns every "<prefix>-*.pid" path.
func globPIDFiles(prefix string) ([]string, error) {
	return filepath.Glob(prefix + "-*.pid")
}

// baseName returns path's final path element — filepath.Base, named locally
// so reload_unix.go's target-name derivation reads plainly.
func baseName(path string) string {
	return filepath.Base(path)
}
