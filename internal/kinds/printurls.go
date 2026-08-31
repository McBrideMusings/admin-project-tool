package kinds

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// checkURLTimeout mirrors admin_lib/core.py::check_url's default timeout=1.
const checkURLTimeout = 1 * time.Second

// ANSI colour codes used by formatURLStatus — mirrors admin_lib/core.py's
// class C (the subset print-urls actually uses).
const (
	ansiGreen = "\033[32m"
	ansiDim   = "\033[2m"
	ansiReset = "\033[0m"
)

// checkURL reports whether url answers with a 2xx/3xx status — the Go port
// of admin_lib/core.py::check_url. Any error (connection refused, timeout,
// TLS failure) reads as down, matching Python's broad `except Exception`.
func checkURL(rawURL string) bool {
	client := &http.Client{
		Timeout: checkURLTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// formatURLStatus renders one status line — the Go port of
// admin_lib/core.py::format_url_status.
func formatURLStatus(label, rawURL string, live bool, labelWidth int) string {
	dot := ansiDim + "○" + ansiReset
	status := ansiDim + "DOWN" + ansiReset
	if live {
		dot = ansiGreen + "●" + ansiReset
		status = ansiGreen + "LIVE" + ansiReset
	}
	padded := label
	for len(padded) < labelWidth {
		padded += " "
	}
	return fmt.Sprintf("  %s%s%s%s  %s %s", padded, ansiDim, rawURL, ansiReset, dot, status)
}

// makePrintURLs is kind="print-urls"'s factory — the Go port of
// admin_lib/interp.py::_make_print_urls / admin_lib/core.py::print_url_status.
// It health-checks every entry of the manifest's top-level [urls] table (each
// URL's "/health" path) in parallel and prints one line per entry. A manifest
// with no [urls] table prints nothing, matching Python's `if not _urls:
// return`.
func makePrintURLs(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	return func(args []string) error {
		if m == nil || len(m.Urls) == 0 {
			return nil
		}
		labels := make([]string, 0, len(m.Urls))
		for label := range m.Urls {
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

		live := make([]bool, len(labels))
		var wg sync.WaitGroup
		for i, label := range labels {
			wg.Add(1)
			go func(i int, rawURL string) {
				defer wg.Done()
				healthURL := strings.TrimRight(rawURL, "/") + "/health"
				live[i] = checkURL(healthURL)
			}(i, m.Urls[label])
		}
		wg.Wait()

		h := newHost()
		for i, label := range labels {
			fmt.Fprintln(h.Stdout, formatURLStatus(label, m.Urls[label], live[i], labelWidth))
		}
		fmt.Fprintln(h.Stdout)
		return nil
	}, nil
}
