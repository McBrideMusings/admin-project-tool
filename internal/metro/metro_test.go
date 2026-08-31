package metro

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeMetroServer starts an httptest server on a fixed port answering
// Metro's exact /status body, so Running can be exercised without a real
// react-native install.
func fakeMetroServer(t *testing.T, port int) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("could not bind port %d for fake Metro: %v", port, err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			fmt.Fprint(w, statusBody)
			return
		}
		w.WriteHeader(404)
	}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	return srv
}

// TestRunningTrueWhenStatusBodyMatches proves Running checks the exact
// packager-status body, not just a 200 — the Go port of
// admin_lib/metro.py::metro_running.
//
// MUTATION EVIDENCE: change Running to just check `err == nil` (any
// response counts) and TestRunningFalseForWrongBody below fails instead —
// together the pair proves the body match is load-bearing.
func TestRunningTrueWhenStatusBodyMatches(t *testing.T) {
	srv := fakeMetroServer(t, 18081)
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if !Running(port) {
		t.Error("Running() = false, want true for a server answering the exact status body")
	}
}

// TestRunningFalseForWrongBody proves a server answering 200 on /status
// with the WRONG body (some unrelated service squatting the port) does not
// read as a live Metro.
func TestRunningFalseForWrongBody(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not metro at all")
	}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if Running(port) {
		t.Error("Running() = true, want false for a non-Metro server on the port")
	}
}

// TestRunningFalseWhenNothingListens proves an unreachable port reads as
// not running.
func TestRunningFalseWhenNothingListens(t *testing.T) {
	if Running(1) {
		t.Error("Running(1) = true, want false")
	}
}

// TestEnsureSkipsStartWhenAlreadyRunning proves Ensure is a no-op (started
// = false, err = nil) when Metro is already answering the port — matching
// admin_lib/metro.py::ensure_metro's `if metro_running(port): return False`.
//
// MUTATION EVIDENCE: remove the `if Running(port) { return false, nil }`
// early return and this test fails — Ensure would try to `npx react-native
// start` on a port that's already bound, which either errors immediately or
// (since Running is already true) is indistinguishable from success by this
// test's own oracle, so instead this test asserts `started == false`
// directly, which only the early return produces.
func TestEnsureSkipsStartWhenAlreadyRunning(t *testing.T) {
	srv := fakeMetroServer(t, 18082)
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	started, err := Ensure(t.TempDir(), port, "", "", time.Second)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if started {
		t.Error("Ensure started = true, want false — a bundler was already up")
	}
}

// TestStopRemovesStalePidFileWhenNothingRunning proves Stop cleans up a
// stale pid file when the port isn't answering, matching
// admin_lib/metro.py::stop_metro's "nothing on the port; drop any stale
// record" branch.
func TestStopRemovesStalePidFileWhenNothingRunning(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "metro.pid")
	if err := os.WriteFile(pidFile, []byte("99999999"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if Stop(pidFile, 1) {
		t.Error("Stop() = true, want false when nothing is listening")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Errorf("stale pid file should have been removed: %v", err)
	}
}

// TestStopReturnsFalseWithoutAPidFile proves Stop refuses to kill a bundler
// it can't attribute to a recorded pid — "admin did not start it" in
// Python's error message.
func TestStopReturnsFalseWithoutAPidFile(t *testing.T) {
	srv := fakeMetroServer(t, 18083)
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	if Stop("", port) {
		t.Error("Stop() = true, want false — no pid file names an owner")
	}
}
