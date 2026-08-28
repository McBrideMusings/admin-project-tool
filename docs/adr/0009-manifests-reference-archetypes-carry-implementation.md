# A project's manifest references; the archetype carries the implementation

A project's `admin.toml` should be almost entirely **reference**: it names its archetypes, names commands, and lists the actions each command steps through. Implementation — anything with control flow, anything more than a single line of shell — belongs in the archetype, not in the project. A manifest is a document someone reads to learn what a project can do; every line of embedded script is a line that job has to survive.

**Why.** The manifest is the only file a project commits, and it is read far more often than it is written. Reading it should answer "what can I run here" in one screen. Embedded implementation defeats that twice: it buries the nine interesting lines under two hundred uninteresting ones, and it makes the interesting lines harder to find because they no longer sit next to each other.

There is a second, larger reason. A bespoke script in a project is nearly always the *first* encounter with a problem, not a *project-specific* problem. Verifying a build ran on the GPU, hot-reloading an overlay, sweeping stale dev processes — each arrived in one project and each is general to a development environment, not to that repo. Keeping implementation in the archetype layer is what makes the second project that hits the problem inherit the answer instead of re-deriving it. A project that writes its own script has, by default, made that impossible.

**What this permits.** A project may reference a sidecar script (`run = "./tools/dev-run.sh"`), and it may inline a multi-line string. Neither is forbidden — an escape hatch that is disallowed is an escape hatch that gets routed around in worse ways. Both are anti-patterns: correct in the moment, and a signal that something wants promoting. A `run` that grows past one line is the trigger to ask which archetype it belongs to.

**The shape that succeeds.** A command names steps; a step names an action; the action came from an archetype. Nothing in the project's document says how anything is done:

```toml
archetypes = ["python", "docs"]

[commands.verify]
desc  = "lint, typecheck and test in one pass"
steps = ["fmt", "typecheck", "test"]
```

**The shape that has failed.** A manifest whose reader has to skip a screen of shell to find the next command:

```toml
[actions.verify]
kind = "shell"
run  = """
  set -euo pipefail
  ... forty more lines ...
"""
```

**Considered and rejected.** Forbidding inline `run` blocks and sidecar references outright, enforced by validation. Rejected because the escape hatch is load-bearing during the window between hitting a problem and knowing which archetype owns it — and because a project that cannot express something in its manifest expresses it in a Makefile instead, where the tool cannot see it at all. The preference is enforced by making promotion easy and by reporting the anti-pattern, not by refusing it.

**Consequence.** `admin check` reports a manifest carrying inline implementation rather than failing it, so the signal arrives without blocking work. The archetype library is expected to grow steadily, and growth driven by promotion out of a specific project is the healthy path rather than a smell. A project whose manifest is long is a project with an archetype waiting to be written.
