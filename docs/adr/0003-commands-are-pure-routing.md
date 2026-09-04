# ADR-0003: Commands are pure routing — `kind`/`run`/`targets` live in `[actions]`

A `[commands.X]` block may carry only `desc` and `steps` (a list of action names). All execution detail — `kind`, `run`, `targets`, `meta`, key bindings — lives in `[actions.X]`. The resolver hard-errors when a command carries any action-only field, naming the fix.

The split lets one action back multiple commands, lets an action be internal (reachable only from `steps`, never listed in the TUI menu), and lets `multi-target` dispatch to other named actions as first-class addressable units.
