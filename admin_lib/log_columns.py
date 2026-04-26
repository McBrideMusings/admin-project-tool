"""Column-based log formatter for structured, header-prefixed log output.

Every log stream routes through ColumnFormatter:
- apple_os_log sources: native Apple header line detected and replaced with ours.
  Data lines are parsed using the fixed native layout and reassembled with only
  the selected columns, so filtering works end-to-end.
- All other sources: header printed before first line, then each line is formatted
  via LogFormatter (timestamp extraction + level colorization).
"""

import sys
from dataclasses import dataclass

from .core import C
from .log_format import LogFormatter


@dataclass
class Column:
    name: str
    width: int  # -1 = fill remaining width


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


def _parse_column_spec(spec: str) -> Column:
    """Parse 'Name:width' or 'Name:*' into a Column."""
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
    then reformats each line via LogFormatter.
    """

    def __init__(self, columns: list[Column], apple_passthrough: bool = False):
        self._columns = columns
        self._apple_passthrough = apple_passthrough
        self._log_formatter = LogFormatter(timestamp_fmt="%H:%M:%S.%f") if not apple_passthrough else None
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
                return self._render_header() + self._log_formatter.format_line(raw_line)
            return self._log_formatter.format_line(raw_line)

    def _reformat_apple_line(self, line: str) -> str:
        if not line.strip():
            return line
        fields = _extract_native_fields(line)
        parts = []
        for col in self._columns:
            val = fields.get(col.name, "")
            if col.width == -1:
                parts.append(val.rstrip())
            else:
                parts.append(val[:col.width].ljust(col.width))
        return "".join(parts).rstrip() + "\n"

    def _render_header(self) -> str:
        sep = "" if self._apple_passthrough else "  "
        parts = []
        for col in self._columns:
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
    Column override strings: 'Name:width' or 'Name:*' for fill-remaining.
    """
    override = (config or {}).get(source, {})
    col_specs = override.get("columns") if isinstance(override, dict) else None

    if col_specs:
        columns = [_parse_column_spec(s) for s in col_specs]
    else:
        columns = DEFAULT_COLUMNS.get(source, DEFAULT_COLUMNS["generic"])

    apple_passthrough = source == "apple_os_log"
    return ColumnFormatter(columns, apple_passthrough=apple_passthrough)
