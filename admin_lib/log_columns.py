"""Column-based log formatter for structured, header-prefixed log output.

Streams a log source through ColumnFormatter:
- Header printed before first non-empty line
- Each line parsed into Timestamp / Level / Message and rendered column-aligned

Apple `os_log` parsing lives in the optional `log_columns_apple` module; when
that module is bundled, it registers an "apple_os_log" layout and an
AppleColumnFormatter that handles Apple's native header passthrough and fixed
column extraction.

Column spec format: 'Name:width' (fixed chars), 'Name:*' (fill remaining), or
'Name:0' (hidden — skipped in header and data).
"""

import re
import sys
from dataclasses import dataclass
import datetime

from .core import C
from .log_format import _LEVEL_COLORS, strip_log_timestamp


@dataclass
class Column:
    name: str
    width: int  # -1 = fill remaining; 0 = hidden


DEFAULT_COLUMNS: dict[str, list[Column]] = {
    "generic": [
        Column("Timestamp", 20),
        Column("Level", 8),
        Column("Message", -1),
    ],
    "docker": [
        Column("Timestamp", 26),
        Column("Level", 8),
        Column("Message", -1),
    ],
}


# Source name → ColumnFormatter subclass. Optional modules (e.g. log_columns_apple)
# register their formatter here at import time.
_FORMATTERS: dict[str, type] = {}


_LEVEL_RE = re.compile(
    r"\b(ERROR|ERR|FATAL|CRIT|WARN|WARNING|INFO|DEBUG|TRACE)\b",
    re.IGNORECASE,
)


def _find_level_color(text: str) -> str | None:
    """Find the color for a log level in text. Returns None if no level found."""
    for pat, color in _LEVEL_COLORS:
        if pat.search(text):
            return color
    return None


def _parse_column_spec(spec: str) -> Column:
    """Parse 'Name:width', 'Name:*', or 'Name:0' into a Column.

    Width 0 hides the column entirely (omitted from header and data lines).
    Width -1 (written as '*') fills the remaining line width.
    """
    if ":" not in spec:
        raise ValueError(f"invalid column spec {spec!r} — expected 'Name:width' or 'Name:*'")
    name, width_str = spec.rsplit(":", 1)
    width = -1 if width_str.strip() == "*" else int(width_str.strip())
    return Column(name.strip(), width)


class ColumnFormatter:
    """Formats log lines under a column header row.

    Prints the column header before the first non-empty line, then parses each
    line into Timestamp/Level/Message fields and renders them column-aligned.
    A new ColumnFormatter instance is created on each rebuild, so the header
    re-prints at stream restart (intentional — signals the restart).
    """

    HEADER_SEP = "  "

    def __init__(self, columns: list[Column]):
        self._columns = columns
        self._header_printed = False
        self._use_color = sys.stdout.isatty()

    def format_line(self, raw_line: str) -> str:
        if not raw_line.strip():
            return raw_line
        if not self._header_printed:
            self._header_printed = True
            return self._render_header() + self._reformat_generic_line(raw_line)
        return self._reformat_generic_line(raw_line)

    def _reformat_generic_line(self, raw_line: str) -> str:
        stripped = raw_line.rstrip("\n")
        ts, rest = strip_log_timestamp(stripped)
        rest = rest.strip()

        level = ""
        m = _LEVEL_RE.search(rest)
        if m:
            level = m.group()

        ts_display = ts.strip() if ts else datetime.datetime.now().strftime("%H:%M:%S.%f")
        field_map = {
            "Timestamp": ts_display,
            "Level": level,
            "Message": rest,
        }

        parts = []
        for col in self._columns:
            if col.width == 0:
                continue
            val = field_map.get(col.name, "")
            if col.width == -1:
                parts.append(val.rstrip())
            else:
                parts.append(val[:col.width].ljust(col.width))

        line = "".join(parts).rstrip()
        if self._use_color:
            color = _find_level_color(line)
            if color:
                for pat in _LEVEL_COLORS:
                    if pat[0].search(line):
                        line = pat[0].sub(lambda m: f"{color}{m.group()}{C.RESET}", line, count=1)
                        break
        return line + "\n"

    def _render_header(self) -> str:
        parts = []
        for col in self._columns:
            if col.width == 0:
                continue
            if col.width == -1:
                parts.append(col.name)
            else:
                parts.append(col.name.ljust(col.width))
        header = self.HEADER_SEP.join(parts).rstrip()

        if self._use_color:
            return f"{C.DIM}{header}{C.RESET}\n"
        return f"{header}\n"


def make_column_formatter(source: str, config: dict | None = None) -> "ColumnFormatter":
    """Create a ColumnFormatter for the given source.

    config: the _OUTPUT_CONFIG dict from admin.toml [output], or None.
    Column override strings: 'Name:width', 'Name:*' for fill-remaining, 'Name:0' to hide.
    A new instance is created on each rebuild so the column header re-prints,
    which signals stream restart to the reader.
    """
    override = (config or {}).get(source, {})
    col_specs = override.get("columns") if isinstance(override, dict) else None

    if col_specs:
        columns = [_parse_column_spec(s) for s in col_specs]
    else:
        columns = DEFAULT_COLUMNS.get(source, DEFAULT_COLUMNS["generic"])

    cls = _FORMATTERS.get(source, ColumnFormatter)
    return cls(columns)
