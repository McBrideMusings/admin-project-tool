"""Tests for admin_lib/log_columns.py.

Covers _reformat_generic_line, _reformat_apple_line, _extract_native_fields,
_parse_column_spec, make_column_formatter, and width=0 hidden column support.
"""
from __future__ import annotations

import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.log_columns import (  # noqa: E402
    Column,
    ColumnFormatter,
    _extract_native_fields,
    _find_level_color,
    _parse_column_spec,
    make_column_formatter,
)


# ---------------------------------------------------------------------------
# _parse_column_spec
# ---------------------------------------------------------------------------

def test_parse_column_spec_fixed_width():
    col = _parse_column_spec("Message:80")
    assert col.name == "Message"
    assert col.width == 80


def test_parse_column_spec_fill():
    col = _parse_column_spec("Message:*")
    assert col.name == "Message"
    assert col.width == -1


def test_parse_column_spec_hidden():
    col = _parse_column_spec("Thread:0")
    assert col.name == "Thread"
    assert col.width == 0


def test_parse_column_spec_error_no_colon():
    try:
        _parse_column_spec("MissingColon")
        assert False, "should raise ValueError"
    except ValueError as e:
        assert "invalid column spec" in str(e)


# ---------------------------------------------------------------------------
# _find_level_color
# ---------------------------------------------------------------------------

def test_find_level_color_error():
    from admin_lib.core import C
    color = _find_level_color("ERROR: something went wrong")
    assert color == C.RED


def test_find_level_color_warn():
    from admin_lib.core import C
    color = _find_level_color("WARN: low disk space")
    assert color == C.YELLOW


def test_find_level_color_none():
    assert _find_level_color("just a plain log line") is None


# ---------------------------------------------------------------------------
# _extract_native_fields
# ---------------------------------------------------------------------------

def test_extract_native_fields_full_line():
    # Build a line matching the fixed native Apple os_log layout exactly:
    # Timestamp=32, Thread=11, Type=12, Activity=21, PID=7, TTL=5, Message=-1
    ts   = "2024-01-15 10:23:45.123456-0800 "  # 32 chars (trailing space)
    thr  = " 0x12345678"                        # 11 chars
    typ  = " Default    "                        # 12 chars
    act  = " 0x0000000000000000  "               # 21 chars
    pid  = " 42345 "                             # 7 chars
    ttl  = "    0"                               # 5 chars
    msg  = "my log message here\n"
    line = ts + thr + typ + act + pid + ttl + msg
    fields = _extract_native_fields(line)
    assert fields["Timestamp"] == ts
    assert fields["Message"].rstrip("\n") == msg.rstrip("\n")


# ---------------------------------------------------------------------------
# ColumnFormatter — generic mode
# ---------------------------------------------------------------------------

def _make_generic_formatter(columns=None):
    cols = columns or [
        Column("Timestamp", 12),
        Column("Level", 8),
        Column("Message", -1),
    ]
    return ColumnFormatter(cols, apple_passthrough=False)


def test_generic_header_printed_before_first_line():
    fmt = _make_generic_formatter()
    out = fmt.format_line("2024-01-01 10:00:00 INFO hello\n")
    assert "Timestamp" in out
    assert "Level" in out
    assert "Message" in out


def test_generic_header_only_once():
    fmt = _make_generic_formatter()
    first = fmt.format_line("2024-01-01 10:00:00 INFO hello\n")
    second = fmt.format_line("2024-01-01 10:00:01 WARN world\n")
    assert "Timestamp" in first
    assert "Timestamp" not in second


def test_generic_extracts_source_timestamp():
    fmt = _make_generic_formatter()
    fmt.format_line("10:00:00 INFO first\n")  # consume header
    out = fmt.format_line("10:23:45.123 WARN low disk\n")
    assert "10:23:45.123" in out


def test_generic_falls_back_to_now_when_no_timestamp():
    import re
    fmt = _make_generic_formatter()
    fmt.format_line("INFO first\n")  # consume header
    out = fmt.format_line("INFO no timestamp here\n")
    # Should contain some time-like pattern (HH:MM:SS)
    assert re.search(r"\d{2}:\d{2}:\d{2}", out)


def test_generic_empty_line_passthrough():
    fmt = _make_generic_formatter()
    out = fmt.format_line("\n")
    assert out == "\n"


def test_generic_level_extracted():
    fmt = _make_generic_formatter()
    fmt.format_line("10:00:00 INFO first\n")  # consume header
    out = fmt.format_line("10:00:01 ERROR crash\n")
    assert "ERROR" in out


# ---------------------------------------------------------------------------
# width=0 hidden column
# ---------------------------------------------------------------------------

def test_hidden_column_absent_from_header():
    cols = [Column("Timestamp", 12), Column("Level", 0), Column("Message", -1)]
    fmt = ColumnFormatter(cols, apple_passthrough=False)
    header_line = fmt.format_line("10:00:00 INFO first\n")
    assert "Level" not in header_line


def test_hidden_column_produces_narrower_output_than_visible():
    line = "10:00:01 WARN second\n"
    # With Level column visible (width=8)
    cols_visible = [Column("Timestamp", 12), Column("Level", 8), Column("Message", -1)]
    fmt_v = ColumnFormatter(cols_visible, apple_passthrough=False)
    fmt_v.format_line("10:00:00 INFO first\n")  # consume header+first
    out_visible = fmt_v.format_line(line)

    # With Level column hidden (width=0)
    cols_hidden = [Column("Timestamp", 12), Column("Level", 0), Column("Message", -1)]
    fmt_h = ColumnFormatter(cols_hidden, apple_passthrough=False)
    fmt_h.format_line("10:00:00 INFO first\n")  # consume header+first
    out_hidden = fmt_h.format_line(line)

    assert len(out_hidden.rstrip()) < len(out_visible.rstrip()), (
        "hidden Level column should produce shorter output than visible Level column"
    )


# ---------------------------------------------------------------------------
# _reformat_apple_line short-line passthrough
# ---------------------------------------------------------------------------

def test_apple_short_line_passthrough():
    fmt = ColumnFormatter([Column("Timestamp", 32), Column("Message", -1)], apple_passthrough=True)
    short = "error: simulator crash\n"
    assert len(short.rstrip("\n")) < 40
    out = fmt.format_line(short)
    assert out == short


# ---------------------------------------------------------------------------
# make_column_formatter with config override
# ---------------------------------------------------------------------------

def test_make_column_formatter_default_generic():
    fmt = make_column_formatter("generic")
    assert isinstance(fmt, ColumnFormatter)


def test_make_column_formatter_custom_columns():
    config = {"generic": {"columns": ["Timestamp:10", "Message:*"]}}
    fmt = make_column_formatter("generic", config)
    header = fmt.format_line("10:00:00 INFO hello\n")
    assert "Timestamp" in header
    assert "Level" not in header  # Level column was not configured


def test_make_column_formatter_unknown_source_uses_generic():
    fmt = make_column_formatter("my_custom_source")
    assert isinstance(fmt, ColumnFormatter)


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
