# ADR-0024: Worktree lifecycle hooks are shell strings in `[worktrees.hooks]`

A project declares up to three hooks in `[worktrees.hooks]`: `post_create`, `post_start` and `pre_remove`. Each is a shell string, run the way a `kind = "shell"` body runs. Archetypes merge into the table by hook name, and the later declaration wins (ADR-0002). Every hook comes from the primary checkout's resolved manifest, which is also where populate reads its patterns. Each hook runs inside the worktree with `ADMIN_WT_PATH`, `ADMIN_WT_BRANCH` and `ADMIN_REPO_ROOT` set, plus `ADMIN_PORT` when the worktree has a port.

`post_create` runs once, after populate, when the tool creates a new worktree, and creation waits for it to finish. If `post_create` fails, the worktree stays, the tool prints the hook's output and exit status, and `post_start` does not run. Otherwise `post_start` runs next, detached: the tool returns without waiting and writes the hook's output to the project's log directory. Opening a worktree that already exists runs neither hook.

`pre_remove` runs before the tool removes a worktree that still exists on disk, whichever surface does the removal. A non-zero exit vetoes the removal: the worktree and its branch stay, and the hook's output is shown as the reason. With `--force`, the hook still runs and the worktree is removed whatever the hook returns.
