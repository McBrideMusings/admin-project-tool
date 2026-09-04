# ADR-0011: The binary is `admin`, and manifest commands are bare verbs

The root command is `admin`; `repo` survives only as a cobra alias for the shell completion wired to it, and may be deleted once nothing invokes it.

`admin <command>` runs the manifest command named `<command>` — there is no `admin run` subcommand wrapper. A bare first argument resolves in order: a tool verb or an existing `cmd/` verb (`new`, `check`, `compile`, `branch`, `worktree`, `prs`, `populate`, plus cobra's `help`/`completion`) first, then a command declared in the project's `admin.toml`; anything else is an error (ADR-0014). An `admin.toml` that exists but fails to parse or resolve is a hard error naming the manifest problem, not a fall-through.

A manifest may not declare a command whose name is a tool verb or an existing `cmd/` verb; `admin check` rejects it, naming the rule and the colliding name.
