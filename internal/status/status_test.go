package status

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func TestHTTPUpAndDown(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()

	if got := Run(manifest.StatusSpec{Kind: manifest.StatusHTTP, URL: ok.URL}, ""); got.State != Up || got.Text != "up" {
		t.Fatalf("200 server: got %+v", got)
	}
	if got := Run(manifest.StatusSpec{Kind: manifest.StatusHTTP, URL: bad.URL}, ""); got.State != Down {
		t.Fatalf("503 server: got %+v", got)
	}
}

func TestJSONPullsCountsIntoShowAndSendsHeaders(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`[{"status":"transcoding"},{"status":"review_ready"},{"status":"review_ready"},{"status":"failed"}]`))
	}))
	defer srv.Close()
	t.Setenv("STATUS_TEST_TOKEN", "s3cret")

	got := Run(manifest.StatusSpec{
		Kind:    manifest.StatusJSON,
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer ${STATUS_TEST_TOKEN}"},
		Pull: map[string]string{
			"running": "length([?status=='transcoding'])",
			"review":  "length([?status=='review_ready'])",
			"failed":  "length([?status=='failed'])",
		},
		Show: "{running} running · {review} in review · {failed} failed",
	}, "")
	if got.State != Up || got.Text != "1 running · 2 in review · 1 failed" {
		t.Fatalf("got %+v", got)
	}
	if auth != "Bearer s3cret" {
		t.Fatalf("Authorization header = %q", auth)
	}
}

func TestJSONFailureIsDownNeverStale(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not json")) }))
	defer srv.Close()
	got := Run(manifest.StatusSpec{Kind: manifest.StatusJSON, URL: srv.URL, Pull: map[string]string{"n": "length(@)"}, Show: "{n}"}, "")
	if got.State != Down || !strings.HasPrefix(got.Text, "not JSON") {
		t.Fatalf("got %+v", got)
	}
}

func TestCommandShowsFirstNonEmptyLine(t *testing.T) {
	got := Run(manifest.StatusSpec{Kind: manifest.StatusCommand, Run: "printf '\\n  hello  \\nsecond\\n'"}, t.TempDir())
	if got.State != Up || got.Text != "hello" {
		t.Fatalf("got %+v", got)
	}
	if got := Run(manifest.StatusSpec{Kind: manifest.StatusCommand, Run: "exit 3"}, t.TempDir()); got.State != Down {
		t.Fatalf("failing command: got %+v", got)
	}
}

func TestValidateRejectsBadExpressionAndUnknownPlaceholder(t *testing.T) {
	errs := Validate("jobs", manifest.StatusSpec{
		Kind: manifest.StatusJSON,
		Pull: map[string]string{"n": "length([?status=="},
		Show: "{n} and {missing}",
	})
	if len(errs) != 2 {
		t.Fatalf("want 2 errors, got %v", errs)
	}
	if errs := Validate("ok", manifest.StatusSpec{Kind: manifest.StatusJSON, Pull: map[string]string{"n": "length(@)"}, Show: "{n}"}); len(errs) != 0 {
		t.Fatalf("valid entry reported %v", errs)
	}
}

// A script that backgrounds a child keeps stdout open after sh is killed at
// the timeout; the checker must still return, as Down, instead of waiting for
// the child. Without cmd.WaitDelay this call blocks for the full sleep.
func TestCommandTimeoutReturnsEvenWhenAChildHoldsStdout(t *testing.T) {
	prev := Timeout
	Timeout = 200 * time.Millisecond
	t.Cleanup(func() { Timeout = prev })

	done := make(chan Result, 1)
	go func() {
		done <- Run(manifest.StatusSpec{Kind: manifest.StatusCommand, Run: "sleep 30 | head -1"}, t.TempDir())
	}()
	select {
	case got := <-done:
		if got.State != Down {
			t.Fatalf("got %+v, want Down", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of a 200ms timeout")
	}
}

func TestJSONFailureHidesTheRequestURL(t *testing.T) {
	t.Setenv("STATUS_TEST_KEY", "abc123")
	got := Run(manifest.StatusSpec{
		Kind: manifest.StatusJSON, URL: "http://127.0.0.1:1/x?token=${STATUS_TEST_KEY}",
		Pull: map[string]string{"n": "length(@)"}, Show: "{n}",
	}, "")
	if got.State != Down || strings.Contains(got.Text, "abc123") || strings.Contains(got.Text, "token=") {
		t.Fatalf("got %+v, want a Down result that omits the query string", got)
	}
}

func TestControlCharactersAreStripped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"s":"ok\u001b[2Jhi"}`))
	}))
	defer srv.Close()
	got := Run(manifest.StatusSpec{Kind: manifest.StatusJSON, URL: srv.URL, Pull: map[string]string{"s": "s"}, Show: "{s}"}, "")
	if strings.ContainsRune(got.Text, 0x1b) {
		t.Fatalf("escape byte survived: %q", got.Text)
	}
}
