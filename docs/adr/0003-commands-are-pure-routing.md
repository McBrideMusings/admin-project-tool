# Commands are pure routing — `kind`/`run`/`targets` live in `[actions]`

A `[commands.X]` block may carry only `desc` and `steps` (a list of action names). All execution detail — `kind`, `run`, `targets`, `meta`, key bindings — lives in `[actions.X]`. The generator hard-errors in `gen/manifest.py` and `gen/archetypes.py` when a command carries any of the action-only fields, with a message pointing at the fix ("Move kind/run/targets to [actions]").

**Why.** Earlier iterations let command blocks grow `kind` + `run` inline for "simple" cases. Over time, every command sprouted execution detail, multiple commands ended up duplicating the same shell snippet, and refactoring became painful — there was no way to share execution between two commands without copy-paste, and no way to define an internal action that *isn't* exposed as a menu entry. Forcing the split made all three problems disappear at once.

The split enables:
- **Action reuse.** One action can back multiple commands (e.g. a `build` action used by both `build` and `release` commands).
- **Internal actions.** Actions called only from `steps`, never exposed in the TUI menu. Reduces menu clutter and lets composite commands compose smaller pieces.
- **`multi-target` dispatch.** The `multi-target` kind dispatches to other named actions — only possible because actions are first-class addressable units.

**Considered and rejected.** Flattening so commands can carry `kind`/`run` inline "for simple cases." This is the natural first instinct (every CLI framework — click, typer, cobra — binds command-to-handler directly), and it's exactly what produced the mess that motivated the split. The error message exists precisely to catch this attempt.

**Consequence.** Writing a one-off command is two blocks instead of one. This is a deliberate trade.
