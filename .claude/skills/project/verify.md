# Verify

The full procedure lives in [`../verify-project/SKILL.md`](../verify-project/SKILL.md); this
page is the short form and the order to run it in.

## Launch

Nothing stays running. Every check runs the interpreter from the working tree:

```bash
go build ./...
go run . check .                 # this repo's admin.toml resolves
```

The PATH `admin` (`~/.admin/admin`) runs whatever was last installed, so it proves nothing
about an uninstalled edit. `bash install.sh` updates it, from the primary checkout only.

## Doctor

`go run . check .` prints `ok: admin-project-tool — N commands, N actions, 1 modules`. Any
other output is a resolve error to fix before driving anything.

## Drive

1. Whole suite: `go test ./...` (3–4 min). One package: `go test ./internal/kinds`.
2. A real manifest other than this repo's: `go run . check <sibling project with archetypes>`;
   the `merge:` lines show the archetype merge.
3. The dashboard against the throwaway demo project, with no personal data in it:

   ```bash
   bash demo/noodle-clip/prepare.sh /private/tmp/claude/atlas
   go build -o /private/tmp/claude/admin-src . && ( cd /private/tmp/claude/atlas && /private/tmp/claude/admin-src )
   ```

   A headless drive uses `vhs` (the recorder `kind = "clip"` uses): a tape that types into the
   dashboard and takes `Screenshot` frames, so no keystroke reaches the user's terminal.
4. `tests/test_install.sh` and `tests/test_uninstall.sh` touch the real `~/.admin`; run them
   on purpose, then reinstall.

## Evidence

The test output, the `ok:` line, and for a dashboard change the screenshot frames, each looked at.

## Logs and state

A command run writes `<project>/tmp/<command>~<action>.<timestamp>.log` (see [logs.md](logs.md)).
`go run . worktree list --json` prints every worktree's git, PR and live-agent state.

## Cleanup

Remove the demo directory and any binary built into `/private/tmp/claude/`. Never delete
`~/.admin/state`.

## Not verification

A clean `git status`, `go build` alone, or `admin <cmd>` from PATH before reinstalling.
