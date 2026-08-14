"""Tests for admin_lib.streams.OutputPump — the single pump every dev-loop
caller (core.run_cmd's pty and pipe paths, streams.stream_subprocess,
reload.run_with_reload, keybindings.run_with_keybindings) now sits on.

Exercises the pump directly rather than through any one caller, since the
whole point of collapsing five copies into one is that the pump's own
contract — formatter line-buffering, flush() on exit, log vs. display
transforms, collect capture — is what every caller now depends on.
"""
from __future__ import annotations

import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.streams import OutputPump  # noqa: E402


def test_no_formatter_passthrough(capsys):
    """With no formatter, each chunk reaches the terminal immediately, un-split —
    required for newline-less output (progress bars, a bare prompt) to show live.
    """
    pump = OutputPump()
    pump.write("building")
    pump.write("...")
    out = capsys.readouterr().out
    assert out == "building..."


def test_formatter_buffers_until_a_complete_line(capsys):
    """A chunk that splits a line mid-way must not reach the terminal until the
    newline arrives — a formatter that lays out columns needs a whole line.
    """
    pump = OutputPump(formatter=lambda line: "X " + line)
    pump.write("hel")
    assert capsys.readouterr().out == ""
    pump.write("lo\n")
    assert capsys.readouterr().out == "X hello\n"


def test_formatter_handles_multiple_lines_in_one_chunk(capsys):
    pump = OutputPump(formatter=lambda line: "X " + line)
    pump.write("one\ntwo\nthre")
    out = capsys.readouterr().out
    assert out == "X one\nX two\n"
    pump.write("e\n")
    assert capsys.readouterr().out == "X three\n"


def test_flush_emits_trailing_partial_line(capsys):
    """flush() is the drain-on-exit case: the child is gone, no more newlines
    are coming, and whatever's still buffered must reach the terminal anyway.
    """
    pump = OutputPump(formatter=lambda line: "X " + line)
    pump.write("no newline yet")
    assert capsys.readouterr().out == ""
    pump.flush()
    assert capsys.readouterr().out == "X no newline yet\n"


def test_flush_is_a_noop_with_nothing_buffered(capsys):
    pump = OutputPump(formatter=lambda line: "X " + line)
    pump.write("complete\n")
    capsys.readouterr()
    pump.flush()
    assert capsys.readouterr().out == ""


def test_flush_is_a_noop_without_a_formatter(capsys):
    pump = OutputPump()
    pump.write("no newline")
    capsys.readouterr()
    pump.flush()
    assert capsys.readouterr().out == ""


def test_log_transform_differs_from_display(tmp_path, capsys):
    """stream_subprocess logs the FORMATTED text; core/reload/keybindings log
    raw text (core additionally stamps + strips ANSI). log_transform is what
    lets each caller pick independently of what reaches the terminal.
    """
    log = tmp_path / "out.log"
    fh = open(log, "a")
    formatter = lambda line: "X " + line
    pump = OutputPump(fh, formatter=formatter, log_transform=formatter)
    pump.write("raw\n")
    fh.close()
    assert log.read_text() == "X raw\n"
    assert capsys.readouterr().out == "X raw\n"


def test_log_gets_raw_text_with_no_log_transform(tmp_path, capsys):
    log = tmp_path / "out.log"
    fh = open(log, "a")
    pump = OutputPump(fh, formatter=lambda line: "X " + line)
    pump.write("raw\n")
    fh.close()
    assert log.read_text() == "raw\n"
    assert capsys.readouterr().out == "X raw\n"


def test_display_transform_applies_only_without_a_formatter(capsys):
    pump = OutputPump(display_transform=lambda t: "[" + t + "]")
    pump.write("hi")
    assert capsys.readouterr().out == "[hi]"


def test_collect_captures_every_raw_chunk(capsys):
    collected = []
    pump = OutputPump(collect=collected, formatter=lambda line: "X " + line)
    pump.write("ab")
    pump.write("c\n")
    assert collected == ["ab", "c\n"]


def test_no_log_fh_is_a_noop_for_logging(capsys):
    """log_fh=None (the default) must not raise — plenty of callers (e.g. a
    formatter-only terminal pump with no log file) construct it this way."""
    pump = OutputPump()
    pump.write("hello\n")
    assert capsys.readouterr().out == "hello\n"
