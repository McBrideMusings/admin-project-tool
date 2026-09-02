# ADR-0021: Agent interaction is code, terminals are config

**Status:** Accepted
**Date:** 2026-09-02
**Context:** epic `admin-ysxr`, ticket `admin-ysxr.5` — [ADR-0020](./0020-the-launcher-is-configuration-not-code.md)
moved WHERE a dispatched action lives (a pane, a window) out of the tool and into a
project's own `[terminal]` shell contract. That left one seam still hard-coded: once a
`kind = "workflow"` action's pane exists, `internal/kinds/workflowdispatch.go` and
`internal/tui/agentpoll.go` talked to whatever was inside it by shelling `herdr agent
start`/`prompt`/`get` directly (`internal/shim/agent.go`). A project running any other
multiplexer, or none, got no workflow dispatch at all — the exact ADR-0019 failure shape
ADR-0020 fixed for *placement*, still present for *protocol*.

## Decision

**Split "where an agent lives" from "what is inside it."**

```
[terminal]     — WHERE it lives. Config, shell strings (ADR-0020, unchanged).
                 spawn → handle, attach(handle), kill(handle)

agent.Driver   — WHAT IS INSIDE it and how to talk to it. Go, typed, compiled in.
                 Start(handle, kind, args) / Prompt(handle, text, until) /
                 Read(handle, lines) / Info(handle)
```

**The test for which side a thing lands on: placement is config, protocol is code.**
`[terminal]`'s four keys are a project's own statement about which multiplexer it runs —
two projects can disagree, so it stays in `admin.toml`. Talking to an agent once a pane
exists is a real protocol: it has a vocabulary (start/prompt/read/status) and a state
machine (idle → working → blocked/done), and every harness that vocabulary addresses
speaks it the same way regardless of which project is asking. Expressing a protocol as
shell strings would push per-harness knowledge into every project's manifest instead of
writing it once; a placement decision expressed as compiled Go would do the opposite —
bake one project's terminal choice into every project's binary. ADR-0020 got placement
right by putting it in config; this ADR gets protocol right by keeping it in code.

**No overlap, no new markup.** `attach`/`kill` stay `[terminal]`'s job — a `Driver` never
opens, closes, or focuses a pane itself. The opaque handle `[terminal].spawn` already
returns is the only reference a `Driver` needs; nothing new goes in `admin.toml`.

```go
type Status string // idle | working | blocked | done | unknown

type Driver interface {
    Start(handle, kind string, args []string) error
    Prompt(handle, text string, until []Status) error
    Read(handle string, lines int) (string, error)
    Info(handle string) (Info, error)
}
```

`internal/kinds/workflowdispatch.go` now: `handle = [terminal].spawn` (with an empty
`ADMIN_CMD`, landing the pane at an interactive shell prompt) → `driver.Start(handle,
agentKind)` → `driver.Prompt(handle, target.Prompt, until=[idle, done, blocked])`.
`internal/tui/agentpoll.go` now calls `driver.Info(handle)` in place of shelling `herdr
agent get`.

## Decision log

### 1A: shell the CLI first, not the socket API

**Decided:** the first (and, today, only) `Driver` — `internal/agent/herdr` — shells the
`herdr agent ...` CLI, argv-identical to what `internal/shim/agent.go` did before this
split. This is the smallest change that proves the seam: every call site already worked
this way, so moving *who* makes the call (a `Driver` behind an interface, not a hard-coded
function) is the whole of this ticket's scope.

**Deferred, not rejected:** herdr exposes a socket API at
`crate::session::active_api_socket_path()` with real event subscriptions, which would let
`agentpoll.go` react to a state change instead of polling it every tick. That is a second
`Driver` implementation behind the same interface — a real improvement, and explicitly out
of scope here. It is its own ticket.

### 2A: driver selection by detection, no config key

**Decided:** a driver package registers itself from its own `init()` (`agent.Register`);
`agent.Select()` returns the first registered driver whose availability check passes. No
`admin.toml` key names which driver to use.

**Why not a config key:** today there is exactly one `Driver`. A selection key would be a
config surface with one legal value, existing only to be filled in "for later" — the same
premature-generality ADR-0019 warns against for structure with no real second case yet.
Detection mirrors how `[terminal]`'s own presence-or-absence already decides launcher
opt-in: nothing to configure, until there is something to choose between.

**When a second `Driver` exists:** selection gets designed then, informed by what that
second driver actually needs to disambiguate against the first (a project running two
agent tools at once? a per-workflow override?) rather than guessed at now.

## Consequences

- `internal/shim/agent.go` is deleted whole; `SplitPane`, `RunInPane`, `FocusPane`,
  `ClosePane` are deleted from `internal/shim/herdr.go` — pane placement is `[terminal]`'s
  job now, not a herdr-specific Go function. `Available`, `OpenInHerdr`, and
  `OpenWorktreePaths` stay: those back herdr's *worktree-adoption* feature (`admin
  worktree` opening a child workspace), a different feature this ADR does not touch.
- `livework.Entry.Via` for a workflow target is `"terminal"`, the same value every
  `[terminal]`-launched entry carries — not `"herdr"`. Which tool is actually running
  inside the pane is a `Driver` concern, not something the live-work registry names.
- A `kind = "workflow"` action now requires **both** an agent (`ResolveAgent`) and a
  `[terminal]` launcher to dispatch at all. A project with `[workflow].agent` set but no
  `[terminal]` fails every target with an error naming `[terminal]` specifically, rather
  than silently defaulting to a herdr pane the way it did before this split.
- `internal/tui/agentpoll.go`'s "hard-coded `herdr agent get` call is unaffected" bullet in
  ADR-0020's own consequences is resolved by this ADR: it now calls `driver.Info` behind
  the same seam workflow dispatch uses, bounded by a `context`-aware `InfoContext` escape
  hatch the herdr `Driver` implements rather than a fifth interface method every `Driver`
  would otherwise have to carry for one caller's timeout need.
