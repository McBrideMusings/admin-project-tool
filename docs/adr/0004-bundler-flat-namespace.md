# Bundler flattens `admin_lib` into a single namespace

> **Status: superseded by [ADR-0006](./0006-interpreter-mode.md).** The flat-namespace bundler (`gen/bundler.py`) is deleted. Interpreter mode imports `admin_lib` as a real package, and `admin compile` produces a standalone via stdlib `zipapp` (whole files copied into an archive, no flattening) — so neither the `__future__`-hoisting nor the `from . import` prohibition below applies any longer. Retained for historical context.

The bundler in `gen/bundler.py` inlines every module under `admin_lib/` directly into the generated `./admin`, producing a single Python file with a flat top-level namespace. This is what makes the "self-contained one-file `./admin`" guarantee from [ADR-0001](./0001-stdlib-only.md) achievable without `pip install`, a zipapp, or sys.path manipulation.

**Two consequences are enforced as gen-time errors, not silent breakage.**

1. **`from . import some_module` is rejected** (`BundlerError`, commit `76c5ebf`). When `admin_lib` is flattened, module objects don't survive — `some_module` becomes a name with nothing bound to it, and `some_module.thing` calls would `NameError` at runtime. Pre-fix, the bundler silently rewrote these imports to `pass`, producing mysterious runtime errors. The fix is intentional strictness: detect at bundle time, error loudly, point at the file. `from .some_module import thing` works (the symbol `thing` survives); the module-object form does not.

2. **`from __future__ import ...` is hoisted and deduped** (commit `0098f45`). Any `__future__` import inside an `admin_lib` module would otherwise land mid-file in the bundle — a `SyntaxError` in Python. The bundler scans all modules, merges the `__future__` features, and emits a single deduped import line right after the generated docstring. This means individual modules can declare `from __future__ import annotations` safely without coordination.

**Why this stance rather than "just make it work."** Both failure modes have natural-looking fixes: AST-rewrite imports to preserve module objects; ship `admin_lib` as a zipapp; emit multi-file output with a top-level `./admin` shim. Each one solves the immediate problem and *breaks the single-file guarantee*. Strict gen-time errors keep the constraint visible: the contributor sees "you can't do that here, write it this way instead" the moment they try, rather than discovering later that the bundle silently dropped half their imports.

**Considered and rejected.** Name-mangling + AST rewrite (preserves module-object semantics, but introduces a layer of magic that makes the bundled output hard to read). Multi-file generated output with a `__main__.py` shim or zipapp (loses single-file readability and the "just open `./admin` in a text editor" guarantee). Both also work against [ADR-0001](./0001-stdlib-only.md)'s tighten-the-constraint-rather-than-add-tooling principle.

**Consequence.** Contributors writing in `admin_lib/` cannot use `from . import other_module` — they must use `from .other_module import thing`. The bundler's error message names the offending file and import. `__future__` imports are free.
