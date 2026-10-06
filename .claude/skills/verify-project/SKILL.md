---
name: verify-project
description: Exercise the admin interpreter from source without disturbing the installed copy at ~/.admin. Use when verifying a change to this repo before committing.
---

# Verify admin-project-tool

This repo builds the `admin` binary that every other project on this machine runs. That
makes verification unusual in one specific way: **the installed copy at `~/.admin` is a
separate thing from the working tree**, and a change you have not installed is invisible
to `admin` anywhere else. Verify against the source entry point, not the installed one.

## The handle

```bash
go run . check .                        # source entry point, this repo's manifest
go run . check /path/to/other/repo      # source entry point, someone else's manifest
go test ./...                            # everything, ~3-4 min
admin build                              # = the check above, via the INSTALLED tool
admin test                               # go test ./... + test_install.sh + test_uninstall.sh
```

`go run .` is the important one. It runs the interpreter **out of the working tree**, so it
reflects the edit you just made. `admin <cmd>` runs `~/.admin/admin`, which still holds
whatever was last installed — use it to confirm the installed copy is stale, never to check
your change.

## Driving a real flow

The honest end-to-end test is resolving a manifest that isn't this repo's — a one-command
manifest proves almost nothing about merge behaviour:

```bash
go run . check <path to a sibling project that uses archetypes>   # e.g. ~/Projects/repo-dash
```

On a good run the archetype/manifest merge is visible in the output and is the part worth
reading (counts vary per project):

```
merge: command 'deploy' overridden by manifest
merge: command 'test' overridden by manifest
merge: command 'vet' overridden by manifest
merge: command 'fmt' overridden by manifest
merge: command 'clean' overridden by manifest
ok: Repo — 10 commands, 9 actions, 1 modules
```

Against this repo's own manifest the line is `ok: admin-project-tool — N commands, N actions, 1 modules`, with N matching `[commands.*]` / `[actions.*]` in `admin.toml`.

A pass is the `ok:` line plus counts that match what the manifest actually declares. A
resolution error names the problem directly (unknown kind, missing action referenced by a
step, unknown guard, a command colliding with the reserved verbs `new`/`check`/`compile`).

## Test suite

```bash
go test ./...
```

`admin test` additionally runs `tests/test_install.sh` and `tests/test_uninstall.sh`, which
touch the real `~/.admin`. Run those deliberately, not as a reflex, and expect the
installed tool to be reinstalled afterwards.

## Gotchas

- **Installing from an unpushed commit embeds a dirty SHA in `~/.admin/VERSION`.** The
  order is edit → commit → push → `bash install.sh`.
- **There is no per-project regeneration.** Installing updates the one interpreter every
  project shares, so a bad install breaks `admin` fleet-wide. Verify with `go run .`
  first; install second.
- `install.sh --link` symlinks instead of copying, which makes working-tree edits live in
  the installed tool. Convenient while iterating, and a trap if you forget it is on.

## Go dashboard / TUI (`internal/tui`, `internal/bd`, `main.go`, `cmd/`)

`internal/tui` (the dashboard), `internal/bd` (the beads reader), `cmd/`, `internal/kinds`,
and the rest of the Go tree share one binary and one test run.

```bash
go build ./...                          # compiles the whole Go tree
go vet ./...                            # static checks
go test ./internal/tui/...              # the dashboard's own package, ~40s
go test ./...                           # everything, ~3-4 min
```

**Render a real frame headlessly** with `--dump-frame` — no TTY, no screenshot. It loads the
dashboard, presses `--keys` in order, waits `--settle`, and prints one frame:

```bash
admin --dump-frame --width 96 --height 30 --settle 4s --keys down
admin --dump-frame --width 80 --height 24 --settle 4s --keys down,down,c,up,enter   # dispatch a target
... | perl -pe 's/\e\]8;;[^\e]*\e\\//g; s/\e\[[0-9;]*m//g'                       # strip OSC 8 + SGR
```

The dashboard opens with focus on the tab bar: press `down` first to put the cursor in the
list. `right`/`left` switch tabs, so never use them to move the cursor; a digit jumps to a
drawn tab (`2` is Workers). A dispatch started by `enter` is a real child that dies when the
dump exits. Check at 80 columns as well as 96.

A behavioral change to `internal/tui` is also proven by:

