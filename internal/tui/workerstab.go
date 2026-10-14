package tui

import (
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/keymap"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// workersTab is the Workers tab as its own model (ADR-0026): its rows and its
// cursor. It reaches the registry of live agent workers and the empty-state
// hint through the work source's handle, and whether the project has a
// manifest through the config handle; its constructor asking for them is its
// declaration. The list-and-band body, attach, kill and the footer are its
// work surface (worksurface.go), the same component the Commands tab holds.
type workersTab struct {
	declared []source
	work     workSource
	cfg      configSource
	surface  workSurface

	cursor int
}

// newWorkersTab builds the tab, declaring the work source by asking for its
// handle.
func newWorkersTab(f feeds) workersTab {
	d := sourceSet{f: f}
	t := workersTab{work: d.work(), cfg: d.config()}
	t.surface = newWorkSurface(t.work, t.cfg, true)
	t.declared = d.declared
	return t
}

func (t *workersTab) sources() []source { return t.declared }

func (t *workersTab) cursorRef() *int { return &t.cursor }

// rows is every live agent worker, from any surface (a command's agents, a
// bead, issue or PR binding), in the registry's own insertion order so the
// list never reorders itself as entries change state. A worker never appears
// on the Commands tab. The rows are the same rowWorker rows the band, attach
// and kill already resolve.
func (t *workersTab) rows(*shell) []rowItem {
	if !t.cfg.ok() {
		return nil
	}
	workers := t.work.workers()
	rows := []rowItem{headerRow("Workers", seg{t: "  " + strconv.Itoa(len(workers))})}
	if len(workers) == 0 {
		return append(rows, rowItem{kind: rowText, label: t.work.emptyHint()})
	}
	for _, e := range workers {
		label := e.Name
		if activity, _ := livework.Default.Line(e.ID); activity != "" {
			label = e.Name + " " + activity
		}
		rows = append(rows, workerRow(e, label))
	}
	return rows
}

// modal is false: the tab has no overlay of its own.
func (t *workersTab) modal() bool { return false }

// currentRow is the row under the cursor, or none while focus is on the tab
// bar.
func (t *workersTab) currentRow(s *shell) (rowItem, bool) {
	return s.rowAt(s.filterRows(t.rows(s)), t.cursor)
}

// key takes the work surface's keys: attach, kill, and a swallowed `r`.
func (t *workersTab) key(s *shell, k string) (bool, tea.Cmd) {
	r, ok := t.currentRow(s)
	return t.surface.key(s, r, ok, k)
}

// activate is ↵ on a worker row: open its session, live or finished
// (livework.Registry.Attach covers both).
func (t *workersTab) activate(s *shell, r rowItem) tea.Cmd {
	return t.surface.attach(s, r)
}

// body is the work surface's list and band over this tab's rows.
func (t *workersTab) body(s *shell, width, height int) ([]string, []hitTarget) {
	return t.surface.body(s, s.filterRows(t.rows(s)), t.cursor, width, height)
}

// bindings is the work surface's footer for the cursor row; the tab has no
// collapse key.
func (t *workersTab) bindings(s *shell) (keymap.RowKind, []keymap.Binding) {
	r, ok := t.currentRow(s)
	return t.surface.bindings(r, ok, "")
}

func (t *workersTab) footer() (string, bool) { return "", false }

// count is the tab-bar badge: every live worker.
func (t *workersTab) count() int { return len(t.work.workers()) }

// loaded is always true: the registry is read fresh on every frame.
func (t *workersTab) loaded() bool { return true }

// freshness reads "live": the registry is read on every frame, with no fetch
// time to report.
func (t *workersTab) freshness() seg { return seg{t: "live", fg: cFaint} }

func (t *workersTab) update(*shell, tea.Msg) tea.Cmd { return nil }

// workerRow is the row for live worker e: the one shape the Workers list, the
// band and the attached view's field block all resolve.
func workerRow(e *livework.Entry, label string) rowItem {
	return rowItem{kind: rowWorker, label: label, work: &workRowData{worker: e}}
}
