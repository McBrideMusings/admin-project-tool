# Handoff: Generic Keybindings for Long-Running Actions

**Status:** implemented (`internal/keybindings`, `kind = "interactive-shell"`); Phase 6 (recodarr adoption) outstanding.
**Driver:** add `r`/`R`/`v` key reloads to recodarr's `dev local` flow, but generalize so any project can wire keys → actions in `admin.toml` without re-implementing the runtime loop.

## Why

The `docker-dev` kind has an `r` keypress baked into its reload loop (`internal/reload`). It's the only kind with interactive key handling. The plain `shell` kind has none — so projects with bespoke supervisors (recodarr's `bun main.js` + Vite combo) can't bind keys without writing a custom runner.

This handoff adds a manifest-driven keybinding system that any long-running action can opt into: `kind = "interactive-shell"`, implemented in `internal/keybindings` and wired into `internal/kinds/interactiveshell.go`. Recodarr is the first consumer (drives the design), but the feature stands alone.

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

## Implementation

Phases 1-5 below are done, in Go:

- **Runtime.** `internal/keybindings.Run` runs the wrapped process as a long-lived subprocess and dispatches keystrokes to bindings — a selector loop over the child's stdout and stdin, cbreak stdin restored on exit, Ctrl-C tears the child down, an unknown key is silently ignored, and a banner prints on entry (`formatBanner`). Each binding is exactly one of `Run` (shell snippet), `Action` (invoke a registered action by name, dispatched through the `RunAction` callback `internal/kinds` supplies), or `Signal`+`PIDFile` (read a PID, send a signal). It does not duplicate `internal/reload`'s SIGUSR1 plumbing — that handler tears the wrapped process down and respawns it; here the wrapped process keeps running and bindings side-fire commands.
- **Manifest model.** `internal/manifest` parses the raw `keys.<keychar>` table on an action; `internal/validate.ParseKeys` validates it — rejects multiple targets on one binding, empty bindings, keys longer than one character, `signal` without `pid_file` and vice versa, and (cross-checked once the action map is built) an `action` referencing an unknown name.
- **Kind wiring.** `kind = "interactive-shell"` is a catalog entry in `internal/kinds/catalog.go` (`Modules: []string{"ports", "log_server", "log_bridge", "keybindings", "log_format"}`) with its factory in `internal/kinds/interactiveshell.go` (`makeInteractiveShell`), registered in `factories.go`.
- **Tests.** `internal/keybindings/keybindings_test.go` and `internal/kinds/interactiveshell_test.go` cover the binding shapes and validation.

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

4. Run `admin check .` in recodarr to validate the manifest — the tool reads `admin.toml` live, so there is no regeneration step.

5. Verify:
   - `./admin dev local` starts as before, prints the binding footer.
   - `r` → web child cycles, worker survives (check `pgrep -f worker.js` PID is stable).
   - `R` → main.js's SIGINT path kills both children; heredoc loop respawns; new worker PID.
   - `v` → Vite restart, bun untouched.
   - Ctrl-C cleans up everything (no orphan `bun main.js` or `vite`).

## Non-goals

- **No multi-key chords or sequences.** Single key, single binding.
- **No Ctrl-/Alt- combos.** v1 sticks to printable ASCII.
- **No history/repeat.** Each press fires once.
- **No introspection command.** `./admin dev-local --keys` is a nice-to-have, not in scope.

## Touchpoints summary

| File | Change |
|---|---|
| `internal/keybindings` | `Run()` + helpers — the runtime loop |
| `internal/manifest` | Parses `keys` on an action |
| `internal/validate` | `ParseKeys` — binding-shape validation and the `action = "@name"` cross-check |
| `internal/kinds/interactiveshell.go` | `makeInteractiveShell`; registered in `factories.go` |
| `internal/keybindings/keybindings_test.go`, `internal/kinds/interactiveshell_test.go` | Binding-shape and validation tests |
| (consumer) `recodarr/admin.toml` | Switch dev-local to `interactive-shell`; add keys; respawn loop (Phase 6, outstanding) |

## Acceptance

- `admin check .` passes on recodarr.
- `./admin dev local` boots, footer shows three bindings.
- `r` restarts web only, worker PID stays stable.
- `R` restarts everything.
- `v` restarts Vite only.
- Ctrl-C exits cleanly with no orphans.
- New tests pass; existing tests unaffected.
