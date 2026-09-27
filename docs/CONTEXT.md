# admin-project-tool

Generates per-project `./admin` task-runner scripts from a short `admin.toml` manifest plus composable archetypes.

## Language

### Domain

**Manifest**:
The normalized state parsed from a project's `admin.toml` — project metadata, command/action definitions, archetype list, log config, URLs. The single source of truth that the generator consumes.
_Avoid_: config, settings

**Archetype**:
A partial manifest (commands, actions, modules, URLs) bundled per project type — `hugo`, `apple`, `docker`, `rust-tauri`, etc. Lives in `archetypes/<name>.py`. Multiple archetypes can be listed in one manifest and are merged in order.
_Avoid_: template, preset, profile

**Detector**:
A file-layout heuristic that scans a project root and proposes which archetypes apply. Lives in `internal/detect`, keyed by its archetype's name.
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
An action's execution strategy. The discriminator that selects an action's factory in `internal/kinds`' `Registry`; the closed catalog of valid kinds is `kinds.Kinds` (`internal/kinds/catalog.go`), which `admin check` consults. Examples: `shell`, `lua`, `multi-target`, `interactive-shell`, `apple-dev-loop`, `kill-port`, `dev-precheck`, `http`. Closed set — adding a new kind means adding a factory.
_Avoid_: type, mode

**Guard**:
A named precondition referenced from an action's or target's `requires` list. A Guard returns pass/fail; on fail it blocks the action and prints an actionable message. Built-in Guards (`git-clean`, `git-pushed`, `deps-fresh`) live in `internal/guards` with dynamic diagnostics; user-defined Guards live in `[guards.<name>]` tables as a shell expression (`run`) plus a static failure string (`fail`). Both share one name namespace; `requires` resolves names through both.
_Avoid_: gate, check, precondition (overloaded), validator

**Global preflight**:
An always-on check that runs before every command (CLI or TUI), independent of any `requires` list, via `guards.RunGlobalPreflight` (memoized — once per invocation). Currently just `deps-fresh`: a node/bun workspace integrity check that blocks when a `workspace:*` dependency is declared but not linked into `node_modules` (stale install — fresh worktree, or a dep added without reinstalling), pointing at the right `<pm> install`. A no-op on non-node projects.
_Avoid_: hook, gate (reserved for Guard)

**Requires**:
The field on an action or multi-target target that lists Guard names. `requires = ["git-clean", "git-pushed"]` runs both Guards in order, fail-fast, before the action's `run`. Parent multi-target actions define a default that children inherit and may override (replace-not-merge); `requires = []` on a child is the explicit opt-out.
_Avoid_: needs, preconditions, before

**Branch** (verb — `admin branch <input>`):
Reserved tool verb that resolves an input (branch name, issue/PR number, issue/PR URL) to a branch and creates/checks it out **in the current working tree** — no worktree. Contrast **Worktree**, which spins up a separate working tree for the same resolved branch. (In repo-dash the `branch` command actually makes a worktree; admin splits the two meanings apart.)
_Avoid_: checkout (overloaded), switch

**Worktree** (verb — `admin worktree <input>`):
Reserved tool verb that resolves the same input set as **Branch**, then creates a separate git worktree for that branch (branch-state attach + post-create install + Herdr association), rather than switching the current tree.
_Avoid_: wt (fine as a shorthand, not the canonical name), tree

**Resolver**:
The input-classification-and-resolution stage shared by **Branch** and **Worktree**. Phase 1 (classify) is pure syntax: `{text, number, PR, issue}` + number + `owner/repo`, no network. Phase 2 (resolve) uses `gh` to turn an issue into a slugged new branch name, a PR into its head ref, and a bare number into whichever it is. Text passes through unchanged.
_Avoid_: parser, classify (that names only phase 1)

