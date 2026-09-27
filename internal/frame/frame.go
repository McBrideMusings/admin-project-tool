// Package frame pins a footer to the bottom of the terminal for the life of
// a long-running action: a status row (label + elapsed time), the absolute
// log path, then one row per declared URL — every row an OSC 8 hyperlink so
// the log path and each URL are clickable. The action's own output scrolls
// in the rows above it via a DECSTBM scroll region.
//
// Only one frame is active at a time (one process runs one dev loop, the
// same assumption internal/luahost's reload machinery makes) —
// Activate/Deactivate/Write are the package-level surface every caller
// goes through.
package frame

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	esc   = "\x1b"
	dim   = esc + "[2m"
	bold  = esc + "[1m"
	cyan  = esc + "[36m"
	green = esc + "[32m"
	reset = esc + "[0m"
)

// URLEntry is one resolved (label, url) pair for the footer — the label and
// the URL are the same string for an entry with nothing to add.
type URLEntry struct {
	Label string
	URL   string
}

// Frame is a scroll region + footer: rows 1..N-h scroll for the child's own
// output, and the footer owns the rest.
type Frame struct {
	Label   string
	URLs    []URLEntry
	LogPath string

	// Rows/Cols are the terminal's size. Exported so a test can override
	// them after construction (New reads the real size, which a test
	// harness rarely wants).
	Rows, Cols int

	w         io.Writer
	started   time.Time
	active    bool
	paneID    string
	stop      chan struct{}
	stopWinch func()
}

func termSize(w io.Writer) (rows, cols int) {
	if f, ok := w.(*os.File); ok {
		if width, height, err := termGetSize(f.Fd()); err == nil && width > 0 && height > 0 {
			return height, width
		}
	}
	return 24, 80
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return termIsTerminal(f.Fd())
}

// herdrPaneID returns the herdr pane id, or "" when not running inside
// herdr. HERDR_ENV=1 is the presence test; HERDR_PANE_ID set-but-empty
// still counts as "not in herdr" rather than printing an empty segment.
func herdrPaneID() string {
	if os.Getenv("HERDR_ENV") != "1" {
		return ""
	}
	return os.Getenv("HERDR_PANE_ID")
}

// newFrame builds a Frame writing to w, sized from w's real terminal size
// (or the 80x24 fallback when w isn't one). Unexported: real callers go
// through Activate, which also runs the should-this-happen-at-all checks; a
// test in this package can call it directly to inspect rendering without
// those checks.
func newFrame(label string, urls []URLEntry, logPath string, w io.Writer) *Frame {
	rows, cols := termSize(w)
	return &Frame{
		Label:   label,
		URLs:    urls,
		LogPath: logPath,
		Rows:    rows,
		Cols:    cols,
		w:       w,
		started: time.Now(),
		paneID:  herdrPaneID(),
	}
}

// Height is the footer's row count: the status/separator row, the log-path
// row, then one row per URL.
func (f *Frame) Height() int { return 2 + len(f.URLs) }

// RegionRows is how many rows are left for the child — never fewer than 2,
// or the region is unusable.
func (f *Frame) RegionRows() int {
	r := f.Rows - f.Height()
	if r < 2 {
		return 2
	}
	return r
}

// -- lifecycle ---------------------------------------------------------

func (f *Frame) enter() {
	io.WriteString(f.w, strings.Repeat("\n", f.Height()))
	f.setRegion()
	f.active = true
	f.drawLocked()
	f.stop = make(chan struct{})
	go f.tick()

	winch := make(chan struct{}, 1)
	f.stopWinch = notifyWinch(winch)
	go f.watchWinch(winch)
}

func (f *Frame) leave() {
	if !f.active {
		return
	}
	f.active = false
	close(f.stop)
	if f.stopWinch != nil {
		f.stopWinch()
		f.stopWinch = nil
	}
	// The terminal may have resized since enter() computed Rows/Cols — a
	// stale value here would position the cursor on a row the footer (at
	// the CURRENT size) still owns, mangling whatever prints next (see
	// admin-0tx3).
	f.Rows, f.Cols = termSize(f.w)
	// Erase the footer rather than scrolling it into scrollback: move to
	// the footer's first row (at the freshly re-read size), erase to end
	// of screen, then reset the scroll region. The cursor is left parked
	// on that row — no trailing newline — so the next thing printed (e.g.
	// AnnounceEnd's "-> wrote: <path>" line) starts exactly where the
	// footer used to be, instead of one row into scrollback below it.
	first := f.RegionRows() + 1
	fmt.Fprintf(f.w, "%s[%d;1H", esc, first)
	io.WriteString(f.w, esc+"[J")
	io.WriteString(f.w, esc+"[r")
	if flusher, ok := f.w.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
}

