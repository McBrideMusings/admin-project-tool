# ADR-0016: A capability a manifest can reach is a kind

Every capability a project can invoke is a declared kind with a factory — there is no second category of "library module a body happens to call." A manifest is never committed, so a repo-wide grep can never establish that a capability is unused; the call sites are `kind="python"` bodies in other projects' uncommitted manifests. Deciding a capability is dead requires a kind nobody declares (greppable, since `admin check` names it), not a grep of this repo.

| Kind | Config table | Replaces |
|---|---|---|
| `electron` | `[electron]` | `dev_electron` / `build_electron` / `deploy_electron` |
| `reload-loop` | — (`run`, `target`) | `run_with_reload` for an arbitrary command |
| `launchd` | `[launchd]` | `launchd_service` / `service_deploy_remote` / `service_uninstall_remote` |
| `go-dist` | `[go_dist]` | `build_go_dist` / `stage_dist` |
| `agent-skill` | — (`run`, `skill_name`) | `install_agent_skill` |

`run_action(name, ...)` is bound into the Lua host alongside them. The init-time coverage check in `factories.go` refuses a declared kind with no factory, so the catalog and factories cannot drift.
