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
python3 ./admin-run check .                    # source entry point, this repo's manifest
python3 ./admin-run check /path/to/other/repo  # source entry point, someone else's manifest
.venv/bin/pytest tests/ -q                     # 961 tests, ~67s
admin build                                    # = the check above, via the INSTALLED tool
admin test                                     # pytest + test_install.sh + test_uninstall.sh
```

`python3 ./admin-run` is the important one. It runs the interpreter **out of the working
tree**, so it reflects the edit you just made. `admin <cmd>` runs `~/.admin/admin`, which
still holds whatever was last installed — use it to confirm the installed copy is stale,
never to check your change.

## Driving a real flow

The honest end-to-end test is resolving a manifest that isn't this repo's — a one-command
manifest proves almost nothing about merge behaviour:

```bash
python3 ./admin-run check <path to a sibling project that uses archetypes>   # e.g. ~/Projects/repo-dash
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
.venv/bin/pytest tests/ -q
```

Expect roughly a thousand tests in about a minute. Use the repo's own `.venv/bin/pytest` —
a bare `pytest` picks up whatever is on PATH.

`admin test` additionally runs `tests/test_install.sh` and `tests/test_uninstall.sh`, which
touch the real `~/.admin`. Run those deliberately, not as a reflex, and expect the
installed tool to be reinstalled afterwards.

## Gotchas

- **Installing from an unpushed commit embeds a dirty SHA in `~/.admin/VERSION`.** The
  order is edit → commit → push → `bash install.sh`.
- **There is no per-project regeneration.** Installing updates the one interpreter every
  project shares, so a bad install breaks `admin` fleet-wide. Verify with `admin-run`
  first; install second.
- `install.sh --link` symlinks instead of copying, which makes working-tree edits live in
  the installed tool. Convenient while iterating, and a trap if you forget it is on.
- The pytest run writes its log under a temp dir, not this repo's `tmp/` — the
  `→ wrote: .../T/tmp…/tmp/run.log` line at the end is normal, not a stray path bug.

## Log-bridge rendering

Bridged browser lines must be indistinguishable from the wrapped process's own output.
Verify without a browser by POSTing a batch at a live listener:

```bash
curl -s -X POST http://127.0.0.1:9988/log -H 'Content-Type: application/json' \
  -d '[{"timestamp":"2026-08-27T17:19:01.723Z","level":"error","message":"boom\n    at f (a.js:1)","host":"localhost"}]'
```

- Pass: the terminal running the action prints `HH:MM:SS  [ERROR] [client] boom` with ERROR
  in the same red as the server's own `[ERROR]`, the continuation line indented under it and
  stamped too, and the pinned footer still intact (the line goes through `frame.write`).
- The `HH:MM:SS` shown is the *entry's* timestamp converted to local time, not arrival —
  a stamp an hour off means `LogFormatter(reuse_timestamp=True)` lost the tz conversion.
- Uncolored, ISO-stamped, footer-overwriting lines mean the sink was never installed;
  `keybindings._install_bridge_sink` is what wires it.

## Tampermonkey provisioning (`admin tampermonkey`)

`admin_lib/tampermonkey.py` installs `tampermonkey/log-bridge.user.js` into every profile
of Arc/Chrome/Brave through Chromium's managed-storage policy (TM 5.5+ `jsonImport`).
It writes one plist per browser into `/Library/Managed Preferences/$USER/` (root-owned, one
password prompt) — the forced layer. A user-level `defaults write` lands as RECOMMENDED and
`chrome.storage.managed` keeps only MANDATORY (`policy_value_store.cc`). Nothing touches
the browser's databases.

- `admin tampermonkey` (also a step of `admin deploy`) — writes the policy, then quits and relaunches each running browser (`--no-restart` skips that).
- `admin tampermonkey-status` (`--json` via `python3 -m admin_lib.tampermonkey --status --json`)
  — per profile: policy in defaults → delivered to the extension (`Managed Extension
  Settings` LevelDB) → script `@meta#` seen in `Local Extension Settings`. Only the
  write-ahead log is read, so a compacted record shows as "not seen".
- Pass: after the browser's next policy refresh (launch, or its periodic reload) and a
  TM service-worker start, `delivered` shows the hash and `installed` lists
  `admin-project-tool log bridge <version>`; the TM dashboard lists it as a normal
  (non-system) script.
- Hash port is tested against TM's algorithm shape in `tests/test_tampermonkey.py`; a
  mismatch in a live run shows up as `provisioning[…]: Hash mismatch` in TM's
  service-worker console (chrome://extensions → Tampermonkey → service worker).
