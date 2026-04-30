# admin-project-tool: Manifest-Driven, Archetype-Composable

## Context

A survey of 16 committed `admin` scripts across `~/Projects` revealed **heavy overlap** — nearly every project has `build / dev / deploy / test / clean`, plus a common tail of `reload / logs / open / uninstall`. One-off commands are rare and always reducible to "name + shell string."

Projects cluster into ~5 archetypes: **docker-to-unraid**, **stash-plugin**, **apple/swift**, **unraid-plugin**, **simple-cli** (Hugo, Go CLI, Firefox extension). Some scripts (`open-assistant`, `apple-coding-tool`) are archetype combinations (docker-unraid + apple) — expressed cleanly by listing multiple archetypes in `admin.toml`.

**The goal:** make `./admin` a reliably-reproducible artifact generated from a short, human-editable `admin.toml` manifest plus a library of composable archetypes. Regeneration is idempotent. Hand-editing is unnecessary (but still allowed via the `[inline]` escape hatch).

## Design Principles

1. **Manifest is truth.** `admin.toml` (committed) fully describes the admin script. `./admin` is a generated bundle from `admin.toml` + archetypes + `admin_lib`.
2. **Archetypes are mixins, not monoliths.** A manifest can list multiple archetypes; the generator merges their command sets. This kills the "one template" bottleneck that forced `open-assistant` and `cctool` to be hand-maintained.
3. **Strict regeneration, with an escape hatch.** `admin regenerate` is idempotent. Hand-editing `./admin` remains legal but a drift audit will warn. The expected loop is: *edit manifest → regenerate → commit both*.
4. **Zero contributor install.** Generated `./admin` is still a self-contained, bundled Python 3.11+ script. Contributors `git clone && ./admin`. Only the project owner needs `admin-project-tool` installed.
5. **Stdlib only.** Manifest parses via stdlib `tomllib` (3.11+). No third-party deps in generated script or generator.
6. **Composability over expressiveness.** The manifest is a short, flat declaration — not a DSL. If a project needs real logic, it drops a custom command as a shell string or, rarely, a small Python snippet the generator inlines verbatim.

## Manifest Format (`admin.toml`)

Lives at project root, committed alongside `./admin`.

```toml
# admin.toml — generates ./admin via `admin regenerate`
project_name = "Portfolio"
python_min   = "3.11"

archetypes = ["hugo"]          # zero or more; merged in listed order

[urls]
dev  = "http://localhost:1313"
prod = "https://pierce.dev"

[targets]                       # optional; for multi-target archetypes (apple mac|ios, etc.)
# default = "mac"
# allowed = ["mac", "ios"]

[commands]                      # override or extend archetype-provided commands
# Override: archetype provided `build`; we replace the shell string.
# build = { run = "hugo --minify", desc = "Production build" }

[commands.custom]               # purely project-specific, not from any archetype
# warm-cache = { run = "./scripts/warm.sh", desc = "Warm CDN cache" }

[inline]                        # escape hatch: raw Python blocks inlined verbatim
# Rare. Use only when a shell string genuinely can't express the logic.
# file = "admin_inline.py"

order = ["build", "dev", "deploy", "", "test", "clean"]
# Display order for the TUI menu and --help. "" (or "---") inserts a blank
# spacer row — useful for grouping primary commands (build/dev/deploy) away
# from secondary ones (test/clean/icons). Commands omitted from `order`
# auto-append after the listed ones in merge order. If `order` is not set
# on the manifest, the last archetype's declared `order` is used.
```

Every key is optional except `project_name` and `archetypes` (may be empty list).

## Archetype Catalog (initial set)

Each archetype is a single file in `~/Projects/admin-project-tool/archetypes/` that declares its commands as shell-string templates plus metadata. Archetypes do **not** generate code directly — they contribute a partial manifest that the generator merges.

Initial archetypes (derived 1:1 from the survey clusters):

- **`hugo`** — `build: hugo`, `dev: hugo server -D`, `clean: rm -rf public resources`, default URL `http://localhost:1313`
- **`go-cli`** — `build: go build ./...`, `test: go test ./...`, `vet`, `fmt`, `deploy: go install ./...`
- **`docker-unraid`** — build image on remote, deploy to Unraid, `logs`, `open`, `reload`, SSH target from config
- **`stash-plugin`** — rollup build + sync to Stash plugin dir, dev/deploy variants
- **`apple`** — xcodebuild, dev_loop_macos/ios, device install, DerivedData clean, `--log LEVEL`, multi-target (mac|ios)
- **`unraid-plugin`** — push source to Unraid USB, register/uninstall
- **`firefox-extension`** — web-ext dev, package `.xpi`, lint
- **`simple`** — minimal `build / test / clean` shell strings (fallback)

