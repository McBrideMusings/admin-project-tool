"""Generic log formatter — consistent timestamps and level coloring for subprocess output."""

import json
import re
import sys
import datetime

from .core import C

_TIMESTAMP_PATTERNS = [
    re.compile(r"\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}[.,]\d+([+-]\d{4}|Z)?\s*"),
    re.compile(r"\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}([+-]\d{4}|Z)?\s*"),
    re.compile(r"^\d{2}:\d{2}:\d{2}[.,]?\d*\s*"),
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
    def __init__(self, timestamp_fmt="%H:%M:%S"):
        self._fmt = timestamp_fmt
        self._use_color = sys.stdout.isatty()

    def format_line(self, raw_line):
        line = raw_line.rstrip("\n")
        if not line.strip():
            return raw_line

        parsed = parse_json_log_line(line)
        if parsed is not None:
            line = self._render_json(parsed)
        else:
            line = self._strip_timestamp(line)
            if self._use_color:
                line = self._colorize_level(line)

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

    def _strip_timestamp(self, line: str) -> str:
        _, rest = strip_log_timestamp(line)
        return rest

    def _colorize_level(self, line):
        for pat, color in _LEVEL_COLORS:
            if pat.search(line):
                return pat.sub(lambda m: f"{color}{m.group()}{C.RESET}", line, count=1)
        return line


def make_formatter(**kwargs):
    return LogFormatter(**kwargs).format_line
