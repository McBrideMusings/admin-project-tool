# Development

Notes for working on `admin-project-tool` itself.

## Layout

- `main.go` / `cmd/` — entry CLI (installed to `~/.admin/admin`, on PATH as `admin`); `cmd/` holds the Cobra command wiring for the tool verbs
- `internal/kinds` — the runtime interpreter that reads `admin.toml` and dispatches commands, one factory per action kind
- `internal/manifest`, `internal/archetype`, `internal/validate`, `internal/detect` — manifest parser, archetype merger, schema validation, archetype detectors
- `internal/` — runtime helpers (logging, ports, log tailing, Apple build helpers, etc.), one package per concern
- `archetypes/` — partial manifests per project type, authored in TOML with Lua or shell bodies
- `tests/` — shell test scripts (`test_install.sh`, `test_uninstall.sh`); Go tests live beside the code as `*_test.go`

## Tests

```bash
admin test
```

`admin test` runs `go test ./...` plus the shell suite (`tests/test_install.sh`, `tests/test_uninstall.sh`), wired up in `[actions.test]` in `admin.toml`.

## In-flight work

- [Keybindings handoff](/development/handoff-keybindings) — Phase 6 (recodarr adoption) outstanding.
