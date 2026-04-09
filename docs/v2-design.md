# admin-project-tool v2: Manifest-Driven, Archetype-Composable

## Context

`admin-project-tool` today generates a per-project `./admin` task runner by picking *one* template (basic / apple / docker) and substituting variables, then bundling `admin_lib` inline. In practice:

- A survey of 16 committed `admin` scripts across `~/Projects` shows **heavy overlap** — nearly every project has `build / dev / deploy / test / clean`, plus a common tail of `reload / logs / open / uninstall`. One-off commands are rare and always reducible to "name + shell string."
- Projects cluster into ~5 archetypes: **docker-to-unraid**, **stash-plugin**, **apple/swift**, **unraid-plugin**, **simple-cli** (Hugo, Go CLI, Firefox extension).
- Two scripts (`open-assistant`, `apple-coding-tool`) are **archetype combinations** (docker-unraid + apple). The current single-template model can't express this, so they're hand-maintained monsters (3000+ lines).
- After generation, Claude (and the user) routinely hand-edit `./admin` to add Hugo dev servers, tweak commands, add URLs. This **drifts the script from any notion of "what init-admin would produce,"** so the `/init-admin` audit flow can only do fuzzy checks.
- Contributors to any project must be able to run `./admin` with zero install. Python 3.11+ is the right interpreter choice (preinstalled on macOS/current Linux, stdlib `tomllib`, stable, cross-platform). Go/Deno/Bun/Node/Bash were all considered and ruled out.

**The v2 goal:** make `./admin` a reliably-reproducible artifact generated from a short, human-editable `admin.toml` manifest plus a library of composable archetypes. Regeneration must be idempotent. Hand-editing should become unnecessary, not forbidden.

## Design Principles

1. **Manifest is truth.** `admin.toml` (committed) fully describes the admin script. `./admin` is a generated bundle from `admin.toml` + archetypes + `admin_lib`.
2. **Archetypes are mixins, not monoliths.** A manifest can list multiple archetypes; the generator merges their command sets. This kills the "one template" bottleneck that forced `open-assistant` and `cctool` to be hand-maintained.
3. **Strict regeneration, with an escape hatch.** `init-admin --regenerate` is idempotent. Hand-editing `./admin` remains legal but a drift audit will warn. The expected loop is: *edit manifest → regenerate → commit both*.
4. **Zero contributor install.** Generated `./admin` is still a self-contained, bundled Python 3.11+ script. Contributors `git clone && ./admin`. Only the project owner needs `admin-project-tool` installed.
5. **Stdlib only.** Manifest parses via stdlib `tomllib` (3.11+). No third-party deps in generated script or generator.
6. **Composability over expressiveness.** The manifest is a short, flat declaration — not a DSL. If a project needs real logic, it drops a custom command as a shell string or, rarely, a small Python snippet the generator inlines verbatim.

## Manifest Format (`admin.toml`)

Lives at project root, committed alongside `./admin`.

```toml
# admin.toml — generates ./admin via `~/.admin/init-admin --regenerate`
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
- **`simple`** — minimal `build / test / clean` shell strings (v1 `basic.py` successor)

Composition works: `archetypes = ["docker-unraid", "apple"]` produces the `open-assistant` / `cctool` shape without hand editing.

**Conflict resolution:** when two archetypes define the same command name, the later one wins; the manifest's own `[commands]` table wins over all archetypes. `init-admin --regenerate` prints a merge report.

## Detectors (starter generators)

`~/Projects/admin-project-tool/detectors/` — one file per stack, each a small function:

```python
def detect(project_dir: Path) -> dict | None:
    # Return a partial manifest (archetype + URL hints) or None.
