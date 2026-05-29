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

## Guards

A project can gate any action or multi-target target behind a named precondition by listing guard names in `requires`. Built-in guards (`git-clean`, `git-pushed`) are registered by `admin_lib/guards.py`; user-defined ones live in `[guards.<name>]` tables. The full design and rejected alternatives live in [ADR-0005](docs/adr/0005-guards-and-requires.md).

```toml
# Built-in guards: reference by name. Custom ones: define inline.
[guards.no-tag]
run  = "test -z \"$(git tag --points-at HEAD)\""
fail = "HEAD is already tagged — bump the version before redeploying."

# Flat action: requires applies to the action body.
[actions.deploy]
kind     = "shell"
run      = "bun run deploy"
requires = ["git-clean", "git-pushed", "no-tag"]

# Multi-target: requires on the parent is the default; children may override.
[actions.preview]
kind     = "multi-target"
requires = ["git-clean", "git-pushed"]

[actions.preview.cf]            # inherits parent ["git-clean", "git-pushed"]
run = "bun run deploy:cf:preview"

[actions.preview.devvit]        # explicit opt-out — no guards for devvit
run      = "bun run deploy:devvit"
requires = []
```

**Pipeline.** `gen/manifest.py` parses `[guards.<name>]` (requires `run` + `fail`, rejects collisions with built-in names). `gen/archetypes.py` parses `requires` on actions and inline target subtables, stores per-target overrides as `meta["target_requires"]` (where `None` means inherit the parent default), and audits at resolve time that every name in any `requires` list maps to a known guard — built-in or user-defined. `gen/render.py` emits one `register_shell_guard(name, run, fail)` call per `[guards.<name>]` entry; `gen/fragments.py` prepends a `run_requires([...])` check to flat-action bodies and a per-target dispatch (resolved at render time, `_target_requires[picked]`) inside multi-target `cmd_*` functions. The `admin_lib/guards` module is auto-pulled into the bundle only when the manifest actually uses guards or `requires`.

**Runtime.** `run_requires` walks the list fail-fast: invokes each named guard, prints its diagnostic via `err()` on first failure, returns 1. Built-in guards produce dynamic diagnostics ("branch 'X' has unpushed commits — push first: git push origin X"); user-defined shell guards run `/bin/sh -c <run>`, capture stdout+stderr, print the captured output then the static `fail` on non-zero exit. The cwd is inherited (the generated `./admin` cd's to project root at boot). Guards do not compose — a guard cannot reference another `requires`. Adding a new built-in means writing a Python function in `admin_lib/guards.py` and adding its name to `GUARDS_BUILTIN` in `gen/manifest.py`.
