package streams

import "testing"

func capture() (*[]string, func(string)) {
	var out []string
	return &out, func(s string) { out = append(out, s) }
}

func joined(out *[]string) string {
	s := ""
	for _, c := range *out {
		s += c
	}
	return s
}

func TestNoFormatterPassthrough(t *testing.T) {
	out, sink := capture()
	pump := &OutputPump{Out: sink}
	pump.Write("building")
	pump.Write("...")
	if got := joined(out); got != "building..." {
		t.Fatalf("got %q", got)
	}
}

func TestFormatterBuffersUntilACompleteLine(t *testing.T) {
	out, sink := capture()
	pump := &OutputPump{Out: sink, Formatter: func(line string) string { return "X " + line }}
	pump.Write("hel")
	if got := joined(out); got != "" {
		t.Fatalf("expected nothing yet, got %q", got)
	}
	pump.Write("lo\n")
	if got := joined(out); got != "X hello\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatterHandlesMultipleLinesInOneChunk(t *testing.T) {
	out, sink := capture()
	pump := &OutputPump{Out: sink, Formatter: func(line string) string { return "X " + line }}
	pump.Write("one\ntwo\nthre")
	if got := joined(out); got != "X one\nX two\n" {
		t.Fatalf("got %q", got)
	}
	pump.Write("e\n")
	if got := joined(out); got != "X one\nX two\nX three\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFlushEmitsTrailingPartialLine(t *testing.T) {
	out, sink := capture()
	pump := &OutputPump{Out: sink, Formatter: func(line string) string { return "X " + line }}
	pump.Write("no newline yet")
	if got := joined(out); got != "" {
		t.Fatalf("expected nothing yet, got %q", got)
	}
	pump.Flush()
	if got := joined(out); got != "X no newline yet\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFlushIsANoopWithNothingBuffered(t *testing.T) {
	out, sink := capture()
	pump := &OutputPump{Out: sink, Formatter: func(line string) string { return "X " + line }}
	pump.Write("complete\n")
	*out = nil
	pump.Flush()
	if got := joined(out); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestFlushIsANoopWithoutAFormatter(t *testing.T) {
	out, sink := capture()
	pump := &OutputPump{Out: sink}
	pump.Write("no newline")
	*out = nil
	pump.Flush()
	if got := joined(out); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestLogTransformDiffersFromDisplay(t *testing.T) {
	var logged []string
	out, sink := capture()
	formatter := func(line string) string { return "X " + line }
	pump := &OutputPump{
		Out:          sink,
		Formatter:    formatter,
		LogTransform: formatter,
		LogFn:        func(s string) { logged = append(logged, s) },
	}
	pump.Write("raw\n")
	if got := joined(&logged); got != "X raw\n" {
		t.Fatalf("logged: got %q", got)
	}
	if got := joined(out); got != "X raw\n" {
		t.Fatalf("displayed: got %q", got)
	}
}

func TestLogGetsRawTextWithNoLogTransform(t *testing.T) {
	var logged []string
	out, sink := capture()
	pump := &OutputPump{
		Out:       sink,
		Formatter: func(line string) string { return "X " + line },
		LogFn:     func(s string) { logged = append(logged, s) },
	}
	pump.Write("raw\n")
	if got := joined(&logged); got != "raw\n" {
		t.Fatalf("logged: got %q", got)
	}
	if got := joined(out); got != "X raw\n" {
		t.Fatalf("displayed: got %q", got)
	}
}

func TestDisplayTransformAppliesOnlyWithoutAFormatter(t *testing.T) {
	out, sink := capture()
	pump := &OutputPump{Out: sink, DisplayTransform: func(t string) string { return "[" + t + "]" }}
	pump.Write("hi")
	if got := joined(out); got != "[hi]" {
		t.Fatalf("got %q", got)
	}
}

func TestCollectCapturesEveryRawChunk(t *testing.T) {
	var collected []string
	pump := &OutputPump{Collect: &collected, Formatter: func(line string) string { return "X " + line }}
	pump.Write("ab")
	pump.Write("c\n")
	if len(collected) != 2 || collected[0] != "ab" || collected[1] != "c\n" {
		t.Fatalf("got %v", collected)
	}
}

func TestNoLogFnIsANoopForLogging(t *testing.T) {
	out, sink := capture()
	pump := &OutputPump{Out: sink}
	pump.Write("hello\n")
	if got := joined(out); got != "hello\n" {
		t.Fatalf("got %q", got)
	}
}

func TestNilOutDoesNotPanic(t *testing.T) {
	pump := &OutputPump{}
	pump.Write("hello")
	pump.Flush()
}
