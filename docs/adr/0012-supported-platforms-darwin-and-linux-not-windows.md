# ADR-0012: Supported platforms are darwin and linux; Windows is not a target

**Status:** Accepted
**Date:** 2026-08-31
**Context:** admin-3rgp.41, the one-binary epic

## Decision

`admin` supports **darwin** and **linux**. Windows is not a supported target, and nothing in
this repo should imply otherwise — no CI matrix entry, no doc claiming eventual Windows
support, no comment promising a future `_windows.go` counterpart for a package that doesn't
have one.

darwin is the real target: `admin` exists to drive Apple builds (xcodebuild, simctl,
devicectl, codesign), and every archetype in daily use assumes it. linux currently builds
cleanly and is worth keeping honest, since nothing about the manifest/archetype/Lua model is
Apple-specific — but linux is a "keep it working," not a platform anything was designed around.

`internal/platform` (added alongside this ADR) is the single source of truth both a person and
a build can check: `Supported` names the GOOS/GOARCH pairs, and `TestSupportedPlatformsBuild`
cross-compiles every one of them on every test run.

## What this is not

This is not a claim that Windows *can't* work. `internal/data/data.go` (`unix.Flock`) and
`internal/ports/ports.go` (`unix.SetsockoptInt`, `syscall.Kill`) call POSIX-only syscalls with
no build tag, so `GOOS=windows go build ./...` fails today (admin-3rgp.41), and
`internal/electron` and `internal/metro` were never given a Windows counterpart either. Every
one of those could be split into `_unix.go`/`_windows.go` the way `internal/reload` and
`internal/luahost`'s `apple_stub.go` already are — the pattern is proven and cheap.

The decision is that doing so isn't worth carrying: nobody has asked to run `admin` on
Windows, nobody is testing it there, and a stub that compiles but has never executed is worse
than an honest, documented gap — it invites a future contributor to trust a code path nobody
has verified. If a real Windows user shows up, the fix is exactly the split described above,
scoped to whichever package they hit first.

## What already works and stays

Three packages already carry a real `_unix.go`/`_windows.go` split, and this ADR does not
touch them: `internal/reload`, `internal/keybindings`, `internal/logbridge`, and
`internal/frame` (`winsize_unix.go`/`winsize_windows.go`). Each `_windows.go` stub there
returns an explicit "not supported on Windows" error or a documented no-op rather than
pretending to work — they exist so `go build ./...` doesn't fail on a missing symbol for
packages that happen to be reachable from platform-agnostic code, not to claim the *feature*
those packages implement (interactive reload, the log bridge, terminal resize) runs on
Windows. Removing them would break the linux/darwin split they implement for no benefit.

## Consequence

`GOOS=windows go build ./...` is expected to keep failing on `internal/data`,
`internal/ports`, `internal/electron`, and `internal/metro`. That failure is not a bug to
triage — `internal/platform`'s test suite only exercises the platforms in `Supported`, so a
green `go test ./...` never implies Windows works.
