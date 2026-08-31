package luahost

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// fakeMetro starts an httptest server whose /status body matches Metro's own
// real one, then reports the port it bound — the same fixture shape
// internal/metro's own tests use, reused here to prove the Lua binding
// actually reaches internal/metro rather than a stub.
func fakeMetro(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("packager-status:running"))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// TestMetroRunningTrueAgainstFakeServer proves metro.running(port) reaches
// internal/metro.Running rather than always returning false — the
// discriminating mutation is deleting registerMetro's "running" entry
// entirely, which would fail this with "attempt to call a nil value".
func TestMetroRunningTrueAgainstFakeServer(t *testing.T) {
	port := fakeMetro(t)
	h, out, _ := newTestHost(t)
	if err := h.Exec(`info(tostring(metro.running(`+strconv.Itoa(port)+`)))`, nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "true") {
		t.Fatalf("expected metro.running to report true against a live fake server, got %q", out.String())
	}
}

// TestMetroRunningFalseWhenNothingListens proves the binding does not just
// always report true.
func TestMetroRunningFalseWhenNothingListens(t *testing.T) {
	h, out, _ := newTestHost(t)
	if err := h.Exec(`info(tostring(metro.running(1)))`, nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "false") {
		t.Fatalf("expected metro.running(1) to report false, got %q", out.String())
	}
}

// TestMetroEnsureSkipsStartWhenAlreadyRunning proves metro.ensure returns
// false (did not start a new process) when a bundler is already answering —
// the case every react-native dev action hits on a second run.
func TestMetroEnsureSkipsStartWhenAlreadyRunning(t *testing.T) {
	port := fakeMetro(t)
	h, out, _ := newTestHost(t)
	dir := t.TempDir()
	err := h.Exec(`local started = metro.ensure("`+dir+`", {port = `+strconv.Itoa(port)+`})
info(tostring(started))`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "false") {
		t.Fatalf("expected ensure() to report false (already running), got %q", out.String())
	}
}

func TestEnvReadsProcessEnvironment(t *testing.T) {
	t.Setenv("ADMIN_LUA_ENV_TEST", "value-here")
	h, out, _ := newTestHost(t)
	if err := h.Exec(`info(env("ADMIN_LUA_ENV_TEST"))`, nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "value-here") {
		t.Fatalf("expected env() to read the process var, got %q", out.String())
	}
}

// TestEnvReturnsNilForUnsetVar proves an unset var comes back as Lua nil,
// not an empty string — a body that does `if env("X") then` must see the
// difference between unset and set-to-empty.
//
// MUTATION EVIDENCE: change envGet's `if !ok` branch to always push
// lua.LString(v) (dropping the LookupEnv ok check, i.e. treat unset as "")
// and this test fails: `tostring(env("ADMIN_LUA_ENV_TEST_UNSET"))` becomes
// "" instead of "nil".
func TestEnvReturnsNilForUnsetVar(t *testing.T) {
	h, out, _ := newTestHost(t)
	if err := h.Exec(`info(tostring(env("ADMIN_LUA_ENV_TEST_UNSET_XYZ")))`, nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "nil") {
		t.Fatalf("expected env() of an unset var to be nil, got %q", out.String())
	}
}
