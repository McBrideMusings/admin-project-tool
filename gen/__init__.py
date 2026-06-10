"""admin-project-tool generator internals — manifest parsing, archetype merge,
kind metadata, and standalone compile.

Runtime execution lives in admin_lib/interp.py (the interpreter). The user-facing
entry point is the `admin` CLI; see gen.compile.compile_standalone for the
standalone-zipapp path (ADR-0006).
"""
