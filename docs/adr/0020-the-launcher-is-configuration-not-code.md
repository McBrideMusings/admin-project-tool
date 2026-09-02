# ADR-0020: The launcher is configuration, not code

**Status:** Accepted
**Date:** 2026-09-02
**Context:** epic `admin-ysxr` — today, a `kind = "workflow"` action, a Work-tab worker, and
`admin worktree` each spawn their own agent by shelling out to a hard-coded launcher
(`herdr pane split`, a raw `Terminal.app` AppleScript nudge) baked into Go. A project that
doesn't run Herdr, or runs a different multiplexer, gets none of it. This is failure shape
1 from [ADR-0019](./0019-the-tool-defines-mechanism-admin-toml-defines-structure.md) —
the tool assuming a shipping target the project never asked for — applied to "how a new
terminal gets spawned" instead of "which platform gets built". This ADR is a direct
application of ADR-0019: the launcher a project uses is structure (a project's own
statement about what terminal environment it runs in), not mechanism, so it belongs in
`admin.toml`, never compiled into the tool.

## Decision

**A launcher is a project-declared shell contract, not a Go integration.** A manifest may
declare a `[terminal]` table:

```toml
[terminal]
spawn  = 'id=$(herdr pane split --current --cwd "$ADMIN_CWD" --no-focus) && herdr pane run "$id" "$ADMIN_CMD" && echo "$id"'
attach = 'herdr focus "$ADMIN_HANDLE"'
kill   = 'herdr pane close "$ADMIN_HANDLE"'
status = 'herdr agent get "$ADMIN_HANDLE"'   # optional
```

The contract:

- **No `[terminal]` declared → run inline.** Nothing changes for a project that never
  opts in; the tool has no default multiplexer opinion.
- **`[terminal]` declared → `handle = sh(spawn, env=ctx)`.** `spawn`'s stdout, trimmed, is
  the opaque handle — the tool never parses or interprets it, only stores and replays it.
  The spawn context carries `ADMIN_CMD` (the command line to run), `ADMIN_LABEL`,
  `ADMIN_CWD`, and — when the launch is row-bound (ADR-0018) — `ADMIN_ROW_KIND`,
  `ADMIN_ROW_ID`, `ADMIN_ROW_URL`.
- **`attach(handle)` and `kill(handle)` shell out with `ADMIN_HANDLE` set** to the stored
  handle. Same shape for the optional `status(handle)`.

