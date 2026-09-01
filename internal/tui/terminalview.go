// terminalview.go is admin-3rgp.65's whole terminal view: attaching to a
// pty-addressed live-work row opens a full-tab terminal over that child's
// pty, rendered through a real terminal emulator (charmbracelet/x/vt) so
// color, cursor positioning and the child's own pinned footer all just
// work — the child's point of view never changes, its pty IS its terminal.
//
// Three things this file owns:
//   - the terminalOpener seam: livework.Registry.Attach for a pty-addressed
//     entry calls back into this package (installTerminalOpener, wired from
//     Model.Init) rather than mutating a value-type Model directly. The
//     callback can only record which pty was requested; work.go's
//     attachEntry reads that back and does the actual transition.
//   - the emulator lifecycle: creating it sized to the body region on
//     attach, feeding it only bytes written since attach (never a replay —
//     the log file holds history), and tearing it down on detach.
//   - the body renderer: header, dashed rule, then either "waiting for
//     output…" or the emulator's own grid — view.go routes here instead of
//     the list/detail layout while attached.
package tui

import (
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// pendingTerminal is the handoff between livework.Registry.Attach (called
// from work.go's attachEntry, on the Bubble Tea loop's own goroutine) and
// the terminal-opener callback the registry invokes synchronously inside
// that same call. The callback cannot mutate a value-type Model itself, so
// it records the requested ptyID here and attachEntry drains it right after
// Attach returns — see installTerminalOpener and takePendingTerminal.
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
	livework.Default.SetTerminalOpener(func(ptyID string) error {
		pendingTerminalMu.Lock()
		pendingTerminal = ptyID
		pendingTerminalMu.Unlock()
		return nil
	})
}

// takePendingTerminal returns and clears whatever ptyID the opener last
// recorded. It is drained unconditionally after every attach attempt (see
// work.go's attachEntry) — a pane-addressed Attach never writes here, so
// there is nothing to leak into a later, unrelated pty attach.
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

// termTickMsg drives the ring-drain poll while a terminal view is open —
// workTickMsg's 1s cadence is far too slow for a terminal a person is
// typing into. Re-armed only while termAttached; see the Update case.
type termTickMsg struct{}

func termTickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return termTickMsg{} })
}

// openTerminal transitions m into the attached state over e: sizes the
// emulator to the body region, records the ring offset so nothing written
// before this moment is ever replayed (the log file holds that), and resizes
// the child's own pty to match. Called only from attachEntry, after
// livework.Default.Attach has already succeeded for e.
func (m Model) openTerminal(e *livework.Entry) Model {
	m.termAttached = true
	m.termEntryID = e.ID
	m.termPTYID = e.PTYID
	m.termHasOutput = false
	if e.Output != nil {
		m.termOff = e.Output.Total()
	} else {
		m.termOff = 0
	}
	rows, cols := m.termRegion()
	m.termRows, m.termCols = rows, cols
	m.termEmu = vt.NewEmulator(cols, rows)
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
	m.termPTYID = ""
	m.termEmu = nil
	m.termOff = 0
	m.termHasOutput = false
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
// the child's own pty to: the body content height minus the header and
// dashed-rule lines, and the full width minus the one leading space every
// rendered line carries. Sizing the CHILD's pty to exactly this — not the
// whole terminal — is what keeps its own pinned footer inside the visible
// grid instead of scrolling off: from the child's point of view this region
// IS its terminal, so its footer pins to the bottom of it.
func (m Model) termRegion() (rows, cols int) {
	width, _, bodyHeight := m.frameGeom()
	contentH := bodyHeight - 1 // frameGeom reserves one line for the outer rule
	if contentH < 1 {
		contentH = 1
	}
	rows = contentH - 2 // header, dashed rule
	if rows < 1 {
		rows = 1
	}
	cols = width - 1 // the leading space every rendered line carries
	if cols < 1 {
		cols = 1
	}
	return rows, cols
}

// terminalKey is handleKey's entire behavior while attached: q and esc both
// detach (the process keeps running), k kills the attached entry, and
// everything else is encoded to bytes and sent to the child — including the
// arrow keys, which a prompting child needs and which this view has no
// scrollback of its own to spend them on instead.
func (m Model) terminalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEsc || msg.String() == "q" {
		return m.detachTerminal(), nil
	}
	if msg.String() == "k" {
		return m.killAttachedTerminal()
	}
	if b := encodeKey(msg); len(b) > 0 {
		if e := m.termEntry(); e != nil && e.Ops.Write != nil {
			_, _ = e.Ops.Write(b)
		}
	}
	return m, nil
}

