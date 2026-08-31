# ADR-0015: Pty allocation uses `github.com/creack/pty`

**Status:** Accepted
**Date:** 2026-08-31
**Context:** admin-3rgp.61, the hosted-pty dashboard

## Decision

The Go binary allocates pseudo-terminals with **`github.com/creack/pty`**, pinned at
`v1.1.24`. It is a direct dependency in `go.mod`.

Two operations are all this repo needs from it: `pty.Start` to run a child on a new pty, and
`pty.Setsize` to size that pty to the region the dashboard will render it in.

## Why a pty at all

`admin-3rgp.61` makes the dashboard host a Command instead of surrendering the terminal to it.
Today activating a command row releases the alt-screen for the whole run, because a dispatched
runner writes to the real terminal and its output — plus the pinned footer's scroll region —
would corrupt the dashboard's own rendering. Giving the child its own pty is what lets both
render at once: from the child's point of view the pty *is* its terminal, so
`internal/frame` writes its scroll region and footer into the pty unchanged and no second
footer implementation appears.

## Why not the standard library

Go's standard library has no pty support of any kind. There is no stdlib-only version of this
decision to prefer.

## Why not hand-roll it on `golang.org/x/sys/unix`

`golang.org/x/sys` is already a direct dependency, so this looked like the zero-new-module
option. It is not: `x/sys/unix` exposes no `Openpt`, so the whole sequence would be
hand-written ioctls, per platform — darwin needs `TIOCPTYGRANT` / `TIOCPTYUNLK` /
`TIOCPTYGNAME`, linux needs `TIOCGPTN` / `TIOCSPTLCK` — plus our own `Setsize`. That is two
implementations of a thing `creack/pty` has carried under test since 2015, written to save one
line in `go.mod`.

## Why ADR-0001 does not forbid this

[ADR-0001](0001-stdlib-only.md) is about **Python**: the generator and the generated `./admin`
script, whose portability guarantee is what forces stdlib-only on `admin_lib/`. It says nothing
about Go, and the Go binary already carries thirteen direct dependencies — `bubbletea`,
`lipgloss`, `cobra`, `gopher-lua`, `x/image` among them.

This is recorded because an agent reading `CLAUDE.md`'s "Stdlib only (with one narrow
carve-out)" heading has already once concluded, incorrectly, that it barred a Go dependency and
halted on it. The principle is real; its scope is Python.

## Why darwin+linux is complete coverage, not a partial port

[ADR-0012](0012-supported-platforms-darwin-and-linux-not-windows.md) rules Windows out as a
target. `creack/pty` covers darwin and linux, which is every platform in
`internal/platform.Supported`. ConPTY is a wholly separate API and there is nothing here to
stub for it — `internal/frame`'s `winsize_windows.go` is a build stub for a package reachable
from platform-agnostic code, not a promise that terminal resize works on Windows.

## Consequence

`go.mod` gains a fourteenth direct dependency. Anything else in this repo needing a pty uses
this one rather than adding a second — including the terminal view over a hosted pty
(`admin-3rgp.65`), which renders that pty's bytes with `github.com/charmbracelet/x/vt` and
allocates nothing itself.
