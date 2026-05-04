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
_audit_order_vs_group_priority = _mod._audit_order_vs_group_priority


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
# _audit_order_vs_group_priority
# ---------------------------------------------------------------------------

def _make_gp_command(name: str, group: int = 0, priority: int = 0) -> Command:
    return Command(name=name, run="", desc=name, kind="shell", source="test", group=group, priority=priority)


def _make_gp_resolved(commands: dict, display_order: list[str]) -> ResolvedManifest:
    m = Manifest(
        project_name="test", python_min="3.11", archetypes=[], modules=[],
        urls={}, overrides={}, custom={}, order=[],
    )
    return ResolvedManifest(
        manifest=m,
        commands=commands,
        actions={},
        urls={},
        modules=["core"],
        merge_log=[],
        display_order=display_order,
    )


def test_order_vs_gp_no_warning_when_all_defaults():
    cmds = {
        "build": _make_gp_command("build"),
        "dev": _make_gp_command("dev"),
        "test": _make_gp_command("test"),
    }
    resolved = _make_gp_resolved(cmds, display_order=["build", "dev", "test"])
    assert _audit_order_vs_group_priority(resolved) == []


def test_order_vs_gp_no_warning_when_display_matches_sort():
    from gen.archetypes import _sort_by_group_priority
    cmds = {
        "build": _make_gp_command("build", group=1, priority=10),
        "dev": _make_gp_command("dev", group=1, priority=20),
        "test": _make_gp_command("test", group=2, priority=10),
    }
    display = _sort_by_group_priority(cmds)
    resolved = _make_gp_resolved(cmds, display_order=display)
    assert _audit_order_vs_group_priority(resolved) == []


def test_order_vs_gp_warns_on_manifest_order_list():
    cmds = {
        "build": _make_gp_command("build", group=1, priority=10),
        "dev": _make_gp_command("dev", group=2, priority=10),
        "test": _make_gp_command("test", group=1, priority=5),
    }
    # Simulate manifest order = ["dev", "build", "test"] overriding group/priority sort
    manifest_order = ["dev", "build", "test"]
    resolved = _make_gp_resolved(cmds, display_order=manifest_order)
    result = _audit_order_vs_group_priority(resolved)
    assert len(result) == 1
    assert "order" in result[0]
    assert "group/priority" in result[0]


def test_order_vs_gp_warns_on_archetype_order_list():
    cmds = {
        "build": _make_gp_command("build", group=1, priority=10),
        "dev": _make_gp_command("dev", group=2, priority=10),
        "clean": _make_gp_command("clean", group=3, priority=5),
    }
    # Simulate archetype order = ["dev", "clean", "build"] overriding group/priority sort
    archetype_order = ["dev", "clean", "build"]
    resolved = _make_gp_resolved(cmds, display_order=archetype_order)
    result = _audit_order_vs_group_priority(resolved)
    assert len(result) == 1
    assert "order" in result[0]


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
