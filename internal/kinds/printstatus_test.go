package kinds

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/status"
)

func TestFormatStatusLineShowsStateAndText(t *testing.T) {
	up := formatStatusLine("api", status.Result{State: status.Up, Text: "up"}, 6)
	if !strings.Contains(up, "●") || !strings.Contains(up, "up") {
		t.Errorf("up line = %q", up)
	}
	down := formatStatusLine("api", status.Result{State: status.Down, Text: "unreachable: HTTP 503"}, 6)
	if !strings.Contains(down, "○") || !strings.Contains(down, "unreachable: HTTP 503") {
		t.Errorf("down line = %q", down)
	}
}

func TestMakePrintStatusNoopWithoutStatusTable(t *testing.T) {
	runner, err := makePrintStatus("print-status", manifest.Action{}, &manifest.Manifest{}, nil)
	if err != nil {
		t.Fatalf("makePrintStatus: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
}

// TestMakePrintStatusEvaluatesEveryEntry proves the runner calls each entry's
// checker: the fake server answers only the path a json entry asks for, so a
// runner that skipped the entry (or hit the wrong URL) records no request.
func TestMakePrintStatusEvaluatesEveryEntry(t *testing.T) {
	var hit []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = append(hit, r.URL.Path)
		w.Write([]byte(`[1,2,3]`))
	}))
	defer srv.Close()

	m := &manifest.Manifest{Status: map[string]manifest.StatusSpec{
		"jobs": {Kind: manifest.StatusJSON, URL: srv.URL + "/api/queue", Pull: map[string]string{"n": "length(@)"}, Show: "{n} jobs"},
	}}
	runner, err := makePrintStatus("print-status", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makePrintStatus: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if len(hit) != 1 || hit[0] != "/api/queue" {
		t.Errorf("requests = %v, want one GET /api/queue", hit)
	}
}
