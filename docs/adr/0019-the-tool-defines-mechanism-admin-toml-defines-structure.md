# ADR-0019: The tool defines mechanism, admin.toml defines structure

The tool supplies mechanism — kinds, the interpreter, the dashboard, guards, worktrees, log framing, the archetype merge. A project's `admin.toml` supplies structure — which commands exist, what they're named, what targets they have, what each one runs, what the project ships to. The tool never assumes a shipping target, never assumes a command name, and never populates a menu a project didn't ask for; `build`, `dev`, `deploy`, `test`, `clean`, `logs`, `kill` are conventions with zero reserved semantics.

An archetype is a starting point a project can edit or discard, not a contract it inherits. It may ship actions as a library and may ship `[commands.*]` routing, but must not ship a `multi-target` table enumerating surfaces — that enumeration is the project's statement about itself.

The test for any new archetype table, tool verb, guard exemption, or literal-string command match: could two projects using it disagree? If yes, it is structure and cannot be compiled into Go or into an archetype.
