# admin-project-tool

Runs per-project tasks from a short `admin.toml` manifest plus a library of composable archetypes. The installed tool **interprets** `admin.toml` at runtime (ADR-0006) — a project commits only the manifest; the `admin` command (installed on PATH at `~/.admin/admin`) finds it by walking up from `$PWD`. `admin compile` builds a standalone `./admin` — a full copy of the Go binary with the project's resolved manifest embedded (`internal/compiledinfo`) — for the rare case the tool isn't installed. See `docs/architecture/design.md` for the full architecture.

## `admin.toml` is NOT committed — this overrides the README

**Do not commit `admin.toml` to any repository except this one. Do not suggest committing it. Do not "fix" a project whose `admin.toml` is untracked — that is the intended state.**

`README.md` says a project commits only `admin.toml`. That describes how this tool will work **once it is public**, and it is deliberately left accurate for that future. It is not how things work today.

Today the tool is private. An `admin.toml` sitting in a public repository advertises the tool, its verbs, and its manifest format, and the tool is not ready to be advertised. So `admin.toml` is caught by a **global** ignore rule on the developer's machine (`~/.config/git/ignore`), which applies to every repository at once rather than per-project. This repo used to be the single exception and track its own, but a pre-commit guard (added in 214ee57) now blocks every commit here while `admin.toml` sits in the index — so this repo's own manifest is untracked like every other project's, and nothing about it travels through git.

Consequences that look like bugs and are not:

- A fresh `git worktree` has no `admin.toml`, so every `admin` command is dead inside it. That is expected. Seed the file into the worktree — `admin worktree` (`cmd/worktree.go`'s `runWorktree`, which copies the project's configured populate files) — is where that belongs — rather than committing it.
- No branch can carry a change to `admin.toml`, so config edits happen in the primary checkout only.
- A fresh clone on a new machine has no task runner until the manifest is copied over by hand.

When the tool goes public this section is what gets deleted, and the README becomes true as written.

### Secrets in manifests

Some `admin.toml` files embed secrets today, which is a second and independent reason not to commit them — and a design gap in the tool rather than a rule for humans to remember. Environment variables are the current workaround and have not been evaluated against the alternatives. Before any manifest is committed anywhere, it gets audited for secrets. See the open design work on giving the tool a real answer for this.

## What this repo is

- **`main.go` / `cmd/` / `internal/`** — the tool: a single static Go binary (installed as `~/.admin/admin`). Reads a project's `admin.toml`, merges archetypes, and dispatches. `cmd/` holds the Cobra command wiring for the tool verbs (`new`, `check`, `compile`, `branch`, `worktree`); `internal/kinds` is the interpreter — `internal/kinds/catalog.go`'s `Kinds` map is the kind catalog and `factories.go`'s `registerBuiltins` is the factory registry. It also holds the manifest frontend: `internal/manifest` (parser), `internal/archetype` (archetype merger — `merge.go`'s `Resolve`), `internal/validate` (command/action schema validation; see [ADR-0008](docs/adr/0008-centralized-schema-validation.md)), `cmd/compile.go` (standalone binary with an embedded manifest), `internal/detect` (archetype detector loader).
- **`archetypes/`** — partial manifests per project type (apple, react-native, hugo, docker, rust-tauri, etc.), authored in TOML with Lua or shell bodies. Mixins, not monoliths. (The data dir — the merger code is `internal/archetype`.)
- **`tools/tampermonkey/`** — standalone Go program that provisions the log-bridge userscript into Tampermonkey; not part of the `admin` binary.
- **`install.sh`** — builds the Go binary and installs it as `~/.admin/admin`, copies (or `--link`s) `archetypes/` to `~/.admin/archetypes/`, and puts `~/.admin` on PATH so `admin` runs from any project. The binary is recompiled on every run including `--link`, since nothing else recompiles Go.

## Design principles (don't violate)

