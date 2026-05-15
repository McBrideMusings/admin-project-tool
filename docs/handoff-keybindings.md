# Handoff: Generic Keybindings for Long-Running Actions

**Status:** ready to start
**Driver:** add `r`/`R`/`v` key reloads to recodarr's `dev local` flow, but generalize so any project can wire keys → actions in `admin.toml` without re-implementing the runtime loop.

## Why

Today the `docker-dev` kind has an `r` keypress baked into `run_with_reload` (`admin_lib/docker.py`). It's the only kind with interactive key handling. The `shell` kind has none — so projects with bespoke supervisors (recodarr's `bun main.js` + Vite combo) can't bind keys without writing custom Python.

This handoff adds a manifest-driven keybinding system that any long-running action can opt into. Recodarr is the first consumer (drives the design), but the feature stands alone.

## Concrete goal — what recodarr wants to write

```toml
[actions.dev-local]
kind = "interactive-shell"
desc = "Bun (web+worker) + Vite on the host, ports 3300 / 3350"
run  = '''bash <<'__DEV_LOCAL_EOM__'
# … same heredoc as today, but it writes its child PIDs to
# /tmp/recodarr-main.pid and /tmp/recodarr-vite.pid …
__DEV_LOCAL_EOM__
'''

[actions.dev-local.keys.r]
label    = "restart web"
signal   = "TERM"
pid_file = "/tmp/recodarr-main.pid"
# main.js's SIGTERM path kills the web child only and exits; the bash
# heredoc's wait-loop respawns it. Worker stays detached.

[actions.dev-local.keys.R]
label    = "restart all"
signal   = "INT"
pid_file = "/tmp/recodarr-main.pid"
# SIGINT path: main.js kills both children, exits, heredoc respawns
# everything.

[actions.dev-local.keys.v]
label = "restart Vite"
run   = '''kill -TERM "$(cat /tmp/recodarr-vite.pid)" 2>/dev/null || true'''
```

The runtime prints a footer like:

```
[dev:local] [r] restart web · [R] restart all · [v] restart Vite · [Ctrl-C] exit
```

## Design decisions (already resolved)

