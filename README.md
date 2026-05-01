# admin-project-tool

Generates and manages `./admin` task-runner scripts for projects. See `docs/design.md` for the full architecture.

## Installation

```bash
./install.sh
```

Installs `admin_lib`, `admin-gen`, and supporting files to `~/.admin/` and adds it to your PATH.

### Flags

| Flag | Description |
|------|-------------|
| `--force` | Reinstall even when already up to date |
| `--register-hooks` | Register the Claude Code SessionStart hook without reinstalling |
| `--shell zsh\|bash\|sh` | Override shell detection for rc-file PATH setup |

## Claude Code SessionStart hook

`install.sh` registers a Claude Code `SessionStart` hook at `~/.admin/hooks/session-admin-check.sh`. At the start of each Claude Code session, the hook checks whether the project's `./admin` script is out of date with the installed version of `admin-project-tool`, and regenerates it automatically if so.

**What it does:**
- Reads the session's `cwd` from Claude's hook stdin
- Exits silently if the project has no `admin.toml` / `admin` files
- Runs `admin-gen --audit` to check for drift
- If drift is detected, runs `admin-gen --regenerate` and reports the result to Claude as context

**To opt out:** remove the `SessionStart` entry for `~/.admin/hooks/session-admin-check.sh` from `~/.claude/settings.json`.

**To register manually** (for users who installed before this feature shipped):

```bash
./install.sh --register-hooks
```
