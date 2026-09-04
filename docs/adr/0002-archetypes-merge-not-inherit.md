# ADR-0002: Archetypes are merged partial manifests, not inherited templates

Archetypes (`archetypes/<name>.py`) contribute partial manifests — dicts of commands, actions, modules, urls — that the generator merges in list order. There is no base template, no `extends`, no override blocks; `archetypes = ["docker", "apple"]` merges both dicts, later wins on conflicts.

The `base` archetype (`archetypes/base/archetype.toml`, `internal/archetype.Resolve`) merges implicitly, first, before every declared archetype and before the project's own manifest, whether or not a project names it in `archetypes`. A project that does name it explicitly gets the identical result — the implicit pass is skipped so the explicit one doesn't double it — and any declared archetype or the project itself still wins on a key `base` also declares.

Conflicts between archetypes are silent: last write wins, and the order in the manifest is meaningful.
