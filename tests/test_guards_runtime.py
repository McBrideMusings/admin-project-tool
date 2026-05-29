"""Runtime tests for `admin_lib/guards.py`.

Exercises the guard registry, `run_requires` semantics, and
`register_shell_guard` shell-execution behavior. Does NOT exercise the
two built-in git-* guards against a real repo — those need a temp-repo
fixture (deferred). Their static structure is covered indirectly by the
audit tests in `test_guards_manifest.py`.
"""
from __future__ import annotations

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
