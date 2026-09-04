# ADR-0017: The orchestrator never lands work

The orchestrator's responsibility ends when a live-work entry reaches `StateExited`. It never merges, never pushes, never opens a pull request — `internal/git` gains no `Merge` and no `Push`, and `internal/gh` stays read-only query code.

Landing stays where it already happens: inside the worker's own session, in its own wrap-up, on the worktree it owns. The worker holds the information the decision needs (what changed, whether tests passed, what's left undone); the orchestrator sees only a `State` enum and a one-line `Activity` string.
