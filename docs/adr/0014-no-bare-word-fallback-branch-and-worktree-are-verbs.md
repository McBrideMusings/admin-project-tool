# ADR-0014: No bare-word fallback — `branch` and `worktree` are the verbs

A bare first argument that is neither a tool/`cmd/` verb nor a command in the project's `admin.toml` is an error. There is no branch-shorthand fallback. The resolution order is two steps: a tool verb or `cmd/` verb (`branch`, `worktree`, `show`, `populate`, `archetypes`, plus cobra's `help`/`completion`), then a command declared in `admin.toml`.

Creating a git ref from an issue, a PR, a bead id, a GitHub URL or a plain name is what `admin branch <input>` and `admin worktree <input>` do explicitly. Both verbs are reserved (`reservedToolVerbs`, `cmd/manifest.go`), so no manifest can shadow them.

`dispatchArgs` registers a hidden command under an unrecognized name whose error names the project's real command list (or that the directory has no `admin.toml`) with the typed word substituted in. A broken-but-present manifest still reports the manifest problem, not "unknown command."
