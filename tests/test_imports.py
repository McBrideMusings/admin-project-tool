"""Every admin_lib module must import cleanly.

Trivial-looking, and it exists because a real bug got through: removing an
import from docker.py that was genuinely unused *in that file* broke web_dev.py,
which imported the name *from* docker as a re-export. Nothing caught it — the
whole suite stayed green because no test imports web_dev at all.

Any module reachable from a manifest is reachable from a `modules = [...]`
list, so a module that cannot import is a runtime crash for any project that
names it. Enumerating admin_lib/*.py from disk rather than a hand-listed set
means a new module is covered the day it lands, with nothing to remember.
"""
from __future__ import annotations

import importlib
import sys
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

MODULES = sorted(
    p.stem for p in (REPO / "admin_lib").glob("*.py")
    if p.stem != "__init__"
)


def test_module_list_is_not_empty():
    # Guards the glob itself: a bad path would make every test below vacuous.
    assert len(MODULES) > 10


@pytest.mark.parametrize("name", MODULES)
def test_module_imports(name):
    importlib.import_module(f"admin_lib.{name}")


@pytest.mark.parametrize("name", MODULES)
def test_module_has_no_third_party_imports(name):
    """Runtime code is stdlib-only (ADR-0001).

    Only try/except-guarded optional imports may name a third-party package;
    an unguarded one at module scope would break a plain `pip`-less install.
    """
    import ast

    src = (REPO / "admin_lib" / f"{name}.py").read_text()
    tree = ast.parse(src)

    guarded: set[int] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Try):
            for child in ast.walk(node):
                guarded.add(id(child))

    stdlib = set(sys.stdlib_module_names)
    for node in ast.walk(tree):
        if id(node) in guarded:
            continue
        if isinstance(node, ast.Import):
            roots = [a.name.split(".")[0] for a in node.names]
        elif isinstance(node, ast.ImportFrom):
            if node.level:      # relative import — our own package
                continue
            roots = [(node.module or "").split(".")[0]]
        else:
            continue
        for root in roots:
            assert root in stdlib or root == "admin_lib", (
                f"admin_lib/{name}.py imports third-party {root!r} unguarded"
            )
