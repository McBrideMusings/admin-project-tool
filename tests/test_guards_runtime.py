"""Runtime tests for `admin_lib/guards.py`.

Exercises the guard registry, `run_requires` semantics, and
`register_shell_guard` shell-execution behavior. Does NOT exercise the
two built-in git-* guards against a real repo — those need a temp-repo
fixture (deferred). Their static structure is covered indirectly by the
audit tests in `test_guards_manifest.py`.
"""
from __future__ import annotations

import json
import os
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib import guards  # noqa: E402


# ---------------------------------------------------------------------------
# Registry behavior
# ---------------------------------------------------------------------------

def test_builtin_guards_registered_at_import():
    assert "git-clean" in guards._guards
    assert "git-pushed" in guards._guards
    assert "deps-fresh" in guards._guards
    assert "deps-fresh" in guards.GUARDS_BUILTIN


def test_register_guard_overrides_existing_name():
    original = guards._guards.get("test-guard")
    try:
        guards.register_guard("test-guard", lambda: (True, None))
        ok, msg = guards._guards["test-guard"]()
        assert ok and msg is None
    finally:
        if original is None:
            guards._guards.pop("test-guard", None)
        else:
            guards._guards["test-guard"] = original


# ---------------------------------------------------------------------------
# run_requires: ordering, fail-fast, exit codes
# ---------------------------------------------------------------------------

def test_run_requires_returns_zero_when_all_pass(capsys):
    guards.register_guard("rg-pass1", lambda: (True, None))
    guards.register_guard("rg-pass2", lambda: (True, None))
    try:
        assert guards.run_requires(["rg-pass1", "rg-pass2"]) == 0
    finally:
        guards._guards.pop("rg-pass1", None)
        guards._guards.pop("rg-pass2", None)


def test_run_requires_fail_fast_returns_one_on_first_failure(capsys):
    calls: list[str] = []

    def _pass(name):
        def _impl():
            calls.append(name)
            return True, None
        return _impl

    def _fail(name):
        def _impl():
            calls.append(name)
            return False, f"{name}: nope"
        return _impl

    guards.register_guard("rg-a", _pass("rg-a"))
    guards.register_guard("rg-b", _fail("rg-b"))
    guards.register_guard("rg-c", _pass("rg-c"))
    try:
        assert guards.run_requires(["rg-a", "rg-b", "rg-c"]) == 1
        # rg-c must not have run (fail-fast).
        assert calls == ["rg-a", "rg-b"]
        err_output = capsys.readouterr().err
        assert "rg-b: nope" in err_output
    finally:
        for n in ("rg-a", "rg-b", "rg-c"):
            guards._guards.pop(n, None)


def test_run_requires_unknown_name_is_failure(capsys):
    assert guards.run_requires(["nope-not-real"]) == 1
    assert "unknown guard 'nope-not-real'" in capsys.readouterr().err


def test_run_requires_empty_list_passes():
    assert guards.run_requires([]) == 0


# ---------------------------------------------------------------------------
# register_shell_guard: exit code → ok/fail, output capture, message format
# ---------------------------------------------------------------------------

def test_shell_guard_zero_exit_passes(capsys):
    guards.register_shell_guard("sh-pass", "true", "should not appear")
    try:
        assert guards.run_requires(["sh-pass"]) == 0
        # Pass path is silent — no err() output.
        assert "should not appear" not in capsys.readouterr().err
    finally:
        guards._guards.pop("sh-pass", None)


def test_shell_guard_nonzero_exit_uses_fail_message(capsys):
    guards.register_shell_guard("sh-fail", "false", "this should appear")
    try:
        assert guards.run_requires(["sh-fail"]) == 1
        err_text = capsys.readouterr().err
        assert "sh-fail: this should appear" in err_text
    finally:
        guards._guards.pop("sh-fail", None)


def test_shell_guard_captured_output_precedes_fail_message(capsys):
    # The shell expression emits a diagnostic on stderr AND exits non-zero.
    # The runtime should display: captured-output, then the static fail.
    guards.register_shell_guard(
        "sh-noisy",
        "echo something-broke >&2; exit 1",
        "fix the broken thing",
    )
    try:
        assert guards.run_requires(["sh-noisy"]) == 1
        err_text = capsys.readouterr().err
        # The order matters: captured output first, then the actionable message.
        idx_out = err_text.find("something-broke")
        idx_fail = err_text.find("sh-noisy: fix the broken thing")
        assert idx_out != -1 and idx_fail != -1
        assert idx_out < idx_fail, f"output should precede fail message:\n{err_text}"
    finally:
        guards._guards.pop("sh-noisy", None)


# ---------------------------------------------------------------------------
# deps-fresh: node/bun workspace integrity
# ---------------------------------------------------------------------------

def _write_pkg(d: Path, obj: dict):
    d.mkdir(parents=True, exist_ok=True)
    (d / "package.json").write_text(json.dumps(obj))