Composition works: `archetypes = ["docker-unraid", "apple"]` produces the `open-assistant` / `cctool` shape without hand editing.

**Conflict resolution:** when two archetypes define the same command name, the later one wins; the manifest's own `[commands]` table wins over all archetypes. `admin regenerate` prints a merge report.

**Display order:** archetypes may declare `"order": [...]` (with `""` entries for blank spacers) to group their commands in the TUI and `--help`. Manifest `order` wins; otherwise the last archetype's `order` is used; otherwise commands render in merge order. Example from the `apple` archetype: `["build", "dev", "deploy", "", "test", "clean", "icons", "reload"]` — the spacer signals the primary/secondary split.

## Authoring Archetypes

### Actions are declared, not scripted

The central rule: **archetype actions must be expressible as a declarative kind + run string, not as inline code.**

Each `kind` is a renderer that knows how to wire up a specific operation. The `run` field is a short, opaque key the renderer interprets — a function name, a script name, a URL, a destination path. No Python syntax should appear in an action definition.

**Good — declarative:**
```python
"actions": {
    "build-ios":  {"kind": "apple", "run": "build_ios",  "desc": "build iOS"},
    "npm-build":  {"kind": "npm",   "run": "build",      "desc": "run npm build"},
    "sync-dist":  {"kind": "sync",  "run": "${DEPLOY_TARGET}/app", "desc": "sync dist/"},
    "reload":     {"kind": "http",  "run": "${APP_URL}/reload", "method": "POST"},
    "full-build": {"kind": "sequence", "steps": ["npm-build", "sync-dist", "reload"]},
}
```

**Bad — inline code leaking into declarations:**
```python
"actions": {
    "build-ios": {
        "kind": "python",
        "run": "build_ios(globals().get('_APPLE_CONFIG') or {})",  # ← NO
    },
}
```

The second form is not declarative — it embeds a call expression that only works because of implicit scope. It can't be composed, validated, or documented without reading the generated code.

### When to add a new kind

If an action needs to call a library function with a config dict, and no existing kind covers it, **add a new kind**. The pattern is always the same:

1. Add `render_<kind>(cmd)` to `gen/fragments.py` — it reads `cmd.run` as the function name (or other opaque token) and emits the call.
2. Register the kind in `_RENDERERS`.
3. Add validation in `_to_command` if the kind requires specific fields.
4. Use the new kind in the archetype's `actions` dict.

Example: `kind = "apple"` renders `<run>(globals().get("_APPLE_CONFIG") or {})`. Any new config-backed function family (server, docker, etc.) follows the same pattern — add `kind = "server"` backed by `_SERVER_CONFIG`, etc.

### `kind = "python"` is for top-level command logic only

Complex top-level commands — ones that parse args, pick targets, branch on config presence, and dispatch to multiple paths — are legitimately expressed as `kind = "python"`. The apple archetype's `build`, `dev`, and `deploy` are the canonical examples: they handle `mac | ios | server` target selection, `--force`, `--log`, `--clean` flags, and multi-step orchestration that can't be expressed in a single function call.

`kind = "python"` is **not** appropriate for actions that are a single function call with a config dict. Those belong in a typed kind.

### Actions vs commands

Actions (`archetype["actions"]`) are building blocks: hidden from the TUI and `--help`, callable only by sequences and `@ref` targets. Commands (`archetype["commands"]`) are the user-facing surface.

A well-authored archetype exposes a small set of composed top-level commands and a richer set of per-target actions for manifests to compose freely. The apple archetype's `build-ios`, `dev-ios`, `dev-device`, `deploy-ios`, and `clean-ios` actions let any manifest build a `kind = "multi-target"` command with iOS as one target — without touching Python.

## Detectors (starter generators)

`~/Projects/admin-project-tool/detectors/` — one file per stack, each a small function:

```python
def detect(project_dir: Path) -> dict | None:
    # Return a partial manifest (archetype + URL hints) or None.
```

Detectors run on a fresh project during `admin new` *without* a manifest. They produce a **starter `admin.toml`** which the user then reviews and commits. Detectors never run on projects that already have `admin.toml` — the manifest is the source of truth from that moment forward.

