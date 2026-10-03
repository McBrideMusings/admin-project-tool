package tui

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/status"
)

// statusProber is the Work tab's cache of [status.<label>] readouts: a
// package-level, mutex-guarded map, read from render and written from
// background goroutines, the same shape as urlProber and for the same
// reason. Refresh starts a goroutine for each entry whose last readout is
// older than its own `every` and returns immediately, so a slow endpoint
// never holds up a repaint; Get is a pure map read.
//
// A failed check replaces the previous readout with its Down result, so the
// pane never keeps showing a value the source stopped confirming.
type statusProber struct {
	mu       sync.Mutex
	result   map[string]status.Result
	at       map[string]time.Time
	inflight map[string]bool
	run      func(manifest.StatusSpec, string) status.Result
}

var statusProbes = newStatusProber()

func newStatusProber() *statusProber {
	return &statusProber{
		result:   map[string]status.Result{},
		at:       map[string]time.Time{},
		inflight: map[string]bool{},
		run:      status.Run,
	}
}

// Get is label's last readout, or an Unknown result with no text before the
// first one lands.
func (p *statusProber) Get(label string) status.Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result[label]
}

// Refresh starts a check for every entry that is stale and not already in
// flight. dir is the project root, the working directory of command entries.
func (p *statusProber) Refresh(entries map[string]manifest.StatusSpec, dir string) {
	now := time.Now()
	labels := make([]string, 0, len(entries))
	for l := range entries {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, label := range labels {
		spec := entries[label]
		p.mu.Lock()
		stale := now.Sub(p.at[label]) >= spec.Interval()
		if p.inflight[label] || !stale {
			p.mu.Unlock()
			continue
		}
		p.inflight[label] = true
		run := p.run
		p.mu.Unlock()

		go func(label string, spec manifest.StatusSpec) {
			r := run(spec, dir)
			p.mu.Lock()
			p.result[label] = r
			p.at[label] = time.Now()
			p.inflight[label] = false
			p.mu.Unlock()
		}(label, spec)
	}
}

// reset clears every readout. Test-only, like urlProber.reset.
func (p *statusProber) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.result = map[string]status.Result{}
	p.at = map[string]time.Time{}
	p.inflight = map[string]bool{}
}

// setRun swaps the checker under the lock and returns the previous one.
func (p *statusProber) setRun(fn func(manifest.StatusSpec, string) status.Result) func(manifest.StatusSpec, string) status.Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	prev := p.run
	p.run = fn
	return prev
}

// statusRows is the Work tab's Status section: a header with the entry
// count and one row per label in label order (dot, label, readout). Nil when
// the manifest declares no [status] entries.
func statusRows(entries map[string]manifest.StatusSpec) []rowItem {
	if len(entries) == 0 {
		return nil
	}
	labels := make([]string, 0, len(entries))
	width := 0
	for l := range entries {
		labels = append(labels, l)
		if len(l) > width {
			width = len(l)
		}
	}
	sort.Strings(labels)

	rows := []rowItem{headerRow("Status", seg{t: "  " + strconv.Itoa(len(labels))})}
	for _, label := range labels {
		r := statusProbes.Get(label)
		dot := seg{t: "·", fg: cFaint}
		switch r.State {
		case status.Up:
			dot = seg{t: "●", fg: cGreen}
		case status.Down:
			dot = seg{t: "○", fg: cRed}
		}
		text := r.Text
		textFg := cFg
		if r.State == status.Down {
			textFg = cRed
		}
		rows = append(rows, rowItem{kind: rowText, leftSegs: []seg{
			{t: " "}, dot, {t: "  "},
			{t: label + strings.Repeat(" ", width-len(label)), fg: cBright},
			{t: "  "},
			{t: text, fg: textFg},
		}})
	}
	return rows
}

// statusStrip renders the Status section as a block pinned above the Work
// tab's scrolling list, so it never scrolls out of view with the cursor. It
// takes at most max lines (the header plus as many entries as fit) and returns
// none when there is no room for the header and one entry.
func statusStrip(entries map[string]manifest.StatusSpec, width, max int) ([]string, []hitTarget) {
	rows := statusRows(entries)
	if len(rows) < 2 || max < 2 {
		return nil, nil
	}
	hidden := 0
	if len(rows) > max {
		hidden = len(rows) - (max - 1)
		rows = rows[:max-1]
	}
	lw := maxi(width-2, 20)
	n := len(rows)
	if hidden > 0 {
		n++
	}
	lines := make([]string, n)
	hits := make([]hitTarget, n)
	if hidden > 0 {
		lines[n-1] = padEnd(" "+padEnd(faintStyle.Render("  ↓ "+strconv.Itoa(hidden)+" more status"), lw), width)
		hits[n-1] = hitTarget{kind: hitNone}
	}
	for i, r := range rows {
		l := ""
		if r.kind == rowHeader {
			l = lipgloss.NewStyle().Bold(true).Render(r.label) + faintStyle.Render(r.rightSegs[0].t)
		} else {
			l = renderSegs(r.leftSegs, nil)
		}
		lines[i] = padEnd(" "+padEnd(l, lw), width)
		hits[i] = hitTarget{kind: hitNone}
	}
	return lines, hits
}
