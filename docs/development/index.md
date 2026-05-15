# Development

Notes for working on `admin-project-tool` itself.

## Layout

- `admin-gen` — entry CLI
- `gen/` — manifest parser, archetype merger, bundler, renderer
- `admin_lib/` — runtime helpers inlined into every generated `./admin`
- `archetypes/` — partial manifests per project type
- `detectors/` — archetype heuristics
- `tests/` — plain `python3 tests/test_X.py` scripts (not pytest)

## Tests

```bash
./admin test
```

Add new test scripts to the `[actions.test]` list in `admin.toml` — there is no glob.

## In-flight work

- [Keybindings handoff](/development/handoff-keybindings) — Phase 6 (recodarr adoption) outstanding.