Initial detectors mirror the archetypes: hugo (`config.yaml` + `themes/`), docker-unraid (`Dockerfile` + existing remote-deploy markers), apple (`.xcodeproj`/`.xcworkspace`), stash-plugin (rollup config + `stash` in package.json), go-cli (`go.mod`, no Dockerfile), firefox-extension (`manifest.json` with `browser_specific_settings`), unraid-plugin (`plg` files / unraid markers). Fallback: `simple`.

Adding a new stack = one detector file + one archetype file.

## Generator Architecture

Top-level flow in `admin-gen`:

1. **Bootstrap** — if no `admin.toml`, run detectors → write starter manifest → prompt user to review.
2. **Load** — parse `admin.toml` with stdlib `tomllib`.
3. **Resolve archetypes** — load each listed archetype file, merge their command dicts in order.
4. **Apply manifest overrides** — manifest `[commands]` and `[commands.custom]` win over archetypes.
5. **Render** — emit the Python admin script as: shebang → header → inlined `admin_lib` → generated command functions (one per merged command, each calling `run_cmd(shell_string)`) → `set_urls()` call → `boot()`.
6. **Write** `./admin` (executable) + `tmp/.gitignore`.

Rendering uses a small set of **code fragments** (not full templates) — one per command *kind* (shell-string, multi-target, apple dev loop, docker reload loop). Each archetype declares which fragment backs each of its commands. This is how archetype composition actually becomes code: the generator stitches fragments together, not whole files.

## Regeneration & Drift Audit

- `admin new` — first-time bootstrap (detect → write manifest → generate).
- `admin regenerate` — re-render `./admin` from `admin.toml`. Idempotent. Standard loop for adding/editing commands. Accepts `--force-dirty` to allow generation from a dirty `admin-project-tool` tree.
- `admin audit` — regenerates in-memory and diffs against `./admin`. Reports: (a) manifest drift (script differs from what manifest would produce), (b) archetype drift (archetype catalog changed since last regen — `admin_lib` hash differs), (c) stale `admin_lib`. Non-zero exit if any drift.
- `/admin` slash command runs the audit and surfaces drift to the user before asking whether to regenerate.

Hand-editing `./admin` is still allowed, but the audit will tell you about it. The expected escape hatch is the manifest's `[inline]` block (a small Python file concatenated into the generated script at a marked insertion point); this keeps hand-written logic *referenced from the manifest* so regeneration preserves it.

## Python 3.11+ Requirement

- Generated `./admin` starts with `#!/usr/bin/env python3` and a runtime guard: `if sys.version_info < (3, 11): sys.exit("admin requires Python 3.11+")`.
- `manifest.python_min` is written into the guard (allowing a project to pin higher if it uses a newer feature).
- Manifest parsing uses stdlib `tomllib` — no third-party TOML dependency.
- `install.sh` verifies the host has Python 3.11+ and refuses to install otherwise.

## Migrating Existing Projects

`admin-gen --from-existing` reads a current `./admin`, detects which commands exist, and reverse-engineers a starter `admin.toml`. Then: regenerate → diff → commit. Generated scripts are typically 60–80% shorter than hand-maintained originals because boilerplate moves into archetypes.

## Verification

End-to-end test plan, performed on a scratch clone of `admin-project-tool`:

1. **Unit: manifest parse + merge**
   - Write fixture `admin.toml`s (empty, single archetype, multi-archetype, with overrides, with custom commands, with inline).
   - Assert merged command set matches expected for each.
2. **Unit: archetype catalog**
   - For each of the 8 archetypes, assert its declared commands render to valid Python and the shell strings are non-empty.
3. **Integration: round-trip on `~/Projects/portfolio`** (Hugo — the project that started this)
   - `admin new` → produces `admin.toml` with `archetypes = ["hugo"]` and a generated `./admin`.
   - `./admin dev` starts `hugo server -D`; `./admin` shows TUI menu with URL liveness.
   - `./admin open` opens the dev URL.
4. **Integration: archetype composition on a fake `cctool`-like fixture**
   - Hand-write `admin.toml` with `archetypes = ["docker-unraid", "apple"]`.
   - Regenerate; assert script has `build`, `dev`, `deploy`, `logs`, `open`, `reload`, `test`, `clean`, and multi-target handling for `mac | ios | server`.
5. **Integration: regeneration idempotency**
   - `admin regenerate` twice in a row; `git diff` must be empty after the second run.
6. **Integration: drift audit**
   - Hand-edit a line in generated `./admin`; run `--audit`; assert non-zero exit and a diff in the report.
   - Revert; assert `--audit` passes.
