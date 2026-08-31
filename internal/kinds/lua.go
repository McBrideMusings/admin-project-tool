package kinds

import (
	"fmt"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// makeLua is kind="lua"'s factory — the piece admin-3rgp.36's
// docs/architecture/lua-host-api.md explicitly deferred to this ticket:
// "wiring an actual kind=\"lua\" into internal/kinds is admin-3rgp.35's job;
// Exec is what that kind's factory calls." `run` is the Lua source text,
// exactly as kind="python"'s `run` held a Python code block before
// ADR-0010 replaced it — a body is always inline source, never a
// script-path reference, since ADR-0010 deletes the kind="python-script"
// half of that old pair along with kind="python" itself.
//
// An optional inline `[actions.X.config]` table is exposed to the body as
// the `config` global. This is a narrower answer than Python's
// _CONFIG_GLOBALS (which exposes a whole manifest-level table like [apple]
// by kind-specific convention) — wiring a specific archetype's own config
// table into a lua action requires the full resolved Manifest, which a
// Factory's (name, action) signature does not carry. Filed as a follow-up
// rather than guessed at here; an action author can pass whatever an
// archetype needs through this inline table today.
func makeLua(name string, action manifest.Action) (Runner, error) {
	source, _ := actionString(action, "run")
	cfg, _ := action["config"].(map[string]any)
	return func(args []string) error {
		if strings.TrimSpace(source) == "" {
			return fmt.Errorf("action %q: kind=\"lua\" needs 'run' (a Lua body)", name)
		}
		h := newHost()
		if err := h.Exec(source, cfg); err != nil {
			h.Err(name + " failed: " + err.Error())
			return fmt.Errorf("action %q: %w", name, err)
		}
		h.Ok(name + " complete")
		return nil
	}, nil
}
