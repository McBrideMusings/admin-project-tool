# admin-project-tool

Runs per-project tasks from a short `admin.toml` manifest plus a library of composable archetypes. The installed tool **interprets** `admin.toml` at runtime (ADR-0006) — a project commits only the manifest; the `admin` command (installed on PATH at `~/.admin/admin`) finds it by walking up from `$PWD`. `admin compile` packages a standalone `./admin` (a zipapp) for the rare case the tool isn't installed. See `docs/architecture/design.md` for the full architecture.

## `admin.toml` is NOT committed — this overrides the README

**Do not commit `admin.toml` to any repository except this one. Do not suggest committing it. Do not "fix" a project whose `admin.toml` is untracked — that is the intended state.**

`README.md` says a project commits only `admin.toml`. That describes how this tool will work **once it is public**, and it is deliberately left accurate for that future. It is not how things work today.

Today the tool is private. An `admin.toml` sitting in a public repository advertises the tool, its verbs, and its manifest format, and the tool is not ready to be advertised. So `admin.toml` is caught by a **global** ignore rule on the developer's machine (`~/.config/git/ignore`), which applies to every repository at once rather than per-project. This repo used to be the single exception and track its own, but a pre-commit guard (added in 214ee57) now blocks every commit here while `admin.toml` sits in the index — so this repo's own manifest is untracked like every other project's, and nothing about it travels through git.

Consequences that look like bugs and are not:

- A fresh `git worktree` has no `admin.toml`, so every `admin` command is dead inside it. That is expected. Seed the file into the worktree — `admin worktree` (`admin_lib/wtverbs.py`) is where that belongs — rather than committing it.
- No branch can carry a change to `admin.toml`, so config edits happen in the primary checkout only.
- A fresh clone on a new machine has no task runner until the manifest is copied over by hand.

When the tool goes public this section is what gets deleted, and the README becomes true as written.

### Secrets in manifests

Some `admin.toml` files embed secrets today, which is a second and independent reason not to commit them — and a design gap in the tool rather than a rule for humans to remember. Environment variables are the current workaround and have not been evaluated against the alternatives. Before any manifest is committed anywhere, it gets audited for secrets. See the open design work on giving the tool a real answer for this.

## What this repo is

- **`admin-run`** — the interpreter CLI (installed as `~/.admin/admin`). Reads a project's `admin.toml`, merges archetypes, registers live command/action callables, and dispatches. Tool verbs: `new`, `check`, `compile`, `branch`, `worktree`.
- **`admin_lib/`** — runtime helpers (logging, port management, log tailing, Apple build helpers, etc.) imported as a real package. `admin_lib/interp.py` is the interpreter: one closure factory per action kind. It also holds the manifest frontend: `manifest.py` (parser), `archetypes.py` (archetype merger), `kinds.py` (kind metadata + ${VAR} scanner), `validate.py` (command/action schema validation — `ValidationError` umbrella; see [ADR-0008](docs/adr/0008-centralized-schema-validation.md)), `compile.py` (standalone zipapp), `detectors.py` (detector loader). Stdlib only.
- **`archetypes/`** — partial manifests per project type (apple, react-native, hugo, docker, rust-tauri, etc.). Mixins, not monoliths. (The data dir — the merger code is `admin_lib/archetypes.py`.)
- **`detectors/`** — heuristics that suggest archetypes from a project's file layout. (The data dir — the loader is `admin_lib/detectors.py`.)
- **`install.sh`** — builds the Go binary and installs it as `~/.admin/admin`, installs the Python interpreter alongside it as `~/.admin/admin-py`, copies (or links) `admin_lib` + the `archetypes`/`detectors` data dirs, and puts `~/.admin` on PATH so `admin` runs from any project. The binary is recompiled on every run including `--link`, since nothing else recompiles Go.

**`admin` on PATH is the Go binary; `admin-py` is the outgoing Python one.** admin-py exists only for what Go has not absorbed yet — the `new` and `compile` verbs, and six action kinds (`command`, `docker`, `file`, `pick`, `python`, `python-script`) that 10 of 51 local projects still declare. Tracked by `admin-3rgp.54` and `admin-3rgp.55`, which block the deletion ticket `admin-3rgp.30`. Do not add callers of `admin-py`.

## Design principles (don't violate)