// killAttachedTerminal is k's binding while attached — the attached entry
// rather than whatever row the cursor last sat on (killCurrentWork reads the
// cursor, which the terminal view has no row list to point anywhere).
func (m Model) killAttachedTerminal() (tea.Model, tea.Cmd) {
	e := m.termEntry()
	if e == nil || e.Ops.Kill == nil {
		return m, nil
	}
	id := e.ID
	if err := e.Ops.Kill(); err != nil {
		m.setStatus("kill failed: "+err.Error(), cRed)
	} else {
		m.setStatus("killed "+id, cDim)
	}
	return m, nil
}

// encodeKey turns a Bubble Tea key event into the bytes a real terminal
// would have sent the child on the same keypress. Bubble Tea's control-key
// KeyTypes (KeyCtrlA..KeyCtrlZ, KeyTab, KeyEnter, KeyBackspace, and every
// other C0 control) are literally the control byte's numeric value — see
// bubbletea's key.go — so casting the type through is exact, not a lookup
// table. Only the cursor keys need their own CSI sequences; nothing else
// vt's emulator would need to interpret arrives from this dashboard's own
// key events (no function keys, no paste bracketing) at this level.
func encodeKey(msg tea.KeyMsg) []byte {
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
	}
	if msg.Type >= 0 && msg.Type <= 127 {
		return []byte{byte(msg.Type)}
	}
	return nil
}

// elapsedMS renders a duration as "0m38s" — the terminal header's elapsed
// field, matching docs/spikes/admin-tui-desktop/hosted.txt exactly. Distinct
// from elapsedStr (workview.go's "<1m / 12m / 1h5m"), which that reference
// frame does not use and which internal/tui's other panes rely on staying
// as-is.
func elapsedMS(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	return strconv.Itoa(total/60) + "m" + strconv.Itoa(total%60) + "s"
}

// renderTermHeader renders the state glyph, command name, state word,
// elapsed time and current activity — the one line that must always agree
// with the emulator grid below it, since both read the same live Entry on
// every frame.
func (m Model) renderTermHeader(width int) string {
	e := m.termEntry()
	name := m.termEntryID
	var state livework.State
	var activity string
	started := time.Now()
	if e != nil {
		name = e.Name
		state, _ = livework.Default.State(e.ID)
		activity = e.Activity
		started = e.StartedAt
	}
	glyph, glyphColor := "○", cDim
	switch state {
	case livework.StateRunning:
		glyph, glyphColor = "●", cGreen
	case livework.StateWaiting:
		glyph, glyphColor = "◐", cYellow
	case livework.StateFailed:
		glyph, glyphColor = "✗", cRed
	}
	line := " " +
		lipgloss.NewStyle().Foreground(glyphColor).Render(glyph) + " " +
		lipgloss.NewStyle().Bold(true).Render(name) +
		faintStyle.Render("  ·  "+string(state)+"  ·  "+elapsedMS(time.Since(started))+"  ·  ") +
		activity
	return truncateLine(line, width)
}

// renderTerminalBody is view.go's full-tab replacement for the list/detail
// layout while attached: header, dashed rule, then either the
// attached-empty copy or the emulator's own grid — never both, and never a
// second footer implementation, since the grid IS the child's screen and
// the child pins its own footer to the bottom of it (see termRegion).
func (m Model) renderTerminalBody(width, height int) ([]string, []hitTarget) {
	lines := make([]string, 0, height)
	lines = append(lines, m.renderTermHeader(width))
	lines = append(lines, faintStyle.Render(strings.Repeat("╌", width)))

	if !m.termHasOutput || m.termEmu == nil {
		lines = append(lines, "")
		lines = append(lines, " "+mutedStyle.Render("waiting for output…"))
	} else {
		rows, cols := m.termEmu.Height(), m.termEmu.Width()
		for y := 0; y < rows; y++ {
			lines = append(lines, " "+renderEmuLine(m.termEmu, y, cols))
		}
	}

	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines, noneHits(height)
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
func renderEmuLine(term *vt.Emulator, y, width int) string {
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
