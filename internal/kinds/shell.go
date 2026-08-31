package kinds

import (
	"fmt"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// emptyCmdErr matches admin_lib/interp.py's _EMPTY_CMD_ERR: a blank `run`
// would otherwise reach /bin/sh -c "" and exit 0, which a factory would then
// report as success — refuse it explicitly instead of trusting the shell to
// fail on nothing.
const emptyCmdErr = "refusing to run an empty command"

// makeShell is kind="shell"'s factory — the Go port of
// admin_lib/interp.py::_make_shell, minus the frame/footer and watch-kind
// pieces (admin-3rgp.11, out of this ticket's scope). `run` is resolved with
// manifest.ResolveComputed, not ResolveEnv: a shell body may reference its
// own shell-local variables (${x} from `x=$(...)`), which ResolveEnv would
// wrongly treat as an unresolved required var and error on.
func makeShell(name string, action manifest.Action, m *manifest.Manifest) (Runner, error) {
	run, _ := actionString(action, "run")
	return func(args []string) error {
		cmd := strings.TrimSpace(manifest.ResolveComputed(run))
		h := newHost()
		if cmd == "" {
			h.Err(emptyCmdErr)
			return fmt.Errorf("action %q: %s", name, emptyCmdErr)
		}
		h.Info("running: " + cmd)
		res, err := h.RunCmd(cmd, luahost.RunCmdOptions{CaptureLog: true})
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if !res.Ok {
			h.Err(name + " failed")
			return fmt.Errorf("action %q failed", name)
		}
		h.Ok(name + " complete")
		return nil
	}, nil
}

// makeShellPassthrough is kind="shell-passthrough"'s factory — the Go port
// of admin_lib/interp.py::_make_shell_passthrough, minus the `interactive`
// knob (a raw, unlogged terminal passthrough — a frame/terminal concern,
// admin-3rgp.11's territory, not this ticket's). Runner args are appended to
// `run`, each shell-quoted, so a caller-supplied argument can never be
// re-interpreted as shell syntax.
func makeShellPassthrough(name string, action manifest.Action, m *manifest.Manifest) (Runner, error) {
	run, _ := actionString(action, "run")
	return func(args []string) error {
		cmdline := run
		if len(args) > 0 {
			quoted := make([]string, len(args))
			for i, a := range args {
				quoted[i] = shellQuote(a)
			}
			cmdline += " " + strings.Join(quoted, " ")
		}
		resolved := strings.TrimSpace(manifest.ResolveComputed(cmdline))
		h := newHost()
		if resolved == "" {
			h.Err(emptyCmdErr)
			return fmt.Errorf("action %q: %s", name, emptyCmdErr)
		}
		h.Info("running: " + resolved)
		res, err := h.RunCmd(resolved, luahost.RunCmdOptions{CaptureLog: true})
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if !res.Ok {
			h.Err(name + " failed")
			return fmt.Errorf("action %q failed", name)
		}
		h.Ok(name + " complete")
		return nil
	}, nil
}