1. **Manifest is truth.** `admin.toml` is the spec. The interpreter reads it live; there is no generated artifact to drift. `admin compile` is the only thing that emits a file.
2. **Commands are pure routing.** `[commands.X]` may only contain `desc` + `steps` (list of action names). `kind`, `run`, `targets` belong in `[actions.X]`. The resolver errors loudly otherwise.
3. **One static Go binary.** `admin` needs nothing installed to run — no interpreter, no venv. Third-party Go modules are fine (see [ADR-0001](docs/adr/0001-one-static-go-binary.md)). Archetypes are TOML with Lua or shell bodies, installed to `~/.admin/archetypes`.
4. **One engine.** `internal/kinds` (the factory registry `factories.go`'s `registerBuiltins` builds against the `Kinds` catalog in `catalog.go`) is the single execution path. `cmd/compile.go`'s `runCompile` packages a copy of the same Go binary, with the project's resolved manifest embedded in `internal/compiledinfo`, via `admin compile` — it never re-implements per-kind logic. Adding a kind = one entry in `internal/kinds.Kinds` plus a `Register` call in `factories.go`'s `registerBuiltins`; `Registry.Validate` checks at startup that every declared kind has a factory.
5. **Tool verbs are reserved.** `new`, `check`, `compile`, `branch`, `worktree` share the `admin <word>` namespace with manifest commands; a manifest may not define a command with one of these names (the CLI errors). `branch`/`worktree` take an `<input>` (branch name / issue-PR number / GitHub URL) resolved by `internal/resolve`; bare → a TUI, `--prune` → cleanup. See `cmd/worktree.go` and `cmd/branch.go`.
6. **The tool defines mechanism; `admin.toml` defines structure.** The tool never assumes a shipping target, a command name, or a menu a project didn't ask for — `build`/`dev`/`deploy`/`test`/`clean`/`logs`/`kill` are conventions with zero reserved semantics. Test for any change: could two projects using this archetype disagree about it? If yes, it's structure and belongs in `admin.toml`, not the tool or the archetype. See [ADR-0019](docs/adr/0019-the-tool-defines-mechanism-admin-toml-defines-structure.md).

## Hard rules (from past pain)

- **Reload state lives only in `internal/reload`; the subprocess output pump only in `internal/streams.OutputPump`.** One process runs one dev loop. Before writing a reload flag, a pid-file dance, or a "read the child's stdout into the terminal and the log" loop: import it. Every caller's real differences — pty vs pipe, formatter or not, whether the log gets raw or formatted text, whether a partial line needs flushing on exit — are `OutputPump`'s constructor parameters, not a reason to write another copy. Platform-specific argv building (`simctl spawn … log stream`, `devicectl … --console`, `adb logcat`) stays in the module owning that platform — only the pump is shared.
- **A new action kind is one entry in `internal/kinds.Kinds` (`catalog.go`) plus a `Register` call in `factories.go`'s `registerBuiltins`.** `Kinds` is the single source of truth (`KnownKinds`/`ConfiglessKinds`/`TargetConsumingKinds` are derived views). `Registry.Register` rejects an undeclared kind, and `Registry.Validate` — called from `factories.go`'s `init()` and again from `cmd.Execute` — fails at startup if any declared kind has no factory, so the catalog and factories can't drift.

## Testing

Tests run via `go test ./...`. New `*_test.go` files are auto-discovered by package. Shell tests (`tests/test_install.sh`, `tests/test_uninstall.sh`) run separately, chained after `go test` in `[actions.test]`.

- Everything: `admin test` (`go test ./...` + the shell suite)
- One package: `go test ./internal/kinds`

Tests that exercise full dispatch write a temp `admin.toml` and run it through the Go interpreter (`internal/kinds`).

## Common workflows

| Goal | Command |
|---|---|
| Validate this repo's `admin.toml` (parse + resolve) | `go run . check .`, or the PATH `admin check .` |
| Install / reinstall to `~/.admin/` | `bash install.sh`, or `admin deploy` to also provision the userscript |
| Run full test suite | `go run . test`, or the PATH `admin test` |
| Build a standalone `./admin` for a project | `admin compile [DIR]` |

**Never `./admin <cmd>` in this repo.** That name belongs to an `admin compile` artifact — a full copy of the Go binary with a *frozen copy* of the manifest embedded — so it runs whatever every command meant on the day it was built, with no warning that it is doing so (`--compiled-info` reports the source manifest and build time; nothing else does). Nothing regenerates it: `admin check` validates and `admin compile` is the only writer. Use the PATH `admin`, or `go run .`, here.

## Log bridge

A project can opt into browser-console-to-admin-log forwarding by adding `[log_bridge] hosts = [...]` to its `admin.toml`. Any subsequent `interactive-shell` action starts a `Bridge` (`internal/logbridge.Start`) on a free port (default `9988-9999`; override per machine with `port_range = [start, end]`), bound to `0.0.0.0` so Tailscale peers can reach it. The userscript at `tampermonkey/log-bridge.user.js` probes the user's hardcoded hosts × ports, picks the admin instance whose `hosts` glob-matches the page, and POSTs batched `console.*` entries to `/log` (`Bridge.handleLog`) where they're appended to the same log file the action is already writing. Each entry carries the page's `host` (not the full URL — keeps auth tokens in query strings out of the log); the listener prints one `client connected: <host> (<browser>)` banner per host, then one `<iso-timestamp> [LEVEL] [CLIENT] message` line per entry (`FormatEntry`) — the same `[LEVEL] <scope> message` order a server process prints, with the console level mapped onto admin's own level words (`log`/`info`→INFO, `warn`→WARN, `error`→ERROR, `debug`→DEBUG, `mapLevel`). Those lines reach the terminal through the action's formatter and `frame.Write` (wired by `internal/keybindings`' `installBridgeSink`), so a browser line and a server line are indistinguishable: same `HH:MM:SS` column, same level colors, footer intact. The timestamp shown is the browser's own, converted to local time (`logformat.Options{ReuseTimestamp: true}`) — arrival lags it by up to the userscript's 1s batch.

