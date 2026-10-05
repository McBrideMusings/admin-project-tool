// terminalview.go is admin-3rgp.65's whole terminal view: attaching to a
// hosted-pty live-work row (Via empty) opens a full-tab terminal over that
// child's pty, rendered through a real terminal emulator
// (charmbracelet/x/vt) so color, cursor positioning and the child's own
// pinned footer all just work — the child's point of view never changes,
// its pty IS its terminal.
//
// Three things this file owns:
//   - the terminalOpener seam: livework.Registry.Attach for a hosted-pty
//     entry calls back into this package (installTerminalOpener, wired from
//     Model.Init) rather than mutating a value-type Model directly. The
//     callback can only record which handle was requested; work.go's
//     attachEntry reads that back and does the actual transition.
//   - sizing: attach resizes the child's pty, and the entry's Screen with
//     it, to the body region. The Screen itself belongs to the hosted pty
//     (internal/kinds/hostedpty.go), which feeds it for the child's whole
//     run.
//   - the body renderer: field rows, dashed rule, then either "waiting for
//     output…" or the Screen's grid — view.go routes here instead of the
//     list/detail layout while attached. The identity row above it
//     (attachBreadcrumb) is view.go's top chrome, in the tab bar's place.
package tui

import (
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// pendingTerminal is the handoff between livework.Registry.Attach (called
// from work.go's attachEntry, on the Bubble Tea loop's own goroutine) and
// the terminal-opener callback the registry invokes synchronously inside
// that same call. The callback cannot mutate a value-type Model itself, so
// it records the requested handle here and attachEntry drains it right
// after Attach returns — see installTerminalOpener and takePendingTerminal.
var (
	pendingTerminalMu sync.Mutex
	pendingTerminal   string
)

// installTerminalOpener wires livework.Default's terminal-opener seam
// (SetTerminalOpener) to the pendingTerminal handoff above. Called once from
// Model.Init — it has no per-model state, so calling it again on a later
// Init (a second dashboard instance in the same process, as a headless test
// might construct) just re-installs the same closure harmlessly.
func installTerminalOpener() {
	livework.Default.SetTerminalOpener(func(handle string) error {
		pendingTerminalMu.Lock()
		pendingTerminal = handle
		pendingTerminalMu.Unlock()
		return nil
	})
}

// takePendingTerminal returns and clears whatever handle the opener last
// recorded. It is drained unconditionally after every attach attempt (see
// work.go's attachEntry) — a launcher-dispatched Attach never writes here,
// so there is nothing to leak into a later, unrelated pty attach.
func takePendingTerminal() (string, bool) {
	pendingTerminalMu.Lock()
	defer pendingTerminalMu.Unlock()
	if pendingTerminal == "" {
		return "", false
	}
	id := pendingTerminal
	pendingTerminal = ""
	return id, true
}

// termTickMsg repaints the terminal view while it is open — the entry's
// Screen changes on the pty drain's goroutine, and workTickMsg's 1s cadence
// is far too slow for a terminal a person is typing into. Re-armed only
// while termAttached; see the Update case.
type termTickMsg struct{}

func termTickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return termTickMsg{} })
}

// openTerminal transitions m into the attached state over e and resizes the
// child's pty — and with it the entry's Screen — to the body region. The
// Screen has been the child's terminal since it started, so everything it
// already printed is on it. Called only from attachEntry, after
// livework.Default.Attach has already succeeded for e.
func (m Model) openTerminal(e *livework.Entry) Model {
	m.termAttached = true
	m.termEntryID = e.ID
	rows, cols := m.termRegion()
	m.termRows, m.termCols = rows, cols
	if e.Ops.Resize != nil {
		_ = e.Ops.Resize(rows, cols)
	}
	return m
}

// detachTerminal returns to the list. It never touches the child — the
// process keeps running, exactly as attaching to a pane never suspends it.
func (m Model) detachTerminal() Model {
	m.termAttached = false
	m.termEntryID = ""
	m.termRows, m.termCols = 0, 0
	return m
}

// termEntry resolves the attached entry by the ID openTerminal recorded, or
// nil once it is no longer attached. Re-resolved from the registry on every
// call rather than cached, since a hosted-pty entry mutates in place
// (SetState, SetActivity) — the header must always read the current State.
func (m Model) termEntry() *livework.Entry {
	if !m.termAttached {
		return nil
	}
	for _, e := range livework.Default.List() {
		if e.ID == m.termEntryID {
			return e
		}
	}
	return nil
}

// termRegion is the pty size the terminal view sizes both the emulator and
// the child's own pty to: the body content height minus the field block
// (termFields) and the dashed-rule line, and the full width minus the one
// leading space every rendered line carries. The identity row lives in the
// top chrome (attachBreadcrumb), outside this budget. Sizing the CHILD's pty to
// exactly this — not the whole terminal — is what keeps its own pinned
// footer inside the visible grid instead of scrolling off: from the child's
// point of view this region IS its terminal, so its footer pins to the
// bottom of it.
func (m Model) termRegion() (rows, cols int) {
	width, _, bodyHeight := m.frameGeom()
	contentH := bodyHeight - 1 // frameGeom reserves one line for the outer rule
	if contentH < 1 {
		contentH = 1
	}
	rows = contentH - len(m.termFields(width)) - 1 // field block, dashed rule
	if rows < 1 {
		rows = 1
	}
	cols = width - 1 // the leading space every rendered line carries
	if cols < 1 {
		cols = 1
	}
	return rows, cols
}

