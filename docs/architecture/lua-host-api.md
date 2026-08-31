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

## Output

| Function | Signature | Behaviour |
|---|---|---|
| `info(msg)` | `(string) -> ()` | Prints `msg` in cyan to stdout, appends it (ANSI-stripped) to the active log. |
| `ok(msg)` | `(string) -> ()` | Prints `msg` in green to stdout, appends it to the active log. |
| `warn(msg)` | `(string) -> ()` | Prints `msg` in yellow to stderr, appends `"WARNING: msg"` to the active log. |
| `err(msg)` | `(string) -> ()` | Prints `msg` in red to stderr, appends `"ERROR: msg"` to the active log. |

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

## Target selection

| Function | Signature | Behaviour |
|---|---|---|
| `pick_target(entries[, prompt])` | `({key=string, description=string}[], string?) -> string?` | Prompts for one of `entries` (an ordered array — order is the display order) and returns the chosen key, or `nil` if there was nothing to pick, stdin isn't a TTY, or the user cancelled (`q` or empty input). Runs entirely in-process against the host's stdin/stdout — the property ADR-0010 requires ("no subprocess and no wire protocol"). |
| `only_target(choices, cmd_name)` | `({key=string, description=string}[], string) -> string?` | Returns the sole entry's key when `choices` has exactly one, announcing it via `info` as `"<cmd_name>: <key> is this project's only target"` so a no-prompt run reads as a deliberate skip, not a silently failed picker. Returns `nil` for zero or more than one choice. |

`pick_target`'s picker is a numbered list read from a line of stdin, not the Python original's
raw-mode arrow-key TUI — it satisfies the "same process, no subprocess" requirement and
degrades the same way on a non-TTY, but is a plainer interaction. A richer in-terminal picker
is a follow-up, not a requirement this page's functions fail to meet.

## Capabilities bound from Go

ADR-0010: "Bind capability from Go (JSON, globbing, plist) rather than expecting it from Lua's
stdlib." Each is a small table of functions, not a global function of its own.

| Function | Signature | Behaviour |
|---|---|---|
| `json.encode(value)` | `(any) -> string` | Encodes a Lua table/string/number/boolean/nil to JSON via `encoding/json`. |
| `json.decode(text)` | `(string) -> any` | Parses JSON into the equivalent Lua table/string/number/boolean/nil. |
| `glob.match(pattern[, dir])` | `(string, string?) -> string[]` | `filepath.Glob` rooted at `dir` (default `"."`), returned as an array of matched paths. |
| `plist.read(path)` | `(string) -> table` | Reads an XML-format `.plist`'s top-level `<dict>` into a table. `<true/>`/`<false/>` become booleans, `<integer>`/`<real>` become numbers, everything else (including `<string>`, nested `<array>`/`<dict>`) is read as its raw text. Binary plists and writing are **not implemented** — a body needing either is a follow-up. |

## Configuration

Every Lua body runs with a global `config` table set from its archetype's config (the table
that, in `admin.toml`, lives under a key like `[apple]` and is projected into
`internal/manifest.Manifest.Apple` and siblings — see [ADR-0009](../adr/0009-manifests-reference-archetypes-carry-implementation.md)).
This replaces the Python interpreter's `globals().get("_APPLE_CONFIG")` lookup: the config
reaches the body as a real, nested Lua table (`config.ios_scheme`, `config.nested.field`,
`config.list[1]`), not a name looked up in a shared namespace.

Wiring a specific archetype's config table into `config` for a given action kind is the calling
factory's job (`internal/kinds`), not `internal/luahost`'s — `Host.Exec(source, cfg)` takes
whatever `map[string]any` its caller hands it.

## What is intentionally not here yet

- **Platform verbs** (`build_ios`, `dev_mac`, `deploy_tv` and siblings) — the Apple-specific
  build/deploy logic ported from `admin_lib/apple.py`. These are business logic for one
  archetype, not general host-VM plumbing, and porting `apple.py`'s ~900 lines of
  `xcodebuild`/`simctl`/codesigning orchestration is its own scoped effort. Filed as a
  follow-up rather than guessed at here.
- **Log pruning** (`retain`) — accepted, not enforced (see above).
- **A richer `pick_target` picker** — arrow-key navigation, scrolling for long lists. The
  current numbered-list picker meets the acceptance criterion (in-process, no subprocess) but
  not the original's UI fidelity.
- **Manifest-level archetype config tables reaching a Lua body automatically.** Python's
  interpreter exposes a whole table like `[apple]` to a `kind = "python"` body by convention
  (`_CONFIG_GLOBALS`), because its namespace-builder sees the full resolved Manifest. A Go
  `kinds.Factory` only receives `(name string, action manifest.Action)` — no Manifest — so
  `makeLua` can only expose what the action itself carries inline (`[actions.X.config]`).
  Wiring a project-level table into every `kind = "lua"` action generically needs either a
  wider Factory signature or a second dispatch-time pass; tracked as a follow-up.
