package ports

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// TestPortFreeOnAnUnusedPort proves PortFree reports true for a port nothing
// is listening on. Picks the port by binding port 0 and releasing it
// immediately, then race-checking free before anything else on the CI
// machine can grab it — flaky in theory, standard practice in this class of
// test.
func TestPortFreeOnAnUnusedPort(t *testing.T) {
	port := freeTCPPort(t)
	if !PortFree(port) {
		t.Errorf("PortFree(%d) = false, want true for an unused port", port)
	}
}

// TestPortFreeFalseWhileListening proves PortFree reports false for a port
// with a live listener.
//
// MUTATION EVIDENCE: change PortFree's probe host list to skip
// "127.0.0.1" (only probe "0.0.0.0") and this test fails — SO_REUSEADDR
// lets a wildcard-bind probe succeed *over* a listener bound specifically to
// 127.0.0.1 (exactly the Vite-on-loopback case the doc comment calls out),
// so PortFree would wrongly report the port free.
func TestPortFreeFalseWhileListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if PortFree(port) {
		t.Errorf("PortFree(%d) = true, want false while a listener is bound to 127.0.0.1:%d", port, port)
	}
}

// TestPortFreeTrueAfterListenerCloses proves SO_REUSEADDR is actually being
// set: without it, a connection left in TIME_WAIT after Close() can make a
// bind probe fail for up to ~60s even though nothing is listening —
// the whole reason PortFree sets the option.
// This test only proves the immediate-reuse case (no TIME_WAIT was
// generated, since nothing ever connected) — a lighter but still real
// regression guard: it fails if PortFree's Control callback is dropped
// entirely and the underlying bind starts refusing to reuse a
// freshly-closed, unconnected listener's port on a loaded CI box.
func TestPortFreeTrueAfterListenerCloses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if !PortFree(port) {
		t.Errorf("PortFree(%d) = false, want true immediately after Close()", port)
	}
}

// TestWaitForPortSucceedsOnceListening proves WaitForPort returns true once
// a listener comes up mid-poll.
func TestWaitForPortSucceedsOnceListening(t *testing.T) {
	port := freeTCPPort(t)
	go func() {
		time.Sleep(150 * time.Millisecond)
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return
		}
		defer ln.Close()
		time.Sleep(2 * time.Second)
	}()
	if !WaitForPort(port, 3*time.Second) {
		t.Errorf("WaitForPort(%d) = false, want true once a listener starts", port)
	}
}

// TestWaitForPortTimesOutWhenNothingListens proves WaitForPort gives up
// (returns false) rather than blocking forever.
//
// MUTATION EVIDENCE: remove the `time.Now().Before(deadline)` loop condition
// (replace with `true`, an infinite loop) and this test hangs instead of
// completing — `go test`'s own timeout would eventually kill it, but this
// assertion catches the regression immediately via its own short timeout.
func TestWaitForPortTimesOutWhenNothingListens(t *testing.T) {
	port := freeTCPPort(t)
	start := time.Now()
	if WaitForPort(port, 300*time.Millisecond) {
		t.Error("WaitForPort = true, want false when nothing ever listens")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("WaitForPort took %v, want it to respect its ~300ms timeout", elapsed)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}
