# Bundler flattens `admin_lib` into a single namespace

Superseded by [ADR-0006](./0006-interpreter-mode.md): the flat-namespace bundler (`gen/bundler.py`) is deleted. Interpreter mode imports `admin_lib` as a real package, and `admin compile` produces a standalone via stdlib `zipapp`, which copies whole files with no flattening.

The bundler used to inline every module under `admin_lib/` into a single flat-namespace file for the "self-contained one-file `./admin`" guarantee from [ADR-0001](./0001-stdlib-only.md). `from . import some_module` was a gen-time error (module objects don't survive flattening), and `from __future__ import ...` lines were hoisted and deduped into one line — both enforced at generation time rather than left as silent breakage.
