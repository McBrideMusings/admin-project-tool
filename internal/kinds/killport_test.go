package kinds

import (
	"os/exec"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/ports"
)

// TestMakeKillPortRejectsNonNumericDefaultPorts proves the build-time
// validation admin_lib/interp.py::_make_kill_port does on `run` — a
// non-numeric token is rejected when the factory is built, not deferred to
// the first Runner call.
func TestMakeKillPortRejectsNonNumericDefaultPorts(t *testing.T) {
	_, err := makeKillPort("kill", manifest.Action{"run": "3000 not-a-port"}, &manifest.Manifest{}, nil)
	if err == nil {
		t.Fatal("makeKillPort accepted a non-numeric port in `run`, want an error")
	}
}

// TestMakeKillPortRequiresAPortWithNoDefault proves a kill-port command
// declared with an empty `run` (no default ports) requires an explicit argv
// port — matching admin_lib/interp.py::_make_kill_port's "port argument
// required" branch.
func TestMakeKillPortRequiresAPortWithNoDefault(t *testing.T) {
	runner, err := makeKillPort("kill", manifest.Action{}, &manifest.Manifest{}, nil)
	if err != nil {
		t.Fatalf("makeKillPort: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Error("runner(nil) = nil, want an error when no default ports and no argv ports are given")
	}
}

// TestMakeKillPortOnAFreePortIsANoOp proves the acceptance criterion
// directly through the kind factory: a port with no holder returns success
// with nothing to kill.
func TestMakeKillPortOnAFreePortIsANoOp(t *testing.T) {
	port := freeTCPPortForKillPort(t)
	runner, err := makeKillPort("kill", manifest.Action{"run": itoaKillPort(port)}, &manifest.Manifest{}, nil)
	if err != nil {
		t.Fatalf("makeKillPort: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Errorf("runner(nil) = %v, want nil for an already-free port", err)
	}
}

// TestMakeKillPortClearsARealHolder proves kill-port's end-to-end
// acceptance criterion: a port with a real listener gets its holder killed
// via the Runner, non-interactively (stdin under `go test` is not a TTY, so
// ports.KillPortInteractive force-kills rather than blocking on a prompt).
func TestMakeKillPortClearsARealHolder(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	port := freeTCPPortForKillPort(t)
	cmd := exec.Command("python3", "-m", "http.server", "--bind", "127.0.0.1", itoaKillPort(port))
	if err := cmd.Start(); err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	// A failed kill must not leave the fixture bound to its port: the next run
	// picks the same first free port, finds it taken, and fails too.
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	go cmd.Wait()
	if !waitForPortBusyKillPort(port, 2*time.Second) {
		_ = cmd.Process.Kill()
		t.Skip("http.server never bound the port in time")
	}

	runner, err := makeKillPort("kill", manifest.Action{"run": itoaKillPort(port)}, &manifest.Manifest{}, nil)
	if err != nil {
		t.Fatalf("makeKillPort: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Errorf("runner(nil) = %v, want nil after clearing a real holder", err)
	}
	if !ports.PortFree(port) {
		t.Errorf("port %d still bound after the kill-port Runner ran", port)
	}
}

func freeTCPPortForKillPort(t *testing.T) int {
	t.Helper()
	for p := 21000; p < 21100; p++ {
		if ports.PortFree(p) {
			return p
		}
	}
	t.Fatal("no free port found in 21000-21100")
	return 0
}

func waitForPortBusyKillPort(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !ports.PortFree(port) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func itoaKillPort(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
