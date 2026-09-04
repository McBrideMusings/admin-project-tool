# Guards are named preconditions referenced from `requires` on actions and targets

An action or multi-target child target may declare `requires = ["<guard-name>", ...]`. Each name resolves through one shared registry holding both Python-defined built-in Guards (`admin_lib/guards.py`) and TOML-defined user guards (`[guards.<name>]` tables). Before the action's `run` fires, each named guard runs in order, fail-fast; on failure the runtime prints the guard's diagnostic and exits non-zero.

A child target's `requires`, if present, fully replaces the parent's — `requires = []` is the explicit opt-out. Built-in guards are Python and produce dynamic diagnostics; user-defined guards are TOML with a static `fail` string. Guards do not compose — a guard cannot itself declare `requires`.

`deps-fresh` also runs as an always-on preflight before every command (`run_global_preflight`), independent of any `requires` annotation — it checks a node/bun workspace for a `workspace:*` dependency declared but not linked into `node_modules`. It exempts `logs` and `kill` and is fail-open: a bug or unusual manifest degrades to a pass, never a crash or a block.