def _workspace_fixture(root: Path, lockfile="bun.lock"):
    """Root workspace with one library (`ui`) and one consumer (`web`) that
    declares `ui` as a `workspace:*` dependency. Returns the consumer dir; the
    `ui` link into web/node_modules is left for the test to create or skip."""
    (root / lockfile).write_text("")
    _write_pkg(root, {"name": "root", "workspaces": ["packages/*", "apps/*"]})
    _write_pkg(root / "packages/ui", {"name": "ui"})
    _write_pkg(root / "apps/web", {"name": "web", "dependencies": {"ui": "workspace:*"}})
    return root / "apps/web"


def test_deps_fresh_passes_for_non_node_project(tmp_path):
    # No package.json at all → no-op pass, so it's safe to run globally across
    # apple/rust/go/hugo archetypes.
    ok, msg = guards.deps_fresh_check(tmp_path)
    assert ok and msg is None


def test_deps_fresh_blocks_when_workspace_dep_unlinked(tmp_path):
    _workspace_fixture(tmp_path)  # no link created → stale
    ok, msg = guards.deps_fresh_check(tmp_path)
    assert ok is False
    assert "ui" in msg
    assert "apps/web" in msg
    assert "bun install" in msg


def test_deps_fresh_passes_when_link_present(tmp_path):
    web = _workspace_fixture(tmp_path)
    (web / "node_modules").mkdir(parents=True)
    os.symlink(tmp_path / "packages/ui", web / "node_modules" / "ui")
    ok, msg = guards.deps_fresh_check(tmp_path)
    assert ok and msg is None


def test_deps_fresh_accepts_hoisted_root_link(tmp_path):
    # bun may hoist the workspace link to the root node_modules instead of the
    # consumer's — that must count as satisfied too.
    _workspace_fixture(tmp_path)
    (tmp_path / "node_modules").mkdir(parents=True)
    os.symlink(tmp_path / "packages/ui", tmp_path / "node_modules" / "ui")
    ok, msg = guards.deps_fresh_check(tmp_path)
    assert ok and msg is None


def test_deps_fresh_install_cmd_follows_lockfile(tmp_path):
    web = _workspace_fixture(tmp_path, lockfile="pnpm-lock.yaml")
    ok, msg = guards.deps_fresh_check(tmp_path)
    assert ok is False
    assert "pnpm install" in msg


def test_run_global_preflight_memoizes(monkeypatch):
    calls = {"n": 0}

    def _fake_check(root=None):
        calls["n"] += 1
        return True, None

    monkeypatch.setattr(guards, "deps_fresh_check", _fake_check)
    monkeypatch.setattr(guards, "_preflight_result", None)
    assert guards.run_global_preflight() == 0
    assert guards.run_global_preflight() == 0
    assert calls["n"] == 1  # second call short-circuits


def test_run_global_preflight_reports_failure(monkeypatch, capsys):
    monkeypatch.setattr(guards, "deps_fresh_check", lambda root=None: (False, "deps-fresh: stale"))
    monkeypatch.setattr(guards, "_preflight_result", None)
    assert guards.run_global_preflight() == 1
    assert "deps-fresh: stale" in capsys.readouterr().err


def test_run_global_preflight_memoizes_failure_verdict(monkeypatch):
    # A failing first call must keep failing on repeat — not silently pass
    # because "it already ran".
    calls = {"n": 0}

    def _fake_fail(root=None):
        calls["n"] += 1
        return False, "deps-fresh: stale"

    monkeypatch.setattr(guards, "deps_fresh_check", _fake_fail)
    monkeypatch.setattr(guards, "_preflight_result", None)
    assert guards.run_global_preflight() == 1
    assert guards.run_global_preflight() == 1
    assert calls["n"] == 1


def test_deps_fresh_excludes_negated_pattern(tmp_path):
    # `!packages/legacy` is an exclusion: an intentionally-uninstalled package
    # there must not block every command.
    (tmp_path / "bun.lock").write_text("")
    _write_pkg(tmp_path, {"name": "root", "workspaces": ["packages/*", "!packages/legacy"]})
    _write_pkg(tmp_path / "packages/legacy", {"name": "legacy", "dependencies": {"ui": "workspace:*"}})
    ok, msg = guards.deps_fresh_check(tmp_path)
    assert ok and msg is None


def test_deps_fresh_ignores_node_modules_descent(tmp_path):
    # A `packages/**` glob must not treat installed deps under node_modules as
    # workspace members.
    (tmp_path / "bun.lock").write_text("")
    _write_pkg(tmp_path, {"name": "root", "workspaces": ["packages/**"]})
    _write_pkg(tmp_path / "packages/ui", {"name": "ui"})
    _write_pkg(
        tmp_path / "packages/ui/node_modules/dep",
        {"name": "dep", "dependencies": {"other": "workspace:*"}},
    )
    ok, msg = guards.deps_fresh_check(tmp_path)
    assert ok and msg is None


def test_deps_fresh_fail_open_on_bad_workspace_glob(tmp_path):
    # An absolute/`~` workspace glob makes Path.glob raise on 3.9–3.12. Because
    # this runs before every command, it must degrade to a no-op pass, never
    # crash the tool.
    (tmp_path / "bun.lock").write_text("")
    _write_pkg(tmp_path, {"name": "root", "workspaces": ["/etc/*"]})
    ok, msg = guards.deps_fresh_check(tmp_path)
    assert ok and msg is None