**Dead branch**:
A local branch that cleanup can force-delete safely, origin untouched: its remote is gone, its PR merged on GitHub (catches squash/rebase that `git branch --merged` misses, gated on not-ahead), it's merged into the default, or it's fully pushed. Everything else is a *survivor* (needs human judgment). Classified by `git.ClassifyDead` (`internal/git`). Surfaced by `admin branch --prune` / `admin worktree --prune` and the bare-verb TUI.
_Avoid_: stale (that's only one weaker signal), orphan

**Binding**:
A key declared in `admin.toml`'s `[keys.<row-kind>]` table, naming a **Command** the dashboard runs when that key is pressed on a row of that kind. `enter` is an ordinary Binding, not a special case: an unbound (key, row kind) pair falls through to the TUI's built-in behavior. Scoped to one **Row kind**, so the same key can differ between a bead row and a branch row.
_Avoid_: hotkey, keymap, shortcut, override

**Row kind**:
What a selectable dashboard row *is* — `bead`, `issue`, `branch`, `pr`, `commit`, `stash`. The axis a **Binding** is scoped to, and the value a bound command reads as `ADMIN_ROW_KIND`. The Work tab's own rows (`command`, `commandTarget`, `worker`) are row kinds too but are not bindable: their Enter already means "run this command" / "attach to this worker".
_Avoid_: row type, item type, entity

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
The `admin compile` verb. Builds a copy of the `admin` binary with the project's resolved `admin.toml` embedded. The compiled artifact is both the portable standalone (no install needed) and the version pin for a project.
_Avoid_: build (reserved for project-level build commands), freeze, bundle

### Live work and supervision

**Live work**:
Everything this repo started that is still alive — a `detach = true` action dispatched through the project's declared `[terminal]` launcher (ADR-0020), and an agent worker dispatched into a git worktree. The two share one registry (`internal/livework`) because that phrase is what they have in common, not because their fields match.
_Avoid_: jobs, processes (a worker is neither)

**Entry**:
One live-work record. Addressed by exactly one of a pane or a hosted pseudo-terminal, never both. Its identity survives a **Retry**, which opens a new pane under the same entry.
_Avoid_: row (that is the Work tab's rendering of an Entry, not the Entry)

**Worker** (type):
An agent running in its own git worktree on its own branch — the half of live work that is an agent rather than a process. An Entry is a Worker only when it carries worker info (`Entry.Worker != nil`); a dev server never is. Unchanged by the Work-tab palette redesign (spike/work-tab-redesign) — `Stuck` and every other Worker-only concept still means agent, specifically.
_Avoid_: agent (herdr's word for the thing inside the pane), job

**Workers** (section):
The Work tab's list of everything currently live — every Entry, hosted command or genuine Worker alike, one section, one cursor. Distinct from the type above on purpose: a hosted command (`docs`, `test`) sits in this section too, told apart from a real Worker only by its row's own badge (`hosted` cyan vs. a pane id in magenta), never by which list it's in. "Call all of them workers" (pierce, spike/work-tab-redesign) — the display noun is deliberately broader than the type.

**MRU strip**:
The Work tab palette's 5 most-recently-run fully-resolved commands (`deploy grafana`, `dev cf` — never a bare parent like `deploy`), most-recent-first, no frequency weighting. Backed by a small per-repo JSON file at `os.UserCacheDir()/work-usage/<safe-nwo>.json`, matching `internal/data/data.go`'s existing cache-path convention. Empty or partial on a fresh project — never seeded with a guess.

**Stuck**:
A Worker that has been in the waiting state — the agent settled on a question or an approval prompt — longer than a threshold. Deliberately narrower than "not making progress": a worker spinning silently is not Stuck under this definition, and is a recorded open question.
_Avoid_: hung, stalled, blocked (herdr's own status word for the underlying state)

**Nudge**:
One canned continuation message sent to a Stuck Worker's agent. Never a keystroke, never a restart — restarting a worker that asked a question loses its work.
_Avoid_: ping, poke, prompt (overloaded with the agent's original prompt)

**Sweep**:
One pass of the **Orchestrator** over the registry: find the Stuck entries, Nudge each at most once per waiting state. Runs on the Work tab's existing tick.
_Avoid_: poll, scan

**Orchestrator**:
The half of the swarm that reads the live-work registry and acts on it without a human. Its responsibility ends when an Entry exits; it never merges, pushes, or opens a pull request ([ADR-0017](adr/0017-the-orchestrator-never-lands-work.md)).
_Avoid_: supervisor, scheduler, daemon (it is not a separate process)

## Relationships

- A **Manifest** lists zero or more **Archetypes** and is merged with each in order.
- Merging produces a flat set of **Commands** and **Actions**.
- A **Command** has one or more **Steps**; each Step names an **Action**.
- An **Action** has exactly one **Kind**; the Kind selects the factory in `internal/kinds` that executes it.
- **Compile** builds a copy of the binary with the resolved **Manifest** embedded.
- A **Detector** proposes which **Archetypes** apply to a project; paired with its archetype by filename.
- An **Action** (or a multi-target child) may carry a `**Requires**` list of **Guard** names; each Guard is checked before the Action runs.
- A long-running **Action**'s dispatch, and each **Worker** a workflow starts, both register an **Entry** in the live-work registry.
- The **Orchestrator** runs a **Sweep** over those Entries, finds the **Stuck** ones, and sends each a **Nudge**.

## Example dialogue

> **Dev:** "If I add a new **Kind** like `interactive-shell`, what else has to move?"
> **Maintainer:** "Add it to `Kinds` in `internal/kinds/catalog.go` and register its factory in `internal/kinds/factories.go` — a declared kind with no factory fails at startup. **Compile** is unaware of **Kinds** — it copies the binary."
>
> **Dev:** "And if a project's **Action** uses a kind the installed tool doesn't know about?"
> **Maintainer:** "Resolve fails fast — `admin check` rejects any action whose **Kind** is not in `kinds.Kinds`. The installed tool reads `admin.toml` live ([ADR-0006](adr/0006-interpreter-mode.md)), so a project always runs against whatever tool is on PATH. To pin a version, `admin compile`."

## Flagged ambiguities

_None yet — the codebase uses each term uniformly. Capture conflicts here as they arise._
