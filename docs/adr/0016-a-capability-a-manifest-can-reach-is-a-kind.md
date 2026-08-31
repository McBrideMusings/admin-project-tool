# ADR-0016: A capability a manifest can reach is a kind

**Status:** Accepted
**Date:** 2026-08-31
**Context:** completes [ADR-0010](0010-lua-and-bash-are-the-authoring-languages.md); reverses the launchd half of admin-3rgp.45

## Decision

**Every capability a project can invoke is a declared kind with a factory. There is no
second category of "library module a body happens to call."**

Five kinds are added, all of them dispatch shapes for implementations that already
existed or had to be restored:

| Kind | Config table | Replaces |
|---|---|---|
| `electron` | `[electron]` | `dev_electron` / `build_electron` / `deploy_electron` |
| `reload-loop` | — (`run`, `target`) | `run_with_reload` for an arbitrary command |
| `launchd` | `[launchd]` | `launchd_service` / `service_deploy_remote` / `service_uninstall_remote` |
| `go-dist` | `[go_dist]` | `build_go_dist` / `stage_dist` |
| `agent-skill` | — (`run`, `skill_name`) | `install_agent_skill` |

`run_action(name, ...)` is bound into the Lua host alongside them, restoring the
`run_command` a `kind="python"` body had in scope.

## Why

ADR-0010 deleted `kind="python"`, which was correct. What it did not account for is that
`kind="python"` was not only an authoring language — **it was the dispatch surface for
every capability that never got a kind of its own.** A project reached `admin_lib` by
writing a Python body that called into it. Remove the body and those capabilities become
unreachable, whether or not their code still exists.

Four states resulted, all invisible until a project tried to run:

- `internal/electron` was a complete port — `StageResources`, `BuildElectron`,
  `InstallElectron`, `DeployElectron`, `DevElectron` — with **zero callers**. Its own
  package doc described itself as "the real, callable, ready-to-wire port."
- `internal/reload.RunWithReload` was reachable only through `docker-dev`, `vite-dev` and
  `docker-reload-loop`. A compiled backend with no container and no vite in front of it
  had to fall back to `interactive-shell`, which can only be killed and re-run.
- `[launchd]` and `[go_dist]` were parsed into `manifest.Launchd` / `manifest.GoDist`,
  exposed to config bodies, and **read by no factory**.
- `run_command` had no Lua equivalent, so a picker routing to one of several sub-actions,
  or a teardown running three in order, had no expressible form at all.

### The zero-call-sites check was scoped to the wrong repo

admin-3rgp.45 deleted `admin_lib/launchd.py` (725 lines) as "cheaper to delete than
port," on a finding that a repo-wide grep confirmed zero call sites. The grep was right
about this repo and wrong about the fleet: the call sites are `kind="python"` bodies in
**other projects'** `admin.toml` files, and a manifest is never committed, so no search
of any repo can find them.

That deletion silently broke `admin deploy server` everywhere it was wired. It surfaced
only because a project happened to need it.

**A grep over this repo cannot establish that a capability is unused.** The consumers are
uncommitted manifests on developers' machines. Deciding a capability is dead requires
either a kind nobody declares — which *is* greppable, because `admin check` names it — or
asking. This is the structural reason the rule above exists: once every capability is a
kind, "is anything using this?" becomes a question the tool can answer.

## Consequences

- A new capability ships as a catalog entry plus a factory. The init-time coverage check
  in `factories.go` already refuses a declared kind with no factory, so the two cannot
  drift.
- `internal/service` restores the launchd/systemd/remote-SSH implementation, including
  the pieces that are not obvious and were each paid for once: the BTM-aware install that
  kickstarts rather than re-registers an unchanged plist, the atomic binary swap under a
  live daemon, and `env_file`/`env_file_keys` reading a dotenv file at generation time
  because a non-interactive `admin deploy` never sourced it.
- `[launchd].args_cmd` is added: a shell command whose stdout is appended to `args` at
  install time. Some services can only be told what to bind to once the machine is in
  front of you — a tailnet address that exists only while the VPN is up. `${VAR}` cannot
  express that, because it resolves against the environment of the very shell that knows
  none of it.
- Config values are resolved with `ResolveEnv`, not `ResolveComputed`. The latter
  deliberately leaves a plain `${HOME}` intact for a shell to expand later, which is right
  for an action's `run` string and wrong for every value consumed in Go.
- `nuke-dev` accepts an inline `[actions.X.config]` that **replaces** `[dev_kill]`, so a
  worktree-scoped teardown can declare no ports at all and be structurally unable to reach
  a standing service.
- ADR-0010 is unchanged: Bash and Lua remain the only two authoring languages. Kinds are
  how a manifest reaches Go, which is what that ADR always intended.
