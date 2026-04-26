"""Column-based log formatter for structured, header-prefixed log output.

Every log stream routes through ColumnFormatter:
- apple_os_log sources: native Apple header line detected and replaced with ours.
  Data lines are parsed using the fixed native layout and reassembled with only
  the selected columns, so filtering works end-to-end.
- All other sources: header printed before first line, then each line is parsed
  into Timestamp / Level / Message fields and rendered column-aligned.

Column spec format: 'Name:width' (fixed chars), 'Name:*' (fill remaining), or
'Name:0' (hidden — skipped in header and data).
"""

import re
import sys
from dataclasses import dataclass
from datetime import datetime

from .core import C
from .log_format import _LEVEL_COLORS, strip_log_timestamp


@dataclass
class Column:
    name: str
    width: int  # -1 = fill remaining; 0 = hidden


# Full native Apple os_log column layout — fixed positions used to parse data lines.
# These widths match what `log stream` / `simctl spawn log stream` actually emits.
APPLE_OS_LOG_NATIVE = [
    Column("Timestamp", 32),
    Column("Thread",    11),
    Column("Type",      12),
    Column("Activity",  21),
    Column("PID",        7),
    Column("TTL",        5),
    Column("Message",   -1),
]

DEFAULT_COLUMNS: dict[str, list[Column]] = {
    "apple_os_log": APPLE_OS_LOG_NATIVE,
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

# Level extraction for generic/docker column rendering.
_LEVEL_RE = re.compile(
    r"\b(ERROR|ERR|FATAL|CRIT|WARN|WARNING|INFO|DEBUG|TRACE)\b",
    re.IGNORECASE,
)


def _find_level_color(text: str) -> str | None:
    """Find the color for a log level in text. Returns None if no level found."""
    if _LEVEL_RE.search(text):
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


def _extract_native_fields(line: str) -> dict[str, str]:
    """Extract all fields from a native Apple log data line using fixed column positions."""
    fields = {}
    pos = 0
    for col in APPLE_OS_LOG_NATIVE:
        if col.width == -1:
            fields[col.name] = line[pos:].rstrip("\n")
        else:
            fields[col.name] = line[pos:pos + col.width]
            pos += col.width
    return fields


class ColumnFormatter:
    """Formats log lines under a column header row.

    apple_passthrough=True: passes preamble lines through until the native Apple
    header (starts with "Timestamp") is detected, swaps it for our own header,
    then parses each data line with the fixed native layout and reassembles with
    only the selected columns.

    apple_passthrough=False: prints our header before the first non-empty line,
    then parses each line into Timestamp/Level/Message fields and renders them
    column-aligned. A new ColumnFormatter instance is created on each rebuild,
    so the header re-prints at stream restart (intentional — signals the restart).
    """

    def __init__(self, columns: list[Column], apple_passthrough: bool = False):
        self._columns = columns
        self._apple_passthrough = apple_passthrough
        self._header_printed = False
        self._use_color = sys.stdout.isatty()

    def format_line(self, raw_line: str) -> str:
        if self._apple_passthrough:
            if not self._header_printed:
                if raw_line.lstrip().startswith("Timestamp"):
                    self._header_printed = True
                    return self._render_header()
                return raw_line  # preamble — pass through

            return self._reformat_apple_line(raw_line)

        else:
            if not raw_line.strip():
                return raw_line
            if not self._header_printed:
                self._header_printed = True
                return self._render_header() + self._reformat_generic_line(raw_line)
            return self._reformat_generic_line(raw_line)

    def _reformat_apple_line(self, line: str) -> str:
        if not line.strip():
            return line
        # Native Apple log lines span all columns (Timestamp=32, Thread=11, ... = 88+ chars).
        # Short lines are error messages or preamble from simctl — pass through raw.
        if len(line.rstrip("\n")) < 40:
            return line
        fields = _extract_native_fields(line)
        parts = []
        for col in self._columns:
            if col.width == 0:
                continue
            val = fields.get(col.name, "")
            if col.width == -1:
                parts.append(val.rstrip())
            else:
                parts.append(val[:col.width].ljust(col.width))
        return "".join(parts).rstrip() + "\n"

    def _reformat_generic_line(self, raw_line: str) -> str:
        stripped = raw_line.rstrip("\n")
        _, rest = strip_log_timestamp(stripped)
        rest = rest.strip()

        level = ""
        m = _LEVEL_RE.search(rest)
        if m:
            level = m.group()

        field_map = {
            "Timestamp": datetime.now().strftime("%H:%M:%S.%f"),
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
        sep = "" if self._apple_passthrough else "  "
        parts = []
        for col in self._columns:
            if col.width == 0:
                continue
            if col.width == -1:
                parts.append(col.name)
            else:
                parts.append(col.name.ljust(col.width))
        header = sep.join(parts).rstrip()

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

    apple_passthrough = source == "apple_os_log"
    return ColumnFormatter(columns, apple_passthrough=apple_passthrough)
