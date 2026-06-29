# admin-project-tool

Runs per-project tasks from a short `admin.toml` manifest plus a library of composable archetypes. The installed tool **interprets** `admin.toml` at runtime (ADR-0006) — a project commits only the manifest; the `admin` command (installed on PATH at `~/.admin/admin`) finds it by walking up from `$PWD`. `admin compile` packages a standalone `./admin` (a zipapp) for the rare case the tool isn't installed. See `docs/architecture/design.md` for the full architecture.

## What this repo is

- **`admin-run`** — the interpreter CLI (installed as `~/.admin/admin`). Reads a project's `admin.toml`, merges archetypes, registers live command/action callables, and dispatches. Tool verbs: `new`, `check`, `compile`.
- **`admin_lib/`** — runtime helpers (logging, port management, log tailing, Apple build helpers, etc.) imported as a real package. `admin_lib/interp.py` is the interpreter: one closure factory per action kind. It also holds the manifest frontend: `manifest.py` (parser), `archetypes.py` (archetype merger), `kinds.py` (kind metadata + ${VAR} scanner), `validate.py` (command/action schema validation — `ValidationError` umbrella; see [ADR-0008](docs/adr/0008-centralized-schema-validation.md)), `compile.py` (standalone zipapp), `detectors.py` (detector loader). Stdlib only.
- **`archetypes/`** — partial manifests per project type (apple, hugo, docker-unraid, rust-tauri, etc.). Mixins, not monoliths. (The data dir — the merger code is `admin_lib/archetypes.py`.)
- **`detectors/`** — heuristics that suggest archetypes from a project's file layout. (The data dir — the loader is `admin_lib/detectors.py`.)
- **`install.sh`** — installs the interpreter + `admin_lib` + the `archetypes`/`detectors` data dirs to `~/.admin/` and puts `~/.admin` on PATH so `admin` runs from any project.

## Design principles (don't violate)

1. **Manifest is truth.** `admin.toml` is the spec. The interpreter reads it live; there is no generated artifact to drift. `admin compile` is the only thing that emits a file.
2. **Commands are pure routing.** `[commands.X]` may only contain `desc` + `steps` (list of action names). `kind`, `run`, `targets` belong in `[actions.X]`. The resolver errors loudly otherwise.
3. **Stdlib only (with one narrow carve-out).** No third-party deps in the tool OR the interpreter/compiled output by default. The only exception is try/except-guarded optional imports that degrade gracefully when absent (today: `Pillow` + `cairosvg` in `admin_lib/icons.py`; `tomli` in `admin_lib/manifest.py` and `admin_lib/apple.py` as a Python 3.9/3.10 backport for `tomllib`). New optional deps must clear the same bar. See [ADR-0001](docs/adr/0001-stdlib-only.md). Manifest parses via `tomllib` (or `tomli` on Python 3.9/3.10); writing TOML is hand-rolled.
4. **One engine.** The interpreter (`admin_lib/interp.py`) is the single execution path. `admin compile` packages that same interpreter + `admin_lib` + the resolved manifest into a zipapp — it never re-implements per-kind logic. Adding a kind = one `_kind(...)` entry in the `admin_lib/kinds.KINDS` catalog + one `register_factory(name, ...)` in `interp.py`; the interpreter checks at import that every kind has a factory.
5. **Tool verbs are reserved.** `new`, `check`, `compile` share the `admin <word>` namespace with manifest commands; a manifest may not define a command with one of these names (the CLI errors).

## Hard rules (from past pain)

- **Never scaffold `admin_inline.py`.** Project-specific Python belongs upstream in `admin_lib/` or `archetypes/`. A per-project inline file is a regression vector.
- **A new action kind is one `_kind(...)` entry in `admin_lib/kinds.KINDS` plus a `register_factory(name, ...)` in `admin_lib/interp.py`.** `KINDS` is the single source of truth (`KNOWN_KINDS`/`CONFIGLESS_KINDS`/`KIND_MODULES` are derived views). `register_factory` rejects an undeclared kind, and `interp.py` raises at import if any declared kind has no factory — so the catalog and factories can't drift, no sync test required.

## Testing

Tests run under `pytest` (dev-only dep — `admin_lib/` and the interpreter stay stdlib-only at runtime). Install with `pip install -r requirements-dev.txt`. New `tests/test_*.py` files are auto-discovered. Shell tests (`tests/test_install.sh`, etc.) are chained after pytest in `[actions.test]`.

Run all: `./admin test`. Tests that exercise full dispatch write a temp `admin.toml` and run it through `admin-run` (see `tests/test_archetypes.py::_run_interpreter`).

## Common workflows

| Goal | Command |
|---|---|
| Validate this repo's `admin.toml` (parse + resolve) | `./admin build` (runs `admin-run check .`) |
| Install / reinstall to `~/.admin/` | `./admin deploy` |
| Run full test suite | `./admin test` |
| Build a standalone `./admin` for a project | `admin compile [DIR]` |

Pre-install (or while developing the tool), invoke the interpreter directly with `./admin-run <verb-or-command>` instead of the PATH `admin`.

