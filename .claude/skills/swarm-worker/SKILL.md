---
name: swarm-worker
description: Rules for a swarm worker running `implement <id> auto` in a linked worktree of admin-project-tool while other workers build and test the same repo. Read before the first build or test.
---

# swarm-worker — admin-project-tool

- **Build and test through the heavy lock.** `go build ./...`, `go vet ./...` and
  `go test ./...` each go through `swarm heavy -- <cmd>`. The Go build cache is shared and safe
  to use from several worktrees at once; the lock is about CPU, not correctness.
- **No `admin.toml` in your worktree.** It is globally git-ignored, so the PATH `admin` finds
  nothing here. Run the tool from source: `go run . <cmd>`, or build a binary into your scratch
  dir with `go build -o <scratch>/admin-<id> .` and run that against the primary checkout.
- **`install.sh` refuses a linked worktree.** Reinstall only after your branch has landed on
  `main`, from the primary checkout: `bash <primary checkout>/install.sh`.
  Two workers landing close together both reinstall; the second install wins, and it holds
  both commits, so that is correct.
- **`internal/tui` is shared by every tab slice of epic admin-ppsn.** Rebase onto `main` right
  before landing and rerun the build and tests after the rebase; another tab slice has
  likely landed while you worked.
- **`.admin/view.toml` in the primary checkout is the user's.** A `--dump-frame` run with `s`
  or `g` keys rewrites it; copy it aside first and restore it after.
