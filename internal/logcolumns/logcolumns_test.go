package logcolumns

import (
	"regexp"
	"testing"
)

// -- parseColumnSpec ---------------------------------------------------------

func TestParseColumnSpecFixedWidth(t *testing.T) {
	col, err := parseColumnSpec("Timestamp:26")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if col.Name != "Timestamp" || col.Width != 26 {
		t.Fatalf("got %+v", col)
	}
}

func TestParseColumnSpecFillRemaining(t *testing.T) {
	col, err := parseColumnSpec("Message:*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if col.Name != "Message" || col.Width != -1 {
		t.Fatalf("got %+v", col)
	}
}

func TestParseColumnSpecHidden(t *testing.T) {
	col, err := parseColumnSpec("Level:0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if col.Name != "Level" || col.Width != 0 {
		t.Fatalf("got %+v", col)
	}
}

func TestParseColumnSpecNoColonIsError(t *testing.T) {
	if _, err := parseColumnSpec("Timestamp"); err == nil {
		t.Fatal("expected an error for a spec with no ':'")
	}
}

// -- ColumnFormatter.FormatLine -----------------------------------------------

func TestColumnFormatterPrintsHeaderOnceBeforeFirstLine(t *testing.T) {
	cf := NewColumnFormatter([]Column{
		{Name: "Timestamp", Width: 12},
		{Name: "Level", Width: 6},
		{Name: "Message", Width: -1},
	})

	first := cf.FormatLine("INFO starting up\n")
	if !regexp.MustCompile(`^Timestamp.*Level.*Message\n`).MatchString(first) {
		t.Fatalf("expected header on first line, got %q", first)
	}

	second := cf.FormatLine("INFO still going\n")
	if regexp.MustCompile(`^Timestamp`).MatchString(second) {
		t.Fatalf("expected no header on second line, got %q", second)
	}
}

func TestColumnFormatterBlankLinePassesThroughUnchangedAndDoesNotConsumeHeader(t *testing.T) {
	cf := NewColumnFormatter(DefaultColumns["generic"])
	if got := cf.FormatLine("\n"); got != "\n" {
		t.Fatalf("got %q, want unchanged", got)
	}
	// The header still hasn't printed — a blank line must not count as "the
	// first non-blank line".
	first := cf.FormatLine("INFO hello\n")
	if !regexp.MustCompile(`^Timestamp`).MatchString(first) {
		t.Fatalf("expected header on first real line, got %q", first)
	}
}

func TestColumnFormatterRendersGenericFieldsIntoColumns(t *testing.T) {
	cf := NewColumnFormatter([]Column{
		{Name: "Level", Width: 6},
		{Name: "Message", Width: -1},
	})
	cf.FormatLine("primer\n") // consume the header

	out := cf.FormatLine("2026-05-22T17:57:25.236Z WARN disk almost full\n")
	if out != "WARN  WARN disk almost full\n" {
		t.Fatalf("got %q", out)
	}
}

func TestColumnFormatterHidesZeroWidthColumn(t *testing.T) {
	cf := NewColumnFormatter([]Column{
		{Name: "Timestamp", Width: 0},
		{Name: "Message", Width: -1},
	})
	header := cf.FormatLine("first\n")
	if regexp.MustCompile(`(?i)timestamp`).MatchString(header) {
		t.Fatalf("expected Timestamp column hidden, got %q", header)
	}
}

// -- MakeColumnFormatter -------------------------------------------------------

func TestMakeColumnFormatterUsesSourceDefaultWithNoConfig(t *testing.T) {
	f := MakeColumnFormatter("docker", nil)
	out := f("first line\n")
	// docker's default Timestamp column is 26 wide — wider than generic's 20.
	if !regexp.MustCompile(`^Timestamp {17}`).MatchString(out) {
		t.Fatalf("got %q, want the docker default (26-wide Timestamp) header", out)
	}
}

func TestMakeColumnFormatterFallsBackToGenericForUnknownSource(t *testing.T) {
	f := MakeColumnFormatter("some-unknown-source", nil)
	out := f("first line\n")
	if !regexp.MustCompile(`^Timestamp {11}`).MatchString(out) {
		t.Fatalf("got %q, want the generic default (20-wide Timestamp) header", out)
	}
}

func TestMakeColumnFormatterUsesOverrideColumns(t *testing.T) {
	config := map[string]any{
		"docker": map[string]any{
			"columns": []any{"Level:6", "Message:*"},
		},
	}
	f := MakeColumnFormatter("docker", config)
	header := f("first\n")
	if !regexp.MustCompile(`^Level {3}Message\n`).MatchString(header) {
		t.Fatalf("got %q, want the overridden Level/Message header", header)
	}
}

func TestMakeColumnFormatterApplesOsLogSelectsAppleFormatter(t *testing.T) {
	f := MakeColumnFormatter("apple_os_log", nil)
	// Apple's preamble passes through untouched until the native header line.
	preamble := "Filtering the log data using ...\n"
	if got := f(preamble); got != preamble {
		t.Fatalf("got %q, want preamble passed through unchanged", got)
	}
}
