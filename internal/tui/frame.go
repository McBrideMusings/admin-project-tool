package tui

import (
	"context"
	"strings"
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
	// Settle is how long background loads get to land before the frame is taken.
	// The dashboard's own first paint is instant off local git, so this is the
	// wait for that load to arrive, not for the network.
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

	p := &pump{msgs: make(chan tea.Msg, 64), model: m}
	p.apply(tea.WindowSizeMsg{Width: opts.Width, Height: opts.Height})
	p.run(p.model.Init())
	p.drain(opts.Settle)

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

// Frames renders several frames of one dashboard from a single load. Settle is a
// deadline rather than a quiescence check, so calling Frame once per key
// sequence pays that whole wait every time — five views of the same repo cost
// five loads of it. Here the load happens once and every sequence starts from a
// copy of the settled model, which is what makes a tool that shows many views of
// one repo (a prototype, a docs dump, a regression sweep) practical.
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

	p := &pump{msgs: make(chan tea.Msg, 64), model: m}
	p.apply(tea.WindowSizeMsg{Width: opts.Width, Height: opts.Height})
	p.run(p.model.Init())
	p.drain(opts.Settle)
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

// pump is a single-goroutine Bubble Tea loop: commands run in the background and
// deliver onto msgs, and only this goroutine ever touches the model — the same
// discipline the real program keeps, which is what makes the frame trustworthy.
type pump struct {
	msgs  chan tea.Msg
	model Model
}

// run executes a command off the loop and feeds its result back. A tea.Batch
// arrives as a BatchMsg holding more commands, so it recurses rather than
// delivering the batch itself as a message the model would ignore.
func (p *pump) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
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

// drain applies messages until d passes. It is a deadline, not a quiescence
// check, because the dashboard is never quiet — the sync poll is always
// outstanding, so waiting for zero in-flight commands would wait a full interval.
func (p *pump) drain(d time.Duration) {
	deadline := time.After(d)
	for {
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
