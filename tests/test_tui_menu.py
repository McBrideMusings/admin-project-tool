"""Tests for admin_lib.core._tui_menu's row-reservation cap.

`_tui_menu` shared the exact unbounded-reservation bug `pick_target` had —
`_make_home` reserved one row per registered command regardless of terminal
height — before both were fixed to window against the terminal's own size.
These mirror test_pick_target.py's reservation-cap coverage for this
function specifically, since `_tui_menu` has its own fixed-row accounting
(a header line, a URL status block, a hint line) around the scrollable
command list that `pick_target` doesn't.
"""
from __future__ import annotations

import os
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

import admin_lib.core as core  # noqa: E402


def _make_commands(n):
    commands = {}
    for i in range(n):
        commands[f"cmd-{i}"] = {"desc": f"desc {i}", "func": lambda args: 0}
    return commands


def test_tui_menu_reserves_no_more_rows_than_the_terminal_has(monkeypatch):
    import shutil
    import termios

    monkeypatch.setattr(core, "_commands", _make_commands(64))
    monkeypatch.setattr(core, "_urls", [])
    monkeypatch.setattr(core, "_project_root", "")
    monkeypatch.setattr(core, "_project_name", "test-project")

    monkeypatch.setattr(core.sys.stdout, "isatty", lambda: True)
    monkeypatch.setattr(shutil, "get_terminal_size", lambda fallback: os.terminal_size((80, 15)))

    reserved = {}

    def fake_make_home(fd, menu_lines):
        reserved["menu_lines"] = menu_lines
        return lambda: None

    monkeypatch.setattr(core, "_make_home", fake_make_home)
    monkeypatch.setattr(core, "read_key", lambda: "q")
    monkeypatch.setattr(core.sys.stdin, "fileno", lambda: 0)
    monkeypatch.setattr(termios, "tcgetattr", lambda fd: None)
    monkeypatch.setattr(termios, "tcsetattr", lambda fd, when, attrs: None)

    core._tui_menu()

    # 15-row terminal, 64 commands + 4 fixed rows (name+blank, blank+hint,
    # no URLs) would be 68 without windowing — must stay within the terminal.
    assert reserved["menu_lines"] < 68
    assert reserved["menu_lines"] <= 15


def test_tui_menu_counter_appears_when_the_command_list_overflows_the_window(
    monkeypatch, capsys
):
    import shutil
    import termios

    monkeypatch.setattr(core, "_commands", _make_commands(64))
    monkeypatch.setattr(core, "_urls", [])
    monkeypatch.setattr(core, "_project_root", "")
    monkeypatch.setattr(core, "_project_name", "test-project")

    monkeypatch.setattr(core.sys.stdout, "isatty", lambda: True)
    monkeypatch.setattr(shutil, "get_terminal_size", lambda fallback: os.terminal_size((80, 15)))
    monkeypatch.setattr(core, "read_key", lambda: "q")
    monkeypatch.setattr(core.sys.stdin, "fileno", lambda: 0)
    monkeypatch.setattr(termios, "tcgetattr", lambda fd: None)
    monkeypatch.setattr(termios, "tcsetattr", lambda fd, when, attrs: None)

    core._tui_menu()

    out = capsys.readouterr().out
    assert "[1/64]" in out
