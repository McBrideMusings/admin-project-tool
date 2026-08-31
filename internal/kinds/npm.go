package kinds

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// pkgRunner is the Go port of admin_lib/interp.py::_pkg_runner: bun if a bun
// lockfile sits next to dirpath's package.json, npm otherwise.
func pkgRunner(dirpath string) string {
	for _, lock := range []string{"bun.lockb", "bun.lock"} {
		if _, err := os.Stat(filepath.Join(dirpath, lock)); err == nil {
			return "bun"
		}
	}
	return "npm"
}

// makeNpm is kind="npm"'s factory — the Go port of
// admin_lib/interp.py::_make_npm. `run` is the package.json script name.
func makeNpm(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	script, ok := actionString(action, "run")
	if !ok || script == "" {
		return nil, fmt.Errorf(`action %q: kind="npm" requires 'run' (a package.json script name)`, name)
	}
	return func(args []string) error {
		run := pkgRunner(".") + " run " + script
		h := newHost()
		res, err := h.RunCmd(run, luahost.RunCmdOptions{CaptureLog: true})
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if !res.Ok {
			h.Err(run + " failed")
			return fmt.Errorf("action %q: %s failed", name, run)
		}
		h.Ok(run + " complete")
		return nil
	}, nil
}
