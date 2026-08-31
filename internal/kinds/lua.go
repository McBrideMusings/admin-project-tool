package kinds

import (
	"fmt"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	lua "github.com/yuin/gopher-lua"
)

// luaErrorMessage returns just the raised value's text for a Lua runtime
// error, dropping gopher-lua's stack trace when present. *lua.ApiError's own
// Error() always concatenates "<message>\n<trace>" (state.go), so a body
// that calls error("build failed") — the only way a kind="lua" action can
// signal failure, there being no Python-style bare sys.exit(1) — would
// otherwise print a "[G]: in function 'error' ..." traceback to the user on
// every deliberate, expected failure (an unknown build target, a failed
// xcodebuild), not just a genuine bug in the body. Falls back to err.Error()
// for anything that isn't an *lua.ApiError (a Go-side error Exec itself
// returned, e.g. a syntax error) so no information is lost there.
func luaErrorMessage(err error) string {
	if apiErr, ok := err.(*lua.ApiError); ok {
		return apiErr.Object.String()
	}
	return err.Error()
}

// makeLua is kind="lua"'s factory — the piece admin-3rgp.36's
// docs/architecture/lua-host-api.md explicitly deferred to this ticket:
// "wiring an actual kind=\"lua\" into internal/kinds is admin-3rgp.35's job;
// Exec is what that kind's factory calls." `run` is the Lua source text,
// exactly as kind="python"'s `run` held a Python code block before
// ADR-0010 replaced it — a body is always inline source, never a
// script-path reference, since ADR-0010 deletes the kind="python-script"
// half of that old pair along with kind="python" itself.
//
// The `config` global a body sees is built in two layers (admin-3rgp.43):
//
//  1. Every non-empty manifest-level archetype config table
//     (archetypeConfigTables(m)) is placed under its own admin.toml name —
//     config.go, config.apple, config.server, and so on. This is the piece
//     admin-3rgp.36 deferred: "wiring a project-level table into every
//     kind=\"lua\" action generically needs either a wider Factory signature
//     or a second dispatch-time pass" — admin-3rgp.43 is that wider
//     signature. Every table is exposed unconditionally to every lua body,
//     mirroring admin_lib/interp.py::build_namespace, which injects
//     _APPLE_CONFIG/_GO_CONFIG/... as globals regardless of which kind is
//     running: a body reads whichever table its archetype cares about by
//     name. A Factory's (name, action) pair alone carries no record of which
//     archetype an action was merged from (manifest.Action is a bare
//     map[string]any with no provenance field), so exposing all tables under
//     their own keys is the option that needs no new attribution machinery
//     — narrower than that would require inventing one.
//  2. An optional inline `[actions.X.config]` table is then merged in at the
//     top level, on top of the domain tables, so an action can still pass
//     ad hoc values the way the pre-existing TestLuaConfigReachesBody
//     exercises — inline keys win on collision with a domain-table name (an
//     inline key literally named "go" would shadow config.go), which is an
//     acceptable, documented edge case: no archetype in this repo names an
//     inline config key after another archetype's domain.
func makeLua(name string, action manifest.Action, m *manifest.Manifest, surf *Surface) (Runner, error) {
	source, _ := actionString(action, "run")
	cfg := archetypeConfigTables(m)
	if inline, ok := action["config"].(map[string]any); ok {
		for k, v := range inline {
			cfg[k] = v
		}
	}
	return func(args []string) error {
		if strings.TrimSpace(source) == "" {
			return fmt.Errorf("action %q: kind=\"lua\" needs 'run' (a Lua body)", name)
		}
		h := newHost()
		if surf != nil {
			h.RunAction = surf.action
		}
		if err := h.Exec(source, cfg, args...); err != nil {
			h.Err(name + " failed: " + luaErrorMessage(err))
			return fmt.Errorf("action %q: %w", name, err)
		}
		h.Ok(name + " complete")
		return nil
	}, nil
}
