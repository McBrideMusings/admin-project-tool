# Swarm

The worker rules live in [`../swarm-worker/SKILL.md`](../swarm-worker/SKILL.md); this page
names the commands.

## Heavy commands

`go build ./...`, `go vet ./...` and `go test ./...` each run through
`"$HOME/.claude/skills/swarm/swarm" heavy -- <command>`.

## The cheap check

`go build ./<package>/...` and `go test ./<package>` for the package you edited.

## Shared caches

The Go build and module caches are shared and safe across worktrees; nothing to set.

## Per-worker resources

A worktree has no `admin.toml` (it is globally git-ignored); run `go run . <cmd>` or a binary
built to the worker's scratch dir. `install.sh` refuses a linked worktree.