**One action can narrow its own bridge.** `hosts = "<glob>"` on an `interactive-shell` or `bridge-listen` action (`${VAR}` expanded) sets `ADMIN_LOG_BRIDGE_HOSTS` before that action's listener starts, so it advertises only that host set instead of the whole `[log_bridge].hosts` list. Two streams — `logs dev` and `logs live`, say — then run side by side and the userscript routes each page's console to the right one.

**An app that prints its own logger prefix into the message is unwrapped.** `17:36:32.366 [DEBUG] [Messaging] msg` arrives as one console argument, so the entry is double-stamped and the browser's level is wrong — a logger that routes debug through `console.log` is reported as `log`. `internal/logbridge.UnwrapInnerPrefix` takes the app's level, discards the duplicate clock (the entry's own ISO stamp is the same instant and has a date), and keeps every tag after it. Only the full `HH:MM:SS[.mmm] [LEVEL]` shape counts, so a message that merely starts with a clock is untouched.

**Ordering.** Each POSTed batch is sorted by entry timestamp (`inTimeOrder`), and the userscript flushes its backlog the instant its listener probe resolves instead of waiting out the first interval. Both exist for the same cause: `@match *://*/*` with no `@noframes` means one instance per frame, each buffering from document-start but only flushing after discovery — so a slow frame's backlog carries timestamps older than lines a fast frame already delivered. Neither fixes cross-frame interleaving within the same second; that would need a reorder window, which would delay the server's own output by as much. Discovery files at `/tmp/admin-project-tool/<pid>.json` are debugging aids; dead-PID files are swept on each start. Bind is `0.0.0.0` unconditionally — anyone on the tailnet can POST. `port_range` is per-machine: set it in each environment's `admin.toml` when the default range conflicts (e.g. a container that only exposes `3300-3399`); update `PROBE_PORTS` in the userscript to cover the union of all ranges.

The userscript is project-agnostic — no prod URLs baked in. It decides which pages to forward at runtime: `localhost`/`127.0.0.1`/`0.0.0.0` are always candidates, and any other host is opt-in via a single Tampermonkey menu command ("Log bridge: watch `<host>`", a host-labelled toggle) that stores an auto-derived glob in `GM` storage. The per-project prod hostname lives only in that project's `admin.toml` `[log_bridge].hosts` (advertised via `/health`), which remains the authoritative final match — so a new project needs one menu click, never a script edit.

## Log line rendering

`internal/logformat` renders every line of a framed action and of `admin logs`: strip whatever timestamp the source printed, stamp one `HH:MM:SS` column, colorize.

Two colorizers, deliberately different in kind:

- **Severity** is a closed set of four — ERROR red, WARN yellow, INFO cyan, DEBUG/TRACE dim — matched against the whole line, first hit only. These colors mean something, so they are assigned by hand.
- **Scope tags** (`internal/logformat.ColorizeScopes`) are an open set: `[api]`, `[supabase]`, `[Messaging]`, `[CLIENT]` — every project invents its own and no table can enumerate them. Each gets a color from `crc32(label.lower())` modulo the palette size, so the color is a pure function of the label: the same tag is the same color in every project, every run, and in a file replayed a year later, with nothing cached anywhere. CRC32, not Go's map iteration order or any salted hash, which would change color on every run. Level words are skipped so an arbitrary hue never buries ERROR. The 56-color palette excludes near-cyan, near-red and near-yellow (they would read as a level word) and stays mid-brightness so it survives a light *and* a dark terminal. Collisions are real and accepted — 20 tags over 56 colors share a hue about 3 times, which costs less than a tag that changes color between runs. `ADMIN_NO_SCOPE_COLORS=1` turns it off.

The scope regex is narrow on purpose: letter-led, space-free, bracketed. It must not match a JSON array printed into a message (`["https://a.js"]`), an empty `userIds=[]`, or the `[31m` inside an escape sequence the level colorizer already inserted.

**structlog's `ConsoleRenderer` is a second member of the `UnwrapInnerPrefix` family** (`internal/logformat.UnpadConsoleRenderer`), handling a different shape of the same double-render problem: an ISO-8601 stamp (already stripped by `StripLogTimestamp`) followed by a level right-padded with spaces (`[info     ]`) and a message padded to a fixed column before the next tag. The padded-level matcher is anchored at position 0 on purpose — that anchor, not a loosened `scopeRe`, is what keeps it from matching a JSON array, an empty `[]`, or a mid-line escape sequence; the scope regex above stays exactly as narrow as this section already describes.

## Pinned footer

A framed action runs inside a DECSTBM scroll region: its output scrolls in the top rows while `internal/frame` pins a footer to the bottom — a status row (action label + elapsed time), the absolute log path, then one row per URL. Every row is an OSC 8 hyperlink, so the log path (as `file://`) and the URLs are clickable. This exists because a dev server prints its URLs once and then buries them under thousands of lines; the port you need is the one thing guaranteed to have scrolled away. The log path is pinned for the same reason and is worth having on its own, which is why `footer = true` exists for an action with no URL at all.

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

An entry naming a key of the top-level `[urls]` table resolves to that entry, label and all. Anything else must be a real URL (contain `://`) — `internal/frame.ValidateURLRefs`, called from `internal/archetype`'s `Resolve`, rejects an entry that is neither, so `admin check` catches `urls = ["local"]` when the table calls that row `dev-cf` instead of painting a footer row that goes nowhere. `${VAR}` is expanded at dispatch time through `internal/frame.ResolveURLs`, so both derived vars (`${LAN_IP}`) and plain env vars work. A bare-string URL labels itself and prints in one column instead of two. Per-target inheritance works exactly like `requires`: a target's resolved URL list holds `nil` for "inherit the parent" and a slice (including empty) for "replace it". The per-target `footer` key inherits the same way, as the tri-state `hasFooter`/`footerVal` pair `internal/frame.ShouldActivate` takes — `hasFooter=false` means "the key was absent" — and an absent target key that reaches the parent's own absent `footer` hands the decision back to the resolved URL list.

A target that delegates (`action = "dev-cf"`) is framed by the *picked target*, not the action it hands off to — the footer goes up before the branch in `internal/kinds/multitarget.go`'s `makeMultiTarget`. `enterFrame` returns the frame it started (or `nil`) and `frame.Deactivate(f)` only tears down that exact frame, so an inner action declaring its own `urls` — whose activation is declined because one is already up — cannot strip the outer footer while the outer action is still running.

**Activation.** `footer` defaults to `true`: a framed action raises a footer — the status rule and the log path — whether or not it declares `urls`, since the log path is worth having on its own on any long-running process (it's what you copy to read the run back without cancelling the process still writing to it). `footer = false` is the only way to suppress it, and does so regardless of `urls` — the one thing an empty `urls = []` cannot say once a *parent* target declares its own list. Absent means footer-on; only an explicit `footer = false` (or a `footer = true` you didn't need to write) changes the outcome. Frame activation still declines when stdout is not a terminal (piped output, CI), when `ADMIN_NO_FRAME` is set, or when a frame is already up; one process runs one dev loop, the same assumption `internal/reload` makes. It does not decline on an empty URL list — `internal/kinds/frame.go`'s `enterFrame` is the single owner of that question, and `internal/frame/urls.go`'s `ShouldActivate` is where the default lives.

**Pipeline.** `internal/validate` checks the `urls` shape and that `footer` is a real bool (parent and per-target — TOML has no truthy strings, so `footer = "true"` is a typo worth reporting); `internal/archetype` normalises entries to label/url pairs and builds the resolved and per-target URL lists plus the tri-state footer flags; `internal/kinds` calls `enterFrame` in the shell, multi-target, and interactive-shell closures and always calls `frame.Deactivate()` via `defer`. Child output reaches the terminal through `frame.Write`, which repaints the footer after each chunk — so every pump must call it rather than writing to stdout directly. In practice this means one thing: every caller writes through `internal/streams.OutputPump`, which is the only place that calls `frame.Write`. `frame.ApplyWinsize` sizes a child's pty to the *region*, not the screen; without it a child that positions its own cursor (wrangler, vite, metro) paints over the footer. `admin logs` reads logs and never runs under a frame.

## Guards

A project can gate any action or multi-target target behind a named precondition by listing guard names in `requires`. Built-in guards (`git-clean`, `git-pushed`, `deps-fresh`) are registered by `internal/guards` at package `init()`; user-defined ones live in `[guards.<name>]` tables. The full design and rejected alternatives live in [ADR-0005](docs/adr/0005-guards-and-requires.md).

`deps-fresh` is special: besides being referenceable in `requires`, it also runs as a **global preflight** before every command (CLI or TUI) via `RunGlobalPreflight`, with no `requires` annotation needed. It checks a node/bun workspace for `workspace:*` dependencies declared but never linked into `node_modules` — the stale-install class (a fresh git worktree never installed, or a workspace dep added without re-running install) that otherwise surfaces as an opaque bundler "failed to resolve import". It's a no-op pass on non-node projects, and fail-open (a bug or unusual manifest degrades to a pass, never a crash/block), so it's safe to run everywhere. The control/diagnostic verbs `logs` and `kill` are exempt so a stale install can't lock you out of recovering. Always-on checks live in `internal/guards/preflight.go` (the preflight analogue of `Builtin`) — see [ADR-0005](docs/adr/0005-guards-and-requires.md) for why deps-fresh is always-on while the git guards stay opt-in.

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

**Pipeline.** Guard handling is concentrated in `internal/guards`, which exposes one function per phase that the pipeline calls: `manifest.Parse`'s `parseGuardTables` (manifest parse — validate `[guards.<name>]` run/fail, reject built-in collisions), `guards.ValidateRequires` (resolve, called from `internal/archetype.Resolve` — every name in any `requires` list maps to a known guard, built-in or user-defined), `guards.Register` (interpreter setup — wire up the shell guards from the manifest's `Guards` map), and `guards.RunRequires` (dispatch, called from `internal/kinds.RunAction` and the multi-target factory). `guards.RequiresOf` reads the `requires` key off a raw `manifest.Action` as a list of strings, erroring if it's present but malformed; `internal/archetype` builds the resolved and per-target `requires` lists, where an absent per-target key means inherit the parent; `internal/kinds` wraps each flat action with a `RunRequires([...])` check and resolves per-target requires inside the multi-target closure.

