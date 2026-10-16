# admin-project-tool: Manifest-Driven, Archetype-Composable

`admin` is a single static Go binary that reads a project's `admin.toml` at runtime and dispatches directly — there is no generated `./admin` script and nothing to keep in sync with the manifest. `internal/manifest` parses it, `internal/archetype`'s `Resolve` merges in any listed archetypes, and `internal/kinds` (the `Kinds` catalog in `catalog.go`, factories registered in `factories.go`) is the single execution path for every action `kind`. See [ADR-0006](../adr/0006-interpreter-mode.md) for why the tool interprets the manifest instead of generating a script, and `admin compile` (`cmd/compile.go`) for the one place a file still gets emitted — a copy of the same Go binary with a project's resolved manifest embedded (`internal/compiledinfo`).

## Context

A survey of 16 committed `admin` scripts across `~/Projects` revealed **heavy overlap** — nearly every project has `build / dev / deploy / test / clean`, plus a common tail of `reload / logs / open / uninstall`. One-off commands are rare and always reducible to "name + shell string."

Projects cluster into ~5 archetypes: **docker-to-unraid**, **stash-plugin**, **apple/swift**, **unraid-plugin**, **simple-cli** (Hugo, Go CLI, Firefox extension). Some scripts (`open-assistant`, `apple-coding-tool`) are archetype combinations (docker + apple) — expressed cleanly by listing multiple archetypes in `admin.toml`.

**The goal:** a short, human-editable `admin.toml` manifest plus a library of composable archetypes fully describes a project's task runner, with nothing generated and nothing to keep in sync — edit the manifest, run `admin`.

## Design Principles

1. **Manifest is truth.** `admin.toml` fully describes the project's commands and actions. The installed `admin` binary reads it live on every run; there is no generated artifact to keep idempotent or audit for drift.
2. **Archetypes are mixins, not monoliths.** A manifest can list multiple archetypes; `internal/archetype`'s `Resolve` merges their command sets. This kills the "one template" bottleneck that forced `open-assistant` and `cctool` to be hand-maintained.
3. **Interpreter installed; standalone on demand.** A project commits only `admin.toml`; the installed `admin` (on PATH) runs it from anywhere inside the project. For a machine without the tool installed, `admin compile` emits a self-contained `./admin` — a full copy of the Go binary with the project's resolved manifest embedded.
4. **One static binary.** `admin` needs nothing installed to run — no interpreter, no venv. Third-party Go modules are fine (see [ADR-0001](../adr/0001-one-static-go-binary.md)); archetypes are TOML with Lua or shell bodies.
5. **Composability over expressiveness.** The manifest is a short, flat declaration — not a DSL. If a project needs real logic, it drops a custom command as a shell string or, for orchestration too complex for that, a `kind = "lua"` body (see [lua-host-api.md](lua-host-api.md)).

## Manifest Format (`admin.toml`)

Lives at project root.

```toml
# admin.toml
project_name = "Portfolio"

archetypes = ["hugo"]          # zero or more; merged in listed order

[status.dev]
kind = "http"
url  = "http://localhost:1313"

[status.prod]
kind = "http"
url  = "https://pierce.dev"

[commands]                      # override or add commands (desc + steps only)
# Override: archetype provided `build`; we change its description.
# build = { desc = "My production build" }

# Add a new command not from any archetype:
# warm-cache = { desc = "Warm CDN cache", steps = ["warm-cache"] }

[actions]                       # where work actually happens (kind + run/targets/etc.)
# warm-cache = { kind = "shell", run = "./scripts/warm.sh", desc = "Warm CDN cache" }

# Display order — two approaches:
#
# 1. Explicit order list (legacy, still supported):
# order = ["build", "dev", "deploy", "", "test", "clean"]
#   "" or "---" inserts a blank spacer. Commands omitted auto-append after.
#   Manifest order wins; otherwise the last archetype's declared order is used.
#
# 2. group + priority on each command (preferred for composable archetypes):
# [commands.dev]
# desc = "Run dev server"; steps = ["run-dev"]; group = 1; priority = 10
# [commands.deploy]
# desc = "Deploy"; steps = ["deploy"]; group = 2; priority = 10
#   Commands sort by (group, priority, name). Spacers appear automatically
#   between groups. All default to group=0, priority=0 → alphabetical, no spacers.
#   Works across multiple archetypes without a central order list.
```

### `[deploy]` — a local directory that mirrors one on the host