1. **Bindings live inline on the action** — `[actions.X.keys.<keychar>]`. Not a separate `[keybindings]` registry. Scope-by-default; less indirection. (Discussed with the human who's picking this up.)
2. **Three target shapes, all supported in v1:**
   - `run = "shell snippet"` — inline shell, executed in a subshell while the wrapped process keeps running.
   - `action = "@another-action"` — invoke another `[actions.X]` by name. Reuses the existing action graph.
   - `signal = "TERM" | "INT" | "USR1" | …`, `pid_file = "/path/to.pid"` — read the PID, send the signal, done.
   
   Exactly one of `run` / `action` / `signal` per binding. Validator rejects combos.
3. **Keys are case-sensitive single characters.** `r` and `R` are distinct (Shift = uppercase). Plain letters + digits + punctuation; no Ctrl/Alt combos in v1 — many are unreachable in cbreak mode. (Ctrl-C / Ctrl-D stay reserved for exit.)
4. **New action kind: `interactive-shell`.** Don't silently upgrade `shell` — too easy to break existing projects whose shells don't expect cbreak stdin. Opt-in via the explicit kind.

## Implementation plan

### Phase 1 — runtime helper

**File:** `admin_lib/keybindings.py` (new module).

Public function:

```python
def run_with_keybindings(cmd, keys, *, label=None, formatter=None):
    """Run `cmd` as a long-lived subprocess; dispatch keystrokes to bindings.

    keys is an OrderedDict[str, dict] where each value has exactly one of:
        {"run": "<shell snippet>"}
        {"action": "<action-name>"}   # resolved to cmd_<safe_ident> at runtime
        {"signal": "TERM", "pid_file": "/path/to.pid"}
    Optional keys on each value: "label" (string, for the footer).

    Layout & lifecycle mirror docker.run_with_reload:
      - selector-loop on (proc.stdout, stdin)
      - cbreak stdin via termios; restored in finally
      - Ctrl-C raises KeyboardInterrupt → SIGINT child → exit
      - On unknown key: silently ignored
      - Banner printed on entry: "[r] restart web · …"
    """
```

Internal helpers:

- `_resolve_action(name)` — looks up `cmd_<safe_ident>(name)` in globals, calls it with `[]`.
- `_send_signal(sig_name, pid_file)` — reads PID, sends `signal.Signals[sig_name.upper()]`. ENOENT / stale PID → warn + no-op. Bad sig name → validator should reject upstream, but defensive at runtime too.
- `_run_inline(snippet)` — `subprocess.Popen(snippet, shell=True)`; non-blocking unless the binding has `wait = true` (deferred to v2).

**Don't duplicate** the SIGUSR1 reload plumbing from `docker.py` — its `r` handler is specific to "tear down the wrapped process and respawn it." Here the wrapped process keeps running; we just side-fire commands.

### Phase 2 — manifest model

**File:** `gen/manifest.py`

Extend `Command` with:

```python
keys: dict[str, dict] = field(default_factory=dict)
```

Where each value is a dict with keys: `label` (optional str), `run` | `action` | `signal+pid_file` (exclusive). Schema validation in the manifest loader; rejects:

- Multiple targets on the same binding.
- Empty bindings.
- Keys longer than 1 char.
- `signal` without `pid_file` and vice versa.
- `action` referencing an unknown action name (cross-checked after the action map is built).

Add `"interactive-shell"` to the kinds enum docstring at line 35.

### Phase 3 — fragment renderer

**File:** `gen/fragments.py`

Add `render_interactive_shell(cmd)`. Pattern matches `render_shell` but emits a call to `run_with_keybindings` with the keys dict literalized:

```python
def render_interactive_shell(cmd: Command) -> str:
    func_name = "cmd_" + _safe_ident(cmd.name)
    keys_repr = _pyrepr(cmd.keys)  # OrderedDict serialized
    body = textwrap.dedent(f"""\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            info({_pyrepr(f"running: {cmd.run}")})
            rc = run_with_keybindings({_pyrepr(cmd.run)}, {keys_repr}, label={_pyrepr(cmd.name)})
            if rc != 0:
                err({_pyrepr(cmd.name + " failed")})
                sys.exit(rc)
            ok({_pyrepr(cmd.name + " complete")})
    """)
    return body
```

Register in the dispatch table:

```python
_ACTION_RENDERERS = {
    ...,
    "interactive-shell": render_interactive_shell,
}

KIND_MODULES = {
    ...,
    "interactive-shell": ["keybindings"],
}
```

### Phase 4 — bundler hookup

**File:** `gen/bundler.py` (or wherever module assembly happens)

`KIND_MODULES["interactive-shell"] = ["keybindings"]` already pulls the new module in. Confirm the bundler hand-rolls section delimiters for `admin_lib/keybindings.py` the same way it does for `docker.py`.

### Phase 5 — tests

**Dir:** `tests/`

Three integration tests, each generating an `admin` from a fixture `admin.toml` and exercising the keybinding loop with `expect`-style input injection (or a Python subprocess + PTY harness — there's likely existing scaffolding in `tests/`):

1. `run` snippet fires on the matching key, doesn't fire on others.
2. `signal + pid_file` sends the right signal to a sleeper process.
3. `action` reference invokes the named action's body (assert via side-effect file).

Plus a unit-level test that the manifest validator rejects each malformed binding listed in Phase 2.

### Phase 6 — apply to recodarr

In `/root/projects/recodarr/admin.toml`:

1. Change `[actions.dev-local]` to `kind = "interactive-shell"`.
2. Add the three `[actions.dev-local.keys.{r,R,v}]` blocks shown above.
3. Update the heredoc to capture and persist PIDs:
   - `echo $BUN_PID > /tmp/recodarr-main.pid` after `BUN_PID=$!`.
   - `echo $VITE_PID > /tmp/recodarr-vite.pid` after `VITE_PID=$!`.
   - Existing `wait -n` + cleanup stays — when `bun main.js` or Vite exits (due to one of our signals), `wait -n` returns, and the loop's `cleanup → wait` resets. To make `r` actually respawn, the heredoc needs to be a `while true; do ...; done` loop that breaks on SIGINT. **This is the trickiest part of Phase 6** — without the respawn loop, sending SIGTERM to `bun main.js` just exits the whole `dev local` session.

   Sketch:

   ```bash
   trap 'echo; echo "[dev:local] shutting down"; kill -TERM "$BUN_PID" "$VITE_PID" 2>/dev/null; exit 0' INT TERM

   while true; do
     RECODARR_DEV_HOT=1 bun main.js & BUN_PID=$!
     echo $BUN_PID > /tmp/recodarr-main.pid
     ( cd web && bun run dev --port 3350 ) & VITE_PID=$!
     echo $VITE_PID > /tmp/recodarr-vite.pid

     wait -n  # returns when either child exits
     # whoever died, kill the other so we relaunch clean
     kill -TERM "$BUN_PID" "$VITE_PID" 2>/dev/null
     wait
     echo "[dev:local] child exited — restarting"
     sleep 0.2
   done
   ```

4. Regen `./admin` via `admin-gen --regenerate`.

5. Verify:
   - `./admin dev local` starts as before, prints the binding footer.
   - `r` → web child cycles, worker survives (check `pgrep -f worker.js` PID is stable).
   - `R` → main.js's SIGINT path kills both children; heredoc loop respawns; new worker PID.
   - `v` → Vite restart, bun untouched.
   - Ctrl-C cleans up everything (no orphan `bun main.js` or `vite`).

## Open questions for the implementer

- **Validator placement.** Manifest validation today happens in `gen/manifest.py` (loader-side) vs `gen/render.py` (cross-reference pass). The `action = "@name"` cross-check needs the latter. Pick whichever fits the existing seams.
- **Footer rendering.** Print on action entry, or only when stdin is a TTY? `run_with_reload` already gates `tty.setcbreak` on the latter; reuse that check.
- **Should `interactive-shell` support `pty = true` like `shell` does?** Probably not in v1 — PTY changes the stdin semantics and would conflict with cbreak. Document as unsupported, refuse in validator if both set.
- **Conflict with archetype-provided keys.** If an archetype-merged action already defines `keys`, project-level keys should override per-key (not whole-table replace). Consistent with how the rest of the manifest merges archetypes today (confirm in `gen/archetypes.py`).

## Non-goals

- **No multi-key chords or sequences.** Single key, single binding.
- **No Ctrl-/Alt- combos.** v1 sticks to printable ASCII.
- **No history/repeat.** Each press fires once.
- **No introspection command.** `./admin dev-local --keys` is a nice-to-have, not in scope.

## Touchpoints summary

| File | Change |
|---|---|
| `admin_lib/keybindings.py` | NEW — `run_with_keybindings()` + helpers |
| `gen/manifest.py` | Add `keys` to `Command`; validate |
| `gen/fragments.py` | `render_interactive_shell`; register kind |
| `gen/render.py` | Cross-reference `action = "@name"` keys |
| `gen/archetypes.py` | Merge `keys` per-key when combining archetype + project |
| `tests/` | Integration + validator tests |
| `docs/design.md` | Document the new kind under the manifest format section |
| (consumer) `recodarr/admin.toml` | Switch dev-local to `interactive-shell`; add keys; respawn loop |

## Acceptance

- `admin-gen --regenerate` succeeds on recodarr.
- `./admin dev local` boots, footer shows three bindings.
- `r` restarts web only, worker PID stays stable.
- `R` restarts everything.
- `v` restarts Vite only.
- Ctrl-C exits cleanly with no orphans.
- New tests pass; existing tests unaffected.
