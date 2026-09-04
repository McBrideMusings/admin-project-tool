# ADR-0009: A project's manifest references; the archetype carries the implementation

A project's `admin.toml` is almost entirely reference: it names its archetypes, names commands, and lists the actions each command steps through. Implementation — anything with control flow, anything more than a single line of shell — belongs in the archetype, not the project.

A project may reference a sidecar script or inline a multi-line `run` string; neither is forbidden, but both are anti-patterns and a signal the logic wants promoting to an archetype. `admin check` reports a manifest carrying inline implementation rather than failing it, so the signal arrives without blocking work.