An app's code and the files it reads at runtime change on different clocks. A
config edit or a new asset does not need an image rebuild, a registry push, and
a container recreate — it needs those bytes on the host. `[deploy]` names a
directory in the repo that mirrors one on the machine running the app, so
editing the local copy and running one command is the whole update path.

```toml
[deploy]
src     = "deploy/appdata"                       # repo-relative, required
dest    = "/mnt/user/appdata/my-app"             # path on the target, required
host    = "${UNRAID_USER:-root}@${UNRAID_HOST}"  # omit for a local copy
exclude = ["*.log"]                              # rsync --exclude patterns
delete  = false                                  # default false
chown   = "${APP_UID:-99}:${APP_GID:-100}"       # rsync --chown; see below
```

`delete` defaults to **false**. A mirror
directory usually points at a folder the running app also writes into — a
history file, a SQLite database, logs. Mirroring exactly would delete that state
on every deploy. Set `delete = true` only when the local copy owns the whole
directory.

`chown` sets the `"UID:GID"` the target should own synced files as, passed
straight to rsync's own `--chown` (`internal/dockerrun.BuildDeploySyncPlan`).
Without it, rsync's `-a` carries the *sending* machine's owner and group onto
every directory it creates on the target, so a container running as a
different uid can read the mirror but not write to it. Remote (rsync) deploys
only: a local (no-`host`) deploy warns and leaves ownership alone, since the
files already land as whoever ran `admin` and there is no rsync side to
reassign them.

`host` is compared against the current machine first (`ADMIN_LOCAL`, then IP
match), so the same manifest works when run *on* the target and from a laptop.

The `docker` archetype exposes it as `admin deploy files`, alongside
`admin deploy image` and `admin deploy all` — and bare `admin deploy` shows a
picker, since a project carrying a `[deploy]` table has a real choice to make.
(Without one there is nothing to pick, so it goes straight to the image, as it
always did.)

**Command/Action split:** `[commands]` entries are thin routing layers — they may only have `desc` and `steps` (a list of action names). All actual work (shell strings, Lua bodies, multi-target dispatch) lives in `[actions]`. Commands are the TUI menu entries; actions are the implementation. `internal/validate` (ADR-0003) errors with a clear message if a `[commands]` entry contains `kind`, `run`, or `targets`.

Every key is optional except `project_name` and `archetypes` (may be empty list).

### `[status.<label>]` — live readouts

One table for every live readout the dashboard and `print-status` show: a URL's reachability, a count pulled from a remote API, the first line of a shell command. `kind` picks a built-in checker (`internal/status`); the entry only configures it, so no project reimplements a URL check.

```toml
[status.prod]                        # kind "http": up when the URL answers 2xx/3xx
kind = "http"
url  = "${PROD_URL}"

[status.jobs]                        # kind "json": GET, decode, pull values, fill a template
kind    = "json"
url     = "${PROD_URL}/api/queue"
headers = { Authorization = "Bearer ${PROD_TOKEN}" }   # optional; ${VAR} resolves when the check runs
every   = "15s"                      # refresh interval; default 5s for http, 15s otherwise
pull = { running = "length([?status=='transcoding'])", failed = "length([?status=='failed'])" }
show = "{running} running · {failed} failed"

[status.disk]                        # kind "command": first non-empty stdout line
kind = "command"
run  = "df -h / | awk 'NR==2 {print $4 \" free\"}'"
```

