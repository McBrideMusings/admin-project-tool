# admin-project-tool

Interprets a project's `admin.toml` task-runner manifest at runtime. A project commits only `admin.toml` — there is no generated `./admin` script. See `docs/architecture/design.md` for the full architecture.

## Installation

```bash
./install.sh
```

Installs the interpreter (`admin_lib`, `gen/`, `archetypes/`, `detectors/`, and the `admin` entrypoint) to `~/.admin/` and adds it to your PATH.

### Flags

| Flag | Description |
|------|-------------|
| `--force` | Reinstall even when already up to date |
| `--shell zsh\|bash\|sh` | Override shell detection for rc-file PATH setup |

## Uninstall

```bash
./uninstall.sh
```

Removes everything `install.sh` writes to `~/.admin/` (`admin_lib`, `archetypes`, `detectors`, the `admin` entrypoint, `VERSION`) and the PATH entry `install.sh` added to your shell rc file. Anything else you've put under `~/.admin/` is left alone.

## Usage

Once installed, `admin` is on your PATH. Run it from anywhere inside a project — it finds `admin.toml` by walking up from the current directory and interprets it at runtime. Editing `admin.toml` takes effect immediately; there is nothing to regenerate.

| Command | Description |
|---------|-------------|
| `admin new` | Detect the project's stack and write a starter `admin.toml` |
| `admin check` | Validate that `admin.toml` parses and resolves |
| `admin compile` | Build a standalone `./admin` binary, resolved manifest embedded, for a machine without the tool installed |
| `admin <command>` | Run a command defined in the project's `admin.toml` |

To update the tool: edit, commit, push, then re-run `./install.sh`. There is no per-project regeneration.

### Running against another worktree

`-w`/`--worktree <target>` runs against a different git worktree of the current repo instead of the current directory:

```bash
admin -w redux-c38.5 dev      # run the "dev" command with that worktree as the project root
admin -w redux-c38.5          # open the dashboard on that worktree instead
```

`<target>` resolves against `git worktree list` in this order, stopping at the first tier that matches: an existing directory path, a worktree's directory name, its branch name, then a substring of either (case-insensitive). Two or more matches at the deciding tier, or no match at all, exits non-zero and lists every candidate rather than guessing. Everything the command runs — the manifest lookup, the working directory, the log directory, `${VAR}` resolution — then behaves as though `admin` had been invoked from inside that worktree. If the target has no `admin.toml` of its own (worktrees never get one, since `admin.toml` is never committed), it borrows the primary checkout's manifest but still executes inside the target worktree. See `docs/guide/worktree-target.md` for the full four-tier resolution and the dashboard's `T` picker.
