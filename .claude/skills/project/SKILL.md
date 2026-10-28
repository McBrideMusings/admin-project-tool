---
name: project
description: "admin-project-tool: how to verify it works (the `admin` CLI and dashboard, run from source), where its logs live (per-project run logs, the supervise and sync logs) and how to fetch them, and how swarm workers build it. Read before declaring a change verified, before diagnosing anything that happened while it ran, and before the first build in a swarm worktree."
---

# admin-project-tool

- **Verify** — read [verify.md](verify.md) before reporting any change works.
- **Logs** — read [logs.md](logs.md) when something happened while it ran: in a project's
  dev loop, a dashboard sync, a reaped worker. It fetches the lines itself; never ask the user
  to paste them.
- **Swarm** — read [swarm.md](swarm.md) before the first build or test in a swarm worktree.

The project has no scripts of its own beyond `install.sh`, `uninstall.sh` and `demo/`.
