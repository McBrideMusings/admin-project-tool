// Package logcolumns renders a log stream under a fixed column header —
// Timestamp / Level / Message columns, aligned, printed once as a header row
// before the first data line — instead of the default HH:MM:SS.mmm-stamped
// passthrough every other pump gets. Driven by a project's admin.toml
// [output] table.
//
// Apple os_log's native "log stream" layout is a special case handled here
// too (AppleColumnFormatter): it passes preamble lines through untouched,
// swaps Apple's own "Timestamp ..." header for the configured one, then
// reparses each data line's fixed native column positions rather than the
// generic Timestamp/Level/Message split every other source gets.
//
// MakeColumnFormatter is the package's one entry point, built to the
// func(string) string shape internal/reload.Options.Formatter and
// internal/streams.OutputPump.Formatter both expect.
package logcolumns

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/logformat"
)

// ANSI codes for the header row itself — data lines are colored through
// logformat.ColorizeLevel, which carries its own copy. Declared locally
// per this repo's existing per-package-local-ANSI-const convention
// (internal/logformat, internal/luahost/output.go, internal/kinds/printurls.go
// each keep their own rather than sharing a table).
const (
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

// Column describes one rendered column: a fixed character width, -1 to fill
// the remainder of the line, or 0 to hide the column entirely (omitted from
// both the header and every data line).
type Column struct {
	Name  string
	Width int
}

// appleOSLogNative is the full native Apple os_log column layout — the fixed
// positions `log stream` / `simctl spawn log stream` emit, used to parse
// data lines regardless of which of those columns the project asked to see.
var appleOSLogNative = []Column{
	{Name: "Timestamp", Width: 32},
	{Name: "Thread", Width: 11},
	{Name: "Type", Width: 12},
	{Name: "Activity", Width: 21},
	{Name: "PID", Width: 7},
	{Name: "TTL", Width: 5},
	{Name: "Message", Width: -1},
}

// DefaultColumns maps a source name (an [output.<source>] table's key, or
// the literal source string a kind factory passes to MakeColumnFormatter)
// to its default column layout when the project declares no override.
var DefaultColumns = map[string][]Column{
	"generic": {
		{Name: "Timestamp", Width: 20},
		{Name: "Level", Width: 8},
		{Name: "Message", Width: -1},
	},
	"docker": {
		{Name: "Timestamp", Width: 26},
		{Name: "Level", Width: 8},
		{Name: "Message", Width: -1},
	},
	"apple_os_log": appleOSLogNative,
}

// levelRe finds a severity-level word in a data line's message so it can be
// shown in the Level column.
var levelRe = regexp.MustCompile(`(?i)\b(ERROR|ERR|FATAL|CRIT|WARN|WARNING|INFO|DEBUG|TRACE)\b`)

// parseColumnSpec parses 'Name:width', 'Name:*' (fill remaining), or
// 'Name:0' (hidden) into a Column.
func parseColumnSpec(spec string) (Column, error) {
	idx := strings.LastIndex(spec, ":")
	if idx < 0 {
		return Column{}, fmt.Errorf("invalid column spec %q — expected 'Name:width' or 'Name:*'", spec)
	}
	name := strings.TrimSpace(spec[:idx])
	widthStr := strings.TrimSpace(spec[idx+1:])
	if widthStr == "*" {
		return Column{Name: name, Width: -1}, nil
	}
	width, err := strconv.Atoi(widthStr)
	if err != nil {
		return Column{}, fmt.Errorf("invalid column spec %q — expected 'Name:width' or 'Name:*'", spec)
	}
	return Column{Name: name, Width: width}, nil
}

// padTrunc renders val in exactly width characters: truncated if longer,
// space-padded on the right if shorter. Operates on runes (not bytes) so
// width counts characters, not UTF-8 bytes.
func padTrunc(val string, width int) string {
	r := []rune(val)
	if len(r) > width {
		r = r[:width]
	}
	if len(r) < width {
		return string(r) + strings.Repeat(" ", width-len(r))
	}
	return string(r)
}

func isTerminalStdout() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// ColumnFormatter formats log lines under a column header row: the header
// prints once, before the first non-blank line, then every line is parsed
// into Timestamp/Level/Message fields and rendered column-aligned. A fresh
// ColumnFormatter is meant to be built on each stream restart so the header
// re-prints — that re-print is the reader's restart signal, not a bug.
type ColumnFormatter struct {
	Columns   []Column
	HeaderSep string

	headerPrinted bool
	useColor      bool
}

// NewColumnFormatter returns a ColumnFormatter for columns, with color
// resolved from whether stdout is a terminal.
func NewColumnFormatter(columns []Column) *ColumnFormatter {
	return &ColumnFormatter{Columns: columns, HeaderSep: "  ", useColor: isTerminalStdout()}
}

// FormatLine renders one raw line, printing the column header first if this
// is the first non-blank line seen. Bound to the func(string) string shape
// internal/reload.Options.Formatter expects.
func (c *ColumnFormatter) FormatLine(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	if !c.headerPrinted {
		c.headerPrinted = true
		return c.renderHeader() + c.reformatGenericLine(raw)
	}
	return c.reformatGenericLine(raw)
}

func (c *ColumnFormatter) reformatGenericLine(raw string) string {
	stripped := strings.TrimRight(raw, "\n")
	ts, rest := logformat.StripLogTimestamp(stripped)
	rest = strings.TrimSpace(rest)

	level := levelRe.FindString(rest)

	tsDisplay := strings.TrimSpace(ts)
	if tsDisplay == "" {
		tsDisplay = time.Now().Format("15:04:05.000000")
	}

	fields := map[string]string{
		"Timestamp": tsDisplay,
		"Level":     level,
		"Message":   rest,
	}

	line := strings.TrimRight(renderRow(c.Columns, fields, ""), " ")
	if c.useColor {
		line = logformat.ColorizeLevel(line)
	}
	return line + "\n"
}

func (c *ColumnFormatter) renderHeader() string {
	names := map[string]string{}
	for _, col := range c.Columns {
		names[col.Name] = col.Name
	}
	header := strings.TrimRight(renderRow(c.Columns, names, c.HeaderSep), " ")
	if c.useColor {
		return ansiDim + header + ansiReset + "\n"
	}
	return header + "\n"
}

// renderRow lays out fields[col.Name] (falling back to "" when a column has
// no matching field, as the header row does for its own name-as-value) into
// one row string, joined by sep, honoring each Column's width/hide/fill
// rule. Shared by header rendering and data-line rendering, which differ
// only in which value each Column pulls and whether they're separated.
func renderRow(columns []Column, fields map[string]string, sep string) string {
	var parts []string
	for _, col := range columns {
		if col.Width == 0 {
			continue
		}
		val := fields[col.Name]
		if col.Width == -1 {
			parts = append(parts, strings.TrimRight(val, " \t\r\n"))
		} else {
			parts = append(parts, padTrunc(val, col.Width))
		}
	}
	return strings.Join(parts, sep)
}

// AppleColumnFormatter is the ColumnFormatter for Apple os_log streams: it
// passes preamble lines through untouched until it sees the native
// "Timestamp ..." header line, swaps that for the configured header, then
// parses each data line by the fixed native column layout and re-renders
// only the configured columns.
type AppleColumnFormatter struct {
	*ColumnFormatter
}

// NewAppleColumnFormatter returns an AppleColumnFormatter for columns.
// Apple's native lines have no inter-column separator, so HeaderSep is "".
func NewAppleColumnFormatter(columns []Column) *AppleColumnFormatter {
	cf := NewColumnFormatter(columns)
	cf.HeaderSep = ""
	return &AppleColumnFormatter{ColumnFormatter: cf}
}

// FormatLine overrides ColumnFormatter.FormatLine: preamble passes through
// raw until the native header line is recognized, then every subsequent
// line is reparsed by the native fixed layout.
func (a *AppleColumnFormatter) FormatLine(raw string) string {
	if !a.headerPrinted {
		if strings.HasPrefix(strings.TrimLeft(raw, " \t"), "Timestamp") {
			a.headerPrinted = true
			return a.renderHeader()
		}
		return raw // preamble — pass through
	}
	return a.reformatAppleLine(raw)
}

func (a *AppleColumnFormatter) reformatAppleLine(line string) string {
	if strings.TrimSpace(line) == "" {
		return line
	}
	// Native Apple log lines span all columns (Timestamp=32, Thread=11, ... =
	// 88+ chars). Short lines are error messages or preamble from simctl —
	// pass through raw.
	if len([]rune(strings.TrimRight(line, "\n"))) < 40 {
		return line
	}
	fields := extractNativeFields(line)
	return strings.TrimRight(renderRow(a.Columns, fields, ""), " ") + "\n"
}

// extractNativeFields slices line at the fixed native column positions
// (appleOSLogNative), by rune (not byte) so positions count characters, not
// UTF-8 bytes. A line shorter than a fixed-width column's start position
// yields "" for that column and every one after it, rather than panicking on
// an out-of-range slice.
func extractNativeFields(line string) map[string]string {
	fields := map[string]string{}
	r := []rune(line)
	pos := 0
	for _, col := range appleOSLogNative {
		start := pos
		if start > len(r) {
			start = len(r)
		}
		if col.Width == -1 {
			fields[col.Name] = strings.TrimRight(string(r[start:]), "\n")
			continue
		}
		end := start + col.Width
		if end > len(r) {
			end = len(r)
		}
		fields[col.Name] = string(r[start:end])
		pos += col.Width
	}
	return fields
}

// MakeColumnFormatter builds the func(string) string formatter for source
// (e.g. "docker", "apple_os_log") from config — the manifest's [output]
// table (archetypeConfigTable(m, "output")), or nil. config[source].columns,
// when present, is a list of 'Name:width' / 'Name:*' / 'Name:0' specs
// overriding DefaultColumns[source]; a spec that fails to parse is skipped
// rather than aborting the whole formatter, the same "just skip the bad
// entry" leniency internal/kinds already applies to other malformed
// [output]-shaped config (manifest shape validation is admin-3rgp.8's job,
// not a formatter constructor's).
//
// A new instance is returned on every call, so a kind factory that rebuilds
// its formatter on each stream restart gets a header re-print each time.
func MakeColumnFormatter(source string, config map[string]any) func(string) string {
	var override map[string]any
	if tbl, ok := config[source].(map[string]any); ok {
		override = tbl
	}

	var columns []Column
	if specs, ok := override["columns"].([]any); ok {
		for _, s := range specs {
			str, ok := s.(string)
			if !ok {
				continue
			}
			if col, err := parseColumnSpec(str); err == nil {
				columns = append(columns, col)
			}
		}
	}
	if len(columns) == 0 {
		if def, ok := DefaultColumns[source]; ok {
			columns = def
		} else {
			columns = DefaultColumns["generic"]
		}
	}

	if source == "apple_os_log" {
		return NewAppleColumnFormatter(columns).FormatLine
	}
	return NewColumnFormatter(columns).FormatLine
}
