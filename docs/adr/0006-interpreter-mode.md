# Interpreter mode replaces mandatory code generation

We are inverting the tool's core model. Instead of *always* generating and committing a self-contained `./admin` script per project (the runtime vendored into every repo), the installed tool reads `admin.toml` and **executes commands directly at runtime** — "interpreter mode." Code generation survives only as an opt-in `admin compile` verb that bundles the interpreter + `admin_lib` + the manifest into a single standalone `.pyz` via stdlib `zipapp`, for the rare case a project must run where the tool isn't installed.

**Status:** accepted. Supersedes the "Regeneration & Drift Audit" section and Principles #4–#5 of `docs/architecture/design.md`, and [ADR-0004](0004-bundler-flat-namespace.md) (the flat-namespace bundler is deleted; `compile` uses stdlib `zipapp`, which copies whole files with no flattening). [ADR-0001](0001-stdlib-only.md) still holds and is reinforced (`zipapp` is stdlib). [ADR-0003](0003-commands-are-pure-routing.md) and [ADR-0005](0005-guards-and-requires.md) are runtime-agnostic and unaffected.

## Context

The vendored `./admin` artifact was causing three concrete pains, all symptoms of *vendoring the runtime into every project* rather than of code generation itself: (a) noisy diffs whenever the runtime was re-inlined, (b) debugging flattened/bundled code, (c) keeping N project copies in sync as the archetype catalog moved. Investigation showed the generated script is ~95% byte-identical `admin_lib` runtime; the genuinely per-project part (command/action wiring) is small. The 24 `render_*` functions in `gen/fragments.py` are a code-*generation* indirection layer that mostly stringifies calls into `admin_lib` functions whose intelligence already runs at runtime. Removing the indirection makes the tool *smaller*.

The load-bearing constraint that forced vendoring was old Principle #4: "contributors `git clone && ./admin` with zero install." The owner confirmed that scenario is essentially empty — these are solo repos and `install.sh` already puts the tool on every machine the owner uses.

## Decision

- **Default = interpreter mode.** A project commits **only `admin.toml`**. The interpreter is installed once (`~/.admin/admin`, on PATH) and is invoked as `admin <command>` from anywhere inside a project — it finds the manifest by walking up from `$PWD`. (An earlier draft committed a tiny `./admin` shim per project; that was dropped in favor of PATH so nothing but the manifest lives in the repo.)
- **`render_*(cmd) -> str` becomes `exec_*(cmd, args) -> int`.** Each renderer collapses into a function that does directly what it used to emit, deleting the `_pyrepr`/`textwrap` stringification scaffolding. `kind = "python"` and the `[inline]` block become `exec()` of manifest-supplied Python in the interpreter's namespace.
- **`admin compile` is the only surviving generation** — one engine. It bundles `{interpreter + admin_lib + embedded admin.toml}` into a standalone `.pyz`. The compiled artifact is also the **version pin** for a project.
- **Versioning is global.** The installed tool's version is what runs every project; updating the tool moves all projects together. `admin compile` pins a project when one specifically needs locking.

## Considered options

- **Keep mandatory codegen, just stop committing the runtime** (reference an installed `admin_lib`, ship a thin generated shim). Fixes diff noise but keeps the generator, the drift machinery, and per-project regeneration — solves the symptom, not the model.
- **Two engines: interpreter for daily use, keep `render_*` for `compile`.** Rejected — doubles the surface area forever; every new `kind` would be written twice. `compile`-as-packaging (bundle the interpreter itself) keeps a single engine.
- **Keep per-project version pinning** via a `pin = "<sha>"` field the interpreter checks. Rejected — carries the cost of the old provenance model to serve staged per-project rollouts, a scenario that doesn't exist for a solo-developer setup. `compile` covers the rare pin need precisely.

## Consequences

- **The drift subsystem evaporates.** `generator_commit`, `admin audit`'s artifact-diffing, Principle #5 idempotency (now meaningful only for `compile` output), and the `SessionStart` auto-regen hook lose their reason to exist — there is no vendored artifact to drift. `admin audit` shrinks to `admin check`: does the manifest parse, resolve, and reference only known guards/kinds? The session hook is deleted or repurposed to that check.
- **Provenance moves up.** Instead of a SHA scattered across N manifests, the tool's own git history + test suite is the record of "what version produced this behavior."
- **Lost property, stated plainly:** no per-project diff to review before a project picks up a catalog change. A latent tool bug reaches all projects at once with nothing committed to point at. Mitigation: the tool's test suite, and `compile` for anything that must stay frozen.
- **Tool verbs share the `admin <word>` namespace with manifest commands.** `new`, `compile`, `check` are reserved; the interpreter errors if a manifest defines a command with a reserved name.
- **Existing generated `./admin` scripts keep working** untouched (they are self-contained), but are deleted project-by-project as part of this change — once the tool is on PATH, the committed `./admin` is redundant. Strip the old `generator_commit` line from each `admin.toml`.
