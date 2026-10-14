package tui

import (
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// workersTab is the Workers tab as its own model (ADR-0026): its rows and its
// cursor. It reaches the registry of live agent workers and the empty-state
// hint through the work source's handle, and whether the project has a
// manifest through the config handle; its constructor asking for them is its
// declaration. The live band, attach and kill it shares with the Commands tab
// stay on the shell's work surface (workview.go, work.go).
type workersTab struct {
	declared []source
	work     workSource
	cfg      configSource

	cursor int
}

// newWorkersTab builds the tab, declaring the work source by asking for its
// handle.
func newWorkersTab(f feeds) workersTab {
	d := sourceSet{f: f}
	t := workersTab{work: d.work(), cfg: d.config()}
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

// key reports every key unhandled: attach, kill and the row bindings are the
// shell's work surface, shared with the Commands tab.
func (t *workersTab) key(*shell, tea.KeyMsg) (bool, tea.Cmd) { return false, nil }

// body is never reached: the shell draws the work surface's list and band
// for this tab and the Commands tab alike (renderWorkBody).
func (t *workersTab) body(*shell, int, int) ([]string, []hitTarget) { return nil, nil }

func (t *workersTab) footer(int) (string, bool) { return "", false }

// count is the tab-bar badge: every live worker.
func (t *workersTab) count() int { return len(t.work.workers()) }

// loaded is always true: the registry is read fresh on every frame.
func (t *workersTab) loaded() bool { return true }

// freshness reads "live": the registry is read on every frame, with no fetch
// time to report.
func (t *workersTab) freshness() seg { return seg{t: "live", fg: cFaint} }

func (t *workersTab) update(tea.Msg) tea.Cmd { return nil }

// workerRow is the row for live worker e: the one shape the Workers list, the
// band and the attached view's field block all resolve.
func workerRow(e *livework.Entry, label string) rowItem {
	return rowItem{kind: rowWorker, label: label, work: &workRowData{worker: e}}
}
