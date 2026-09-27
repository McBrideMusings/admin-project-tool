# Guide

Hands-on docs for using `admin-project-tool` in a project.

> **Status:** stub. The architecture overview at [/architecture/design](/architecture/design) is the canonical reference until this section is fleshed out. The manifest, archetype-merge, and action-**kind** model it documents is current.

Jumping-off points into that reference:

- [Lua host API](/architecture/lua-host-api) — the functions a `kind = "lua"` body can call, and the Go modules that back them.
- [`apple` archetype `[apple]` schema](/archetypes/apple) — every `[apple]` config key.
- [Running against another worktree (`-w`/`--worktree`)](/guide/worktree-target) — run a command, or open the dashboard, against a different git worktree without `cd`'ing there.

## Quick start

```bash
# In a target project:
admin new    # detect the stack, write a starter admin.toml
admin check  # validate that admin.toml parses and resolves
admin        # run the menu (interprets admin.toml at runtime)
```
