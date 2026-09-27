# ADR-0006: Interpreter mode replaces mandatory code generation

The installed tool reads a project's `admin.toml` and executes commands directly at runtime — "interpreter mode" — rather than generating and committing a self-contained `./admin` script. A project commits only `admin.toml`; the interpreter (`~/.admin/admin`, on PATH) is invoked as `admin <command>` from anywhere inside the project and finds the manifest by walking up from `$PWD`.

`admin compile` is the only generation path: it builds a copy of the `admin` binary with the project's resolved `admin.toml` embedded, for a project that must run where the tool isn't installed, and doubles as a version pin.

Versioning is global — the installed tool's version runs every project, and updating the tool moves all projects together. Tool verbs (`new`, `check`, `compile`, `branch`, `worktree`) share the `admin <word>` namespace with manifest commands; the interpreter errors if a manifest defines a command with a reserved name.
