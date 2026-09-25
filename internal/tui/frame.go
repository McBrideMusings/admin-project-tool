package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// FrameOptions configures a headless render — see Frame.
type FrameOptions struct {
	Width, Height int
	// Keys are driven in order before the frame is taken, so a pane that only
	// exists once focused (the Cleanup box) or a tab other than the first can be
	// dumped. Names are the ones Bubble Tea reports: a single character is that
	// key, and "tab" / "enter" / "esc" / "space" / "up" / "down" / "left" /
	// "right" name themselves.
	Keys []string
	// Settle is a backstop, not the normal wait: drain returns as soon as no
	// command is in flight, and Settle only bounds how long a genuinely stuck
	// command (one that never delivers) can hold up the frame. The dashboard's
	// own first paint is instant off local git, so quiescence is ordinarily
	// reached in microseconds; Settle exists for the pathological case.
	Settle time.Duration
}

// Frame renders one frame of the dashboard without a terminal and returns it, so
// a change can be checked at the surface with one command instead of driving a
// live TUI. It runs the real Init commands through the real Update loop — the
// frame is what the program would have drawn, not a reconstruction.
//
// Two things differ, because headless has no user: commands are bounded by
// Settle rather than by a keypress, and anything still in flight when that runs
// out simply is not in the frame (the header's `⟳ syncing` says so, exactly as it
// would on screen). Cancel ctx after this returns to wind down whatever was
// abandoned — the file watcher and the sync poll both outlive the frame.
func Frame(ctx context.Context, m Model, opts FrameOptions) string {
	if opts.Width <= 0 {
		opts.Width = 120
	}
	if opts.Height <= 0 {
		opts.Height = 40
	}
	if opts.Settle <= 0 {
		opts.Settle = 2 * time.Second
	}

	p := loadedPump(m, opts)

	// Keys go in after the loads, so a pane that only appears once data has
	// arrived is focusable — pressing tab against an empty dashboard would land
	// somewhere else entirely. Each gets a short drain of its own for whatever it
	// kicked off.
	for _, k := range opts.Keys {
		p.apply(keyMsg(k))
		p.drain(150 * time.Millisecond)
	}
	return p.model.View()
}

// Frames renders several frames of one dashboard from a single load. Calling
// Frame once per key sequence would re-run Init and pay its own settle wait
// every time — five views of the same repo cost five loads of it. Here the
// load happens once and every sequence starts from a copy of the settled
// model, which is what makes a tool that shows many views of one repo (a
// prototype, a docs dump, a regression sweep) practical.
//
// Each sequence gets its own copy, so a mode toggle in one cannot leak into the
// next — `g` twice across two sequences would otherwise turn tree mode back off.
// Model is a value type; the maps behind it are only read while rendering.
func Frames(ctx context.Context, m Model, opts FrameOptions, seqs [][]string) []string {
	if opts.Width <= 0 {
		opts.Width = 120
	}
	if opts.Height <= 0 {
		opts.Height = 40
	}
	if opts.Settle <= 0 {
		opts.Settle = 2 * time.Second
	}

	p := loadedPump(m, opts)
	base := p.model

	out := make([]string, len(seqs))
	for i, seq := range seqs {
		p.model = base
		for _, k := range seq {
			p.apply(keyMsg(k))
			p.drain(60 * time.Millisecond)
		}
		out[i] = p.model.View()
	}
	return out
}

// loadedPump sizes m, runs its Init commands through the real Update loop and
// waits (bounded by opts.Settle) for them to land — the shared start of Frame,
// Frames and ShowRows.
func loadedPump(m Model, opts FrameOptions) *pump {
	p := &pump{msgs: make(chan tea.Msg, 64), model: m}
	p.apply(tea.WindowSizeMsg{Width: opts.Width, Height: opts.Height})
	p.run(p.model.Init())
	p.drain(opts.Settle)
	return p
}

// pump is a single-goroutine Bubble Tea loop: commands run in the background and
// deliver onto msgs, and only this goroutine ever touches the model — the same
// discipline the real program keeps, which is what makes the frame trustworthy.
type pump struct {
	msgs  chan tea.Msg
	model Model
	// inFlight counts commands that have been started but have not yet
	// delivered (or been dropped). drain treats zero as quiescent. It is only
	// ever touched from the pump's own goroutine (via run, synchronously, for
	// increments) and from each command's own goroutine (for its decrement),
	// so drain never races its own increments against its own read.
	inFlight int32
}

// run executes a command off the loop and feeds its result back. A tea.Batch
// arrives as a BatchMsg holding more commands, so it recurses rather than
// delivering the batch itself as a message the model would ignore. The
// in-flight counter is incremented here, synchronously, before the goroutine
// starts, and decremented only after the result (or each recursed
// sub-command) has been handed off — so a reader that observes zero is
// guaranteed there is nothing left to arrive.
func (p *pump) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	atomic.AddInt32(&p.inFlight, 1)
	go func() {
		defer atomic.AddInt32(&p.inFlight, -1)
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				p.run(c)
			}
			return
		}
		if msg == nil {
			return
		}
		select {
		case p.msgs <- msg:
		default: // the frame is already taken; drop it rather than block forever
		}
	}()
}

// apply hands one message to the model and schedules whatever it asked for.
func (p *pump) apply(msg tea.Msg) {
	next, cmd := p.model.Update(msg)
	if nm, ok := next.(Model); ok {
		p.model = nm
	}
	p.run(cmd)
}

// drain applies messages until the pump is quiescent — no command started by
// this drain (or by a message it applied) is still in flight — with backstop
// as a hard ceiling for the pathological case of a command that never
// delivers. Quiescence, not the backstop, is the normal exit: a recurring
// command (the dashboard's sync poll re-arms itself via its own tea.Cmd)
// still reads as quiescent the instant it is between ticks, which is true
// almost all the time, so this returns as soon as real work is done instead
// of always paying the full backstop like a fixed deadline did.
func (p *pump) drain(backstop time.Duration) {
	deadline := time.After(backstop)
	for {
		select {
		case msg := <-p.msgs:
			p.apply(msg)
			continue
		default:
		}
		if atomic.LoadInt32(&p.inFlight) == 0 {
			// A command hands its message off before it decrements, so a zero
			// read can follow a send that landed after the check above: look
			// once more before calling the pump quiet.
			select {
			case msg := <-p.msgs:
				p.apply(msg)
				continue
			default:
			}
			return
		}
		select {
		case msg := <-p.msgs:
			p.apply(msg)
		case <-deadline:
			return
		}
	}
}

// keyMsg turns a key name into the tea.KeyMsg the model would receive for it.
// Anything unrecognised is sent as its literal runes, so a plain letter binding
// needs no entry here.
func keyMsg(name string) tea.KeyMsg {
	switch strings.ToLower(name) {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
}