7. **Integration: migration of one real project**
   - Run `admin-gen --from-existing` against `~/Projects/firefox-tab-bsp` (smallest at 680 lines).
   - Inspect generated `admin.toml` for fidelity; regenerate; confirm `./admin dev` still launches Firefox Nightly with auto-reload.
8. **Cross-platform smoke**
   - Manually run generated script on macOS (baseline) and on one Linux host (WSL or remote). `./admin` TUI renders; `./admin build` runs.
   - Python 3.11+ guard: temporarily rename `python3` to a 3.10 binary; assert the script exits cleanly with the guard message.
9. **Claude Code flow**
   - `/admin` on a fresh project: verify the skill phases (detect → confirm → bootstrap → commit prompt) produce `admin.toml` + `./admin` + updated `.gitignore` + `read-logs.md`.
   - `/admin` on an existing project with drifted admin: verify the audit phase surfaces the drift and offers `--from-existing` migration.

## Resolved Design Decisions

### Environment variable interpolation (replaces "global defaults" question)

No global `~/.admin/admin.toml.d/` layer. Cross-project values that vary by host (SSH targets, IP addresses, deploy hosts) and anything secret-adjacent are **never** encoded in `admin.toml`. Instead, the manifest references environment variables and the generator emits code that resolves them at `./admin` runtime.

**Syntax** — `${VAR}` or `${VAR:-default}` inside any string value in `admin.toml`:

```toml
[archetype_config.docker-unraid]
ssh_host = "${UNRAID_HOST}"
ssh_user = "${UNRAID_USER:-root}"

[commands.custom]
deploy-staging = { run = "rsync -a ./dist ${STAGING_HOST}:/srv/app", desc = "Deploy to staging" }

[urls]
prod = "${PROD_URL:-https://example.com}"
```

**Resolution rules:**
- Interpolation happens at **runtime** inside the generated `./admin`, not at generation time. The generated script contains the literal `${VAR}` placeholder and a small resolver that expands it via `os.environ` when the command runs. This keeps `./admin` portable across machines with different env.
- If a referenced variable is unset and has no default, the command fails fast with a clear error (`admin: required env var UNRAID_HOST is not set`) before running anything.
- A new `./admin env` command (provided by `admin_lib.core`) lists every env var the script references and shows which are set/unset on the current machine. This is the discovery mechanism for new contributors.
- The drift audit treats `${VAR}` as a literal — manifest and generated script must agree on the placeholder, and resolution is deferred to runtime.

**Where the env vars themselves live:** out of scope. Users set them via shell rc files, direnv, `.envrc`, or whatever they prefer. `admin.toml` only references them.

### Archetype versioning by source commit (replaces "version archetypes" question)

Archetypes are not semver-versioned. Instead, the generator records the **git commit SHA of the `admin-project-tool` repo** that was used at generation time, and embeds it into both:

1. The header of the generated `./admin` script (`# generated from admin-project-tool @ <sha>`)
2. A new top-level field in `admin.toml`:

   ```toml
   generator_commit = "a1b2c3d4e5f6..."
   ```

**Behavior:**
- `admin regenerate` reads the current `admin-project-tool` HEAD SHA, regenerates, and updates `generator_commit` in the manifest. The user reviews and commits both files together.
- `admin audit` checks: (a) generated script matches what the *current* generator would produce, AND (b) `generator_commit` in the manifest matches the current `admin-project-tool` HEAD. If the SHAs differ, the audit reports it as informational ("regenerator-tool has moved since last regen — run `admin regenerate` to refresh") rather than as drift, unless the regenerated output actually differs.
- This gives a deterministic, auditable provenance trail without inventing a version number scheme. Two projects pinned to the same commit are guaranteed to produce identical scripts from identical manifests.
- The generator refuses to run if `admin-project-tool`'s working tree is dirty (unless `--force-dirty`), so the recorded SHA is always meaningful. A dirty-tree generation records the SHA with a `+dirty` suffix and the audit always flags it.

**Migration safety:** when the user pulls a newer `admin-project-tool` and runs `--regenerate` across projects, the diff in each project's `./admin` is the exact set of changes the catalog introduced. They can review and commit per-project at their own pace. Old projects still on an older `generator_commit` keep working; they just won't pick up new archetype features until regenerated.

### Out of scope

- **Windows CI** — not a priority. Generated script must still be Python-3.11-stdlib-only so it *could* run on Windows, but no automated cross-platform testing.