// terminalKey is handleKey's entire behavior while attached: esc alone
// detaches (the process keeps running), and everything else — q and k
// included — is encoded to bytes and sent to the child. admin-3rgp.71
// narrowed this from admin-3rgp.65's original q/esc/k contract precisely
// because a child prompting for one of those letters (e.g. "Enter a number,
// or q to cancel:") could never be answered while attached; the arrow keys
// stay encoded through for the same reason — a prompting child needs them,
// and this view has no scrollback of its own to spend them on instead. There
// is no longer a way to kill the child from this view — esc back to the list
// and kill it from there.
func (m Model) terminalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEsc {
		return m.detachTerminal(), nil
	}
	if b := encodeKey(msg); len(b) > 0 {
		if e := m.termEntry(); e != nil && e.Ops.Write != nil {
			_, _ = e.Ops.Write(b)
		}
	}
	return m, nil
}

// encodeKey turns a Bubble Tea key event into the bytes a real terminal
// would have sent the child on the same keypress. Alt on a plain byte or
// rune prefixes ESC, as a terminal sends Meta; Alt on a CSI key uses the
// xterm modifier form (\x1b[1;3A, \x1b[3;3~), since a doubled ESC would read
// to the child as a bare Esc followed by stray bytes.
func encodeKey(msg tea.KeyMsg) []byte {
	b := encodeKeyType(msg)
	if !msg.Alt || len(b) == 0 {
		return b
	}
	if len(b) > 2 && b[0] == 0x1b && b[1] == '[' {
		body, final := string(b[2:len(b)-1]), b[len(b)-1]
		if body == "" {
			body = "1"
		}
		return []byte("\x1b[" + body + ";3" + string(final))
	}
	return append([]byte{0x1b}, b...)
}

// encodeKeyType is encodeKey without the Alt modifier. Bubble Tea's
// control-key KeyTypes (KeyCtrlA..KeyCtrlZ, KeyTab, KeyEnter, KeyBackspace,
// and every other C0 control) are literally the control byte's numeric
// value — see bubbletea's key.go — so casting the type through is exact, not
// a lookup table. Cursor and editing keys need their own CSI sequences;
// function keys and paste bracketing never arrive from this dashboard.
func encodeKeyType(msg tea.KeyMsg) []byte {
	switch msg.Type {
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return nil
		}
		return []byte(string(msg.Runes))
	case tea.KeySpace:
		return []byte(" ")
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyDelete:
		return []byte("\x1b[3~")
	case tea.KeyHome:
		return []byte("\x1b[H")
	case tea.KeyEnd:
		return []byte("\x1b[F")
	case tea.KeyPgUp:
		return []byte("\x1b[5~")
	case tea.KeyPgDown:
		return []byte("\x1b[6~")
	}
	if msg.Type >= 0 && msg.Type <= 127 {
		return []byte{byte(msg.Type)}
	}
	return nil
}

// attachBreadcrumb renders the row the tab bar gives up while attached (see
// view.go's frameRaw): what is attached on the left — verbatim "attached",
// the state glyph, the entry's name, the state word and elapsed. Reuses
// commandGlyph/stateColor/elapsedStr (workview.go) rather than a second
// glyph/duration mapping. This is the one line that must always agree with
// the emulator grid below it, since both read the same live Entry on every
// frame — see termEntry. The log path and urls get rows of their own under
// it (termFields), at the top rather than under the output: a child's output
// is what scrolls, so anything pinned beneath it either floats in dead space
// when the output is short or gets pushed off when it is long.
func (m Model) attachBreadcrumb(width int) string {
	e := m.termEntry()
	name := m.termEntryID
	var state livework.State
	started := time.Now()
	if e != nil {
		name = e.Name
		state, _ = livework.Default.State(e.ID)
		started = e.StartedAt
	}
	left := []seg{
		{t: "attached", fg: cFg},
		{t: "  ·  ", fg: cFaint},
		{t: commandGlyph(state), fg: stateColor(state)},
		{t: " " + name, fg: cFg},
		{t: "  ·  " + string(state) + "  ·  " + elapsedStr(time.Since(started)), fg: cFaint},
	}
	inner := width - 1 // the leading space this line always carries
	return " " + renderSegs(composeLR(left, nil, inner), nil)
}

