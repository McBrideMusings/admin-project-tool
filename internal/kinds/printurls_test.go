package kinds

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestCheckURLTreats2xxAnd3xxAsLive proves CheckURL matches
// admin_lib/core.py::check_url's "200 <= status < 400" window.
//
// MUTATION EVIDENCE: change the upper bound from 400 to 300 and this test
// fails on the 302 case — a redirecting health endpoint (common behind a
// reverse proxy) would wrongly read as down.
func TestCheckURLTreats2xxAnd3xxAsLive(t *testing.T) {
	for _, status := range []int{200, 204, 301, 302, 399} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if status >= 300 && status < 400 {
				w.Header().Set("Location", "/elsewhere")
			}
			w.WriteHeader(status)
		}))
		if !CheckURL(srv.URL) {
			t.Errorf("CheckURL: status %d should read as live", status)
		}
		srv.Close()
	}
}

// TestCheckURLTreats4xxAnd5xxAsDown proves a client/server error reads as
// down.
func TestCheckURLTreats4xxAnd5xxAsDown(t *testing.T) {
	for _, status := range []int{400, 404, 500, 503} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		if CheckURL(srv.URL) {
			t.Errorf("CheckURL: status %d should read as down", status)
		}
		srv.Close()
	}
}

// TestCheckURLUnreachableIsDown proves a connection failure reads as down,
// matching Python's broad `except Exception: return False`.
func TestCheckURLUnreachableIsDown(t *testing.T) {
	if CheckURL("http://127.0.0.1:1") {
		t.Error("CheckURL against an unreachable port should be down")
	}
}

// TestFormatURLStatusShowsLiveOrDown proves formatURLStatus's text content
// switches on the `live` flag — the Go port of
// admin_lib/core.py::format_url_status.
//
// MUTATION EVIDENCE: swap the ternary so `live=true` renders "DOWN" and this
// test fails on the live-case substring check.
func TestFormatURLStatusShowsLiveOrDown(t *testing.T) {
	live := formatURLStatus("api", "http://x", true, 6)
	if !strings.Contains(live, "LIVE") || strings.Contains(live, "DOWN") {
		t.Errorf("live status line = %q, want it to contain LIVE and not DOWN", live)
	}
	down := formatURLStatus("api", "http://x", false, 6)
	if !strings.Contains(down, "DOWN") || strings.Contains(down, "LIVE") {
		t.Errorf("down status line = %q, want it to contain DOWN and not LIVE", down)
	}
}

// TestMakePrintURLsNoopWithoutURLsTable proves a manifest with no [urls]
// table prints nothing and doesn't error — matching
// admin_lib/core.py::print_url_status's `if not _urls: return`.
func TestMakePrintURLsNoopWithoutURLsTable(t *testing.T) {
	runner, err := makePrintURLs("print-urls", manifest.Action{}, &manifest.Manifest{}, nil)
	if err != nil {
		t.Fatalf("makePrintURLs: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
}

// TestMakePrintURLsChecksEveryDeclaredURL proves the Runner health-checks
// every entry in the manifest's [urls] table by hitting <url>/health —
// matching admin_lib/core.py::print_url_status's
// `url.rstrip("/") + "/health"`.
//
// MUTATION EVIDENCE: drop the "/health" suffix (health-check the bare URL
// instead) and this test fails — the fake server only answers 200 on
// "/health" and 404 on everything else, so hitURLs below would record "down"
// for a live server.
func TestMakePrintURLsChecksEveryDeclaredURL(t *testing.T) {
	var hitPaths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitPaths = append(hitPaths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	m := &manifest.Manifest{Urls: map[string]string{"api": srv.URL}}
	runner, err := makePrintURLs("print-urls", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makePrintURLs: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	found := false
	for _, p := range hitPaths {
		if p == "/health" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a /health request, got paths %v", hitPaths)
	}
}
