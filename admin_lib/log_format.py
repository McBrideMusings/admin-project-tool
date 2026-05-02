"""Generic log formatter — consistent timestamps and level coloring for subprocess output."""

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


class LogFormatter:
    def __init__(self, timestamp_fmt="%H:%M:%S"):
        self._fmt = timestamp_fmt
        self._use_color = sys.stdout.isatty()

    def format_line(self, raw_line):
        line = raw_line.rstrip("\n")
        if not line.strip():
            return raw_line

        line = self._strip_timestamp(line)
        if self._use_color:
            line = self._colorize_level(line)

        ts = datetime.datetime.now().strftime(self._fmt)
        return f"{ts}  {line}\n"

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