## Log bridge

A project can opt into browser-console-to-admin-log forwarding by adding `[log_bridge] hosts = [...]` to its `admin.toml`. Any subsequent `interactive-shell` action spins up a `ThreadingHTTPServer` on a free port (default `9988-9999`; override per machine with `port_range = [start, end]`), bound to `0.0.0.0` so Tailscale peers can reach it. The userscript at `tampermonkey/log-bridge.user.js` probes the user's hardcoded hosts × ports, picks the admin instance whose `hosts` glob-matches the page, and POSTs batched `console.*` entries to `/log` where they're appended to the same log file the action is already writing. Each entry carries the page's `host` (not the full URL — keeps auth tokens in query strings out of the log); the listener prints one `client connected: <host> (<browser>)` banner per host, then bare `[client] [level] message` lines. Discovery files at `/tmp/admin-project-tool/<pid>.json` are debugging aids; dead-PID files are swept on each start. Bind is `0.0.0.0` unconditionally — anyone on the tailnet can POST. `port_range` is per-machine: set it in each environment's `admin.toml` when the default range conflicts (e.g. a container that only exposes `3300-3399`); update `PROBE_PORTS` in the userscript to cover the union of all ranges.

The userscript is project-agnostic — no prod URLs baked in. It decides which pages to forward at runtime: `localhost`/`127.0.0.1`/`0.0.0.0` are always candidates, and any other host is opt-in via a single Tampermonkey menu command ("Log bridge: watch `<host>`", a host-labelled toggle) that stores an auto-derived glob in `GM` storage. The per-project prod hostname lives only in that project's `admin.toml` `[log_bridge].hosts` (advertised via `/health`), which remains the authoritative final match — so a new project needs one menu click, never a script edit.

## Guards

A project can gate any action or multi-target target behind a named precondition by listing guard names in `requires`. Built-in guards (`git-clean`, `git-pushed`, `deps-fresh`) are registered by `admin_lib/guards.py`; user-defined ones live in `[guards.<name>]` tables. The full design and rejected alternatives live in [ADR-0005](docs/adr/0005-guards-and-requires.md).

`deps-fresh` is special: besides being referenceable in `requires`, it also runs as a **global preflight** before every command (CLI or TUI) via `run_global_preflight`, with no `requires` annotation needed. It checks a node/bun workspace for `workspace:*` dependencies declared but never linked into `node_modules` — the stale-install class (a fresh git worktree never installed, or a workspace dep added without re-running install) that otherwise surfaces as an opaque bundler "failed to resolve import". It's a no-op pass on non-node projects, and fail-open (a bug or unusual manifest degrades to a pass, never a crash/block), so it's safe to run everywhere. The control/diagnostic verbs `logs` and `kill` are exempt so a stale install can't lock you out of recovering. Always-on checks live in the `_PREFLIGHT_CHECKS` tuple (the preflight analogue of `GUARDS_BUILTIN`) — see [ADR-0005](docs/adr/0005-guards-and-requires.md) for why deps-fresh is always-on while the git guards stay opt-in.

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

**Pipeline.** Guard handling is concentrated in `admin_lib/guards.py`, which exposes one hook per phase that the pipeline delegates to: `parse_guard_tables` (manifest parse — validate `[guards.<name>]` run/fail, reject built-in collisions), `validate_requires` (resolve — every name in any `requires` list maps to a known guard, built-in or user-defined), `register` (interpreter setup — wire up the shell guards), and `run_requires` (dispatch). The parse/validate hooks take a caller-injected `exc` (the phase's error class — `ManifestError` / `ArchetypeError`, both subclasses of `admin_lib/validate.ValidationError`) and raise it directly — the same exc-injection pattern `admin_lib/validate.py` uses. `admin_lib/validate.py` checks the `requires`/`target_requires` **shape** (list of strings); `admin_lib/archetypes.py` builds `meta["requires"]`/`meta["target_requires"]` (where `None` means inherit the parent default) (storing per-target overrides as `meta["target_requires"]`, where `None` means inherit the parent default); `admin_lib/interp.py` wraps each flat action with a `run_requires([...])` check and resolves per-target requires inside the multi-target closure (`_target_requires[picked]`).

**Runtime.** `run_requires` walks the list fail-fast: invokes each named guard, prints its diagnostic via `err()` on first failure, returns 1. Built-in guards produce dynamic diagnostics ("branch 'X' has unpushed commits — push first: git push origin X"); user-defined shell guards run `/bin/sh -c <run>`, capture stdout+stderr, print the captured output then the static `fail` on non-zero exit. The cwd is inherited (the interpreter cd's to project root at boot). Guards do not compose — a guard cannot reference another `requires`. Adding a new built-in means writing a Python function in `admin_lib/guards.py` and adding its name to `GUARDS_BUILTIN` there (the single source of truth). `run_global_preflight` runs the always-on checks (currently just `deps-fresh`) once per invocation before dispatch — `boot()` calls it before the CLI command runs and the TUI calls it before executing a selection; it's memoized so nested dispatch (sequence steps, `@`-refs) only checks once.