This ticket (`admin-ysxr.1`) implements only the table's parse and validation — `admin
check` accepts or rejects a `[terminal]` declaration. Nothing dispatches through it yet;
that is `admin-ysxr.2` onward.

## Why

A four-key `spawn`/`attach`/`kill`/`status` contract, expressed as shell strings a project
supplies, is the smallest mechanism that can address every multiplexer without the tool
knowing any of their names. It reuses the `${VAR}`/env-substitution machinery every other
action kind already has, so nothing new has to be built to make it dispatch-time
expandable. And it keeps the tool honest about the ADR-0019 test — could two projects
disagree about how a terminal gets spawned? Obviously: one runs Herdr, one runs tmux, one
runs nothing and wants everything inline. That disagreement has to live in each project's
own manifest.

## Decision log (from the admin-ysxr epic)

### 1. Contract shape: spawn/attach/kill/status, not a single opaque-handle command

**Decided (1A):** four named commands — `spawn` prints a handle, `attach`/`kill` consume
one via `ADMIN_HANDLE`, `status` is optional.

**Rejected: one `spawn` command that prints an opaque handle on stdout, with no separate
attach/kill.** A single command cannot express every multiplexer's real shape: `tmux
new-window -- <cmd>` and macOS's `Terminal.app` both conflate "make a terminal" with
"run something in it" in ways that don't compose with a later "now bring that terminal to
the front" or "now close it" — those need to *address* the terminal that command just
created, which requires the command to hand back something addressable. Splitting spawn
from attach/kill is what makes a handle a real handle instead of a fire-and-forget
side effect.

### 2. Opt-out: a per-action `detach` flag, not a mode key or presence-based switch

**Decided (2A):** `detach = true` on an action (or per multi-target target, inheriting the
default the way `requires`/`urls` already do) opts a specific action out of the project's
declared launcher and runs it inline instead. The `Lifetime` kind-catalog entry is deleted
— a launcher is configuration on top of any action, not a kind of its own.

**Rejected: a `terminal = "<name>"` key against multiple `[terminal.<name>]` tables.**
Every project in scope needs exactly one launcher; a named-table indirection buys
multi-launcher support nobody asked for, at the cost of every action needing to say which
one it means.

**Rejected: letting the mere presence of `[terminal]` decide everything, with no
per-action opt-out, while keeping `Lifetime` as a separate kind.** Two mechanisms for the
same decision (a kind-catalog entry *and* a table) is exactly the kind of duplication
ADR-0019's failure shapes warn about — pick one, and `detach` is the one that composes
with the rest of the manifest's per-action/per-target override vocabulary.

### 3. Built-in default when nothing is configured: the dashboard's hosted pty

**Decided (3A):** when no `[terminal]` is declared, the TUI's dashboard runs the action in
its own hosted pty and renders it directly — this is mechanism, not structure, because
the dashboard needs the file descriptor itself to render output; there is no shell command
a project could supply that would substitute for having the fd. `livework.Entry`'s
`PaneID`/`PTYID` fields collapse into one `Handle` field plus a `Via` discriminator
(hosted pty vs. external launcher) rather than keeping two address kinds and adding a
third.

**Rejected: keeping `PaneID` and `PTYID` as they are and adding `Handle` as a third
address kind.** Three overlapping "where is this thing running" fields is the harder
surface to keep consistent; collapsing to one `Handle` + `Via` is the version that scales
to whatever launcher shape comes next without a fourth field arriving with it.

### 4. The optional fifth key: `status`

`status` is optional because a launcher that supplies no way to poll liveness still gets a
usable spawn/attach/kill contract — the tool just can't tell whether the handle is still
alive without shelling out to `attach`/`kill` and checking their exit code. Declaring it
keeps `agentpoll.go`'s herdr-agent polling generic: today it shells `herdr agent get
"$ADMIN_HANDLE"` directly, hard-coded to Herdr: once a project supplies `status`, that
poll can run *the project's own command* instead of a Herdr-specific one.

## Considered and rejected

**Skip the manifest entirely and keep the launcher a Go integration, generalized behind an
interface (a `Launcher` Go type with a Herdr implementation and a tmux implementation).**
Rejected on the same grounds as every other failure shape in ADR-0019: a compiled interface
still requires the tool to ship one implementation per multiplexer, forever behind
whatever multiplexer a given project has chosen — and a project inventing its own launch
script would have nowhere to put it. Four shell strings in `admin.toml` cover every
multiplexer that exists and every one that doesn't yet, with no new Go code required per
target.

**Make `spawn` return a structured document (JSON) instead of an opaque string.**
Rejected — the tool has no use for structure inside the handle; it only ever passes the
same bytes back as `ADMIN_HANDLE`. Requiring JSON would force every launcher author to
serialize something the tool never deserializes.

## Consequences

- A project that wants dispatched work (`kind = "workflow"` actions, Work-tab workers,
  `admin worktree`) to open in a real terminal pane, rather than the dashboard's hosted
  pty, declares `[terminal]` once and every dispatch site picks it up — no per-feature
  opt-in.
- `internal/kinds`' `Lifetime` entry, and `livework.Entry`'s `PaneID`/`PTYID` split, remain
  as they are until `admin-ysxr.3`/`.4` land — this ticket changes only parsing and
  validation.
- `internal/tui/agentpoll.go`'s hard-coded `herdr agent get` call is unaffected until a later
  ticket wires `status` through it.
