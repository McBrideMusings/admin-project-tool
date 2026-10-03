package kinds

import (
	"fmt"
	"sort"
	"sync"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/status"
)

// ANSI colour codes used by formatStatusLine.
const (
	ansiGreen = "\033[32m"
	ansiRed   = "\033[31m"
	ansiDim   = "\033[2m"
	ansiReset = "\033[0m"
)

// formatStatusLine renders one readout: label, a state dot, the text.
func formatStatusLine(label string, r status.Result, labelWidth int) string {
	dot := ansiDim + "·" + ansiReset
	switch r.State {
	case status.Up:
		dot = ansiGreen + "●" + ansiReset
	case status.Down:
		dot = ansiRed + "○" + ansiReset
	}
	padded := label
	for len(padded) < labelWidth {
		padded += " "
	}
	return fmt.Sprintf("  %s%s  %s %s", ansiDim, padded+ansiReset, dot, r.Text)
}

// makePrintStatus is kind="print-status"'s factory. It evaluates every
// [status.<label>] entry of the manifest once, in parallel, and prints one
// line per entry. A manifest with no [status] table prints nothing.
func makePrintStatus(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	return func(args []string) error {
		if m == nil || len(m.Status) == 0 {
			return nil
		}
		labels := make([]string, 0, len(m.Status))
		for label := range m.Status {
			labels = append(labels, label)
		}
		sort.Strings(labels)

		labelWidth := 0
		for _, l := range labels {
			if len(l) > labelWidth {
				labelWidth = len(l)
			}
		}
		labelWidth += 2

		results := make([]status.Result, len(labels))
		var wg sync.WaitGroup
		for i, label := range labels {
			wg.Add(1)
			go func(i int, spec manifest.StatusSpec) {
				defer wg.Done()
				results[i] = status.Run(spec, m.Root)
			}(i, m.Status[label])
		}
		wg.Wait()

		h := newHost(m)
		for i, label := range labels {
			fmt.Fprintln(h.Stdout, formatStatusLine(label, results[i], labelWidth))
		}
		fmt.Fprintln(h.Stdout)
		return nil
	}, nil
}
