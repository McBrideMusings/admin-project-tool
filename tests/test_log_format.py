"""Tests for admin_lib/log_format.py — JSON parsing + plain-line formatting."""
from __future__ import annotations

import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.log_format import (  # noqa: E402
    LogFormatter,
    normalize_timestamp,
    parse_json_log_line,
    strip_log_timestamp,
)


_ANSI = re.compile(r"\x1b\[[0-9;]*m")


def _strip_ansi(s: str) -> str:
    return _ANSI.sub("", s)


def _fmt() -> LogFormatter:
    f = LogFormatter()
    f._use_color = False
    return f


# ---------------------------------------------------------------------------
# parse_json_log_line
# ---------------------------------------------------------------------------

def test_parse_json_uvicorn_access():
    line = (
        '{"timestamp": "2026-05-22T17:57:25.236Z", "level": "INFO", '
        '"logger": "uvicorn.access", "message": "127.0.0.1 - GET / 200"}'
    )
    parsed = parse_json_log_line(line)
    assert parsed == {
        "level": "INFO",
        "logger": "uvicorn.access",
        "message": "127.0.0.1 - GET / 200",
    }


def test_parse_json_alt_keys():
    line = '{"levelname": "warning", "name": "app", "msg": "hi"}'
    assert parse_json_log_line(line) == {
        "level": "WARNING",
        "logger": "app",
        "message": "hi",
    }


def test_parse_json_event_key():
    line = '{"event": "started"}'
    assert parse_json_log_line(line) == {"level": "", "logger": "", "message": "started"}


def test_parse_json_missing_message_returns_none():
    assert parse_json_log_line('{"level": "INFO"}') is None


def test_parse_json_plain_line_returns_none():
    assert parse_json_log_line("INFO:     Uvicorn running on http://127.0.0.1:8000") is None


def test_parse_json_array_returns_none():
    assert parse_json_log_line('[{"message": "hi"}]') is None


def test_parse_json_malformed_returns_none():
    assert parse_json_log_line('{"message": "unterminated') is None


def test_parse_json_with_extra_fields_ignored():
    line = '{"level": "INFO", "message": "ok", "extra": {"x": 1}, "timestamp": "z"}'
    parsed = parse_json_log_line(line)
    assert parsed == {"level": "INFO", "logger": "", "message": "ok"}


# ---------------------------------------------------------------------------
# LogFormatter.format_line — JSON path
# ---------------------------------------------------------------------------

def test_format_json_line_renders_level_logger_message():
    f = _fmt()
    line = '{"level": "INFO", "logger": "uvicorn.access", "message": "GET / 200"}\n'
    out = _strip_ansi(f.format_line(line))
    # leading wallclock timestamp + two-space separators
    assert re.match(r"^\d{2}:\d{2}:\d{2}  INFO   uvicorn\.access  GET / 200\n$", out)


def test_format_json_line_omits_missing_level():
    f = _fmt()
    line = '{"logger": "app", "message": "hello"}\n'
    out = _strip_ansi(f.format_line(line))
    assert re.match(r"^\d{2}:\d{2}:\d{2}  app  hello\n$", out)


def test_format_json_line_omits_missing_logger():
    f = _fmt()
    line = '{"level": "WARNING", "message": "hmm"}\n'
    out = _strip_ansi(f.format_line(line))
    # level pad is min-width 5 — WARNING (7 chars) is not truncated
    assert re.match(r"^\d{2}:\d{2}:\d{2}  WARNING  hmm\n$", out)


# ---------------------------------------------------------------------------
# LogFormatter.format_line — plain-line path (regression)
# ---------------------------------------------------------------------------

def test_format_plain_line_strips_leading_timestamp():
    f = _fmt()
    line = "2026-05-22 12:34:56.789 INFO  starting up\n"
    out = _strip_ansi(f.format_line(line))
    assert re.match(r"^\d{2}:\d{2}:\d{2}  INFO  starting up\n$", out)


def test_format_plain_line_no_timestamp_passes_through():
    f = _fmt()
    line = "INFO:     Uvicorn running on http://127.0.0.1:8000\n"
    out = _strip_ansi(f.format_line(line))
    assert re.match(r"^\d{2}:\d{2}:\d{2}  INFO:     Uvicorn running on http://127\.0\.0\.1:8000\n$", out)


def test_format_blank_line_unchanged():
    f = _fmt()
    assert f.format_line("\n") == "\n"


# ---------------------------------------------------------------------------
# strip_log_timestamp (existing helper, regression)
# ---------------------------------------------------------------------------

def test_strip_log_timestamp_iso_with_millis():
    ts, rest = strip_log_timestamp("2026-05-22T17:57:25.236Z INFO foo")
    assert ts == "2026-05-22T17:57:25.236Z"
    assert rest == "INFO foo"


def test_strip_log_timestamp_no_match():
    ts, rest = strip_log_timestamp("no timestamp here")
    assert ts == ""
    assert rest == "no timestamp here"


# -- timestamp reuse ----------------------------------------------------------

def test_normalize_timestamp_utc_converts_to_local():
    import datetime
    expect = (datetime.datetime(2026, 8, 27, 17, 19, 1, tzinfo=datetime.timezone.utc)
              .astimezone().strftime("%H:%M:%S"))
    assert normalize_timestamp("2026-08-27T17:19:01.723Z") == expect


def test_normalize_timestamp_bare_time_kept():
    assert normalize_timestamp("13:18:56.412") == "13:18:56"


def test_normalize_timestamp_unparseable_returns_none():
    assert normalize_timestamp("not-a-time-at-all") is None


def test_reuse_timestamp_prints_the_lines_own_time():
    f = LogFormatter(reuse_timestamp=True)
    f._use_color = False
    out = f.format_line("2026-08-27T17:19:01.723Z [INFO] [client] hi\n")
    assert out.endswith("[INFO] [client] hi\n")
    assert out.startswith(normalize_timestamp("2026-08-27T17:19:01.723Z"))


def test_reuse_timestamp_falls_back_to_now_without_one():
    import datetime
    f = LogFormatter(reuse_timestamp=True)
    f._use_color = False
    out = f.format_line("[INFO] [client] hi\n")
    assert out.startswith(datetime.datetime.now().strftime("%H:%M"))
