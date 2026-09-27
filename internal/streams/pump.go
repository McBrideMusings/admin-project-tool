// Package streams holds OutputPump — the single place a running child
// process's output reaches the terminal, a log, and/or an in-memory
// collector, from one read of one pipe.
//
// This loop used to exist as five separate copies of the same
// read-a-chunk/format/write-to-terminal/write-to-log logic; this repo's
// CLAUDE.md records that as the reason the pump exists in exactly one place
// at all. Every real difference between callers — pty vs pipe, whether a
// formatter is set, whether the log gets raw or formatted text, whether a
// partial line needs flushing on exit — is a field on OutputPump, not a
// second body of this type.
package streams

import "strings"

// OutputPump feeds child-process output to a terminal writer, a log
// callback, and/or a collector — the one place every caller writes through.
//
// Out:              called with the text that should reach the terminal.
// Left nil is a valid, if unusual, configuration — a pump used only to
//
//	collect/log without ever displaying anything.
//
// LogFn:             called with each chunk/line that should be logged, or
//
//	nil to skip logging entirely.
//
// Formatter:         when set, Out only ever receives a complete line (with
//
//	its trailing newline) — output is buffered until "\n"
//	arrives, so a formatter that lays out columns always gets a
//	whole line. When unset, every chunk reaches Out immediately,
//	un-split — required for newline-less output (progress bars, a
//	bare prompt) to appear live.
//
// Collect:           appended to with every raw chunk Write receives, or
//
//	nil to skip collection.
//
// LogTransform:      applied to a chunk before it reaches LogFn. Defaults to
//
//	the raw chunk. Pass Formatter itself here to log the
//	formatted text instead of the raw text.
//
// DisplayTransform:  applied to a chunk before Out, when no Formatter is
//
//	set. No effect once Formatter is set — the formatter IS the
//	display transform in that mode.
type OutputPump struct {
	Out              func(text string)
	LogFn            func(text string)
	Formatter        func(lineWithNewline string) string
	Collect          *[]string
	LogTransform     func(text string) string
	DisplayTransform func(text string) string

	buf strings.Builder
}

// Write feeds one chunk — a whole line or an arbitrary read — through the
// pump.
func (p *OutputPump) Write(text string) {
	if p.LogFn != nil {
		if p.LogTransform != nil {
			p.LogFn(p.LogTransform(text))
		} else {
			p.LogFn(text)
		}
	}
	if p.Collect != nil {
		*p.Collect = append(*p.Collect, text)
	}
	if p.Formatter != nil {
		p.buf.WriteString(text)
		p.drainCompleteLines()
		return
	}
	out := text
	if p.DisplayTransform != nil {
		out = p.DisplayTransform(text)
	}
	p.emit(out)
}

func (p *OutputPump) drainCompleteLines() {
	for {
		s := p.buf.String()
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			return
		}
		line := s[:idx]
		p.buf.Reset()
		p.buf.WriteString(s[idx+1:])
		p.emit(p.Formatter(line + "\n"))
	}
}

// Flush emits a trailing, newline-less partial line once the child is gone.
// Only meaningful with a Formatter set (the no-formatter path already wrote
// every chunk immediately in Write, partial line included) — a no-op
// otherwise, or when nothing is buffered.
func (p *OutputPump) Flush() {
	if p.Formatter == nil || p.buf.Len() == 0 {
		return
	}
	line := p.buf.String()
	p.buf.Reset()
	p.emit(p.Formatter(line + "\n"))
}

func (p *OutputPump) emit(s string) {
	if p.Out != nil {
		p.Out(s)
	}
}