`pull` values are [JMESPath](https://jmespath.org) expressions evaluated against the response body. A null result shows as `-`, a whole number drops its `.0`, anything else that is not a string shows as compact JSON. `show` fills `{name}` from `pull`; `admin check` rejects an expression that does not compile and a `{name}` no `pull` entry produces. Each request or command is cut off after 5 seconds. A failed check shows its reason and a hollow red dot, never the previous value.

`${VAR}` in `url`, `headers` and `run` resolves each time the check runs, so a token never lands in the manifest; `admin env` lists the variables an entry references. The `http` kind checks the URL itself, not a `/health` path under it.

Where it shows: the Work tab's Status section lists every entry (dot, label, readout), refreshed on each entry's own interval; an action of kind `print-status` prints them once. An action's `urls = ["prod"]` can name an entry that has a `url` to pin it in the footer and the Work band. Archetypes declare entries, and a project's `[status.prod]` replaces an archetype's `[status.prod]` whole.

## Archetype Catalog

Each archetype is a directory under `archetypes/` (e.g. `archetypes/apple/archetype.toml`) that declares commands and actions as a partial manifest — `internal/archetype`'s `Resolve` merges it into the project's own `admin.toml`. Archetypes do not generate code; a `kind = "shell"` action's `run` is a literal shell string, and a `kind = "lua"` action's body is Lua executed by `internal/luahost`.

Current archetypes (one directory each under `archetypes/`): `apple`, `base`, `cloudflare-workers`, `docker`, `docs`, `firefox-extension`, `forge-engine`, `go-cli`, `herdr`, `hugo`, `raylib-game`, `react-native`, `rust`, `rust-tauri`, `simple`, `unraid-plugin`.

Composition works: `archetypes = ["docker", "apple"]` merges both archetypes' commands into one manifest.

**Conflict resolution:** when two archetypes define the same command name, the later one wins; the manifest's own `[commands]` table wins over all archetypes.

**Display order:** Two mechanisms. *Preferred for composable archetypes:* each command declares `group` (int, default 0) and `priority` (int, default 0). Commands sort by `(group, priority, name)`; spacers appear automatically between groups. Commands with all defaults are alphabetical with no spacers. *Legacy explicit list:* manifest or archetype declares `order = ["build", "dev", "", "test"]` with `""` / `"---"` spacers; manifest wins over archetype order. Both mechanisms support sub-command target ordering for `multi-target` actions: inline target subtables may also declare `group`/`priority`, which sorts them and emits spacers in the target-picker menu.

## Authoring Archetypes

### Actions are declared, not scripted

The central rule: **archetype actions must be expressible as a declarative kind + run string, not as inline code.**

Each `kind` is a factory in `internal/kinds` (the `Kinds` catalog in `catalog.go`) that knows how to wire up a specific operation. The `run` field is a short, opaque key the factory interprets — a verb name, a script name, a URL, a destination path.

**Good — declarative:**
```toml
[commands.deploy]
desc  = "Full deploy"
steps = ["npm-build", "sync-dist", "reload"]

[actions.build-ios]
kind = "apple"
run  = "build_ios"
desc = "build iOS"

[actions.npm-build]
kind = "shell"
run  = "npm run build"
desc = "run npm build"

[actions.sync-dist]
kind = "sync"
run  = "${DEPLOY_TARGET}/app"
desc = "sync dist/"

[actions.reload]
kind   = "http"
run    = "${APP_URL}/reload"
method = "POST"
```

**Bad — a shell body doing the work a typed kind should:**
```toml
[actions.build-ios]
kind = "shell"
run  = "curl -X POST https://api.example.com/build?config=$(cat build.json)"  # ← NO
```

The second form is not declarative — the shell string encodes control flow (parsing `build.json`, building a URL) that belongs in a factory the archetype can validate and reuse, not in a one-off string every consuming project has to get right itself.

### `supervise = false` — an action whose daemon outlives admin

Every process admin starts for an action is stamped with `ADMIN_RUN_ID` and `ADMIN_CHECKOUT` and swept (SIGTERM, then SIGKILL, including children that left the process group with `setsid`) when the action stops or admin exits for any reason (`internal/supervise`). An action that starts a daemon meant to outlive admin (a VM, a tmux server, a database) sets `supervise = false`: its processes are not tagged and no watcher sweeps them. The key is a boolean on the action (`admin check` rejects any other value) and defaults to true. It applies to the process kinds (`shell`, `shell-passthrough`, `interactive-shell`, `reload-loop`, `docker-reload-loop`, `docker-dev`, `electron`, `vite-dev`, `npm`, `docs`, `go`, `raylib`) and to a hosted command whose steps all set it.

### When to add a new kind

If an action needs config-driven logic and no existing kind covers it, **add a new kind**. The pattern (CLAUDE.md's hard rules, `internal/kinds/catalog.go`):

1. Add an entry to the `Kinds` catalog in `internal/kinds/catalog.go`.
2. Register a factory for it with `Register(...)` in `internal/kinds/factories.go`'s `registerBuiltins`.
3. `Registry.Validate` fails at startup if a declared kind has no factory, so the catalog and factories can't drift.
4. Use the new kind in the archetype's `[actions.*]` tables.

Example: `kind = "apple"` (`internal/kinds/apple.go`) reads the action's `run` as a verb name (`build_mac`, `dev_ios`, ...) and dispatches it against the project's `[apple]` config table. A new config-backed function family follows the same shape — a factory that reads its own top-level manifest table by name.

### `kind = "interactive-shell"` — long-running shell with keybindings

A `kind = "shell"` action runs to completion and exits. `kind = "interactive-shell"` is the variant for long-lived processes (supervisors, dev loops, watchers) that should accept single-character keypresses to side-fire commands while the wrapped process keeps running. Each binding sits inline on the action under `keys.<keychar>`:

```toml
[actions.dev-local]
kind = "interactive-shell"
desc = "Bun (web+worker) + Vite on the host"
run  = '''bash <<'__DEV_LOCAL_EOM__'
# ... long-running supervisor; writes child PIDs to /tmp/*.pid ...
__DEV_LOCAL_EOM__
'''

[actions.dev-local.keys.r]
label    = "restart web"
signal   = "TERM"
pid_file = "/tmp/recodarr-main.pid"

[actions.dev-local.keys.R]
label    = "restart all"
signal   = "INT"
pid_file = "/tmp/recodarr-main.pid"

[actions.dev-local.keys.v]
label = "restart Vite"
run   = '''kill -TERM "$(cat /tmp/recodarr-vite.pid)" 2>/dev/null || true'''
```

Each binding has exactly one target shape:

- `run = "<shell snippet>"` — fire and forget in a subshell while the wrapped process keeps running.
- `action = "<action-name>"` — invoke another `[actions.X]` by name. Cross-references are validated when the manifest resolves. A leading `@` is optional (`action = "@restart"` is equivalent to `action = "restart"`).
- `signal = "TERM" | "INT" | "USR1" | …`, `pid_file = "<path>"` — read the PID file, send the signal. Stale/missing PID file warns and no-ops.

Keys are case-sensitive single characters: `r` and `R` are distinct. Ctrl-C / Ctrl-D are reserved for exit. Ctrl/Alt combos are not in scope. The runtime prints a footer banner with the configured bindings on entry.

**Distinction from `kind = "docker-reload-loop"`**: docker-reload-loop's `r` tears down and respawns the wrapped process. `interactive-shell` keeps the wrapper running and side-fires whatever the binding declares — useful when the wrapped script supervises its own children and only wants part of the tree cycled.

**Archetype/manifest merging**: actions are whole-table replaced when both an archetype and the manifest declare the same `[actions.X]`. A project that wants to keep an archetype's `keys` but tweak one must re-declare them on the manifest side.

### `kind = "lua"` is for complex action logic only

Complex orchestration — code that parses args, picks targets, branches on config presence, and dispatches to multiple paths — is legitimately expressed as a `kind = "lua"` action, executed by `internal/luahost`. The `apple` archetype's verb dispatch (`internal/kinds/apple_darwin.go`'s `runAppleVerb`) is the canonical example in Go: it handles `mac | ios | tv` target selection, `--force`/`--log`/`--clean` flags, and multi-step orchestration — though for `apple` specifically that dispatch itself lives in Go rather than in a Lua body, since it doesn't exist without the Go binary.

`kind = "lua"` is not appropriate for an action that is a single function call with a config dict. Those belong in a typed kind.

### Reusable host functions in `kind = "lua"` bodies

A `kind = "lua"` body runs against `internal/luahost`'s registered function tables (`apple.*`, `dockerrun.*`, `metro.*`, ...) — see [lua-host-api.md](lua-host-api.md) for the full function catalog, including the simulator helpers (`apple.resolve_simulator`, `apple.ensure_booted_simulator`, `apple.install_and_launch_sim`, ...) that drive a simulator from a Lua action.

### Commands vs actions — the key rule

**Commands** (`[commands.*]`) are pure routing: TUI menu entries with `desc` and `steps` only. They may not have `kind`, `run`, or `targets`. Each command calls one or more actions in order. `internal/validate` enforces this and errors clearly on violations.

**Actions** (`[actions.*]`) are the implementation: hidden from TUI and `--help`, dispatched by `internal/kinds`. They have `kind` + `run`/`targets`/etc. All actual work — shell strings, Lua bodies, multi-target dispatch, apple verb calls — lives in actions.

The canonical pattern:
```toml
[commands.build]
desc  = "Build the project"
steps = ["build"]

[actions.build]
kind = "shell"
run  = "make all"
desc = "Build the project"
```

A well-authored archetype exposes a small set of composed top-level commands and a richer set of per-target actions for manifests to compose freely. The apple archetype's `build-ios`, `dev-ios`, `dev-device`, `deploy-ios`, and `clean-ios` actions let any manifest build a `kind = "multi-target"` action with iOS as one target.

### Ordering commands

Two mechanisms control the order commands appear in the TUI menu and `--help`. They are **mutually exclusive** — whichever order list is present takes precedence and silently ignores group/priority values on commands.

**group + priority (preferred for new archetypes and manifests):**

Each command declares `group` (int, default 0) and `priority` (int, default 0). Commands sort by `(group, priority, name)`; spacers appear automatically between groups. Because each command carries its own placement, this works correctly when multiple archetypes are composed without a central coordinator.

```toml
[commands.dev]
desc = "Run dev server"
steps = ["run-dev"]
group = 1
priority = 10

[commands.deploy]
desc = "Deploy"
steps = ["deploy"]
group = 2
priority = 10
```

**order list (legacy, still supported):**

The manifest or an archetype declares `order = ["build", "dev", "", "test"]` where `""` or `"---"` inserts a spacer. Manifest order wins over archetype order. Commands omitted from the list append alphabetically after.

```toml
order = ["build", "dev", "", "test", "clean"]
```

Drop the `order` key to switch to group/priority, or drop the group/priority fields to stay with the order list.

## Detectors

`internal/detect` — one function per stack (`detect.go`'s `Propose`, `detectors.go`'s per-stack `detect<Name>` functions), each inspecting the project root and returning a proposed archetype plus any config it can infer.

Detectors run on a fresh project during `admin new` *without* a manifest. They produce a **starter `admin.toml`** which the user then reviews and commits. Detectors never run on projects that already have `admin.toml` — the manifest is the source of truth from that moment forward.

Detectors mirror the archetype catalog: hugo (`config.yaml` + `themes/`), docker (`Dockerfile` + existing remote-deploy markers), apple (`.xcodeproj`/`.xcworkspace`), React Native (`package.json` with the RN dependency), go-cli (`go.mod`, no Dockerfile), rust (`Cargo.toml`). Fallback: `simple`.

Adding a new stack = one `detect<Name>` function in `internal/detect/detectors.go` + one archetype directory under `archetypes/`.

## Resolved Design Decisions

### Environment variable interpolation

No global `~/.admin/admin.toml.d/` layer. Cross-project values that vary by host (SSH targets, IP addresses, deploy hosts) and anything secret-adjacent are **never** encoded in `admin.toml`. Instead, the manifest references environment variables and the tool resolves them when a command dispatches.

**Syntax** — `${VAR}` or `${VAR:-default}` inside any string value in `admin.toml`:

```toml
[archetype_config.docker]
ssh_host = "${UNRAID_HOST}"
ssh_user = "${UNRAID_USER:-root}"

[commands.custom]
deploy-staging = { run = "rsync -a ./dist ${STAGING_HOST}:/srv/app", desc = "Deploy to staging" }

[status.prod]
kind = "http"
url  = "${PROD_URL:-https://example.com}"
```

**Resolution rules:**
- Interpolation happens at **dispatch time**, via `internal/manifest.ResolveEnv`, so the same manifest works unchanged across machines with different environments.
- If a referenced variable is unset and has no default, the command fails fast with a clear error before running anything.
- Two names are computed rather than read (`manifest.ComputedVars`), and an exported value still wins over the computed one: `${LAN_IP}`, the address another device on the network reaches this machine at, and `${WORKTREE_SLUG}`, a name for the checkout being dispatched (`internal/slug.FromRoot`: the root's path below `$HOME` with `/` turned into `-`, so `~/Projects/app` is `Projects-app` and its worktree `~/.worktrees/app` is `.worktrees-app`). Writing `launch_args = ["--defaults-suite=app-${WORKTREE_SLUG}"]` keeps two checkouts of one project from sharing saved state.
- `admin env` (`cmd/env.go`) lists every env var the manifest references and shows which are set/unset on the current machine — the discovery mechanism for new contributors. `admin check`'s config-table `${VAR}` check (`cmd/check_env.go`, `cmd/envrefs.go`) flags a reference to an unset variable with no default at validation time.

**Where the env vars themselves live:** out of scope. Users set them via shell rc files, direnv, `.envrc`, or whatever they prefer. `admin.toml` only references them.

### Out of scope

- **Windows CI** — not a priority. No automated cross-platform testing.
