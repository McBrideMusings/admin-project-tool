"""Shared test infrastructure for admin-project-tool tests."""
from __future__ import annotations

import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
if str(REPO) not in sys.path:
    sys.path.insert(0, str(REPO))

from gen.manifest import Command  # noqa: E402


def make_command(
    name: str = "test",
    run: str = "echo test",
    desc: str = "test command",
    kind: str = "shell",
    source: str = "test",
    targets: dict | None = None,
    meta: dict | None = None,
) -> Command:
    """Factory for Command objects with sensible defaults for tests."""
    return Command(
        name=name,
        run=run,
        desc=desc,
        kind=kind,
        source=source,
        targets=targets or {},
        meta=meta or {},
    )