1. **Manifest is truth.** `admin.toml` is the spec. The interpreter reads it live; there is no generated artifact to drift. `admin compile` is the only thing that emits a file.
2. **Commands are pure routing.** `[commands.X]` may only contain `desc` + `steps` (list of action names). `kind`, `run`, `targets` belong in `[actions.X]`. The resolver errors loudly otherwise.
3. **Stdlib only (with one narrow carve-out).** No third-party deps in the tool OR the interpreter/compiled output by default. The only exception is try/except-guarded optional imports that degrade gracefully when absent (today: `Pillow` + `cairosvg` in `admin_lib/icons.py`; `tomli` in `admin_lib/manifest.py` and `admin_lib/apple.py` as a Python 3.9/3.10 backport for `tomllib`). New optional deps must clear the same bar. See [ADR-0001](docs/adr/0001-stdlib-only.md). Manifest parses via `tomllib` (or `tomli` on Python 3.9/3.10); writing TOML is hand-rolled.
4. **One engine.** The interpreter (`admin_lib/interp.py`) is the single execution path. `admin compile` packages that same interpreter + `admin_lib` + the resolved manifest into a zipapp — it never re-implements per-kind logic. Adding a kind = one `_kind(...)` entry in the `admin_lib/kinds.KINDS` catalog + one `register_factory(name, ...)` in `interp.py`; the interpreter checks at import that every kind has a factory.
5. **Tool verbs are reserved.** `new`, `check`, `compile`, `branch`, `worktree` share the `admin <word>` namespace with manifest commands; a manifest may not define a command with one of these names (the CLI errors). `branch`/`worktree` take an `<input>` (branch name / issue-PR number / GitHub URL) resolved by `admin_lib/resolve.py`; bare → a TUI, `--prune` → cleanup. See `admin_lib/wtverbs.py`.
6. **The tool defines mechanism; `admin.toml` defines structure.** The tool never assumes a shipping target, a command name, or a menu a project didn't ask for — `build`/`dev`/`deploy`/`test`/`clean`/`logs`/`kill` are conventions with zero reserved semantics. Test for any change: could two projects using this archetype disagree about it? If yes, it's structure and belongs in `admin.toml`, not the tool or the archetype. See [ADR-0019](docs/adr/0019-the-tool-defines-mechanism-admin-toml-defines-structure.md).

## Hard rules (from past pain)

