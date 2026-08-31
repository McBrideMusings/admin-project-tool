# The Lua host API

Settled by [ADR-0010](../adr/0010-lua-and-bash-are-the-authoring-languages.md): an archetype
action body is Bash (`kind = "shell"`, no host namespace) or Lua, running in-process in a Lua
VM embedded in the Go binary. This page documents the surface a Lua body is written against —
every global the host registers into the VM. It is a **versioned interface**: a function
signature here does not change without updating this page in the same commit.

Implementation: [`internal/luahost`](https://github.com/McBrideMusings/admin-project-tool/tree/main/internal/luahost)
(`github.com/yuin/gopher-lua`, pure Go, Lua 5.1, no cgo). `internal/luahost.Host.Exec(source,
config)` is the entrypoint `kind = "lua"`'s factory (`internal/kinds`'s `makeLua`) calls to run
a Lua body — `run` holds the body's source text, exactly as `kind = "python"`'s `run` held a
Python code block before this ADR replaced it. An inline `[actions.X.config]` table on the
action, if present, becomes the `config` global.

A Lua body gets Lua's `base`, `table`, `string` and `math` libraries. It does **not** get `os`
or `io` — anything that touches the process goes through a host function instead, so the
surface stays exactly what this page lists rather than whatever Lua's stdlib happens to expose.

## Arguments

| Global | Shape | Behaviour |
|---|---|---|
| `args` | `string[]` | The CLI arguments the action was invoked with — `args[1]`, `args[2]`, ... (1-indexed, per Lua array convention), `#args` for the count. Mirrors the Python original's bare `args` name (`sys.argv`'s tail). Always present, as an empty table when no arguments were given. |

## Output

| Function | Signature | Behaviour |
|---|---|---|
| `info(msg)` | `(string) -> ()` | Prints `msg` in cyan to stdout, appends it (ANSI-stripped) to the active log. |
| `ok(msg)` | `(string) -> ()` | Prints `msg` in green to stdout, appends it to the active log. |
| `warn(msg)` | `(string) -> ()` | Prints `msg` in yellow to stderr, appends `"WARNING: msg"` to the active log. |
| `err(msg)` | `(string) -> ()` | Prints `msg` in red to stderr, appends `"ERROR: msg"` to the active log. |
| `print(msg)` | `(string) -> ()` | Writes `msg` plus a newline to stdout verbatim — no colour, no log write. For output a caller captures and re-parses (`$(admin run-args)`, `admin diff --format json`) rather than a diagnostic a person reads: `info`/`ok`/`warn`/`err` all wrap `msg` in an ANSI colour code unconditionally, which corrupts a captured string. Added by [admin-3rgp.37](https://github.com/McBrideMusings/admin-project-tool/issues) for the docker archetype's `run-args` action — the Go port of Python's bare `print()` builtin. |

## Log context

| Function | Signature | Behaviour |
|---|---|---|
| `set_log_context(...parts[, {retain=N}])` | `(string..., table?) -> ()` | Names this run's log file — deferred until first actual write, not created here. Parts join with `-` (e.g. `set_log_context("dev", "cf")` → `tmp/dev-cf.<timestamp>.log`). A no-op once a context is already active: the first call to actually write wins, so an outer step-runner's name never clobbers an inner target's. |
| `set_log_context_path(path[, {retain=N}])` | `(string, table?) -> ()` | Same deferral rule, but names an explicit path instead of a timestamped stem. |

`retain` (default 0, meaning "no pruning") is accepted for interface parity with the Python
original's `retain=` kwarg; pruning older same-stem logs is **not implemented** — see
`Host.pruneRetain`'s doc comment. This is a tracked gap, not a silent one.

## Running a command

| Function | Signature | Behaviour |
|---|---|---|
| `run_cmd(cmd[, {dir=path, capture_log=bool}])` | `(string, table?) -> (bool, string)` | Runs `cmd` through `/bin/sh -c` in-process via Go's `os/exec` — no subprocess-of-a-subprocess, no wire protocol. Streams each line to stdout as it arrives (not buffered until exit) and, unless `capture_log = false`, appends it to the active log. Returns `ok` (exit code zero) and the combined captured output. `dir` defaults to the project root. |

Process supervision (a long-running dev-server loop with reload) is explicitly **not** part of
`run_cmd` — that logic lives in the host's own reload/output-pump machinery per the project's
"never re-hand-roll the pump" rule, not in a Lua body.

| `run_group(commands[, {dir=path}])` | `({tag=string, cmd=string}[], table?) -> number` | Runs every `commands[i].cmd` concurrently through `/bin/sh -c`, prefixing each one's output with `"[tag] "` as it streams — the Go port (admin-3rgp.37) of the one archetype body ADR-0010 names as NOT becoming Lua: a multi-process dev loop (forge-engine's workspace mode) that used raw `threading.Lock` + `subprocess.Popen` + `poll`/`wait` in Python. Composes `internal/streams.OutputPump` (one per child, tagging its lines) with this repo's own process/signal handling — not a third hand-rolled pump. Ctrl+C is forwarded to every still-running child, given 5s, then killed. Returns the highest exit code across every child (0 if all exited zero), matching the Python original's `sys.exit(max(rcs))`. |

