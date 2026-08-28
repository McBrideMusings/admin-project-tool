# ADR-0010: Lua and Bash are the two archetype authoring languages

**Status:** Accepted
**Date:** 2026-08-28
**Supersedes:** the `kind = "python"` half of [ADR-0006](0006-interpreter-mode.md)
**Context:** [ADR-0009](0009-manifests-reference-archetypes-carry-implementation.md), the one-binary epic

## Decision

An archetype action body is written in **Bash** or **Lua**, and nothing else.

- **Bash** — `kind = "shell"`. No host namespace. Free, already works, covers most actions.
- **Lua** — a body with access to the host API. Runs **in-process**, in a Lua VM embedded in
  the Go binary, with host functions registered directly into the Lua state.

`kind = "python"` and `kind = "python-script"` are **deleted**. No Python is shipped, invoked,
or authored anywhere in the tool or its archetypes.

## Why the previous answer stopped being correct

`kind = "python"` was not a language preference. It was a consequence of the interpreter being
written in Python: a body could be `exec()`'d straight into the interpreter's own globals, so
the shared namespace cost nothing. ADR-0006 records exactly that — "`exec()` of
manifest-supplied Python in the interpreter's namespace."

Porting the interpreter to Go inverts that. Python becomes the **only** candidate that needs a
subprocess, a stdio wire protocol, a generated stub preamble, and an interactive callback that
drives the terminal from the parent while the child blocks. It also makes `python3` a runtime
dependency of a binary whose headline decision was to have none — the same epic supersedes
ADR-0001 specifically to get a dependency-free artifact.

The property that made Python right is the property being deleted.

## Why Lua

**It removes the boundary instead of guarding it.** There is no protocol, no preamble, and no
subprocess. `pick_target` is a Go function registered into the Lua state. The RPC design this
ADR replaces was machinery whose entire purpose was to survive a process boundary that Lua
does not create.

**It keeps `go install` honest.** A pure-Go Lua VM needs no cgo and cross-compiles, so the
binary runs on a machine with no Python, no Node, and no toolchain of its own.

**It matches what the bodies actually do.** Measured across the 14 extracted archetype bodies
(567 lines) at the time of this decision:

| What they call | Count |
|---|---|
| Host API — `err` 29, `info` 23, `ok` 8, `set_log_context` 7, `pick_target` 7, `only_target` 4, `run_cmd` 4, `warn` 4, plus platform verbs | ~130 |
| Python stdlib | `sys.exit` 32, a few dict `.get`, `os.getcwd`, `shlex.quote`, `shutil.rmtree` |
| `json`, `plistlib`, `urllib`, `os.walk` | 0 |

The bodies are thin orchestration over a host API. They need control flow, strings, tables,
and a way to call the host. They do not need a large standard library — which is precisely
Lua's profile. A small stdlib stops being a weakness once the host is in the same process:
JSON, globbing and plist reading are bound in as Go functions rather than reimplemented.

**It is the standard choice for this role.** nginx, redis, neovim and wireshark all embed Lua
for user-authored extension.

## Rejected

**Python over subprocess + stdio RPC.** Better known, and a `python3` dependency is not a hard
ask on a developer machine. Rejected because it reintroduces a runtime dependency the epic
exists to remove, and because the entire RPC apparatus is cost paid to cross a boundary that
choosing differently deletes outright.

**Starlark (`go.starlark.net`).** Python syntax *and* pure-Go embedding — on paper it wins both
arguments at once. Rejected because it is designed for hermetic, deterministic, side-effect-free
build configuration, which is the opposite of spawning processes and prompting a human. The
restricted control flow (no `while`) collides with bodies that already use it.

**Supporting several interpreted languages.** admin is a macro layer; a project brings its own
Node, bun, cmake or toolchain. Being able to author *admin's own* actions in any language is
not a capability worth its cost. One free language and one embedded language is the whole set.

## Consequences

- The 14 archetypes are rewritten in Lua. They are not carried over byte for byte — the epic's
  "their scripts stay as they are" no longer holds, and the conversion done under
  `admin-3rgp.4` is superseded rather than kept.
- The three-way `run` ambiguity that produced this decision disappears with the kind. There is
  no inline-source-vs-script-path question to answer, because there is no `python` kind.
- The host API becomes an explicit, named surface — the set of Go functions registered into the
  Lua state — instead of whatever happened to be in the interpreter's globals. That surface is
  now a versioned interface and needs to be documented.
- `admin-3rgp.30` strengthens: no Python remains in the repository at all, not merely outside
  archetype payloads.
- One current body performs process supervision with `threading` + `subprocess.Popen` +
  `poll`/`wait`. That moves into the host, where the project's own rules already say reload and
  output-pump logic must live — it does not become Lua.
- Choice of Lua VM library and version is an implementation decision, not settled here.
  `gopher-lua` (pure Go, Lua 5.1) is the leading candidate and must be confirmed against the
  no-cgo and cross-compile requirements before adoption.