- **Never scaffold `admin_inline.py`.** Project-specific Python belongs upstream in `admin_lib/` or `archetypes/`. A per-project inline file is a regression vector.
- **Reload state lives only in `admin_lib/reload.py`; the subprocess output pump only in `admin_lib/streams.OutputPump`.** Both were once several copies: the `_rebuild_requested` flag + SIGUSR1 handler existed in `docker.py` *and* `apple.py`, and the pump was written out separately in `core.run_cmd` (pty and pipe paths), `streams.stream_subprocess`, `reload.run_with_reload`, and twice inside `keybindings.run_with_keybindings` — five bodies of the same read-a-chunk/format/write-to-terminal/write-to-log loop (issue #475). Nothing broke — one process runs one dev loop — but `run_with_reload` filed under `docker.` was invisible to every non-Docker project that needed exactly it, so projects hand-rolled their own (term grew a whole node supervisor), and the same thing happened to the pump three more times. Before writing a reload flag, a pid-file dance, or a "read the child's stdout into the terminal and the log" loop: import it. Every caller's real differences — pty vs pipe, formatter or not, whether the log gets raw or formatted text, whether a partial line needs flushing on exit — are `OutputPump`'s constructor parameters, not a reason to write a sixth copy. Platform-specific argv building (`simctl spawn … log stream`, `devicectl … --console`, `adb logcat`) stays in the module owning that platform — only the pump is shared.
- **Removing an "unused" import needs a repo-wide grep first.** `admin_lib` modules re-export: `web_dev.py` imported `_wait_or_kill` *from* `docker.py`, so an import genuinely unused *within* `docker.py` was still load-bearing. `tests/test_imports.py` imports every module for exactly this reason — it caught nothing at the time only because it didn't exist yet.
- **A new action kind is one `_kind(...)` entry in `admin_lib/kinds.KINDS` plus a `register_factory(name, ...)` in `admin_lib/interp.py`.** `KINDS` is the single source of truth (`KNOWN_KINDS`/`CONFIGLESS_KINDS`/`KIND_MODULES` are derived views). `register_factory` rejects an undeclared kind, and `interp.py` raises at import if any declared kind has no factory — so the catalog and factories can't drift, no sync test required.

## Testing

Tests run under `pytest` (dev-only dep — `admin_lib/` and the interpreter stay stdlib-only at runtime). New `tests/test_*.py` files are auto-discovered. Shell tests (`tests/test_install.sh`, etc.) are chained after pytest in `[actions.test]`.

**pytest lives in `.venv`, not on any system python.** `python3 -m pytest` fails with `No module named pytest` and it looks like a missing dependency — it isn't:

- Everything: `admin test` (pytest + the shell suite)
- One file: `.venv/bin/pytest tests/test_apple.py`

Only run `pip install -r requirements-dev.txt` when `.venv` is genuinely absent, and install it into the venv.

Tests that exercise full dispatch write a temp `admin.toml` and run it through `admin-run` (see `tests/test_archetypes.py::_run_interpreter`).

## Common workflows

| Goal | Command |
|---|---|
| Validate this repo's `admin.toml` (parse + resolve) | `go run . check .`, or the PATH `admin check .` |
| Install / reinstall to `~/.admin/` | `bash install.sh`, or `admin deploy` to also provision the userscript |
| Run full test suite | `go run . test`, or the PATH `admin test` |
| Build a standalone `./admin` for a project | `admin compile [DIR]` |

Pre-install (or while developing the Python-only half of the tool — `new`, `compile`, and the six action kinds `admin-py` still owns), invoke the interpreter directly with `./admin-run <verb-or-command>` instead of the PATH `admin`. This repo's own `admin.toml` declares `kind = "workflow"` (admin-7wy5), which `admin-py`'s catalog does not know — so `./admin-run <anything>` in *this* repo now fails at manifest parse; validate it with the Go checker or the PATH `admin` instead.

**Never `./admin <cmd>` in this repo.** That name belongs to an `admin compile` zipapp, which embeds a *frozen copy* of the manifest — so it runs whatever every command meant on the day it was built, with no warning that it is doing so. Nothing regenerates it: `admin check` validates and `admin compile` is the only writer. Use the PATH `admin`, or `go run .`, here.

## Log bridge

A project can opt into browser-console-to-admin-log forwarding by adding `[log_bridge] hosts = [...]` to its `admin.toml`. Any subsequent `interactive-shell` action spins up a `ThreadingHTTPServer` on a free port (default `9988-9999`; override per machine with `port_range = [start, end]`), bound to `0.0.0.0` so Tailscale peers can reach it. The userscript at `tampermonkey/log-bridge.user.js` probes the user's hardcoded hosts × ports, picks the admin instance whose `hosts` glob-matches the page, and POSTs batched `console.*` entries to `/log` where they're appended to the same log file the action is already writing. Each entry carries the page's `host` (not the full URL — keeps auth tokens in query strings out of the log); the listener prints one `client connected: <host> (<browser>)` banner per host, then one `<iso-timestamp> [LEVEL] [CLIENT] message` line per entry — the same `[LEVEL] <scope> message` order a server process prints, with the console level mapped onto admin's own level words (`log`/`info`→INFO, `warn`→WARN, `error`→ERROR, `debug`→DEBUG). Those lines reach the terminal through the action's formatter and `frame.write` (wired by `keybindings._install_bridge_sink`), so a browser line and a server line are indistinguishable: same `HH:MM:SS` column, same level colors, footer intact. The timestamp shown is the browser's own, converted to local time (`LogFormatter(reuse_timestamp=True)`) — arrival lags it by up to the userscript's 1s batch.

**An app that prints its own logger prefix into the message is unwrapped.** `17:36:32.366 [DEBUG] [Messaging] msg` arrives as one console argument, so the entry is double-stamped and the browser's level is wrong — a logger that routes debug through `console.log` is reported as `log`. `unwrap_inner_prefix` takes the app's level, discards the duplicate clock (the entry's own ISO stamp is the same instant and has a date), and keeps every tag after it. Only the full `HH:MM:SS[.mmm] [LEVEL]` shape counts, so a message that merely starts with a clock is untouched.

**Ordering.** Each POSTed batch is sorted by entry timestamp (`_in_time_order`), and the userscript flushes its backlog the instant its listener probe resolves instead of waiting out the first interval. Both exist for the same cause: `@match *://*/*` with no `@noframes` means one instance per frame, each buffering from document-start but only flushing after discovery — so a slow frame's backlog carries timestamps older than lines a fast frame already delivered. Neither fixes cross-frame interleaving within the same second; that would need a reorder window, which would delay the server's own output by as much. Discovery files at `/tmp/admin-project-tool/<pid>.json` are debugging aids; dead-PID files are swept on each start. Bind is `0.0.0.0` unconditionally — anyone on the tailnet can POST. `port_range` is per-machine: set it in each environment's `admin.toml` when the default range conflicts (e.g. a container that only exposes `3300-3399`); update `PROBE_PORTS` in the userscript to cover the union of all ranges.

The userscript is project-agnostic — no prod URLs baked in. It decides which pages to forward at runtime: `localhost`/`127.0.0.1`/`0.0.0.0` are always candidates, and any other host is opt-in via a single Tampermonkey menu command ("Log bridge: watch `<host>`", a host-labelled toggle) that stores an auto-derived glob in `GM` storage. The per-project prod hostname lives only in that project's `admin.toml` `[log_bridge].hosts` (advertised via `/health`), which remains the authoritative final match — so a new project needs one menu click, never a script edit.

## Log line rendering

`admin_lib/log_format.py` renders every line of a framed action and of `admin logs`: strip whatever timestamp the source printed, stamp one `HH:MM:SS` column, colorize.

Two colorizers, deliberately different in kind:

- **Severity** (`_LEVEL_COLORS`) is a closed set of four — ERROR red, WARN yellow, INFO cyan, DEBUG/TRACE dim — matched against the whole line, first hit only. These colors mean something, so they are assigned by hand.
- **Scope tags** (`colorize_scopes`) are an open set: `[api]`, `[supabase]`, `[Messaging]`, `[CLIENT]` — every project invents its own and no table can enumerate them. Each gets a color from `crc32(label.lower()) % len(_SCOPE_PALETTE)`, so the color is a pure function of the label: the same tag is the same color in every project, every run, and in a file replayed a year later, with nothing cached anywhere. CRC32, not the builtin `hash()`, which is salted per process and would change color on every run. Level words are skipped so an arbitrary hue never buries ERROR. The 56-color palette excludes near-cyan, near-red and near-yellow (they would read as a level word) and stays mid-brightness so it survives a light *and* a dark terminal. Collisions are real and accepted — 20 tags over 56 colors share a hue about 3 times, which costs less than a tag that changes color between runs. `ADMIN_NO_SCOPE_COLORS=1` turns it off.

The scope regex is narrow on purpose: letter-led, space-free, bracketed. It must not match a JSON array printed into a message (`["https://a.js"]`), an empty `userIds=[]`, or the `[31m` inside an escape sequence the level colorizer already inserted.

**structlog's `ConsoleRenderer` is a second member of the `unwrap_inner_prefix` family** (Go: `internal/logformat.UnpadConsoleRenderer`), handling a different shape of the same double-render problem: an ISO-8601 stamp (already stripped by `StripLogTimestamp`) followed by a level right-padded with spaces (`[info     ]`) and a message padded to a fixed column before the next tag. The padded-level matcher is anchored at position 0 on purpose — that anchor, not a loosened `scopeRe`, is what keeps it from matching a JSON array, an empty `[]`, or a mid-line escape sequence; the scope regex above stays exactly as narrow as this section already describes.

## Pinned footer

A framed action runs inside a DECSTBM scroll region: its output scrolls in the top rows while `admin_lib/frame.py` pins a footer to the bottom — a status row (action label + elapsed time), the absolute log path, then one row per URL. Every row is an OSC 8 hyperlink, so the log path (as `file://`) and the URLs are clickable. This exists because a dev server prints its URLs once and then buries them under thousands of lines; the port you need is the one thing guaranteed to have scrolled away. The log path is pinned for the same reason and is worth having on its own, which is why `footer = true` exists for an action with no URL at all.

**The trade:** lines scrolled out of a *partial* scroll region do not enter the terminal's scrollback. That is acceptable only because a framed action is already writing the same output to `LOG_FILE`, timestamped and ANSI-stripped — history moves from scrollback into a file rather than being lost. `admin logs` reads it back.

```toml
[urls]                                     # the existing project-level table
local = "http://localhost:8787"
lan   = "http://${LAN_IP}:8787"

[actions.dev]
kind = "multi-target"
urls = ["local"]                           # default footer for every target

[actions.dev.cf]
run  = "bun run dev:cf"
urls = ["local", "lan", "http://127.0.0.1:9999"]   # override for this target

[actions.dev.devvit]
run    = "bun run dev:devvit"
urls   = []                                # no URL rows...
footer = false                             # ...and opt out of the footer entirely

[actions.dev.playtest]
run = "bun run dev:playtest"               # nothing local to link, footer defaults on anyway: status rule + log path
```

An entry naming a key of the top-level `[urls]` table resolves to that entry, label and all. Anything else must be a real URL (contain `://`) — `frame.validate_url_refs`, called from `archetypes.resolve`, rejects an entry that is neither, so `admin check` catches `urls = ["local"]` when the table calls that row `dev-cf` instead of painting a footer row that goes nowhere. `${VAR}` is expanded at dispatch time through `resolve_computed` then `resolve_env`, so both derived vars (`${LAN_IP}`) and plain env vars work. A bare-string URL labels itself and prints in one column instead of two. Per-target inheritance works exactly like `requires`: `meta["target_urls"]` holds `None` for "inherit the parent" and a list (including `[]`) for "replace it". `meta["target_footer"]` inherits the same way, with `None`/`True`/`False` — and a `None` that reaches the parent's own `footer` of `None` hands the decision back to the resolved URL list.

A target that delegates (`action = "dev-cf"`) is framed by the *picked target*, not the action it hands off to — the footer goes up before the branch in `_make_multi_target`. `_enter_frame` returns the frame it started (or `None`) and `frame.deactivate(f)` only tears down that exact frame, so an inner action declaring its own `urls` — whose `activate()` is declined because one is already up — cannot strip the outer footer while the outer action is still running.

**Activation.** `footer` defaults to `true`: a framed action raises a footer — the status rule and the log path — whether or not it declares `urls`, since the log path is worth having on its own on any long-running process (it's what you copy to read the run back without cancelling the process still writing to it). `footer = false` is the only way to suppress it, and does so regardless of `urls` — the one thing an empty `urls = []` cannot say once a *parent* target declares its own list. Absent means footer-on; only an explicit `footer = false` (or a `footer = true` you didn't need to write) changes the outcome. This is a behavior change from the tool's earlier default (`resolvedURLCount > 0`, i.e. no footer without at least one URL): every existing framed action with no `urls` and no explicit `footer` newly gains a footer. There is no migration mechanism beyond `footer = false` on the actions that don't want one — a script that scrapes terminal output and assumed no footer needs that flag added by hand. `frame.activate` still declines when stdout is not a terminal (piped output, CI), when `ADMIN_NO_FRAME` is set, or when a frame is already up; one process runs one dev loop, the same assumption `admin_lib/reload.py` makes. It no longer declines on an empty URL list — `interp._enter_frame` (Go: `internal/kinds/frame.go`'s `enterFrame`) is the single owner of that question, and `internal/frame/urls.go`'s `ShouldActivate` is where the default lives.

**Pipeline.** `validate.py` checks the `urls` shape and that `footer` is a real bool (parent and per-target — TOML has no truthy strings, so `footer = "true"` is a typo worth reporting); `archetypes.py` normalises entries to `{label, url}` and builds `meta["urls"]`/`meta["target_urls"]` plus the tri-state `meta["footer"]`/`meta["target_footer"]`; `interp.py` calls `_enter_frame` in the shell, multi-target, and interactive-shell closures and always `frame.deactivate()`s in a `finally`. Child output reaches the terminal through `frame.write`, which repaints the footer after each chunk — so every pump must call it rather than `sys.stdout.write`. In practice this means one thing: every caller (`core.run_cmd`'s pty and pipe paths, `streams.stream_subprocess`, `reload.py`, `keybindings.py`) writes through `streams.OutputPump`, which is the only place that calls `frame.write`. `frame.apply_winsize` sizes a child's pty to the *region*, not the screen; without it a child that positions its own cursor (wrangler, vite, metro) paints over the footer. `logtail.py` is deliberately not wired — `admin logs` reads logs and never runs under a frame.

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

## Detached dispatch

A project can declare a `[terminal]` table (ADR-0020) naming the launcher it wants a detached action dispatched into — whatever multiplexer or terminal app the project actually runs, never one the tool assumes:

```toml
[terminal]
spawn  = 'id=$(herdr pane split --current --direction right --cwd "$ADMIN_CWD" --no-focus | jq -r .result.pane.pane_id) && herdr pane run "$id" "$ADMIN_CMD" >/dev/null && echo "$id"'
attach = 'herdr focus "$ADMIN_HANDLE"'
kill   = 'herdr pane close "$ADMIN_HANDLE"'
status = 'herdr agent get "$ADMIN_HANDLE"'   # optional

[actions.dev]
kind   = "vite-dev"
detach = true
```

`detach = true` on an action (or per multi-target target, inheriting the parent's default the way `requires`/`urls` already do) is what opts it into the launcher, whichever producer dispatched it — a plain-CLI `admin dev` and a dashboard row-enter both consult it and land on the same launcher: that shells `spawn` with `ADMIN_CMD`/`ADMIN_LABEL`/`ADMIN_CWD` set to the re-entry command line, this project's root, and the action's label, plus `ADMIN_ROW_KIND`/`ADMIN_ROW_ID`/`ADMIN_ROW_URL` when the dispatch is row-bound (ADR-0018); `spawn`'s trimmed stdout is the opaque handle recorded in the live-work registry, and `attach`/`kill` are later shelled with `ADMIN_HANDLE` set to it. No `kind` is reserved for this — a kind is just what runs; whether its dispatch goes through the launcher is a per-action declaration, never assumed from the kind (ADR-0019/ADR-0020).

**No `[terminal]` declared, or `detach` absent/false, → run inline** — nothing changes for a project that never opts in. A `[terminal]` declared but a detached action's `spawn` failing degrades to the caller's own default surface with one warning line, the same "the tool must never be why a dev loop doesn't start" rule the guards' preflight follows. `internal/kinds.RunCommand` takes a `*CommandProducer`, but `detach` resolution doesn't branch on it: `internal/kinds.launchCommand` resolves the command's own launcher/`detach` declaration once, up front, for a `nil` producer (the plain CLI) exactly as for a non-nil one (the dashboard, which cannot host the run in its own process) — either dispatches to the launcher, or falls back to the caller's own default surface: running inline for the CLI, `Launch` (the hosted pty) for the dashboard. A process already running inside a launched dispatch (`ADMIN_REENTRY`, set by the `__run-action`/`__run-command` re-entry verbs) always runs inline regardless of the producer, so nothing reached from inside an already-launched run — a step, a multi-target target one level down — can reach a second launcher.

## Row-scoped key bindings

A project can bind keys to commands at the row level by declaring `[keys.<row-kind>]` tables mapping keys to command names — one table per bindable row kind (`bead`, `issue`, `branch`, `pr`, `commit`, `stash`). A bound key invokes the named command with the row's data passed as environment variables (`ADMIN_ROW_KIND`, `ADMIN_ROW_ID`, `ADMIN_ROW_URL`); the command's `desc` becomes the key's label in the row footer, replacing the built-in binding. This repo dogfoods the feature for implementing work:

```toml
[commands.implement]
desc  = "Dispatch an agent over the selected bead(s) (ADR-0018 dogfood)"
steps = ["implement"]

[actions.implement]
kind  = "workflow"
over  = "selection"
agent = "claude"

[keys.bead]
enter = "implement"
```

The resulting Beads footer shows `↵ Dispatch an agent over the selected bead(s) (ADR-0018 dogfood)` — the command's description auto-fills the key label so the footer advertises what the binding does.

Three validation rules govern bindings (`internal/validate/keys.go`, `RowKeys`): reserved keys are rejected (`keymap.Reserved` — q, ctrl+c, esc, tab, shift+tab, the arrows, j, k, 1-4, /); the Work tab's own row kinds (`command`, `commandTarget`, `worker` — `keymap.NotBindable`) are not bindable and get a dedicated error naming them; shadowing a built-in is allowed and reported — `admin check` prints e.g. `keys.bead.d shadows the bead detail overlay` and still exits 0 (shadows come back on a separate return value, never in the error).

Dispatch (`internal/tui/rowkeys.go`, `rowKeyDispatch`) is consulted before the built-in switch in `Model.handleKey`; a miss (no table, no row, unbindable kind, unbound key) returns ok=false and the built-in switch runs exactly as today; a hit dispatches the named command through `dispatchCommandCmd` — the same path a Commands-tab row uses — with the row's environment variables; if that command already has a live non-worker entry the dashboard attaches to it rather than re-running it (admin-c2zl).

A binding resolving to a single-step `kind="workflow"` action with `over = "selection"` dispatches over the tracker selection through the same picker/confirm overlay `space`-then-`w` uses when the ACTIVE pane's selection is nonempty, and immediately over the cursor row alone (no overlay, outcome on the status line, missing agent reported as a red status line) when it is empty. Every other command shape acts on the cursor row alone however many rows are selected — the environment variables carry one row and have no plural form.

**Pipeline.** `manifest.Keys` (raw top-level `[keys]` table, `internal/manifest/manifest.go:122`, unvalidated at parse) → `validate.RowKeys` against the manifest's declared command names (called by `cmd/check.go`'s `checkRowKeys` and by the TUI's `buildRowKeys`) → `internal/keymap` holding the built-in labels keyed by row kind, the leaf package both `validate` and `tui` import → `internal/tui/rowkeys.go` for the lookup and dispatch, with `Model.keymap()`/`rowPairs` (`internal/tui/view.go:675`) merging bindings into the footer: a bound key that shadows a built-in replaces that pair's label in place, an unshadowing one is appended after the built-ins. `[keys]` is the one raw archetype table merged per key rather than whole-value (`internal/archetype/merge.go`'s `mergeRowKeys`).

See [ADR-0018](docs/adr/0018-row-scoped-key-bindings.md) for the rejected alternatives.

## Tampermonkey provisioning

`admin_lib/tampermonkey.py` installs `tampermonkey/log-bridge.user.js` into Tampermonkey (5.5+) on every profile of Arc/Chrome/Brave by writing the extension's managed-storage policy (`jsonImport`, a `data:` URL carrying TM's export JSON plus TM's structural hash) as a forced preference at `/Library/Managed Preferences/$USER/<bundle id>.extensions.<extension id>.plist` (one `sudo`; a user-level `defaults write` is RECOMMENDED-level and Chromium's `policy_value_store.cc` only passes MANDATORY to `chrome.storage.managed`). It never writes the browser's LevelDB — hand-written records there are ignored by TM. `admin deploy` runs it; `admin tampermonkey-status` reads back policy → delivery → install per profile. The module docstring has the mechanism and the hash algorithm.

The committed script carries no machine addresses: its `@connect` lines and `PROBE_HOSTS` hold `${TAILSCALE_HOST_MAC}` / `${TAILSCALE_HOST_UNRAID}`, which `build_provisioning` expands via `expand_env_placeholders` from the deploying shell's environment (the values live in `~/.claude/.env`). Only `UPPER_CASE` names are placeholders — the script's JS template literals (`${host}`) are left alone. An unset var aborts the provisioning. Because the expanded bytes are what gets hashed, a changed address re-provisions on the next `admin deploy` like any other script edit. The file is therefore not installable by hand as-is — `admin tampermonkey` is the only install path.


<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:6cd5cc61 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md for details and anti-patterns.

## Agent Context Profiles

The managed Beads block is task-tracking guidance, not permission to override repository, user, or orchestrator instructions.

- **Conservative (default)**: Use `bd` for task tracking. Do not run git commits, git pushes, or Dolt remote sync unless explicitly asked. At handoff, report changed files, validation, and suggested next commands.
- **Minimal**: Keep tool instruction files as pointers to `bd prime`; use the same conservative git policy unless active instructions say otherwise.
- **Team-maintainer**: Only when the repository explicitly opts in, agents may close beads, run quality gates, commit, and push as part of session close. A current "do not commit" or "do not push" instruction still wins.

## Session Completion

This protocol applies when ending a Beads implementation workflow. It is subordinate to explicit user, repository, and orchestrator instructions.

1. **File issues for remaining work** - Create beads for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Handle git/sync by active profile**:
   ```bash
   # Conservative/minimal/default: report status and proposed commands; wait for approval.
   git status

   # Team-maintainer opt-in only, unless current instructions forbid it:
   git pull --rebase
   git push
   git status
   ```
5. **Hand off** - Summarize changes, validation, issue status, and any blocked sync/commit/push step

**Critical rules:**
- Explicit user or orchestrator instructions override this Beads block.
- Do not commit or push without clear authority from the active profile or the current user request.
- If a required sync or push is blocked, stop and report the exact command and error.
<!-- END BEADS INTEGRATION -->
