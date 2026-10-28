# Logs

The tool is a CLI and a dashboard on this Mac; nothing ships to a remote store, and nothing
runs in production apart from the user's own machines.

## Per-project run logs

- **Where:** `<project root>/tmp/` by default, or `[logging] dir` in that project's
  `admin.toml` (`internal/luahost/logcontext.go`'s `ResolveLogDir`). One file per run, named
  `<command>~<action>.<YYYY-MM-DD_HHMMSS_ns>.log`, timestamped and ANSI-stripped. A framed
  action's footer shows the absolute path.
- **Fetch:** `ls -t <project>/tmp/*.log | head` for the newest, then read it. Browser console
  lines forwarded by the log bridge land in the same file as `[CLIENT]` lines.

## Supervise log

- **Where:** `~/.admin/state/supervise.log`, one line per watcher start and process sweep
  (`internal/supervise`).
- **Fetch:** `grep '<run id or pid>' ~/.admin/state/supervise.log`, or `tail -50` for the
  newest. Lines carry an ISO-8601 local timestamp with offset.

## Sync log

- **Where:** `~/Library/Logs/admin/sync.log` on macOS, `$XDG_STATE_HOME/admin/log/sync.log`
  elsewhere (`internal/data/synclog.go`). Every dashboard sync, `done` predicate run and reap,
  one line each naming its repo.
- **Fetch:** `grep '<repo name>' ~/Library/Logs/admin/sync.log | tail -50`.

## Live-work registry

`~/.admin/state/livework/<repo>-<hash>/<id>.json`, one file per launcher-dispatched entry,
removed when the entry ends.

## Retention

Nothing rotates these files; they stay until deleted. macOS removes `/private/tmp` after three
untouched days, which only matters for a project whose `[logging] dir` points there.

## From a line to a fix

Reproduce with `go run . <command>` in the project the line names, then `diagnose`, then an
`implement` pass, then `bash install.sh` once it lands.
