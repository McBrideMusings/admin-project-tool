// panepoll.go is the Work tab's per-tick pane-liveness poller: it runs the
// project's declared [terminal].status command (ADR-0020) against every
// terminal-dispatched entry once per tick, purely to notice a handle that
// died independent of anything this process did — the user closed the herdr
// pane by hand, or the OS reaped it. Nothing else in this codebase ever
// observes that: internal/livework's own doc comment is explicit that an
// entry only transitions when its producer reports having watched the real
// thing settle, and closing a pane by hand is never reported to the
// producer that opened it (internal/kinds/pane.go's recordLiveWork wires
// Kill to SetState(StateExited), but nothing wires "someone else closed it"
// to anything). Left unfixed, an entry whose pane the user closed sits at
// StateRunning forever, which is why a dispatching row shows "starting" and
// refuses a second run (admin-qk77) even after the pane it points at is
// long gone.
//
// This complements agentpoll.go, which polls herdr's own agent status word
// (blocked/working) for workflow entries only, to distinguish "waiting on a
// prompt" from "still working". panepoll.go answers a narrower, more
// fundamental question — does the handle still exist at all — for every
// terminal-via entry, agent worker or plain detached action alike, which is
// exactly what admin-ysxr's own ADR text says the status key exists for
// ("keeps herdr agent polling generic ... any launcher may leave it unset").
//
// A project that declares no [terminal].status leaves this a documented
// no-op: the key is optional, and a launcher with nothing to report status
// through works exactly as it did before this poller existed.
package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// paneStatusTimeout bounds a single status query so one wedged launcher
// command can't stall a whole tick — mirrors agentPollTimeout's role in
// agentpoll.go.
const paneStatusTimeout = 2 * time.Second

// paneStatusFn is the test seam for pollPaneStatusCmd's per-entry query,
// bound to kinds.PaneGone by default.
var paneStatusFn = kinds.PaneGone

// paneStatusResult is one entry's liveness-check outcome.
type paneStatusResult struct {
	id   string
	gone bool
	err  error
}

// paneStatusMsg carries a completed liveness-check round back into Update.
type paneStatusMsg struct {
	results []paneStatusResult
}

// panePollInflight makes an overlapping tick a no-op, exactly like
// agentPollInflight — the previous round's shell-outs might still be
// running when the next tick fires.
var panePollInflight atomic.Bool

// pollPaneStatusCmd snapshots the live registry and queries statusCmd once
// for every running-or-waiting terminal-dispatched entry, returning the
// results as a single tea.Msg. statusCmd == "" (no [terminal].status
// declared) short-circuits to a nil message — the documented no-op default.
func pollPaneStatusCmd(statusCmd string) tea.Cmd {
	if statusCmd == "" {
		return func() tea.Msg { return nil }
	}
	return func() tea.Msg {
		if !panePollInflight.CompareAndSwap(false, true) {
			return nil
		}
		defer panePollInflight.Store(false)

		var targets []*livework.Entry
		for _, e := range livework.Default.List() {
			// A worker's vanished pane is reap.go's business: a finished
			// worker's work may still be on disk, so its row must not flip to
			// exited and drop out of the reap.
			if e.Via != "terminal" || e.Worker != nil {
				continue
			}
			st, ok := livework.Default.State(e.ID)
			if !ok || (st != livework.StateRunning && st != livework.StateWaiting) {
				continue
			}
			targets = append(targets, e)
		}
		if len(targets) == 0 {
			return nil
		}

		results := make([]paneStatusResult, len(targets))
		var wg sync.WaitGroup
		wg.Add(len(targets))
		for i, e := range targets {
			go func(i int, id, handle string) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), paneStatusTimeout)
				defer cancel()
				gone, err := paneStatusFn(ctx, statusCmd, handle)
				results[i] = paneStatusResult{id: id, gone: gone, err: err}
			}(i, e.ID, e.Handle)
		}
		wg.Wait()

		return paneStatusMsg{results: results}
	}
}

// applyPaneStatuses writes msg's results into the live registry. An entry
// found gone is set StateExited — the same terminal state Kill already
// records when it closes a pane deliberately, so a pane closed by hand and
// one closed via the Work tab's own kill key are indistinguishable
// afterward. A failed or inconclusive query leaves the entry untouched, and
// a result is dropped if the entry has since moved off Running/Waiting on
// its own — the same guard applyAgentStatuses uses.
func applyPaneStatuses(msg paneStatusMsg) {
	for _, r := range msg.results {
		if r.err != nil || !r.gone {
			continue
		}
		st, ok := livework.Default.State(r.id)
		if !ok || (st != livework.StateRunning && st != livework.StateWaiting) {
			continue
		}
		livework.Default.SetState(r.id, livework.StateExited, nil)
	}
}
