package kinds

import (
	"fmt"
	"os"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// makeDocs is kind="docs"'s factory — the Go port of
// admin_lib/interp.py::_make_docs. Reads the manifest's [docs] table (dir,
// script) and runs "<bun|npm> run <script>" in dir, picking bun/npm via the
// same pkgRunner lockfile check as kind="npm" — scoped to dir here, unlike
// makeNpm's cwd-only check, since a docs site can be a bun workspace nested
// inside an otherwise-npm project (or vice versa), matching Python's
// _pkg_runner(dirpath) call.
func makeDocs(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	cfg := archetypeConfigTable(m, "docs")
	dirpath, _ := cfg["dir"].(string)
	if dirpath == "" {
		dirpath = "."
	}
	script, _ := cfg["script"].(string)
	if script == "" {
		script = "docs:dev"
	}

	return func(args []string) error {
		h := newHost()
		if info, err := os.Stat(dirpath); err != nil || !info.IsDir() {
			msg := "docs: no such directory: " + dirpath + " (set [docs].dir)"
			h.Err(msg)
			return fmt.Errorf("action %q: %s", name, msg)
		}
		run := pkgRunner(dirpath) + " run " + script
		if dirpath != "." {
			run = "cd " + shellQuote(dirpath) + " && " + run
		}
		res, err := h.RunCmd(run, luahost.RunCmdOptions{CaptureLog: true})
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if !res.Ok {
			h.Err(run + " failed")
			return fmt.Errorf("action %q: %s failed", name, run)
		}
		h.Ok("docs server exited")
		return nil
	}, nil
}
