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
// Python reads the binary/main path config from the manifest-level [go]
// table (ns.get("_GO_CONFIG")), injected by the interpreter's namespace
// build. A Factory here only receives (name, action) — no Manifest — so
// this ticket reads `binary`/`main` as the action's own inline keys instead
// (`[actions.build].binary`). Wiring a project-level [go] table into every
// kind="go" action generically is the same gap makeLua's doc comment
// flags for [actions.X.config]; tracked as a follow-up rather than solved
// here by threading Manifest into every Factory signature.
func makeGoKind(name string, action manifest.Action) (Runner, error) {
	mode, _ := actionString(action, "run")
	if mode == "" {
		mode = "build"
	}
	if !goModes[mode] {
		return nil, fmt.Errorf(
			"action %q: kind=\"go\" run must be one of build|install|clean, got %q", name, mode)
	}
	binary, _ := actionString(action, "binary")
	main, _ := actionString(action, "main")

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
