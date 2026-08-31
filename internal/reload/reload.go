// Package reload is the Go port of admin_lib/reload.py — the ONE place a
// rebuild request (SIGUSR1, or an 'r' keypress from a live terminal) and its
// PID-file bookkeeping live.
//
// The Python original held two shapes on one piece of state, because dev
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
// Both previously kept their own `_rebuild_requested` flag and their own
// SIGUSR1 handler in the Python — docker.py for the first, apple.py for the
// second — which is exactly the duplication this repo's CLAUDE.md records as
// past pain. Both shapes live in this one package now, so a future port never
// has to choose between adopting this package and writing a second copy of
// its own.
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

// PIDPrefix mirrors admin_lib/reload.py's _ADMIN_PID_PREFIX. A per-target
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
// directories as needed. The Go port of admin_lib/reload.py's inline
// `with open(pid_file, "w") as f: f.write(str(os.getpid()))`.
func WritePIDFile(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// CleanupPIDFile removes path if it exists. The Go port of
// admin_lib/reload.py's cleanup_pid_file — best-effort, errors swallowed.
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

// globPIDFiles returns every "<prefix>-*.pid" path, mirroring
// admin_lib/reload.py's `glob.glob(f"{prefix}-*.pid")`.
func globPIDFiles(prefix string) ([]string, error) {
	return filepath.Glob(prefix + "-*.pid")
}

// baseName returns path's final path element — filepath.Base, named locally
// so reload_unix.go's target-name derivation reads as plainly as the Python
// os.path.basename call it ports.
func baseName(path string) string {
	return filepath.Base(path)
}
