"""Apple `os_log` column layout — optional companion to log_columns.

Bundling this module registers an "apple_os_log" layout in
log_columns.DEFAULT_COLUMNS and an AppleColumnFormatter that:
- passes raw preamble lines through until the native Apple "Timestamp..." header
  is seen, then swaps it for our own header
- parses each subsequent line using fixed Apple os_log column widths and
  reassembles only the selected columns

Apple-archetype projects should declare this module after `log_columns` so the
registration runs at bundle-load time.
"""

from .log_columns import (
    Column,
    ColumnFormatter,
    DEFAULT_COLUMNS,
    _FORMATTERS,
)


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


class AppleColumnFormatter(ColumnFormatter):
    """ColumnFormatter for Apple os_log streams.

    Passes preamble lines through until the native "Timestamp..." header is
    detected, swaps it for our own header, then parses each data line with the
    fixed native layout and reassembles with only the selected columns.
    """

    HEADER_SEP = ""  # Apple lines have no inter-column separator

    def format_line(self, raw_line: str) -> str:
        if not self._header_printed:
            if raw_line.lstrip().startswith("Timestamp"):
                self._header_printed = True
                return self._render_header()
            return raw_line  # preamble — pass through
        return self._reformat_apple_line(raw_line)

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


DEFAULT_COLUMNS["apple_os_log"] = APPLE_OS_LOG_NATIVE
_FORMATTERS["apple_os_log"] = AppleColumnFormatter
