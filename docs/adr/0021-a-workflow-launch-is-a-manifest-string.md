# ADR-0021: A workflow launch is a manifest string and its finish is authored

A `kind = "workflow"` action declares `run`, a shell string the `[terminal]` launcher's pane executes with `ADMIN_TARGET` set to the target id. The tool names no agent and sends no prompt of its own. `agent.Driver` only reports status (`Info`), and `agent.Select()` picks it from the environment.

An action may declare `done`, a shell predicate the dashboard polls with `ADMIN_TARGET` set; exit 0 marks the worker finished. With no `done`, the command ending is the finish.

A finished worker is torn down (`[terminal].kill`, `git worktree remove`, `git branch -d`) only when its worktree is clean and its branch is merged into `origin/main`. It holds its concurrency slot until then. Placement stays configuration (ADR-0020).
