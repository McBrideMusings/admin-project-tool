# Archetypes are merged partial manifests, not inherited templates

Archetypes (`archetypes/<name>.py`) contribute partial manifests — dicts of commands, actions, modules, urls — that the generator merges in list order. There is no base template, no `extends`, no override blocks. `archetypes = ["docker-unraid", "apple"]` means "merge both dicts, later wins on conflicts."

**Why.** Composability is the load-bearing requirement: real projects mix archetypes (an Apple app that also deploys via docker-unraid; a Forge engine project that's also a Rust workspace). A template-inheritance model would force a diamond resolution and require explicit `super()` calls between archetypes that have no inherent ordering relationship. Dict merge is order-explicit at the call site and trivially debuggable — you can print the manifest between merges.

**Considered and rejected.** Jinja-style template inheritance with overridable blocks. Nicer to read in isolation, but blocks `archetypes = [A, B]` from working without each archetype knowing about the other. Also pulls in `jinja2`, which violates [ADR-0001](./0001-stdlib-only.md).

**Consequence.** Conflicts between archetypes are silent (last write wins) — the order in the manifest is meaningful. Validation in `gen/archetypes.py` catches structural errors (a command with `kind`) but not semantic clashes (two archetypes defining `build` differently). If a real clash emerges, the right fix is to rename one side, not introduce inheritance.