**Runtime.** `RunRequires` walks the list fail-fast: invokes each named guard, returns an error carrying the first failing guard's diagnostic. Built-in guards produce dynamic diagnostics ("branch 'X' has unpushed commits — push first: git push origin X"); user-defined shell guards run `/bin/sh -c <run>` (`registerShellGuard`), capture stdout+stderr, prepend the captured output to the static `fail` message on non-zero exit. The cwd is inherited (the interpreter cd's to project root at boot). Guards do not compose — a guard cannot reference another `requires`. Adding a new built-in means writing a Go function in `internal/guards` and adding its name to the `Builtin` map there (the single source of truth). `RunGlobalPreflight` runs the always-on checks (currently just `deps-fresh`) once per invocation before dispatch — the CLI boot path calls it before the command runs and the TUI calls it before executing a selection; it's memoized so nested dispatch (sequence steps, `@`-refs) only checks once.

## Dispatch locale

A project can declare a `[terminal]` table (ADR-0020) naming the launcher it wants a window-locale action dispatched into — whatever multiplexer or terminal app the project actually runs, never one the tool assumes:

```toml
[terminal]
spawn  = 'id=$(herdr pane split --current --direction right --cwd "$ADMIN_CWD" --no-focus | jq -r .result.pane.pane_id) && herdr pane run "$id" "$ADMIN_CMD" >/dev/null && echo "$id"'
attach = 'p=$(herdr pane get "$ADMIN_HANDLE") && herdr workspace focus "$(printf %s "$p" | jq -r .result.pane.workspace_id)" >/dev/null && herdr tab focus "$(printf %s "$p" | jq -r .result.pane.tab_id)" >/dev/null'
kill   = 'herdr pane close "$ADMIN_HANDLE"'
status = 'herdr agent get "$ADMIN_HANDLE"'   # optional
read   = 'herdr pane read "$ADMIN_HANDLE" --lines 40'   # optional

[actions.dev]
kind   = "vite-dev"
locale = "window"
```

