package kinds

import (
	"fmt"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// goModes mirrors admin_lib/interp.py's _GO_MODES.
var goModes = map[string]bool{"build": true, "install": true, "clean": true}

// makeGoKind is kind="go"'s factory — the Go port of
// admin_lib/interp.py::_make_go. `run` names the mode (build|install|clean,
// default "build"), not a shell command.
//
// binary/main come from the manifest-level [go] table (m.Go), exactly as
// Python's _make_go reads ns.get("_GO_CONFIG") — admin-3rgp.43 is what makes
// that table reachable here: a Factory now receives the resolved Manifest,
// not just the one action being dispatched. There is deliberately no
// per-action inline override (an [actions.X].binary key): Python's
// _make_go never had one either, so adding one here would be new surface
// with no precedent to port, and a project builds at most a handful of Go
// binaries through this kind, all of which already share the single project-
// level [go] table.
func makeGoKind(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	mode, _ := actionString(action, "run")
	if mode == "" {
		mode = "build"
	}
	if !goModes[mode] {
		return nil, fmt.Errorf(
			"action %q: kind=\"go\" run must be one of build|install|clean, got %q", name, mode)
	}
	cfg := archetypeConfigTable(m, "go")
	binary, _ := cfg["binary"].(string)
	main, _ := cfg["main"].(string)

	return func(args []string) error {
		var run string
		switch mode {
		case "build":
			if binary != "" {
				run = "go build -o " + shellQuote(binary) + " " + orDefault(main, ".")
			} else {
				run = "go build " + orDefault(main, "./...")
			}
		case "install":
			run = "go install " + orDefault(main, "./...")
		case "clean":
			run = "go clean ./..."
			if binary != "" {
				// `go clean` empties the build cache; it never removes a -o
				// target, so the binary this action built is removed by name.
				run += " && rm -f " + shellQuote(binary)
			}
		}
		h := newHost(m)
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