## Target selection

| Function | Signature | Behaviour |
|---|---|---|
| `pick_target(entries[, prompt])` | `({key=string, description=string}[], string?) -> string?` | Prompts for one of `entries` (an ordered array — order is the display order) and returns the chosen key, or `nil` if there was nothing to pick, stdin isn't a TTY, or the user cancelled (`q` or empty input). Runs entirely in-process against the host's stdin/stdout — the property ADR-0010 requires ("no subprocess and no wire protocol"). |
| `only_target(choices, cmd_name)` | `({key=string, description=string}[], string) -> string?` | Returns the sole entry's key when `choices` has exactly one, announcing it via `info` as `"<cmd_name>: <key> is this project's only target"` so a no-prompt run reads as a deliberate skip, not a silently failed picker. Returns `nil` for zero or more than one choice. |

`pick_target`'s picker is a numbered list read from a line of stdin, not the Python original's
raw-mode arrow-key TUI — it satisfies the "same process, no subprocess" requirement and
degrades the same way on a non-TTY, but is a plainer interaction. A richer in-terminal picker
is a follow-up, not a requirement this page's functions fail to meet.

## Environment

| Function | Signature | Behaviour |
|---|---|---|
| `env(name)` | `(string) -> string?` | Reads a process environment variable, or `nil` if unset. A Lua body gets neither `os` nor `io`, so this is the one narrow read-only exception — most bodies never need it, since embedding `"${VAR}"` directly in a string passed to `run_cmd` lets `/bin/sh -c` resolve it without Lua seeing the value; this exists for the smaller set of bodies that branch in Lua on whether a var is set. |

## Docker run-config rendering and drift

