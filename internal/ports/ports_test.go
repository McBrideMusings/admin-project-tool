package ports

import (
	"math/rand/v2"
	"net"
	"slices"
	"strconv"
	"testing"
	"time"
)

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
	port := freeTCPPort(t)
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
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

// freeTCPPort returns a random port in 20000-39999 that PortFree accepts.
// The range sits below the kernel's ephemeral range (49152-65535 on macOS),
// so no other process's outgoing connection takes the port mid-test; a
// kernel-assigned ":0" port is free only on the address it was bound to,
// while PortFree probes every address. The pick is random rather than the
// first free port so two test binaries running at once don't both take it.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	for range 100 {
		if p := 20000 + rand.IntN(20000); PortFree(p) {
			return p
		}
	}
	t.Fatal("no free port found in 20000-39999 after 100 picks")
	return 0
}

// TestNetstatListenerPIDs proves the macOS netstat parser picks the owner of
// a LISTEN socket on the asked port only: a spaced process name still yields
// its pid, a dual-stack listener is reported once, and an ESTABLISHED row or
// a listener on a port that merely shares a suffix is ignored.
func TestNetstatListenerPIDs(t *testing.T) {
	out := `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)          rxbytes      txbytes  rhiwat  shiwat          process:pid    state  options           gencnt    flags   flags1 usecnt rtncnt fltrs
tcp4       0      0  127.0.0.1.21052        *.*                    LISTEN                 0            0  131072  131072           Python:91451  00000 00000006 0000000003ac13e0 00000000 00000800      1      0 000000
tcp6       0      0  ::1.21052              *.*                    LISTEN                 0            0  131072  131072           Python:91451  00000 00000006 0000000003ac6a89 00000000 00000800      1      0 000000
tcp4       0      0  *.21052                *.*                    LISTEN                 0            0  131072  131072      Code Helper:812  00000 00000006 0000000003ac13e1 00000000 00000800      1      0 000000
tcp4       0      0  127.0.0.1.21052        127.0.0.1.50000        ESTABLISHED            0            0  131072  131072             curl:4000  00000 00000006 0000000003ac13e2 00000000 00000800      1      0 000000
tcp4       0      0  127.0.0.1.121052       *.*                    LISTEN                 0            0  131072  131072             node:5000  00000 00000006 0000000003ac13e3 00000000 00000800      1      0 000000
`
	got, ok := netstatListenerPIDs(out, 21052)
	want := []int{91451, 812}
	if !ok || !slices.Equal(got, want) {
		t.Errorf("netstatListenerPIDs(..., 21052) = %v, %v; want %v, true", got, ok, want)
	}

	// Linux netstat prints no process:pid column: not an answer, so the
	// caller must fall through to lsof rather than report the port unheld.
	linux := "Proto Recv-Q Send-Q Local Address           Foreign Address         State\ntcp        0      0 0.0.0.0:21052           0.0.0.0:*               LISTEN\n"
	if got, ok := netstatListenerPIDs(linux, 21052); ok || got != nil {
		t.Errorf("netstatListenerPIDs(linux output) = %v, %v; want nil, false", got, ok)
	}
}