```

Detectors run on a fresh project during `init-admin` *without* a manifest. They produce a **starter `admin.toml`** which the user then reviews and commits. Detectors never run on projects that already have `admin.toml` — the manifest is the source of truth from that moment forward.

Initial detectors mirror the archetypes: hugo (`config.yaml` + `themes/`), docker-unraid (`Dockerfile` + existing remote-deploy markers), apple (`.xcodeproj`/`.xcworkspace`), stash-plugin (rollup config + `stash` in package.json), go-cli (`go.mod`, no Dockerfile), firefox-extension (`manifest.json` with `browser_specific_settings`), unraid-plugin (`plg` files / unraid markers). Fallback: `simple`.

Adding a new stack in v2 = one detector file + one archetype file. No new full template.

## Generator Architecture

New top-level flow in `init-admin`:

1. **Bootstrap** — if no `admin.toml`, run detectors → write starter manifest → prompt user to review.
2. **Load** — parse `admin.toml` with stdlib `tomllib`.
3. **Resolve archetypes** — load each listed archetype file, merge their command dicts in order.
4. **Apply manifest overrides** — manifest `[commands]` and `[commands.custom]` win over archetypes.
5. **Render** — emit the Python admin script as: shebang → header → inlined `admin_lib` (via existing `bundle` logic) → generated command functions (one per merged command, each calling `run_cmd(shell_string)`) → `set_urls()` call → `boot()`.
6. **Write** `./admin` (executable) + `tmp/.gitignore`.

Rendering uses a small set of **code fragments** (not full templates) — one per command *kind* (shell-string, multi-target, apple dev loop, docker reload loop). Each archetype declares which fragment backs each of its commands. This is how archetype composition actually becomes code: the generator stitches fragments together, not whole files.

## Regeneration & Drift Audit

- `~/.admin/init-admin` — first-time bootstrap (detect → write manifest → generate).
- `~/.admin/init-admin --regenerate` — re-render `./admin` from `admin.toml`. Idempotent. Standard loop for adding/editing commands.
- `~/.admin/init-admin --audit` — regenerates in-memory and diffs against `./admin`. Reports: (a) manifest drift (script differs from what manifest would produce), (b) archetype drift (archetype catalog changed since last regen — `admin_lib` hash differs), (c) stale `admin_lib`. Non-zero exit if any drift.
- `/init-admin` slash command phase 1 now runs the audit and surfaces drift to the user before asking whether to regenerate.

Hand-editing `./admin` is still allowed, but the audit will tell you about it. The expected escape hatch is the manifest's `[inline]` block (a small Python file concatenated into the generated script at a marked insertion point); this keeps hand-written logic *referenced from the manifest* so regeneration preserves it.

## Python 3.11+ Requirement

- Generated `./admin` starts with `#!/usr/bin/env python3` and a runtime guard: `if sys.version_info < (3, 11): sys.exit("admin requires Python 3.11+")`.
- `manifest.python_min` is written into the guard (allowing a project to pin higher if it uses a newer feature).
- Manifest parsing uses stdlib `tomllib` — no third-party TOML dependency.
- `install.sh` verifies the host has Python 3.11+ and refuses to install otherwise.

## Migration of Existing 16 Admin Scripts

Not part of v2 itself, but v2 must make migration trivial:

1. `init-admin --from-existing` — read a current bundled `./admin`, detect which commands exist, reverse-engineer a starter `admin.toml`. Runs per project, interactive.
2. Regenerate → diff → commit. The generated script will be shorter than the originals (the monsters shrink by 60–80% because boilerplate moves into archetypes).

Explicitly out of scope for v2 v1: automated migration of every project. Manual, one at a time.

## Files To Create / Modify

All paths relative to `~/Projects/admin-project-tool/`.

**New directories:**
- `archetypes/` — one `.py` per archetype (8 initial: hugo, go-cli, docker-unraid, stash-plugin, apple, unraid-plugin, firefox-extension, simple)
- `detectors/` — one `.py` per stack, mirroring archetypes
- `fragments/` — code fragments backing each command *kind* (shell, multi-target, apple-dev-loop, docker-reload-loop). Replaces/supersedes current `templates/fragments/`.

**Rewritten:**
- `init-admin` — gains manifest load/resolve/render pipeline; keeps existing detect + interactive confirm flow for first-time bootstrap only. Add `--regenerate`, `--audit`, `--from-existing` subcommands. Gate on Python 3.11+.
- `bundle` — still inlines `admin_lib`, but is now called from inside `init-admin`'s render step rather than as a separate user-facing tool. Keep it callable standalone for manual re-bundles of legacy scripts.
- `install.sh` — Python 3.11+ check, deploys new `archetypes/` and `detectors/` directories.
- `templates/` — **removed.** Replaced entirely by archetypes + fragments. Keep a git tag on the last v1 commit for reference.