func (f *Frame) tick() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			mu.Lock()
			if f.active {
				f.drawLocked()
			}
			mu.Unlock()
		case <-f.stop:
			return
		}
	}
}

// watchWinch forwards SIGWINCH notifications into reflow, mirroring tick's
// lock-check-unlock shape. Exits when the frame's stop channel closes, the
// same signal tick already watches for.
func (f *Frame) watchWinch(winch <-chan struct{}) {
	for {
		select {
		case <-winch:
			mu.Lock()
			f.reflow()
			mu.Unlock()
		case <-f.stop:
			return
		}
	}
}

// reflow re-reads the terminal size and re-issues the scroll region and
// footer for it — the resize response to a SIGWINCH. Callers must hold mu.
// Independent of any real signal so a test can call it directly after
// mutating Rows/Cols.
func (f *Frame) reflow() {
	if !f.active {
		return
	}
	f.Rows, f.Cols = termSize(f.w)
	f.setRegion()
	f.drawLocked()
}

func (f *Frame) setRegion() {
	bottom := f.RegionRows()
	fmt.Fprintf(f.w, "%s[1;%dr", esc, bottom)
	fmt.Fprintf(f.w, "%s[%d;1H", esc, bottom)
}

// -- painting ------------------------------------------------------------

// drawLocked repaints the footer. Callers must hold mu.
func (f *Frame) drawLocked() {
	if !f.active {
		return
	}
	first := f.RegionRows() + 1
	io.WriteString(f.w, esc+"7") // DECSC — save cursor
	for i, line := range f.ContentRows() {
		fmt.Fprintf(f.w, "%s[%d;1H%s[2K%s", esc, first+i, esc, line)
	}
	io.WriteString(f.w, esc+"8") // DECRC — restore cursor
}

// ContentRows renders the footer's own lines — the status row, the log-path
// row, then one row per URL. Exported so a test (and a future non-terminal
// renderer) can inspect exactly what a repaint would draw, independent of
// the escape-sequence plumbing around it.
func (f *Frame) ContentRows() []string {
	out := []string{f.statusRow(), f.logRow()}
	// A bare-string URL labels itself; printing it in both columns is
	// noise, so those rows carry no label and don't widen the label
	// column.
	width := 0
	for _, e := range f.URLs {
		if e.Label != e.URL && len(e.Label) > width {
			width = len(e.Label)
		}
	}
	for _, e := range f.URLs {
		var col string
		if e.Label != e.URL {
			col = bold + padRight(e.Label, width) + reset + "  "
		} else {
			col = strings.Repeat(" ", width+2)
		}
		shown := clip(e.URL, f.Cols-width-6)
		out = append(out, fmt.Sprintf(" %s●%s %s%s%s%s", green, reset, col, cyan, osc8(e.URL, shown), reset))
	}
	return out
}

func padRight(s string, width int) string {
	if n := width - len([]rune(s)); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func (f *Frame) statusRow() string {
	text := " " + f.Label + " · " + elapsed(time.Since(f.started))
	if f.paneID != "" {
		text += " · " + f.paneID
	}
	text += " "
	ruleLen := f.Cols - len([]rune(text)) - 2
	if ruleLen < 0 {
		ruleLen = 0
	}
	return dim + "──" + text + strings.Repeat("─", ruleLen) + reset
}

func (f *Frame) logRow() string {
	shown := clip(f.LogPath, f.Cols-4)
	return "   " + dim + osc8(fileURL(f.LogPath), shown) + reset
}

func osc8(target, label string) string {
	return esc + "]8;;" + target + esc + "\\" + label + esc + "]8;;" + esc + "\\"
}

func fileURL(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	u := url.URL{Path: abs}
	return "file://" + u.EscapedPath()
}

func elapsed(d time.Duration) string {
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
	}
}

