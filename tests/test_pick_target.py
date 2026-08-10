"""Tests for admin_lib.core.pick_target's non-TTY diagnostic (issue #444).

Without a TTY there is no picker, but the message printed in its place must
name the missing argument first — the actual failure — and mention the lack
of a TTY only as context for why the interactive fallback didn't fire.
"""
from __future__ import annotations

import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

import admin_lib.core as core  # noqa: E402


def test_pick_target_non_tty_names_the_missing_targets(monkeypatch, capsys):
    monkeypatch.setattr(core.sys.stdout, "isatty", lambda: False)

    result = core.pick_target({"mac": "macOS build", "ios": "iOS build"}, prompt="Build which target?")

    assert result is None
    stderr = capsys.readouterr().err
    assert "no target given (expected one of: mac, ios)" in stderr
    # The TTY detail is real context, but must trail the missing-argument diagnosis.
    assert stderr.index("no target given") < stderr.index("not a TTY")


def test_pick_target_non_tty_respects_order_and_skips_spacers(monkeypatch, capsys):
    monkeypatch.setattr(core.sys.stdout, "isatty", lambda: False)

    targets = {"a": "desc a", "b": "desc b", "c": "desc c"}
    result = core.pick_target(targets, prompt="Pick", order=["b", "", "a"])

    assert result is None
    stderr = capsys.readouterr().err
    assert "expected one of: b, a" in stderr


def test_pick_target_non_tty_with_no_targets_does_not_crash(monkeypatch, capsys):
    monkeypatch.setattr(core.sys.stdout, "isatty", lambda: False)

    result = core.pick_target({}, prompt="Pick")

    assert result is None
    stderr = capsys.readouterr().err
    assert "no target given" in stderr
    assert "expected one of" not in stderr