// termFields is the attached view's field block: the same rows the docked
// band shows under its title for this entry — a command's log path and one
// row per configured url, or a worker's five rows. It finds them by matching
// the attached entry to its Commands or Workers row, with collapse off so a hidden target
// row still counts. An entry with no row falls back to its log path alone.
func (m Model) termFields(width int) []string {
	if !m.termAttached {
		return nil
	}
	all := m
	all.workCollapsed = false
	for _, r := range append(all.workRows(), all.workerRows()...) {
		e := bandEntry(r)
		if e == nil || e.ID != m.termEntryID {
			continue
		}
		return bandFields(e, r.work, width)
	}
	if e := m.termEntry(); e != nil {
		if logPath, _ := livework.Default.LogPath(e.ID); logPath != "" {
			return bandFieldsCommand(&workRowData{logPath: logPath}, width)
		}
	}
	return nil
}

// renderTerminalBody is view.go's full-tab replacement for the list/detail
// layout while attached: the field block (termFields), the dashed rule, then
// either the attached-empty copy or the emulator's own grid — never both,
// and never a second footer implementation, since the grid IS the child's
// screen and the child pins its own footer to the bottom of it (see
// termRegion). The identity row is attachBreadcrumb, in the top chrome.
func (m Model) renderTerminalBody(width, height int) ([]string, []hitTarget) {
	lines := make([]string, 0, height)
	lines = append(lines, m.termFields(width)...)
	lines = append(lines, faintStyle.Render(strings.Repeat("╌", width)))

	avail := height - len(lines)
	if avail < 1 {
		avail = 1
	}

	e := m.termEntry()
	if e == nil || e.Screen == nil || e.Output == nil || e.Output.Total() == 0 {
		empty := make([]string, avail)
		if avail >= 2 {
			empty[1] = " " + mutedStyle.Render("waiting for output…")
		} else {
			empty[0] = " " + mutedStyle.Render("waiting for output…")
		}
		lines = append(lines, empty...)
	} else {
		lines = append(lines, renderTermOutput(e.Screen, avail)...)
	}

	if len(lines) < height {
		lines = append(lines, make([]string, height-len(lines))...)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines, noneHits(height)
}

// renderTermOutput renders the attached entry's Screen bottom-aligned to
// avail rows: a contiguous run of untouched rows at the very BOTTOM of the
// grid — the part of a fresh or short-lived child's screen its cursor
// hasn't reached yet — floats to the TOP as blank fill instead, so a short
// tail leaves its gap above rather than below (the ticket's own wording,
// matching docs/spikes/admin-tui-desktop/hosted.txt's output sitting on the
// floor with blank rows between it and the dashed rule). A row the child
// wrote blank lines into on purpose, anywhere but that trailing run, is
// left exactly where the child put it — only the untouched tail moves.
func renderTermOutput(screen *vt.SafeEmulator, avail int) []string {
	rows, cols := screen.Height(), screen.Width()
	raw := make([]string, rows)
	for y := 0; y < rows; y++ {
		raw[y] = renderEmuLine(screen, y, cols)
	}
	trailingBlank := 0
	for trailingBlank < rows && raw[rows-1-trailingBlank] == "" {
		trailingBlank++
	}
	content := raw[:rows-trailingBlank]

	out := make([]string, 0, avail)
	for i := 0; i < avail-len(content); i++ {
		out = append(out, "")
	}
	for _, l := range content {
		out = append(out, " "+l)
	}
	if len(out) > avail {
		out = out[len(out)-avail:]
	}
	return out
}

// renderEmuLine renders one row of the emulator's grid as styled text:
// adjacent cells sharing the same style are coalesced into one SGR run
// (uv.Style.String() emits the full, absolute SGR sequence for a style, not
// a diff, so switching runs never bleeds a color forward), and the trailing
// run of blank, unstyled cells is dropped rather than padding every line
// out to the full column count with resets. This is what carries the
// child's own [ERROR]/[WARN]/[INFO]/[DEBUG] and scope-tag colors through
// unchanged — the child emits them as real SGR sequences into what is, from
// its point of view, a real terminal.
func renderEmuLine(term *vt.SafeEmulator, y, width int) string {
	type cellInfo struct {
		style   uv.Style
		content string
	}
	cells := make([]cellInfo, 0, width)
	end := 0
	for x := 0; x < width; x++ {
		c := term.CellAt(x, y)
		if c != nil && c.Width == 0 {
			// A wide rune's continuation cell — already emitted as part of
			// the rune before it.
			continue
		}
		var ci cellInfo
		if c == nil {
			ci = cellInfo{content: " "}
		} else {
			ci = cellInfo{style: c.Style, content: c.Content}
			if ci.content == "" {
				ci.content = " "
			}
		}
		cells = append(cells, ci)
		blank := ci.style.IsZero() && ci.content == " "
		if !blank {
			end = len(cells)
		}
	}

	var b strings.Builder
	var cur *uv.Style
	for i := 0; i < end; i++ {
		ci := cells[i]
		if cur == nil || !cur.Equal(&ci.style) {
			b.WriteString(ci.style.String())
			s := ci.style
			cur = &s
		}
		b.WriteString(ci.content)
	}
	if cur != nil {
		b.WriteString(ansi.ResetStyle)
	}
	return b.String()
}