func clip(text string, width int) string {
	r := []rune(text)
	if len(r) <= width {
		return text
	}
	if width <= 0 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}

// ---------------------------------------------------------------------------
// Module surface — every caller goes through these.
// ---------------------------------------------------------------------------

var (
	mu     sync.Mutex
	active *Frame
)

// CanActivate reports whether Activate(..., w) would go on to raise a frame
// — the same three environment checks Activate itself runs (a frame already
// up, ADMIN_NO_FRAME, w not a terminal), exposed so a caller can skip
// frame-only side effects (materializing a log file, calling SetLogContext)
// when it already knows Activate is about to decline. It does not reserve
// anything: Activate re-checks the same conditions under its own lock, so a
// caller between CanActivate and Activate racing another Activate still
// gets a correct (if possibly stale) decline from Activate itself.
func CanActivate(w io.Writer) bool {
	mu.Lock()
	defer mu.Unlock()
	if active != nil {
		return false
	}
	if os.Getenv("ADMIN_NO_FRAME") != "" {
		return false
	}
	return isTerminalWriter(w)
}

// Activate starts a frame writing to w, or returns nil when framing can't
// or shouldn't apply: w isn't a terminal (piped output, CI), ADMIN_NO_FRAME
// is set, or a frame is already up.
//
// An empty urls is NOT by itself a reason to decline — the caller (a kind
// factory reading its action's `footer`/`urls` keys) owns the
// should-there-be-a-footer question; an action carrying footer=true
// deliberately arrives here with nothing to link, and ContentRows already
// degrades to the status row plus the log row (Height() is 2 either way).
func Activate(label string, urls []URLEntry, logPath string, w io.Writer) *Frame {
	mu.Lock()
	defer mu.Unlock()
	if active != nil {
		return nil
	}
	if os.Getenv("ADMIN_NO_FRAME") != "" {
		return nil
	}
	if !isTerminalWriter(w) {
		return nil
	}
	f := newFrame(label, urls, logPath, w)
	active = f
	f.enter()
	return f
}

// Deactivate tears down the frame f started. Ignores any frame it does not
// own: a multi-target whose picked target delegates to another action puts
// two candidate frames on the call stack — the inner one's Activate is
// declined (a frame is already up) and returns nil, and nil must tear down
// nothing, or an unconditional teardown would strip the outer footer while
// the outer action is still running.
func Deactivate(f *Frame) {
	mu.Lock()
	defer mu.Unlock()
	if f == nil || active == nil || f != active {
		return
	}
	active.leave()
	active = nil
}

// Write writes text to w and, if a frame is active, repaints it — the one
// place every caller writes child output through, so the footer is redrawn
// after every chunk. The repaint always targets the active frame's own
// writer (not necessarily w): only one frame is ever active in a process,
// consistent with the "one process runs one dev loop" assumption, and this
// keeps a caller writing to some other stream from corrupting a footer it
// has no relationship to.
func Write(w io.Writer, text string) {
	mu.Lock()
	defer mu.Unlock()
	io.WriteString(w, text)
	if active != nil {
		active.drawLocked()
	}
}

// ApplyWinsize sizes a child's pty (given its master fd) to the active
// frame's scroll region, not the full screen. Without this a child that
// positions its own cursor absolutely (wrangler, vite, metro) believes it
// owns rows the footer is using and paints over it. No-op when no frame is
// active; best-effort — an ioctl failure is swallowed, never surfaced.
func ApplyWinsize(fd uintptr) {
	mu.Lock()
	f := active
	mu.Unlock()
	if f == nil {
		return
	}
	SetWinsize(fd, f.RegionRows(), f.Cols)
}

// SetWinsize is ApplyWinsize's caller-sized twin: it sizes fd directly to
// rows/cols rather than deriving them from whatever frame (if any) is
// currently active. This is the seam admin-3rgp.61 sizes a dashboard-owned
// pty through — the region the dashboard's terminal view will render the
// child into, known only to that caller, not to any frame the child itself
// may go on to activate inside the pty. Same best-effort/ioctl-swallowing
// contract as ApplyWinsize.
func SetWinsize(fd uintptr, rows, cols int) {
	applyWinsize(fd, rows, cols)
}
