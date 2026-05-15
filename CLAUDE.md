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
3. **Stdlib only (with one narrow carve-out).** No third-party deps in the generator OR the generated script by default. The only exception is try/except-guarded optional imports that degrade gracefully when absent (today: `Pillow` + `cairosvg` in `admin_lib/icons.py`). New optional deps must clear the same bar. See [ADR-0001](docs/adr/0001-stdlib-only.md). Manifest parses via `tomllib`; writing TOML is hand-rolled.
4. **Zero contributor install.** Generated `./admin` is fully self-contained — contributors `git clone && ./admin`.
5. **Idempotent regeneration.** `admin-gen --regenerate` must produce byte-identical output for the same inputs.

## Hard rules (from past pain)

- **Never scaffold `admin_inline.py`.** Project-specific Python belongs upstream in `admin_lib/` or `archetypes/`. A per-project inline file is a regression vector.
- **`from __future__` imports must be hoisted by the bundler.** Mid-file `__future__` imports are a `SyntaxError` in the bundled output. The bundler dedupes them into a single header line — see `gen/render.py`.
- **Reject `from . import module`** in archetype/lib code. The bundler flattens namespaces; the module object doesn't survive. Bundler raises `BundlerError` at gen time.

## Testing

Tests currently run as plain `python3 tests/test_X.py` scripts with `__main__` blocks. This is an accident, not a decision — see issue [#367](https://github.com/McBrideMusings/admin-project-tool/issues/367) for the pending migration to pytest. Until that lands: new tests follow the existing manual-loop pattern and must be added by hand to `[actions.test]` in `admin.toml` (no glob).

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
