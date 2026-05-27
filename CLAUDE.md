# admin-project-tool

Generates per-project `./admin` task-runner scripts from a short `admin.toml` manifest plus a library of composable archetypes. See `docs/architecture/design.md` for the full architecture.

## What this repo is

- **`admin-gen`** — the generator CLI. Reads a project's `admin.toml`, merges archetypes, bundles `admin_lib`, and emits a self-contained `./admin` Python script.
- **`admin_lib/`** — runtime helpers inlined into every generated script (logging, port management, log tailing, Apple build helpers, etc.). Stdlib only.
- **`archetypes/`** — partial manifests per project type (apple, hugo, docker-unraid, rust-tauri, etc.). Mixins, not monoliths.
- **`detectors/`** — heuristics that suggest archetypes from a project's file layout.
- **`gen/`** — generator internals: manifest parser, bundler, archetype merger, renderer.
- **`install.sh`** — installs `admin-gen` + `admin_lib` to `~/.admin/`, wires PATH, registers a Claude Code `SessionStart` hook that auto-regenerates stale `./admin` scripts.

## Design principles (don't violate)

1. **Manifest is truth.** `admin.toml` is the spec. `./admin` is a generated artifact — never hand-edit beyond what `admin-gen --audit` tolerates.
2. **Commands are pure routing.** `[commands.X]` may only contain `desc` + `steps` (list of action names). `kind`, `run`, `targets` belong in `[actions.X]`. The generator errors loudly otherwise.
3. **Stdlib only (with one narrow carve-out).** No third-party deps in the generator OR the generated script by default. The only exception is try/except-guarded optional imports that degrade gracefully when absent (today: `Pillow` + `cairosvg` in `admin_lib/icons.py`; `tomli` in `gen/manifest.py` and `admin_lib/apple.py` as a Python 3.9/3.10 backport for `tomllib`). New optional deps must clear the same bar. See [ADR-0001](docs/adr/0001-stdlib-only.md). Manifest parses via `tomllib` (or `tomli` on Python 3.9/3.10); writing TOML is hand-rolled.
4. **Zero contributor install.** Generated `./admin` is fully self-contained — contributors `git clone && ./admin`.
5. **Idempotent regeneration.** `admin-gen --regenerate` must produce byte-identical output for the same inputs.

## Hard rules (from past pain)

- **Never scaffold `admin_inline.py`.** Project-specific Python belongs upstream in `admin_lib/` or `archetypes/`. A per-project inline file is a regression vector.
- **`from __future__` imports must be hoisted by the bundler.** Mid-file `__future__` imports are a `SyntaxError` in the bundled output. The bundler dedupes them into a single header line — see `gen/render.py`.
- **Reject `from . import module`** in archetype/lib code. The bundler flattens namespaces; the module object doesn't survive. Bundler raises `BundlerError` at gen time.

## Testing

Tests run under `pytest` (dev-only dep — bundled `admin_lib/` and the generated `./admin` stay stdlib-only). Install with `pip install -r requirements-dev.txt`. New `tests/test_*.py` files are auto-discovered — no manual registration in `admin.toml`. Shell tests (`tests/test_install.sh`, etc.) are chained after pytest in `[actions.test]`.

Run all: `./admin test`.

## Common workflows

| Goal | Command |
|---|---|
| Regenerate this repo's `./admin` after editing `admin.toml` | `./admin build` |
| Run the generator against an external project interactively | `./admin dev` |
| Install / reinstall to `~/.admin/` | `./admin deploy` |
| Run full test suite | `./admin test` |

## Session hook

`install.sh` registers a `SessionStart` hook at `~/.admin/hooks/session-admin-check.sh`. On every Claude Code session, it diff-checks the project's `./admin` against the installed `admin-project-tool` version and silently regenerates if stale. To disable: remove the entry from `~/.claude/settings.json`.

## Log bridge

A project can opt into browser-console-to-admin-log forwarding by adding `[log_bridge] hosts = [...]` to its `admin.toml`. Any subsequent `interactive-shell` action spins up a `ThreadingHTTPServer` on a free port (default `9988-9999`; override per machine with `port_range = [start, end]`), bound to `0.0.0.0` so Tailscale peers can reach it. The userscript at `tampermonkey/log-bridge.user.js` probes the user's hardcoded hosts × ports, picks the admin instance whose `hosts` glob-matches the page, and POSTs batched `console.*` entries to `/log` where they're appended to the same log file the action is already writing. Discovery files at `/tmp/admin-project-tool/<pid>.json` are debugging aids; dead-PID files are swept on each start. Bind is `0.0.0.0` unconditionally — anyone on the tailnet can POST. `port_range` is per-machine: set it in each environment's `admin.toml` when the default range conflicts (e.g. a container that only exposes `3300-3399`); update `PROBE_PORTS` in the userscript to cover the union of all ranges.

## Git gate

A project can gate its deploy-style commands behind preflight git checks by adding a `[git_gate]` table to `admin.toml`:

```toml
[git_gate]
commands       = ["preview", "staging", "deploy"]  # optional; defaults to these three
require_clean  = true   # block if the working tree has uncommitted/untracked changes
require_pushed = true   # block if the current branch isn't fully pushed to origin
```

`gen/manifest.py` parses + validates the table; `gen/render.py` emits a `_GIT_GATE_CONFIG` dict plus a built-in `git-gate` action (backed by `admin_lib.core.git_gate`) and prepends `git-gate` to the `steps` of each guarded command. At runtime the action calls `git_gate(cfg)`, which checks the enabled rules (`git status --porcelain` for clean, `git rev-list origin/<branch>..HEAD` for pushed), prints the offending rule + the exact fix command, and `sys.exit(1)` to abort before the real deploy step. Both rules default `true`; set either to `false` to drop it. Only step-based commands can be gated (non-step commands are skipped). Feature branches are fine — they just have to exist on origin with no unpushed commits.
