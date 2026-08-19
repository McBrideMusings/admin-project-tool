"""Tests for admin_lib.core.pick_target's non-TTY diagnostic (issue #444).

Without a TTY there is no picker, but the message printed in its place must
name the missing argument first — the actual failure — and mention the lack
of a TTY only as context for why the interactive fallback didn't fire.
"""
from __future__ import annotations

import os
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


# `_scroll_offset` is the windowing math behind the interactive picker's
# scrolling — a list longer than the terminal used to overflow the reserved
# row count (`_make_home`), scrolling the real viewport out from under the
# picker and leaving duplicated/ghosted rows on every redraw thereafter (a
# 60+ item list, e.g. etv-station's channel picker). These exercise just the
# offset math, with no terminal involved.
class TestScrollOffset:
    def test_selection_within_window_does_not_move_offset(self):
        assert core._scroll_offset(offset=0, sel_row=5, window=10, total_rows=100) == 0

    def test_selection_past_the_bottom_scrolls_by_the_minimum(self):
        # window [0,10); selecting row 10 must bring it to the last visible
        # row (9), not re-center — offset becomes 1, not e.g. 5.
        assert core._scroll_offset(offset=0, sel_row=10, window=10, total_rows=100) == 1

    def test_selection_above_the_top_scrolls_up_to_match(self):
        assert core._scroll_offset(offset=20, sel_row=15, window=10, total_rows=100) == 15

    def test_offset_never_goes_negative(self):
        assert core._scroll_offset(offset=0, sel_row=0, window=10, total_rows=100) == 0

    def test_offset_never_exceeds_what_the_list_can_fill(self):
        # Selecting the very last row must not push the window past the end
        # of the list, leaving trailing blank rows.
        assert core._scroll_offset(offset=0, sel_row=99, window=10, total_rows=100) == 90

    def test_a_list_no_taller_than_the_window_never_scrolls(self):
        assert core._scroll_offset(offset=0, sel_row=4, window=10, total_rows=5) == 0

    def test_spacer_rows_count_toward_the_scroll_window_like_any_other_row(self):
        # display_order = a, "", b, "", c, "", d, "", e, "", f  (6 items, 5
        # spacers, 11 display rows). `item_rows` — the mapping pick_target
        # itself builds from display_order — puts "d" at display row 6.
        display_order = ["a", "", "b", "", "c", "", "d", "", "e", "", "f"]
        item_rows = [i for i, e in enumerate(display_order) if e]
        assert item_rows == [0, 2, 4, 6, 8, 10]

        # Selecting "d" (items index 3) with a 4-row window starting at 0
        # must scroll exactly enough to bring row 6 into view — including
        # the spacer rows in between, not just counting selectable items.
        offset = core._scroll_offset(
            offset=0, sel_row=item_rows[3], window=4, total_rows=len(display_order)
        )
        assert offset == 3
        assert display_order[offset : offset + 4] == ["", "c", "", "d"]


class TestWindowSize:
    """`_window_size` is what stops `_make_home` from ever reserving more
    rows than the terminal has — the fix's whole point. These pin its floor
    behavior at pathologically short terminals directly, rather than only
    inferring it from `pick_target`'s reservation as
    `test_pick_target_reserves_no_more_rows_than_the_terminal_has` does.
    """

    def test_normal_terminal_leaves_room_for_prompt_and_hint(self):
        assert core._window_size(total_rows=100, term_rows=24) == 21

    def test_short_list_never_grows_the_window_past_itself(self):
        assert core._window_size(total_rows=5, term_rows=24) == 5

    def test_floors_at_one_row_for_a_terminal_shorter_than_the_reserved_lines(self):
        for term_rows in (3, 2, 1, 0, -5):
            assert core._window_size(total_rows=100, term_rows=term_rows) == 1

    def test_floors_at_one_even_when_the_list_is_also_short(self):
        assert core._window_size(total_rows=1, term_rows=0) == 1


def test_pick_target_reserves_no_more_rows_than_the_terminal_has(monkeypatch):
    """The historical bug: `_make_home` reserved `len(display_order) + 1`
    rows regardless of terminal height, so a list taller than the terminal
    scrolled the viewport during the initial reservation and every redraw
    landed on the wrong row. Assert the reservation is capped at the
    terminal's own height, not the full list length.
    """
    import shutil

    monkeypatch.setattr(core.sys.stdout, "isatty", lambda: True)
    monkeypatch.setattr(shutil, "get_terminal_size", lambda fallback: os.terminal_size((80, 15)))

    reserved = {}

    def fake_make_home(fd, menu_lines):
        reserved["menu_lines"] = menu_lines
        return lambda: None

    monkeypatch.setattr(core, "_make_home", fake_make_home)
    # Bail out of the input loop immediately via a synthetic "q" — this test
    # only cares what _make_home was called with, not the interaction.
    monkeypatch.setattr(core, "read_key", lambda: "q")
    monkeypatch.setattr(core.sys.stdin, "fileno", lambda: 0)

    import termios

    monkeypatch.setattr(termios, "tcgetattr", lambda fd: None)
    monkeypatch.setattr(termios, "tcsetattr", lambda fd, when, attrs: None)

    targets = {f"item-{i}": f"desc {i}" for i in range(64)}
    core.pick_target(targets, prompt="Pick")

    # 15-row terminal: window is capped well under 64, so the reservation
    # (window + 1 hint row) must be too — never the full 65-row list.
    assert reserved["menu_lines"] < 65
    assert reserved["menu_lines"] <= 15


def test_pick_target_counter_appears_when_spacers_push_the_list_past_the_window(
    monkeypatch, capsys
):
    """The counter used to gate on `num` (selectable items only), so a list
    with few selectable items but enough spacer rows to overflow the window
    scrolled silently — the user had no indication anything was off-screen.
    It must gate on whether the *display* overflows, spacers included.
    """
    import shutil
    import termios

    monkeypatch.setattr(core.sys.stdout, "isatty", lambda: True)
    monkeypatch.setattr(shutil, "get_terminal_size", lambda fallback: os.terminal_size((80, 13)))
    monkeypatch.setattr(core, "read_key", lambda: "q")
    monkeypatch.setattr(core.sys.stdin, "fileno", lambda: 0)
    monkeypatch.setattr(termios, "tcgetattr", lambda fd: None)
    monkeypatch.setattr(termios, "tcsetattr", lambda fd, when, attrs: None)

    # 6 selectable items <= window (13-row terminal -> window=10), but 12
    # spacer rows push display_order to 18 entries -- past the window.
    targets = {"a": "x", "b": "x", "c": "x", "d": "x", "e": "x", "f": "x"}
    order = []
    for key in targets:
        order.append(key)
        order.extend([""] * 2)  # two spacer rows after every item

    core.pick_target(targets, prompt="Pick", order=order)

    out = capsys.readouterr().out
    assert "[1/6]" in out, "counter must show even though num (6) <= window"
