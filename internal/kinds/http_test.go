package kinds

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestHTTPPostsMethodAndBody proves kind="http" actually sends the
// configured method and body to the URL in `run` — verified against a real
// httptest.Server rather than mocking the transport.
func TestHTTPPostsMethodAndBody(t *testing.T) {
	var gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	runner, err := makeHTTP("hook", manifest.Action{
		"run":  srv.URL,
		"body": `{"hello":"world"}`,
	}, nil, nil)
	if err != nil {
		t.Fatalf("makeHTTP: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST (the default)", gotMethod)
	}
	if gotBody != `{"hello":"world"}` {
		t.Errorf("body = %q, want the configured body", gotBody)
	}
}

// TestHTTPRespectsExplicitMethod proves an explicit `method` overrides the
// POST default.
//
// MUTATION EVIDENCE: hard-code makeHTTP's method to always be "POST"
// (ignoring the action's own `method` key), and this test fails — the
// server sees POST instead of the configured GET.
func TestHTTPRespectsExplicitMethod(t *testing.T) {
	var hits int32
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		gotMethod = r.Method
	}))
	defer srv.Close()

	runner, err := makeHTTP("ping", manifest.Action{"run": srv.URL, "method": "GET"}, nil, nil)
	if err != nil {
		t.Fatalf("makeHTTP: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("server was hit %d times, want 1", hits)
	}
	if gotMethod != "GET" {
		t.Errorf("method = %q, want GET", gotMethod)
	}
}

// TestHTTPNeverFailsOnErrorResponse proves the Python-parity quirk this
// factory deliberately keeps: a >=400 response, or a request that can't be
// sent at all, only warns — the Runner still returns nil.
func TestHTTPNeverFailsOnErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	runner, err := makeHTTP("flaky", manifest.Action{"run": srv.URL}, nil, nil)
	if err != nil {
		t.Fatalf("makeHTTP: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil even on a 500 response (Python parity)", err)
	}
}

// TestHTTPUnresolvedEnvVarIsError proves a URL referencing an unset
// ${VAR} with no default is a hard error, matching resolve_env's fatal
// behaviour (the one case makeHTTP's Runner does return non-nil).
func TestHTTPUnresolvedEnvVarIsError(t *testing.T) {
	runner, err := makeHTTP("hook", manifest.Action{"run": "https://example.invalid/${TOTALLY_UNSET_VAR}"}, nil, nil)
	if err != nil {
		t.Fatalf("makeHTTP: %v", err)
	}
	if err := runner(nil); err == nil || !strings.Contains(err.Error(), "TOTALLY_UNSET_VAR") {
		t.Fatalf("runner() = %v, want an error naming the unset var", err)
	}
}