Bound by [admin-3rgp.37](https://github.com/McBrideMusings/admin-project-tool/issues), wrapping
[`internal/dockerrun`](https://github.com/McBrideMusings/admin-project-tool/tree/main/internal/dockerrun)
— a pure, host-independent Go port of admin_lib/docker.py's `[docker_run]` cfg↔argv renderers,
drift comparison, and `[deploy]` file-sync planner. The docker archetype's `run-args`, `deploy`,
and `diff` actions all call through this one table rather than each carrying its own copy of the
field-table renderer, so the cfg→argv and inspect→argv directions (and the declared/last-deploy/
live three-way diff `diff` computes) cannot drift apart the way the original two hand-written
Python functions once did (issue #388).

| Function | Signature | Behaviour |
|---|---|---|
| `dockerrun.build_args_from_cfg(cfg[, env_vars[, env_file_vars]])` | `(table, table?, table?) -> string[]` | Renders a `[docker_run]` table to argv pieces (no `docker run -d --name X` prefix, no image). `env_vars`/`env_file_vars` are `{NAME=VALUE}` tables; omitting `env_vars` falls back to the admin process's own environment (matches `env_vars=None` → `os.environ`). Every `${VAR}`/`${VAR:-default}` reference in a bind-mount host path, port, env value, label, or the `user` field is expanded here; an unresolved reference drops that entry (warned via `warn()`) rather than emitting a literal placeholder. |
| `dockerrun.build_args_from_inspect(inspect)` | `(table) -> string[]` | Translates a decoded `docker inspect` blob (`json.decode`'s output, single container) into the same argv shape, so it can be compared against `build_args_from_cfg`'s output. |
| `dockerrun.compute_drift(target_argv, live_argv)` | `(string[], string[]) -> (string[], string[])` | Diffs two argv-pieces lists, returning `(in_first_only, in_second_only)` as sorted `"flag\tvalue"` strings. Byte-size flags (`--memory` and siblings) are normalized to bytes first so `"24g"` and `"25769803776"` don't read as divergent. |
| `dockerrun.format_drift_report(in_target_only, in_live_only[, refusing])` | `(string[], string[], bool?) -> string` | Renders a drift report for human eyeballs (`refusing` default `true` appends the "rerun with --force" line). Every `-e` entry is masked by variable name (`is_secret_env_name`) before rendering — issue #436/admin-wxz0. |
| `dockerrun.live_argv_for_drift(inspect, image_env, image_labels, image_workdir, image_user, cfg)` | `(table, string[], table, string, string, table?) -> string[]` | Strips noise that's never expressible from TOML (image-baked env/labels, `--network bridge`'s implicit default, an autogenerated `--hostname`/`--mac-address`, a `--workdir`/`--user` that only matches the image's own default) before a drift compare. Passing `cfg = nil` strips every such field unconditionally — the shape `diff`'s last-deploy-vs-live axis uses, since neither side of that comparison has a `[docker_run]` table to declare a field against. |
| `dockerrun.mask_argv_for_display(argv)` / `dockerrun.mask_env_assignment(assignment)` | `(string[]) -> string[]` / `(string) -> string` | Display-only secret masking (issue #436) — never call on the argv actually passed to `docker run` or written to a restore script. |
| `dockerrun.is_secret_env_name(name)` | `(string) -> bool` | The `*_TOKEN`/`*_KEY`/`*_SECRET`/`*_PASSWORD` suffix heuristic every masking function above is built on. |
| `dockerrun.default_container_name(image)` / `dockerrun.restore_image_from_inspect(inspect, image)` | `(string) -> string` / `(table, string) -> string` | Derives a container name from an image reference (`"ghcr.io/me/app:latest"` → `"app"`); picks the SHA a restore script should reference over the tag, when the container is already running. |
| `dockerrun.provision_commands(cfg[, env_vars])` / `dockerrun.health_check_shell(cfg[, env_vars])` | `(table, table?) -> string[]` / `(table, table?) -> string?` | Shell snippets for bind-mount dir provisioning (`mkdir`/`chown`) and a post-deploy health-check poll, both expanding `${VAR}` the same way `build_args_from_cfg` does. |
| `dockerrun.mount_env_deps(cfg)` | `(table) -> string[]` | Every env-var name an `optional_mounts`/`bind_mounts` host path references — `deploy` uses this to refuse a remote deploy that would otherwise resolve a mount path against the wrong machine's environment (see `remote_env_file` below). |
| `dockerrun.parse_env_text(text)` | `(string) -> table` | Parses `KEY=value` env-file text (`#` comments, blank lines skipped; values are **not** unquoted, matching `docker --env-file`'s own literal handling). |
| `dockerrun.build_deploy_sync_plan(cfg, env_vars, is_local, dry_run)` | `(table, table?, bool, bool) -> (table?, string[])` | Resolves a `[deploy]` table into a sync plan — `src`/`dest`/`target`/`is_remote`/`host`/`rsync_cmd`/`chown`/`post_sync_cmd` — or `nil` plus a list of user-facing error strings (missing `src`/`dest`, an unknown key). This is the **only** implementation of a `[deploy]` sync in the tool, so `chown` and `post_sync` (admin-9bex) can't be silently dropped by a second, more-generic sync path the way they could when `admin_lib/interp.py`'s `_make_sync` existed alongside `deploy_files`. |

`[docker_run]`'s `remote_env_file` (a path on the *target*, read over ssh) is not itself a
`dockerrun` function — a Lua body reads it with `run_cmd("ssh ... cat ...")` and parses the result
with `parse_env_text`, then passes it as `build_args_from_cfg`'s `env_vars` argument. This keeps
`dockerrun` itself free of ssh/network I/O, matching the rest of this table's "pure" functions.

## React Native — Metro

Bound by [admin-3rgp.37](https://github.com/McBrideMusings/admin-project-tool/issues), wiring
`internal/metro` (admin-3rgp.14's self-contained Go port of `admin_lib/metro.py`) into a Lua
table for the first time — that package's own doc comment named this exact gap.

| Function | Signature | Behaviour |
|---|---|---|
| `metro.ensure(app_dir[, {port=, log_file=, pid_file=}])` | `(string, table?) -> bool` | Starts Metro in `app_dir` unless one is already answering `/status` on `port` (default `8081`). Returns whether it started a new process. `log_file`/`pid_file` default under the active log directory (`tmp/metro.log`, `tmp/metro.pid`). Raises a Lua error if a freshly started Metro exits immediately or never answers within 60s — the same "must not continue toward a build that would only show a red screen" contract the Python original enforced with `RuntimeError`. |
| `metro.stop([{port=, pid_file=}])` | `(table?) -> bool` | Stops a Metro started by `ensure`, returning whether one was actually stopped. |
| `metro.running([port])` | `(number?) -> bool` | True if a Metro bundler is answering `/status` on `port` (default `8081`). |

## Capabilities bound from Go

ADR-0010: "Bind capability from Go (JSON, globbing, plist) rather than expecting it from Lua's
stdlib." Each is a small table of functions, not a global function of its own.

| Function | Signature | Behaviour |
|---|---|---|
| `json.encode(value)` | `(any) -> string` | Encodes a Lua table/string/number/boolean/nil to JSON via `encoding/json`. |
| `json.decode(text)` | `(string) -> any` | Parses JSON into the equivalent Lua table/string/number/boolean/nil. |
| `glob.match(pattern[, dir])` | `(string, string?) -> string[]` | `filepath.Glob` rooted at `dir` (default `"."`), returned as an array of matched paths. |
| `fs.read(path)` | `(string) -> string?` | Reads a whole text file, or `nil` if it can't be read. The general "a body needs a file's contents" capability where `plist.read`/`glob.match` don't apply. |
| `plist.read(path)` | `(string) -> table` | Reads an XML-format `.plist`'s top-level `<dict>` into a table. `<true/>`/`<false/>` become booleans, `<integer>`/`<real>` become numbers, everything else (including `<string>`, nested `<array>`/`<dict>`) is read as its raw text. Binary plists and writing are **not implemented** — a body needing either is a follow-up. |

## Configuration

Every Lua body runs with a global `config` table, built by `internal/kinds.makeLua`
(admin-3rgp.43) in two layers:

1. Every non-empty manifest-level archetype config table — the ones that, in `admin.toml`, live
   under a key like `[apple]` or `[go]` and project into `internal/manifest.Manifest.Apple`,
   `.Go`, and siblings (see [ADR-0009](../adr/0009-manifests-reference-archetypes-carry-implementation.md))
   — is placed under its own admin.toml name: `config.go`, `config.apple`, `config.server`, and
   so on (`internal/kinds.archetypeConfigTables`). Every table is exposed unconditionally to
   every Lua body, regardless of which kind is running — the same shape as the Python
   interpreter's `_CONFIG_GLOBALS` (`_APPLE_CONFIG`, `_GO_CONFIG`, ...), which likewise injected
   every table into the exec namespace unconditionally and let a body read whichever name it was
   authored against. A `kinds.Factory` carries the full resolved `*manifest.Manifest`
   (admin-3rgp.43's signature change — see `internal/kinds/registry.go`'s `Factory` doc comment),
   so `makeLua` can reach any of these tables without a per-action record of which archetype an
   action came from (`manifest.Action` is a bare `map[string]any` with no such provenance field).
2. An optional inline `[actions.X.config]` table is merged on top, at the top level, so an
   action can still pass its own ad hoc values (`config.value`) the way the original single-table
   design worked. An inline key wins on collision with a domain-table name (an inline key
   literally named `go` would shadow `config.go`).

A body reads a nested field the normal way: `config.go.binary`, `config.apple.ios_scheme`,
`config.nested.field`, `config.list[1]`. This replaces the Python interpreter's
`globals().get("_APPLE_CONFIG")` lookup with a real, nested Lua table under one predictable
global rather than N string-prefixed globals.

Wiring these tables into `config` is entirely `internal/kinds`' job — `internal/luahost.Host.Exec(source,
cfg)` is unchanged by admin-3rgp.43 and still just takes whatever `map[string]any` its caller
hands it as the `config` global.

## Apple platform verbs

Bound by [admin-3rgp.38](https://github.com/McBrideMusings/admin-project-tool/issues), ported
from `admin_lib/apple.py`. Registered only on darwin builds (`apple_register_darwin.go`); a
non-darwin build gets a no-op `registerApple` (`apple_stub.go`) and the global `apple` table is
simply absent — the same "undefined global" a body gets for typoing any other host function
name, rather than a table of functions that all error. This table itself is a **partial** port —
the OS-facing primitives (xcodebuild, simctl, devicectl, codesign, log parsing), pbxproj parsing,
icon backup/dev-banner-swap/restore, and glyph-based fresh icon generation are bound — see "What
is intentionally not here yet" below for what remains unbound. Continuous dev-loop supervision and
attached device console output, this table's other historical gap, are covered too, but as the Go-side
`kind = "apple"`/`kind = "apple-dev-loop"` action kinds rather than more entries in this table —
see "What kind=\"apple\"/kind=\"apple-dev-loop\" cover" just below the table.

All error paths that the Python original reported via `warn()`/`err()` mid-call are relayed the
same way here (the Lua binding calls `h.Warn`/`h.Err`), so a body sees the identical explanatory
text, not just a bare `nil`.

| Function | Signature | Behaviour |
|---|---|---|
| `apple.sim_platforms()` / `apple.device_platforms()` | `() -> string[]` | The registered simulator/device platform keys (`"ios"`, `"tv"`), sorted. |
| `apple.log_predicate(process, subsystem[, level])` | `(string, string, string?) -> string` | A macOS `log stream` predicate for level `"app"` (default), `"debug"`, `"system"`, or `"verbose"`. |
| `apple.xcode_is_signed_out(log_text)` | `(string) -> bool` | True when `log_text` shows a build that failed for lack of a signed-in Apple ID. |
| `apple.app_store_connect_key_rejected(log_text)` | `(string) -> bool` | True when `log_text` shows xcodebuild refusing an `-authenticationKey*` credential outright. |
| `apple.diagnose_build_failure(log_path)` | `(string) -> ()` | Reads `log_path` and `warn()`s known-fixable remediation (a locked login keychain, a signed-out Xcode). |
| `apple.app_store_connect_auth_flags()` | `() -> string[]` | The `-authenticationKey*` xcodebuild flags when `APP_STORE_CONNECT_KEY_PATH`/`_KEY_ID`/`_ISSUER_ID` are all set and the key file exists on disk, else `{}`. |
| `apple.find_app(pattern)` | `(string) -> string?` | Globs `pattern` (`~` expanded) and returns the newest match by mtime, or `nil`. |
| `apple.clean_derived_data(project_name)` | `(string) -> ()` | Removes every `~/Library/Developer/Xcode/DerivedData/<project_name>-*` directory. |
| `apple.resolve_signing_identity([selector])` | `(string?) -> string?` | Resolves a code-signing identity's SHA-1 hash from the keychain; `selector` is a case-insensitive substring of the identity description. Warns and returns `nil` on no match or an ambiguous one. |
| `apple.sign(path[, {identity=, identifier=, deep=}])` | `(string, table?) -> string?` | Code-signs `path` with a stable `identifier` so TCC grants survive a rebuild; returns the identity used, or `nil` (with `err()`) on failure. |
| `apple.dev_macos_postprocess(built_app_path, dev_product_name, dev_display_name[, sign_identity])` | `(string, string, string, string?) -> string` | Renames a Debug `.app`, patches `CFBundleName`/`CFBundleDisplayName`, re-signs; returns the renamed path. |
| `apple.target_build_dir({project=, scheme=, config=, destination=, extra_settings=, allow_provisioning=})` | `(table) -> string?` | Asks xcodebuild for `TARGET_BUILD_DIR` — the exact directory a build wrote into, rather than a DerivedData glob (which is keyed by absolute path and multiplies per git worktree). |
| `apple.xcodebuild({project=, scheme=, config=, destination=, action=, extra_settings=, allow_provisioning=})` | `(table) -> (bool, string)` | Runs xcodebuild with grep-filtered output under the per-checkout build lock, full log at `<LogDir>/admin-xcode-build.log`. Retries once on a rejected App Store Connect key, and once on a signed-out Xcode when `run_cmd`'s stdin is an interactive terminal (prints instructions, waits for Return). Returns `ok` and the filtered output. |
| `apple.find_simulator(device_name)` | `(string) -> string?` | Finds a simulator by exact name, preferring a booted instance. |
| `apple.resolve_simulator(platform)` | `(string) -> (string?, string?)` | Picks the best simulator for `"ios"`/`"tv"`: booted first, else newest available. Returns device id and name. |
| `apple.boot_simulator(device_id)` | `(string) -> ()` | Issues `simctl boot` and opens Simulator.app. Returns once boot is requested, not once it completes. |
| `apple.ensure_booted_simulator(platform)` | `(string) -> (string?, string?)` | Resolves a simulator and blocks until it has finished booting. |
| `apple.install_and_launch_sim(device_id, app_path, bundle_id[, launch_args])` | `(string, string, string, string[]?) -> ()` | Installs and launches, passing `launch_args` as `UIApplication` launch arguments. |
| `apple.terminate_sim_app(device_id, bundle_id)` / `apple.is_sim_app_running(device_id, bundle_id)` | `(string, string) -> ()` / `(string, string) -> bool` | |
| `apple.app_bundle_identifier(app_path)` | `(string) -> string?` | Reads `CFBundleIdentifier` via PlistBuddy (handles binary Info.plist, unlike `plist.read`). |
| `apple.find_connected_device(platform[, device_type])` | `(string, string?) -> (string?, string?, string?)` | Finds a connected physical device via devicectl; returns id, name, udid. `device_type` (e.g. `"iPhone"`) disambiguates when more than one candidate of the platform is connected. |
| `apple.install_on_device(device_id, app_path)` | `(string, string) -> bool` | |
| `apple.launch_on_device(device_id, bundle_id[, env])` | `(string, string, table?) -> bool` | One-shot launch (terminates any existing instance first); `env` is a string→string table of launch environment variables. Does **not** attach console output — see below. |
| `apple.terminate_device_app(device_id, bundle_id)` / `apple.is_device_app_running(device_id, bundle_id)` | `(string, string) -> ()` / `(string, string) -> bool` | |
| `apple.find_project_path([explicit])` | `(string?) -> string?` | Returns `explicit` unchanged if given, else the first `*.xcworkspace`/`*.xcodeproj` in the process's cwd, else `nil`. |
| `apple.read_pbxproj(project_path)` | `(string) -> table` | Parses `project_path`'s `project.pbxproj` (an OpenStep ASCII plist) into a table via `plutil -convert json`, cached per path for the process's lifetime. A `.xcworkspace` resolves to a sibling `.xcodeproj` of the same base name when one exists. Returns `{}` on any failure (missing file, no sibling project, a `plutil` error) — callers fall back to manifest-supplied values. |
| `apple.xcode_target_settings(project_path, scheme[, configuration])` | `(string, string, string?) -> table` | The `buildSettings` table for the `PBXNativeTarget` named `scheme`, at `configuration` (default `"Debug"`) — falling back to the target's first build configuration when the named one isn't found. `{}` on any resolution failure. Only sees settings written into the pbxproj itself; `.xcconfig` values and `$(VAR)` substitutions come back as literals. |
| `apple.xcode_setting(project_path, scheme, key)` | `(string, string, string) -> string?` | `xcode_target_settings(project_path, scheme).key` (always at `"Debug"`), or `nil` if missing, non-string, or an unresolved `$(...)` substitution the caller can't act on. |
| `apple.backup_icons(icon_dirs, backup_dir)` / `apple.restore_icons(icon_dirs, backup_dir)` | `(table, string) -> ()` | Copies every `*.png` between each `icon_dirs[label]` appiconset directory and `backup_dir/label`. `icon_dirs` is `{label = path, ...}` (e.g. `{mac = "...", ios = "..."}`); a missing source directory is skipped, not an error. `restore_icons` removes `backup_dir` when done. |
| `apple.backup_tv_icons(brandassets_dir, backup_dir)` / `apple.restore_tv_icons(brandassets_dir, backup_dir)` | `(string, string) -> ()` | Same shape as `backup_icons`/`restore_icons`, but for a tvOS `.brandassets` catalog — a directory tree, not a flat set of PNGs, so it's copied whole rather than by filename. |
| `apple.swap_dev_icons_banner(icon_dirs)` | `(table) -> bool` | Overlays a red diagonal dev banner (stdlib-rendered — see below) onto every `*.png` in each `icon_dirs[label]`, in place, flattening to opaque RGB so macOS renders the icon full-bleed. Binds only `swap_dev_icons`' "overlay the existing icon art" path (`admin_lib/apple.py`'s `has_glyph=False` branch) — see below for what's not bound. Returns `false` if any file failed to process. |
| `apple.apply_tv_dev_banner(brandassets_dir)` | `(string) -> bool` | Overlays the same dev banner onto an *existing* tvOS `.brandassets` tree in place — the overlay-in-place counterpart to `generate_tv_icons`'s `dev=true`, for a project shipping its own tvOS art rather than a glyph config. Only the topmost `Front` layer of each imagestack and the Top Shelf images are touched; `Back`/`Middle` and the art itself are left untouched. Unlike `swap_dev_icons_banner`, this does **not** flatten to opaque RGB — the Front layer keeps its alpha channel so it still floats transparently over `Middle`/`Back`. Returns `false` (after warning via `h.Err`) if a file other than a plain decode failure could not be processed; a directory that doesn't exist is skipped, not an error. |
| `apple.swap_dev_icon_composer(icon_path, backup_dir)` / `apple.restore_icon_composer(icon_path, backup_dir)` | `(string, string) -> bool` / `(string, string) -> ()` | Injects (or removes) an opaque `DEV`-banner group into an Icon Composer `.icon` bundle's `icon.json`, rendering the banner asset sized to `Assets/icon-raw.png` (or 1024 if absent). `swap` backs up the pristine `icon.json` to `backup_dir` first and returns `false` (no error) if `icon.json` is missing — not an Icon-Composer-shaped bundle. `restore` prefers the byte-exact backup, else strips the banner group from whatever's on disk, and removes the generated banner asset and `backup_dir` either way. |
| `apple.generate_icons(sizes, output_dir[, {bg_color=, glyph_svg=, glyph_text=, glyph_color=, glyph_scale=, dev=, filename_format=}])` | `(number[], string, table?) -> bool` | Renders a flat mac/iOS appiconset ladder — one PNG per entry in `sizes` — into `output_dir`, always flattened to opaque RGB (ADR-0007). Precedence is `glyph_svg` (a path to an `.svg` file) over `glyph_text` over a plain `bg_color` square; `dev` overlays the same stdlib-rendered banner `swap_dev_icons_banner` uses. `glyph_scale` (default `0.55`) is the glyph's fraction of the icon's shorter side. `filename_format` defaults to `"{size}.png"`. Returns `false` after warning (via `h.Err`) if any size fails to render — a bad `glyph_svg` path or malformed color aborts the whole call, matching the Python original's "warn, build continues without custom icons" contract, not a crash. |
| `apple.generate_tv_icons(brandassets_dir[, {bg_color=, glyph_svg=, glyph_text=, glyph_color=, glyph_scale=, dev=}])` | `(string, table?) -> bool` | Generates a complete tvOS `.brandassets` catalog (both `App Icon` imagestacks' Front/Middle/Back layers, both Top Shelf image sets, and every `Contents.json`) from the same glyph inputs `generate_icons` takes. Always overwrites `brandassets_dir`. The `Back` layer of every imagestack is always opaque (tvOS errors on a transparent bottom layer); `Middle` is always a transparent spacer; only `Front` and the Top Shelf images carry the glyph and the `dev` banner. Returns `false` after warning on failure, same contract as `generate_icons`. |

## What kind="apple"/kind="apple-dev-loop" cover (admin-3rgp.12)

Continuous dev-loop supervision (`dev_mac`/`dev_ios`/`dev_tv`/`dev_device`) and attached device
console output — the two items this section used to list as deferred — are ported, but **not**
as `apple.*` Lua bindings. They live in `internal/kinds` (`apple_darwin.go`,
`appledevloop_darwin.go`) as the Go implementation of `kind = "apple"` (one verb per `run` value —
`build_mac`, `dev_ios`, `deploy_tv`, `clean_ios`, ...) and `kind = "apple-dev-loop"` (the same
target-picker shape as `kind = "multi-target"`, registered to the same factory, classified
`LongRunning` so it dispatches into its own herdr pane). A Lua archetype body never calls these
directly — they're a manifest-driven action kind, the same shape `docker-dev`/`vite-dev` already
are — composing:

- `internal/reload`'s caller-owned shape (`SetupReload`/`PollForReload`/`IsRebuildRequested`/
  `ClearRebuildRequest`) for the "run the whole build-install-launch cycle again" signal.
- `internal/streams.OutputPump` for the attached log/console stream.
- The already-bound primitives below (`apple.xcodebuild` et al., called directly from Go as
  `luahost.XcodebuildFiltered` and siblings — no Lua VM involved) for every OS-facing call, plus
  two new Go-only starters for the long-running streams the bound primitives don't cover:
  `luahost.StreamMacLog` (`log stream` / `simctl spawn ... log stream`) and
  `luahost.StartDeviceConsole` (`devicectl device process launch --console`).

Real-machine verified (a genuine multi-target `.xcodeproj`, this ticket's pass): `build_mac`,
`build_ios` (simulator auto-resolve), `clean_ios`, and a full `dev_mac` cycle — build, launch,
attach `log stream`, detect the launched app dying, tear down and return with no error. Not
exercised end to end this pass: `dev_ios`/`dev_tv` (needs a booted simulator, not run against
one), `dev_device`/`deploy_ios`/`deploy_tv` (need a paired physical device), and anything reading
App Store Connect credentials (no key configured in this environment).

Deliberately NOT ported by kind="apple" either — same three items admin-3rgp.38 already deferred,
still open:

- The dev/prod bundle-suffix convention layer (`_compose_dev_settings`, `_entitlements_paths`,
  `_target_needs_provisioning_profile`) and the mac dev-rename/re-sign postprocessing
  (`dev_macos_postprocess`) it would feed — a dev build here builds with the project's own
  signing settings, unpatched. Observed live in this pass: a project whose
  `PRODUCT_BUNDLE_IDENTIFIER` is composed via an `.xcconfig` variable
  (`$(SCRATCHPAD_BUNDLE_ID_SUFFIX)`) gets that substitution back as a literal string in the
  resolved log predicate — `apple.xcode_setting`'s own documented limitation, not a bug in the
  dev loop, but a concrete case where the missing bundle-suffix layer would matter.
- Dev-icon-swap hooks (`build_icon_hooks`) around each build — `apple.backup_icons`/
  `apple.swap_dev_icons_banner`/`apple.restore_icons` are bound and callable, just not wired into
  a pre/post-build hook here yet.
- App Store Connect device registration (`ensure_device_registered`) — lives in
  `admin_lib/appstoreconnect.py`, a separate module this ticket does not port; a device deploy
  that would need it still reports Apple's own "team has no devices" error, just not pre-empted.

- **Xcode-project introspection for the dev/prod bundle-suffix scheme** (`_compose_dev_settings`,
  `_entitlements_paths`, `_target_needs_provisioning_profile`) — derives `ADMIN_DEV_BUNDLE_SUFFIX`
  and decides whether a target needs a real provisioning profile, built on top of the pbxproj
  parsing that *is* now bound (`apple.read_pbxproj` / `apple.xcode_target_settings` /
  `apple.xcode_setting`). This layer is project-specific convention-following over what those
  primitives return, not a further parsing capability; a Lua body can still pass a fully composed
  `extra_settings` string into `apple.xcodebuild` today, just not have this derivation done for it.
- **Log pruning** (`retain`) — accepted, not enforced (see above).
- **A richer `pick_target` picker** — arrow-key navigation, scrolling for long lists. The
  current numbered-list picker meets the acceptance criterion (in-process, no subprocess) but
  not the original's UI fidelity.

Manifest-level archetype config tables reaching a Lua body automatically — the gap this section
used to list here — is closed by admin-3rgp.43: see "Configuration" above.
