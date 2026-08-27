"""Generic log formatter — consistent timestamps and level coloring for subprocess output."""

import json
import re
import sys
import datetime

from .core import C

# Each pattern eats at most ONE space after the stamp, not the whole run: the
# rest is the line's own indentation, and a stack trace's continuation lines are
# unreadable without it (they are what a log file's timestamped multi-line
# records mostly consist of).
_TIMESTAMP_PATTERNS = [
    re.compile(r"\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}[.,]\d+([+-]\d{4}|Z)?[ \t]?"),
    re.compile(r"\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}([+-]\d{4}|Z)?[ \t]?"),
    re.compile(r"^\d{2}:\d{2}:\d{2}[.,]?\d*[ \t]?"),
]


def strip_log_timestamp(line: str) -> tuple[str, str]:
    """Strip a leading timestamp from line. Returns (timestamp_str, remainder).

    Returns ("", line) if no pattern matches.
    """
    for pat in _TIMESTAMP_PATTERNS:
        m = pat.match(line)
        if m:
            return line[:m.end()].rstrip(), line[m.end():]
    return "", line


_BARE_TIME_RE = re.compile(r"^(\d{2}):(\d{2}):(\d{2})")


def normalize_timestamp(ts: str, fmt: str = "%H:%M:%S") -> str | None:
    """Render a timestamp already present in a log line as local `fmt`.

    Accepts what `strip_log_timestamp` yields: an ISO-8601 stamp (`Z`, an
    offset, or naive) or a bare `HH:MM:SS[.f]` wall clock. A stamp carrying a
    timezone is converted to local time, so a browser's UTC `2026-08-27T17:19:01.723Z`
    prints in the same column, in the same zone, as a local process's own lines.
    Returns None when nothing parses — the caller falls back to arrival time.
    """
    s = ts.strip()
    if not s:
        return None
    m = _BARE_TIME_RE.match(s)
    if m:
        h, mi, sec = (int(g) for g in m.groups())
        try:
            return datetime.datetime.now().replace(
                hour=h, minute=mi, second=sec, microsecond=0).strftime(fmt)
        except ValueError:
            return None
    s = s.replace(",", ".")
    if s.endswith("Z"):
        s = s[:-1] + "+00:00"
    try:
        dt = datetime.datetime.fromisoformat(s)
    except ValueError:
        return None
    if dt.tzinfo is not None:
        dt = dt.astimezone()
    return dt.strftime(fmt)


_LEVEL_COLORS = [
    (re.compile(r"\b(ERROR|ERR|FATAL|CRIT)\b", re.IGNORECASE), C.RED),
    (re.compile(r"\b(WARN|WARNING)\b", re.IGNORECASE), C.YELLOW),
    (re.compile(r"\b(INFO)\b", re.IGNORECASE), C.CYAN),
    (re.compile(r"\b(DEBUG|TRACE)\b", re.IGNORECASE), C.DIM),
]

_LEVEL_COLOR_MAP = {
    "ERROR": C.RED, "ERR": C.RED, "FATAL": C.RED, "CRIT": C.RED, "CRITICAL": C.RED,
    "WARN": C.YELLOW, "WARNING": C.YELLOW,
    "INFO": C.CYAN,
    "DEBUG": C.DIM, "TRACE": C.DIM,
}

_JSON_MESSAGE_KEYS = ("message", "msg", "event")
_JSON_LEVEL_KEYS = ("level", "levelname", "severity")
_JSON_LOGGER_KEYS = ("logger", "name", "logger_name")


def _first_present(obj: dict, keys: tuple[str, ...]):
    """Return obj[k] for the first k in keys present in obj, else None."""
    for k in keys:
        if k in obj:
            return obj[k]
    return None


def parse_json_log_line(line: str) -> dict | None:
    """If `line` is a single-object JSON log record with a message-like field,
    return {"level": str, "logger": str, "message": str}. Otherwise return None.
    """
    s = line.strip()
    if not (s.startswith("{") and s.endswith("}")):
        return None
    try:
        obj = json.loads(s)
    except ValueError:
        return None
    if not isinstance(obj, dict):
        return None
    msg = _first_present(obj, _JSON_MESSAGE_KEYS)
    if msg is None:
        return None
    level = _first_present(obj, _JSON_LEVEL_KEYS)
    logger = _first_present(obj, _JSON_LOGGER_KEYS)
    return {
        "level": str(level).upper() if level is not None else "",
        "logger": str(logger) if logger is not None else "",
        "message": str(msg),
    }


class LogFormatter:
    """Stamp every line with one timestamp format and colorize its level.

    reuse_timestamp: when the line already carries its own timestamp, print
    that (converted to local time) instead of the moment the line arrived.
    Off by default — a subprocess's stamp is usually a duplicate of now(). On
    for sources where arrival time is a lie: log-bridge entries batched up to a
    second in the browser, and a log file replayed by `admin logs`.
    """

    def __init__(self, timestamp_fmt="%H:%M:%S", reuse_timestamp=False):
        self._fmt = timestamp_fmt
        self._reuse_timestamp = reuse_timestamp
        self._use_color = sys.stdout.isatty()

    def format_line(self, raw_line):
        line = raw_line.rstrip("\n")
        if not line.strip():
            return raw_line

        ts = None
        parsed = parse_json_log_line(line)
        if parsed is not None:
            line = self._render_json(parsed)
        else:
            stamp, line = strip_log_timestamp(line)
            if self._reuse_timestamp and stamp:
                ts = normalize_timestamp(stamp, self._fmt)
            if self._use_color:
                line = self._colorize_level(line)

        if ts is None:
            ts = datetime.datetime.now().strftime(self._fmt)
        return f"{ts}  {line}\n"

    def _render_json(self, parsed: dict) -> str:
        level = parsed["level"]
        logger = parsed["logger"]
        message = parsed["message"]
        parts = []
        if level:
            level_str = f"{level:<5}"
            color = _LEVEL_COLOR_MAP.get(level)
            if self._use_color and color:
                level_str = f"{color}{level_str}{C.RESET}"
            parts.append(level_str)
        if logger:
            parts.append(f"{C.DIM}{logger}{C.RESET}" if self._use_color else logger)
        parts.append(message)
        return "  ".join(parts)

    def _colorize_level(self, line):
        for pat, color in _LEVEL_COLORS:
            if pat.search(line):
                return pat.sub(lambda m: f"{color}{m.group()}{C.RESET}", line, count=1)
        return line


def make_formatter(**kwargs):
    return LogFormatter(**kwargs).format_line
