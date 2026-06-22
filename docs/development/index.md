# Development

Notes for working on `admin-project-tool` itself.

## Layout

- `admin-run` — entry CLI (installed to `~/.admin/admin`, on PATH as `admin`)
- `admin_lib/interp.py` — the runtime interpreter that reads `admin.toml` and dispatches commands
- `gen/` — manifest parser, archetype merger, fragments, compile (zipapp builder), detectors
- `admin_lib/` — runtime helpers (logging, ports, log tailing, Apple build helpers, etc.)
- `archetypes/` — partial manifests per project type
- `detectors/` — archetype heuristics
- `tests/` — plain `python3 tests/test_X.py` scripts (not pytest)

## Tests

```bash
admin test
```

Add new test scripts to the `[actions.test]` list in `admin.toml` — there is no glob.

## In-flight work

- [Keybindings handoff](/development/handoff-keybindings) — Phase 6 (recodarr adoption) outstanding.
