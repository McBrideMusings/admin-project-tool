# admin-project-tool

Generates per-project `./admin` task-runner scripts from a short `admin.toml` manifest plus composable archetypes.

## Language

### Domain

**Manifest**:
The normalized state parsed from a project's `admin.toml` — project metadata, command/action definitions, archetype list, log config, URLs. The single source of truth that the generator consumes.
_Avoid_: config, settings

**Archetype**:
A partial manifest (commands, actions, modules, URLs) bundled per project type — `hugo`, `apple`, `docker-unraid`, `rust-tauri`, etc. Lives in `archetypes/<name>.py`. Multiple archetypes can be listed in one manifest and are merged in order.
_Avoid_: template, preset, profile

**Detector**:
A file-layout heuristic that scans a project root and proposes which archetypes apply. Lives in `detectors/<name>.py`, paired by filename with its archetype.
_Avoid_: probe, sniffer

**Command**:
A TUI menu entry. Routing only — may contain `desc` and `steps` (a list of action names) and nothing else. The generator errors if a command carries `kind`, `run`, or `targets`.
_Avoid_: task, target (overloaded), recipe

**Action**:
The unit of execution behind one or more commands. Carries `kind`, plus the fields that kind expects (`run`, `targets`, etc.). One action can be reused by multiple commands.
_Avoid_: handler, runner (overloaded)

**Step**:
A string in a command's `steps` list; names an action to dispatch. Reading `steps = ["build", "test"]` literally: "this command runs the `build` action then the `test` action."
_Avoid_: stage, phase

**Kind**:
An action's execution strategy. The discriminator that picks a renderer in `gen/fragments.py`. Examples: `shell`, `python`, `multi-target`, `interactive-shell`, `apple-dev-loop`, `kill-port`, `http`. Closed set — adding a new kind means adding a renderer.
_Avoid_: type, mode

**Bundler**:
The component in `gen/bundler.py` that inlines `admin_lib` modules into the generated `./admin` as a flat namespace — strips relative imports, hoists `__future__` imports, dedupes.
_Avoid_: packer, linker

**Fragment**:
A rendered snippet emitted by `gen/fragments.py` — one per command (the function the TUI calls) and one per action (the dispatch body for that kind). Composed by the renderer into the final `./admin`.
_Avoid_: section, block

**Generator_commit**:
The git SHA of `admin-project-tool` at the time a manifest was last regenerated, written into `admin.toml` and embedded in the `./admin` header banner.
_Avoid_: version, build_id

**Drift**:
A mismatch between a project's `admin.toml` `generator_commit` and the currently installed `admin-project-tool` SHA. Detected by `admin-gen --audit`; the SessionStart hook regenerates silently when drift is found.
_Avoid_: stale, out-of-date

**Guard**:
A named precondition referenced from an action's or target's `requires` list. A Guard returns pass/fail; on fail it blocks the action and prints an actionable message. Built-in Guards (`git-clean`, `git-pushed`) live in `admin_lib/guards.py` as Python with dynamic diagnostics; user-defined Guards live in `[guards.<name>]` tables as a shell expression (`run`) plus a static failure string (`fail`). Both share one name namespace; `requires` resolves names through both.
_Avoid_: gate, check, precondition (overloaded), validator

**Requires**:
The field on an action or multi-target target that lists Guard names. `requires = ["git-clean", "git-pushed"]` runs both Guards in order, fail-fast, before the action's `run`. Parent multi-target actions define a default that children inherit and may override (replace-not-merge); `requires = []` on a child is the explicit opt-out.
_Avoid_: needs, preconditions, before

## Relationships

- A **Manifest** lists zero or more **Archetypes** and is merged with each in order.
- Merging produces a flat set of **Commands** and **Actions**.
- A **Command** has one or more **Steps**; each Step names an **Action**.
- An **Action** has exactly one **Kind**; the Kind selects which renderer in `gen/fragments.py` emits its **Fragment**.
- The **Bundler** inlines `admin_lib/` into the rendered output; the **Generator_commit** is stamped into the header.
- A **Detector** proposes which **Archetypes** apply to a project; paired with its archetype by filename.
- **Drift** is the audit signal that something downstream of the **Generator_commit** has changed.
- An **Action** (or a multi-target child) may carry a `**Requires**` list of **Guard** names; each Guard is checked before the Action runs.

## Example dialogue

> **Dev:** "If I add a new **Kind** like `interactive-shell`, what else has to move?"
> **Maintainer:** "Add a renderer in `gen/fragments.py`, then validate the new fields it accepts in `gen/manifest.py` and `gen/archetypes.py`. The **Bundler** is unaware of **Kinds** — it just inlines `admin_lib/`."
>
> **Dev:** "And if a project's **Action** uses the new kind, but the installed tool doesn't know about it?"
> **Maintainer:** "The **Generator_commit** in their **Manifest** won't match the installed SHA — that's **Drift**. `admin-gen --audit` flags it; the SessionStart hook regenerates against the new tool."

## Flagged ambiguities

_None yet — the codebase uses each term uniformly. Capture conflicts here as they arise._