`locale` says where a dispatch runs, and it has two values: `window` (the launcher above) and `hosted` (a pty the dashboard owns, which dies with it, and what runs when nothing is declared). `inline` is not a value. `locale = "window"` on an action (or per multi-target target, inheriting the parent's default the way `requires`/`urls` already do) is what opts it into the launcher; `locale` in `[terminal]` itself is the project-wide default at the base of that chain (terminal → action → target, most specific wins, an explicit `locale = "hosted"` beats a `window` default, and an archetype should not set it — two projects on one archetype can disagree, which makes it structure under ADR-0019) — but only for a dashboard dispatch (a row-enter). A `[terminal]` table declaring only `locale = "hosted"` needs no launcher keys; any other declaration needs `spawn`, `attach` and `kill` together. A workflow action itself always runs on the dashboard's surface (`internal/kinds.dispatchLocale` resolves it to hosted), because its Runner reads a yes/no from that terminal; its workers take the workflow's own `locale`, defaulting to `window`: `window` opens each in a launcher pane whose command is the action's `run` string, with `ADMIN_TARGET` set to the target id (`internal/kinds/workflowdispatch.go`'s `dispatchOne`); `hosted` runs the same `run` string under `/bin/sh -c` on a dashboard-owned pty in the target's worktree, needs no `[terminal]` and no herdr, and frees its concurrency slot when the process exits (`workflowhosted.go`'s `hostedOne`). From a plain shell a hosted workflow runs its `run` strings inline, one target at a time, each on the terminal until it exits. `detach` is not a key, and `admin check` rejects it. **A plain shell dispatch never consults `locale`, whatever the manifest declares**: `admin dev` and `admin cmd` typed at a shell always run inline. A dashboard row-enter shells `spawn` with `ADMIN_CMD`/`ADMIN_LABEL`/`ADMIN_CWD` set to the re-entry command line, this project's root, and the action's label, plus `ADMIN_ROW_KIND`/`ADMIN_ROW_ID`/`ADMIN_ROW_URL` when the dispatch is row-bound (ADR-0018); `spawn`'s trimmed stdout is the opaque handle recorded in the live-work registry, and `attach`/`kill` are later shelled with `ADMIN_HANDLE` set to it. The optional `read` is shelled the same way (`internal/kinds.ReadLauncherPane`) on the Work tab's tick, only for the launcher-dispatched row under the cursor (`internal/tui/panetail.go`); its stdout replaces `livework.Entry.Snapshot`, which the live band renders in place of the hosted-pty `Output` ring. A launcher with no `read` leaves the band on "no local output - attach to see the real session". No `kind` is reserved for this — a kind is just what runs; whether its dispatch goes through the launcher is a per-action `locale` declaration, never assumed from the kind (ADR-0019/ADR-0020).

**No `[terminal]` declared, or `locale` absent or `hosted`, → run on the dashboard's hosted pty (inline from a shell)** — nothing changes for a project that never opts in. A `[terminal]` declared but a window-locale action's `spawn` failing degrades to the caller's own default surface with one warning line, the same "the tool must never be why a dev loop doesn't start" rule the guards' preflight follows. `internal/kinds.RunCommand` takes a `*CommandProducer`, and `locale` resolution is producer-gated: `internal/kinds.launchCommand` — which resolves the command's own launcher/`locale` declaration — is reached only for a non-nil producer (the dashboard, which cannot host the run in its own process); a nil producer (the plain-CLI shape) runs inline, and the CLI itself dispatches through `internal/kinds.RunCommandInline`, which never calls `RunCommand`/`launchCommand` at all (ADR-0020 amendment, `admin-wwrg`). A process already running inside a launched dispatch (`ADMIN_REENTRY`, set by the `__run-action`/`__run-command` re-entry verbs) always runs inline regardless of the producer, so nothing reached from inside an already-launched run — a step, a multi-target target one level down — can reach a second launcher.

**A finished window worker is reaped on the dashboard tick** (`internal/tui/reappoll.go` → `internal/kinds/reap.go`'s `ReapWorker`). Finished means the action's `done` predicate exits 0 (run in the worker's worktree with `ADMIN_TARGET` set, 5s deadline; an error or timeout is not finished), or, with no `done`, `[terminal].status` reporting the pane gone. Reap then requires the worktree clean and `origin/main..<branch>` empty — fixed in Go, not configurable — and on a pass runs `[terminal].kill`, removes the worktree and branch, marks the row exited and calls `kinds.ReleaseWorkerSlot`, the only place a window worker's ceiling slot is freed besides the worker's Kill op. On a fail everything stays and the reason shows on the row (`livework.Entry.Note`). The pane poller skips workers so a vanished pane cannot drop a row out of the reap; a worker with `done` whose pane closed before `done` passed stays until killed.

## Row-scoped key bindings

A project can bind keys to commands at the row level by declaring `[keys.<row-kind>]` tables mapping keys to command names — one table per bindable row kind (`bead`, `issue`, `branch`, `pr`, `commit`, `stash`, and the Work tab's `command`, `commandTarget`, `worker`). A bound key invokes the named command with the row's data passed as environment variables (`ADMIN_ROW_KIND`, `ADMIN_ROW_ID`, `ADMIN_ROW_URL`); the command's `desc` becomes the key's label in the row footer, replacing the built-in binding. This repo dogfoods the feature for implementing work:

```toml
[commands.implement]
desc  = "Dispatch an agent over the selected bead(s) (ADR-0018 dogfood)"
steps = ["implement"]

[actions.implement]
kind  = "workflow"
over  = "selection"
run   = "claude '/implement $ADMIN_TARGET afk'"
done  = 'bd show "$ADMIN_TARGET" --json | jq -e ".[0].status == \"closed\""'

[keys.bead]
enter = "implement"
```

The resulting Beads footer shows `↵ Dispatch an agent over the selected bead(s) (ADR-0018 dogfood)` — the command's description auto-fills the key label so the footer advertises what the binding does.

On a Work row, `ADMIN_ROW_ID` is the live-work id the row's run is keyed on: the command name, `<command>:<target>` for a target row, or the worker's entry id. That is also what a reload loop records as its owner, so a key bound to a `kind = "server-reload"` command reloads only that row's loop:

```toml
[commands.reload]
desc  = "reload this loop"
steps = ["reload"]

[actions.reload]
kind = "server-reload"

[keys.command]
r = "reload"
```

Every reload loop (`reload.RunWithReload` and `reload.SetupReload`) writes a JSON record to `/tmp/admin-run-<target>-<pid>.pid` holding its pid, target, project root, and owning live id — set once per process by `internal/kinds`' `runSteps` via `reload.SetOwner`. `server-reload` signals only loops whose root is its own project: all of them with no argument, those whose target contains `args[0]`, or, from a Work-row binding, those `reload.MatchRow` matches (the row's live id, or any of a command row's targets).

Three validation rules govern bindings (`internal/validate/keys.go`, `RowKeys`): reserved keys are rejected (`keymap.Reserved` — q, ctrl+c, esc, tab, shift+tab, the arrows, j, k, 1-4, /); an unknown row kind is rejected with the list of bindable ones; shadowing a built-in is allowed and reported — `admin check` prints e.g. `keys.bead.d shadows the bead detail overlay` and still exits 0 (shadows come back on a separate return value, never in the error).

Dispatch (`internal/tui/rowkeys.go`, `rowKeyDispatch`) is consulted before the built-in switch in `Model.handleKey`; a miss (no table, no row, unbindable kind, unbound key) returns ok=false and the built-in switch runs exactly as today; a hit dispatches the named command through `dispatchCommandCmd` — the same path a Commands-tab row uses — with the row's environment variables; if that command already has a live non-worker entry the dashboard attaches to it rather than re-running it (admin-c2zl).

A binding resolving to a single-step `kind="workflow"` action with `over = "selection"` dispatches over the tracker selection through the same picker/confirm overlay `space`-then-`w` uses when the ACTIVE pane's selection is nonempty, and immediately over the cursor row alone (no overlay, outcome on the status line, missing agent reported as a red status line) when it is empty. Every other command shape acts on the cursor row alone however many rows are selected — the environment variables carry one row and have no plural form.

**Pipeline.** `manifest.Keys` (raw top-level `[keys]` table, `internal/manifest/manifest.go:122`, unvalidated at parse) → `validate.RowKeys` against the manifest's declared command names (called by `cmd/check.go`'s `checkRowKeys` and by the TUI's `buildRowKeys`) → `internal/keymap` holding the built-in labels keyed by row kind, the leaf package both `validate` and `tui` import → `internal/tui/rowkeys.go` for the lookup and dispatch, with `Model.keymap()`/`rowPairs` (`internal/tui/view.go:675`) merging bindings into the footer: a bound key that shadows a built-in replaces that pair's label in place, an unshadowing one is appended after the built-ins. `[keys]` is the one raw archetype table merged per key rather than whole-value (`internal/archetype/merge.go`'s `mergeRowKeys`).

See [ADR-0018](docs/adr/0018-row-scoped-key-bindings.md) for the rejected alternatives.

## Tampermonkey provisioning

`tools/tampermonkey` (a standalone Go program, `go run ./tools/tampermonkey`) installs `tampermonkey/log-bridge.user.js` into Tampermonkey (5.5+) on every profile of Arc/Chrome/Brave by writing the extension's managed-storage policy (`jsonImport`, a `data:` URL carrying TM's export JSON plus TM's structural hash) as a forced preference at `/Library/Managed Preferences/$USER/<bundle id>.extensions.<extension id>.plist` (one `sudo`; a user-level `defaults write` is RECOMMENDED-level and Chromium's `policy_value_store.cc` only passes MANDATORY to `chrome.storage.managed`). It never writes the browser's LevelDB — hand-written records there are ignored by TM. `admin deploy` runs it; `--status` (`admin tampermonkey-status`) reads back policy → delivery → install per profile. The `tools/tampermonkey` package comment (`main.go`) has the mechanism and the hash algorithm.

The committed script carries no machine addresses: its `@connect` lines and `PROBE_HOSTS` hold `${TAILSCALE_HOST_MAC}` / `${TAILSCALE_HOST_UNRAID}`, which `provision.go`'s `buildProvisioning` expands via `expandEnvPlaceholders` from the deploying shell's environment (the values live in `~/.claude/.env`). Only `UPPER_CASE` names are placeholders — the script's JS template literals (`${host}`) are left alone. An unset var aborts the provisioning. Because the expanded bytes are what gets hashed, a changed address re-provisions on the next `admin deploy` like any other script edit. The file is therefore not installable by hand as-is — `go run ./tools/tampermonkey` (wired up as `admin tampermonkey`) is the only install path.


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
