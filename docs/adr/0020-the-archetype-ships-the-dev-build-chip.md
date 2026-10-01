# ADR-0020: The archetype ships the DEV-build chip, with no call site

**Status:** Accepted
**Date:** 2026-09-02
**Context:** the first UI code any archetype ships into a consuming app. Archetypes have
until now shipped commands and config only, so how source reaches a project — and how it
stays there — had no precedent to follow.

## Decision

The `apple` archetype owns a DEV-build marker: a small red chip pinned to the
bottom-leading corner of every window a dev build opens, reading `DEV` for the shared dev
app and `DEV·<worker>` for a per-worker build.

Three mechanisms together:

1. **The tool writes the source.** `AdminDevChip.swift` and `AdminDevChipBootstrap.m` are
   `go:embed`ed in `internal/kinds/resources/` and rewritten into the project on **every**
   apple verb — `runAppleVerb` materializes them before dispatching, so no verb bypasses
   it. Local edits and deletions are undone by the next build.
2. **There is no call site.** `AdminDevChipBootstrap.m` calls `install()` from an
   Objective-C `+load`, before `main`, reaching the Swift class by name through
   `NSClassFromString`. No line of app code references the chip.
3. **The manifest names only destinations.** `apple.dev_chip_dirs` lists the source
   directories that receive the files. The tool supplies every byte of content; the
   project supplies the layout the tool cannot guess (ADR-0019).

The dev/prod gate is the runtime bundle identifier — a trailing `.dev` or an infix
`.dev.`, the convention `ADMIN_DEV_BUNDLE_SUFFIX` already establishes. A production build
compiles both files and draws nothing.

## Why

apple-notepad hand-rolled this marker, and commit `347d9c4` deleted it — the view file
and both call sites — as a side effect of unrelated chrome work. Nobody noticed for two
days. On 2026-09-02 that cost real data: unisolated automation drove the shared dev app
and overwrote two of the user's actual notes with test fixtures, with nothing on screen
saying which of three running apps was which.

The lesson is not "restore the file". A marker a project owns is a marker a refactor
deletes, and the deletion is invisible precisely because the marker is decoration. Both
halves of that failure are addressed by removing the condition rather than guarding it:
a file the tool rewrites cannot stay deleted, and a call site that does not exist cannot
be removed. Neither half is a check that can report a false pass.

Three properties from the reference implementation are load-bearing and survive here:

- **Attached at the window, not inside one view.** Every screen carries it — panel,
  takeover, settings sheet, and a system alert's dimmed backdrop. A marker present on one
  surface is one you learn to distrust.
- **It names the bundle suffix.** Per-worker builds are also dev builds, so the bare word
  `DEV` cannot distinguish three simultaneously-running apps — the case that actually
  caused the confusion.
- **It never intercepts input and never affects layout**, so it cannot change what it is
  marking.

## Alternatives rejected

**A build setting plus conditional compilation** (`-D DEV_BUILD`, or a generated
`isDevBuild` constant). Ships no UI, so every project still writes its own chip and every
project's chip is still deletable. That is the status quo that failed.

**A transient file, written before the build and deleted after** — how the dev-icon banner
swap works. Source has to sit in a target's build phase to compile, so a transient file
either is not compiled or churns the pbxproj on every build. The icon swap works because
an asset catalog is a directory whose contents are not enumerated in the project file.

**Shipping it inside a Swift package the project depends on.** Cleanest distribution, and
it still needs a line in the app to reach the window — which is exactly the line that got
deleted. It also puts the chip behind a version bump instead of behind the tool the user
already reinstalls.

**A guard: fail the build when the call site is missing.** Loud rather than silent, so it
satisfies the letter of the requirement. But it leaves the condition in place — app code
that must contain a particular line — and buys a check that can be disabled, skipped on a
platform, or made to pass while the marker is gone. Removing the call site is strictly
better than policing it.

## Consequences

- Two generated files appear in each directory `dev_chip_dirs` names, carrying a
  DO-NOT-EDIT header. They are committed by the project; drift shows as a git diff and is
  repaired by the next build.
- An otherwise pure-Swift target gains one Objective-C file. Swift bans both `+load` and
  `+initialize`, so this is the only way to run code with no reference from app code. The
  file needs no header and no bridging header, so adding it to a target is a drag-and-drop.
- A hand-managed pbxproj needs the two files added to a target once. Projects using
  xcodegen or Xcode's file-system-synchronized groups need nothing.
- `dev_chip_dirs` naming a path that is not a directory is a hard error. A skip there
  would leave the chip silently absent, which is the failure this ADR exists to prevent.
- Scope is the `apple` archetype, macOS and iOS. Other archetypes are untouched; the
  pattern generalizes but nothing else adopts it yet.
