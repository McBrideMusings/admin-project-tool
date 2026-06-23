"""Standalone compile (ADR-0006).

`admin compile` packages the interpreter + admin_lib + a project's
*resolved* manifest into a single executable zipapp (`./admin`). The compiled
artifact runs the exact same interpreter (admin_lib.interp) against the embedded
manifest — there is no per-command code generation. It is the one surviving
"generation" path and doubles as a project's version pin: the tool's source is
frozen inside the archive.

The manifest is resolved at compile time (archetypes merged) and embedded as a
pickle, so the archive needs no archetype files and no TOML parser at runtime.
"""
from __future__ import annotations

import base64
import pickle
import shutil
import stat
import tempfile
import zipapp
from pathlib import Path

# Package copied into the archive. The pickled ResolvedManifest references
# admin_lib.manifest.Command / admin_lib.archetypes.ResolvedManifest, and the
# interpreter + manifest frontend now all live under admin_lib.
# NOTE (migration): the pickle records these import paths, so a ./admin compiled
# before the gen/ -> admin_lib/ move (which pickled gen.manifest.* paths) will
# fail to unpickle here — recompile to fix.
_RUNTIME_PACKAGES = ("admin_lib",)

_MAIN = '''\
"""Entry point for a compiled (zipapp) ./admin. Runs the embedded manifest."""
import base64
import os
import pickle
import sys

from _embedded import RESOLVED_B64, INLINE_B64
from admin_lib import interp

resolved = pickle.loads(base64.b64decode(RESOLVED_B64))
inline_body = base64.b64decode(INLINE_B64).decode() if INLINE_B64 else None
# The compiled ./admin sits at the project root; resolve it from argv[0].
project_root = os.path.dirname(os.path.abspath(sys.argv[0]))
interp.interpret(resolved, project_root=project_root, argv=sys.argv, inline_body=inline_body)
'''


def compile_standalone(resolved, repo_dir: Path, target: Path, inline_body: str | None = None) -> None:
    """Write a standalone executable zipapp to `target` embedding `resolved`."""
    repo_dir = Path(repo_dir)
    target = Path(target)

    with tempfile.TemporaryDirectory() as td:
        build = Path(td) / "build"
        build.mkdir()

        for pkg in _RUNTIME_PACKAGES:
            shutil.copytree(
                repo_dir / pkg,
                build / pkg,
                ignore=shutil.ignore_patterns("__pycache__", "*.pyc"),
            )

        resolved_b64 = base64.b64encode(pickle.dumps(resolved)).decode()
        inline_b64 = base64.b64encode(inline_body.encode()).decode() if inline_body else ""
        (build / "_embedded.py").write_text(
            f"RESOLVED_B64 = {resolved_b64!r}\nINLINE_B64 = {inline_b64!r}\n"
        )
        (build / "__main__.py").write_text(_MAIN)

        # Write to a temp file first, then move into place, so a failed compile
        # never leaves a half-written ./admin.
        tmp_target = Path(td) / "admin.pyz"
        zipapp.create_archive(build, tmp_target, interpreter="/usr/bin/env python3")
        shutil.move(str(tmp_target), str(target))

    target.chmod(target.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