**Unchanged (reused as-is):**
- `admin_lib/core.py` — `command` decorator, `add_spacer`, `run_cmd`, `set_urls`, `boot`, TUI, signal handling. The generator emits code that calls into these exactly as today. Command display order follows registration order (which the generator drives from `admin.toml`'s `order` list); spacers are blank rows inserted via `add_spacer()`.
- `admin_lib/apple.py`, `docker.py`, `remote.py`, `unraid.py`, `cache.py`, `icons.py`, `logs.py` — archetypes reference these from their fragments.

**Claude Code integration:**
- `~/.claude/skills/init-admin/SKILL.md` — update phases to describe manifest-first flow: bootstrap writes `admin.toml` *and* `./admin`, subsequent runs are `--regenerate` or `--audit`.
- `~/.claude/CLAUDE.md` — `admin convention` section: replace "bundled single file" framing with "generated from `admin.toml`; regenerate with `~/.admin/init-admin --regenerate`; hand edits discouraged; use `[inline]` escape hatch for project-specific Python."

## Verification

End-to-end test plan, performed on a scratch clone of `admin-project-tool`:

1. **Unit: manifest parse + merge**
   - Write fixture `admin.toml`s (empty, single archetype, multi-archetype, with overrides, with custom commands, with inline).
   - Assert merged command set matches expected for each.
2. **Unit: archetype catalog**
   - For each of the 8 archetypes, assert its declared commands render to valid Python and the shell strings are non-empty.
3. **Integration: round-trip on `~/Projects/portfolio`** (Hugo — the project that started this)
   - `init-admin .` → produces `admin.toml` with `archetypes = ["hugo"]` and a generated `./admin`.
   - `./admin dev` starts `hugo server -D`; `./admin` shows TUI menu with URL liveness.
   - `./admin open` opens the dev URL.
4. **Integration: archetype composition on a fake `cctool`-like fixture**
   - Hand-write `admin.toml` with `archetypes = ["docker-unraid", "apple"]`.
   - Regenerate; assert script has `build`, `dev`, `deploy`, `logs`, `open`, `reload`, `test`, `clean`, and multi-target handling for `mac | ios | server`.
5. **Integration: regeneration idempotency**
   - `init-admin --regenerate` twice in a row; `git diff` must be empty after the second run.
6. **Integration: drift audit**
   - Hand-edit a line in generated `./admin`; run `--audit`; assert non-zero exit and a diff in the report.
   - Revert; assert `--audit` passes.
7. **Integration: migration of one real project**
   - Run `init-admin --from-existing` against `~/Projects/firefox-tab-bsp` (smallest at 680 lines).
   - Inspect generated `admin.toml` for fidelity; regenerate; confirm `./admin dev` still launches Firefox Nightly with auto-reload.
8. **Cross-platform smoke**
   - Manually run generated script on macOS (baseline) and on one Linux host (WSL or remote). `./admin` TUI renders; `./admin build` runs.
   - Python 3.11+ guard: temporarily rename `python3` to a 3.10 binary; assert the script exits cleanly with the guard message.
9. **Claude Code flow**
   - `/init-admin` on a fresh project: verify the skill phases (detect → confirm → bootstrap → commit prompt) produce `admin.toml` + `./admin` + updated `.gitignore` + `read-logs.md`.
   - `/init-admin` on a v1 project with bundled-but-drifted admin: verify the audit phase surfaces the drift and offers `--from-existing` migration.

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

**Where the env vars themselves live:** out of scope for v2. Users set them via shell rc files, direnv, `.envrc`, or whatever they prefer. `admin.toml` only references them.

### Archetype versioning by source commit (replaces "version archetypes" question)

Archetypes are not semver-versioned. Instead, the generator records the **git commit SHA of the `admin-project-tool` repo** that was used at generation time, and embeds it into both:

1. The header of the generated `./admin` script (`# generated from admin-project-tool @ <sha>`)
2. A new top-level field in `admin.toml`:

   ```toml
   generator_commit = "a1b2c3d4e5f6..."
   ```

**Behavior:**
- `init-admin --regenerate` reads the current `admin-project-tool` HEAD SHA, regenerates, and updates `generator_commit` in the manifest. The user reviews and commits both files together.
- `init-admin --audit` checks: (a) generated script matches what the *current* generator would produce, AND (b) `generator_commit` in the manifest matches the current `admin-project-tool` HEAD. If the SHAs differ, the audit reports it as informational ("regenerator-tool has moved since last regen — run `--regenerate` to refresh") rather than as drift, unless the regenerated output actually differs.
- This gives a deterministic, auditable provenance trail without inventing a version number scheme. Two projects pinned to the same commit are guaranteed to produce identical scripts from identical manifests.
- The generator refuses to run if `admin-project-tool`'s working tree is dirty (unless `--force-dirty`), so the recorded SHA is always meaningful. A dirty-tree generation records the SHA with a `+dirty` suffix and the audit always flags it.

**Migration safety:** when the user pulls a newer `admin-project-tool` and runs `--regenerate` across projects, the diff in each project's `./admin` is the exact set of changes the catalog introduced. They can review and commit per-project at their own pace. Old projects still on an older `generator_commit` keep working; they just won't pick up new archetype features until regenerated.

### Out of scope for v2

- **Windows CI** — not a priority. Generated script must still be Python-3.11-stdlib-only so it *could* run on Windows, but no automated cross-platform testing.