1. A focused `go test` exercising the exact `Model.Update` transition through its message
   types and package-level test seams (e.g. `internal/bd`'s `runner` var), not by re-deriving
   the behavior from reading the diff.
2. A mutation check: temporarily revert the fix in a scratch copy, confirm the new test
   fails, restore, confirm it passes again. `go test ./internal/tui/...` after restoring
   must match its diff stat against `HEAD` exactly — a test-only change leaves every
   production file's diff untouched.

`seededModel()` in `internal/tui/model_test.go` is the standard fixture for a `Model` with
sane defaults; most `internal/tui` tests build one and mutate the fields they need.

## Log-bridge rendering

Bridged browser lines must be indistinguishable from the wrapped process's own output.
Verify without a browser by POSTing a batch at a live listener:

```bash
curl -s -X POST http://127.0.0.1:9988/log -H 'Content-Type: application/json' \
  -d '[{"timestamp":"2026-08-27T17:19:01.723Z","level":"error","message":"boom\n    at f (a.js:1)","host":"localhost"}]'
```

- Pass: the terminal running the action prints `HH:MM:SS  [ERROR] [CLIENT] boom` with ERROR
  in the same red as the server's own `[ERROR]`, the continuation line indented under it and
  stamped too, and the pinned footer still intact (the line goes through `frame.Write`).
- Post a message shaped like an app's own logger — `"17:36:32.366 [DEBUG] [Messaging] hi"` — and
  the rendered line must read `HH:MM:SS  [DEBUG] [CLIENT] [Messaging] hi`: the app's level, one
  timestamp, `[Messaging]` kept. Two timestamps or an `[INFO]` means `internal/logbridge.UnwrapInnerPrefix` missed it.
- Post a batch with its entries out of order; they must land sorted. Scope tags (`[CLIENT]`,
  `[Messaging]`) must each be a color, stable across reruns — `ADMIN_NO_SCOPE_COLORS=1` turns
  that off, which is the fastest way to confirm a coloring bug isn't a terminal-palette problem.
- The `HH:MM:SS` shown is the *entry's* timestamp converted to local time, not arrival —
  a stamp an hour off means `logformat.Options{ReuseTimestamp: true}` lost the tz conversion.
- Uncolored, ISO-stamped, footer-overwriting lines mean the sink was never installed;
  `internal/keybindings`' `installBridgeSink` is what wires it.

## Tampermonkey provisioning (`admin tampermonkey`)

`tools/tampermonkey` installs `tampermonkey/log-bridge.user.js` into every profile
of Arc/Chrome/Brave through Chromium's managed-storage policy (TM 5.5+ `jsonImport`).
It writes one plist per browser into `/Library/Managed Preferences/$USER/` (root-owned, one
password prompt) — the forced layer. A user-level `defaults write` lands as RECOMMENDED and
`chrome.storage.managed` keeps only MANDATORY (`policy_value_store.cc`). Nothing touches
the browser's databases.

- `admin tampermonkey` (also a step of `admin deploy`) — writes the policy, then quits and relaunches each running browser (`--no-restart` skips that).
- `admin tampermonkey-status` (`--json` via `go run ./tools/tampermonkey --status --json`)
  — per profile: policy in defaults → delivered to the extension (`Managed Extension
  Settings` LevelDB) → script `@meta#` seen in `Local Extension Settings`. Only the
  write-ahead log is read, so a compacted record shows as "not seen".
- Pass: after the browser's next policy refresh (launch, or its periodic reload) and a
  TM service-worker start, `delivered` shows the hash and `installed` lists
  `admin-project-tool log bridge <version>`; the TM dashboard lists it as a normal
  (non-system) script.
- Hash port is tested against TM's algorithm shape in `tools/tampermonkey/tampermonkey_test.go`; a
  mismatch in a live run shows up as `provisioning[…]: Hash mismatch` in TM's
  service-worker console (chrome://extensions → Tampermonkey → service worker).

## Clip kind (`internal/clip`, `kind = "clip"`)

Drive it from a scratch project whose `admin.toml` has a `[clip]` table (see
`docs/guide/clip.md`) with `launch` pointing at a binary built from this tree
(`go build -o <scratch>/admin .`), then run `<scratch>/admin clip --no-publish` from that
project. The run's state is `<out>/clip.json`; `<out>/clip.log` holds every subprocess argv
and exit.

A pass:
- exit 0, and `clip.json` `seconds` equals `period` exactly (the clipper counts frames);
- `seam_diff` under `seam_threshold` with `crossfade: 0` for a choreography that returns to
  its start, and `crossfade` equal to `fade` for one that doesn't;
- five files under `<out>/stills/`; `still-4` beside `still-0` is the seam, so check it
  with `screenshot-checker` at full resolution, never a downscaled contact sheet;
- a `budget` the clip can't meet exits 1 with every quality tried listed in `attempts`.

`go test ./internal/clip` runs the clipper through real ffmpeg on synthetic takes, both the
periodic path and the crossfade path; it skips when ffmpeg is missing.
