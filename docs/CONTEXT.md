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
An action's execution strategy. The discriminator that selects an action's runtime closure factory in `admin_lib/interp._FACTORIES`; the closed catalog of valid kinds (`KNOWN_KINDS`) lives in `admin_lib/kinds.py`, which the manifest/archetype validators consult. Examples: `shell`, `python`, `multi-target`, `interactive-shell`, `apple-dev-loop`, `kill-port`, `http`. Closed set — adding a new kind means adding a factory.
_Avoid_: type, mode

**Bundler**:
The component in `gen/bundler.py` that inlined `admin_lib` modules into the generated `./admin` as a flat namespace — strips relative imports, hoists `__future__` imports, dedupes.
_Avoid_: packer, linker
_Status_: removed. The flat-namespace bundler was deleted under **Bundler flat namespace** ([ADR-0004](adr/0004-bundler-flat-namespace.md)); **Interpreter mode** ([ADR-0006](adr/0006-interpreter-mode.md)) imports `admin_lib` as a real package and **Compile** produces a `zipapp` (whole files, no flattening).

**Fragment**:
A rendered snippet emitted by `gen/fragments.py` — one per command (the function the TUI calls) and one per action (the dispatch body for that kind). Composed by the renderer into the final `./admin`.
_Avoid_: section, block
_Status_: removed under **Interpreter mode** ([ADR-0006](adr/0006-interpreter-mode.md)) — there is no rendering step; the interpreter builds live closures directly (`admin_lib/interp.py`). `gen/fragments.py` was reduced to kind metadata + the `${VAR}` scanner and renamed `admin_lib/kinds.py`.

**Generator_commit**:
The git SHA of `admin-project-tool` at the time a manifest was last regenerated, written into `admin.toml` and embedded in the `./admin` header banner.
_Avoid_: version, build_id
_Status_: removed under **Interpreter mode** ([ADR-0006](adr/0006-interpreter-mode.md)) — the global installed tool version replaces the per-project pin; **Compile** is the new pin.

**Drift**:
A mismatch between a project's `admin.toml` `generator_commit` and the currently installed `admin-project-tool` SHA. Detected by `admin-gen --audit`; the SessionStart hook regenerates silently when drift is found.
_Avoid_: stale, out-of-date
_Status_: removed under **Interpreter mode** ([ADR-0006](adr/0006-interpreter-mode.md)) — with no vendored artifact there is nothing to drift; `admin audit` shrinks to a manifest-validity `check`.

**Guard**:
A named precondition referenced from an action's or target's `requires` list. A Guard returns pass/fail; on fail it blocks the action and prints an actionable message. Built-in Guards (`git-clean`, `git-pushed`, `deps-fresh`) live in `admin_lib/guards.py` as Python with dynamic diagnostics; user-defined Guards live in `[guards.<name>]` tables as a shell expression (`run`) plus a static failure string (`fail`). Both share one name namespace; `requires` resolves names through both.
_Avoid_: gate, check, precondition (overloaded), validator

**Global preflight**:
An always-on check that runs before every command (CLI or TUI), independent of any `requires` list, via `run_global_preflight` in `admin_lib/guards.py` (memoized — once per invocation). Currently just `deps-fresh`: a node/bun workspace integrity check that blocks when a `workspace:*` dependency is declared but not linked into `node_modules` (stale install — fresh worktree, or a dep added without reinstalling), pointing at the right `<pm> install`. A no-op on non-node projects.
_Avoid_: hook, gate (reserved for Guard)

**Requires**:
The field on an action or multi-target target that lists Guard names. `requires = ["git-clean", "git-pushed"]` runs both Guards in order, fail-fast, before the action's `run`. Parent multi-target actions define a default that children inherit and may override (replace-not-merge); `requires = []` on a child is the explicit opt-out.
_Avoid_: needs, preconditions, before

### Architecture

Terms for reasoning about the *shape* of the code (not the domain). Used by architecture reviews; keep them stable.

**Module**:
Anything with an interface and an implementation — a function, class, file, or package. Scale-agnostic.
_Avoid_: component, service, unit

**Interface**:
Everything a caller must know to use a module correctly — type signature, invariants, ordering, error modes, required config, perf characteristics. Wider than just the type-level surface.
_Avoid_: API, signature (those mean only the type-level surface)

**Implementation**:
What's inside a module — hidden from callers.

**Depth**:
Leverage at the interface. A module is **deep** when a large amount of behaviour sits behind a small interface; **shallow** when the interface is nearly as complex as the implementation.

**Seam**:
A place where behaviour can be altered without editing in that place — the location at which a module's interface lives (Michael Feathers).
_Avoid_: boundary (overloaded with DDD's bounded context)

**Adapter**:
A concrete thing satisfying an interface at a seam. Names the *role* (which slot it fills), not the substance.

**Leverage**:
What callers gain from depth — more capability per unit of interface they must learn.

**Locality**:
What maintainers gain from depth — change, bugs, and knowledge concentrate at one place instead of spreading across callers.

### Execution model (interpreter mode)

**Interpreter mode**:
The default execution model after [ADR-0006](adr/0006-interpreter-mode.md): the installed `admin` tool (on PATH) reads `admin.toml` and executes commands at runtime — a project commits only the manifest; nothing is generated or vendored. Replaces mandatory code generation; **Compile** is the opt-in path to a standalone artifact.
_Avoid_: runtime mode, JIT

**Compile**:
The `admin compile` verb. Packages the interpreter + `admin_lib` + the project's resolved `admin.toml` into a single self-contained `.pyz` (stdlib `zipapp`). The compiled artifact is both the portable standalone (no install needed) and the version pin for a project.
_Avoid_: build (reserved for project-level build commands), freeze, bundle

## Relationships

- A **Manifest** lists zero or more **Archetypes** and is merged with each in order.
- Merging produces a flat set of **Commands** and **Actions**.
- A **Command** has one or more **Steps**; each Step names an **Action**.
- An **Action** has exactly one **Kind**; the Kind selects the runtime closure factory in `admin_lib/interp.py` that executes it.
- **Compile** packages `admin_lib/` + the resolved **Manifest** into a standalone `zipapp`.
- A **Detector** proposes which **Archetypes** apply to a project; paired with its archetype by filename.
- **Drift** is the audit signal that something downstream of the **Generator_commit** has changed.
- An **Action** (or a multi-target child) may carry a `**Requires**` list of **Guard** names; each Guard is checked before the Action runs.

## Example dialogue

> **Dev:** "If I add a new **Kind** like `interactive-shell`, what else has to move?"
> **Maintainer:** "Add a factory to `admin_lib/interp._FACTORIES` and its name to `KNOWN_KINDS` in `admin_lib/kinds.py`, then validate the new fields it accepts in `admin_lib/manifest.py` and `admin_lib/archetypes.py`. **Compile** is unaware of **Kinds** — it just packages `admin_lib/`."
>
> **Dev:** "And if a project's **Action** uses a kind the installed tool doesn't know about?"
> **Maintainer:** "Resolve fails fast — `admin_lib/archetypes.py` checks every action's **Kind** against `KNOWN_KINDS` and raises `unknown kind`. There's no **Drift** to track: the installed tool reads `admin.toml` live ([ADR-0006](adr/0006-interpreter-mode.md)), so a project always runs against whatever tool is on PATH. To pin a version, `admin compile`."

## Flagged ambiguities

_None yet — the codebase uses each term uniformly. Capture conflicts here as they arise._
