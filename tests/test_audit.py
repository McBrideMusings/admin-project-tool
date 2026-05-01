"""Tests for admin-gen audit helpers."""
from __future__ import annotations

import importlib.util
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from gen.archetypes import ResolvedManifest  # noqa: E402
from gen.manifest import Command, Manifest  # noqa: E402

# Load admin-gen as a module (it has no .py extension).
import importlib.machinery  # noqa: E402
_loader = importlib.machinery.SourceFileLoader("admin_gen", str(REPO / "admin-gen"))
_spec = importlib.util.spec_from_loader("admin_gen", _loader)
_mod = importlib.util.module_from_spec(_spec)
_loader.exec_module(_mod)
_audit_suggest_kill_port = _mod._audit_suggest_kill_port


def _make_command(name: str, kind: str) -> Command:
    return Command(name=name, run="", desc=name, kind=kind, source="test")


def _make_resolved(modules: list[str], kinds: list[str]) -> ResolvedManifest:
    m = Manifest(
        project_name="test", python_min="3.11", archetypes=[], modules=[],
        urls={}, overrides={}, custom={}, order=[],
    )
    actions = {k: _make_command(k, k) for k in kinds}
    return ResolvedManifest(
        manifest=m,
        commands={},
        actions=actions,
        urls={},
        modules=modules,
        merge_log=[],
        display_order=[],
    )


# ---------------------------------------------------------------------------
# _audit_suggest_kill_port
# ---------------------------------------------------------------------------

def test_kill_port_no_ports_module():
    resolved = _make_resolved(modules=["core"], kinds=[])
    assert _audit_suggest_kill_port(resolved) == []


def test_kill_port_ports_module_no_kill_command():
    resolved = _make_resolved(modules=["core", "ports"], kinds=["shell"])
    result = _audit_suggest_kill_port(resolved)
    assert len(result) == 1
    assert "kill-port" in result[0]


def test_kill_port_ports_module_with_kill_port_command():
    resolved = _make_resolved(modules=["core", "ports"], kinds=["kill-port"])
    assert _audit_suggest_kill_port(resolved) == []


def test_kill_port_ports_module_with_nuke_dev_command():
    resolved = _make_resolved(modules=["core", "ports"], kinds=["nuke-dev"])
    assert _audit_suggest_kill_port(resolved) == []


def test_kill_port_ports_module_mixed_commands_no_kill():
    resolved = _make_resolved(modules=["core", "ports"], kinds=["shell", "http", "sequence"])
    result = _audit_suggest_kill_port(resolved)
    assert len(result) == 1


# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    failed = 0
    for fn in tests:
        try:
            fn()
            print(f"OK  {fn.__name__}")
        except Exception as e:
            print(f"FAIL {fn.__name__}: {e}")
            failed += 1
    if failed:
        sys.exit(failed)
    else:
        print(f"\nAll {len(tests)} tests passed.")
