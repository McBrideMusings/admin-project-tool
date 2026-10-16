package supervise

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/ports"
)

// TestHelperProcess is the stand-in daemon: the test binary re-executed, so
// the sweep can read its environment (a system binary hides it on macOS).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("SUPERVISE_TEST_HELPER") != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(60 * time.Second)
}

func startDaemon(t *testing.T, supervised bool) (*exec.Cmd, *Run) {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	// Simulate running inside an enclosing supervised run: the tag is inherited.
	c.Env = append(os.Environ(), "SUPERVISE_TEST_HELPER=1", ports.EnvRunID+"=r-test-enclosing")
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // Node's detached: true
	run := Start(c, t.TempDir(), supervised)
	if err := c.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	go c.Wait()
	t.Cleanup(func() { _ = c.Process.Kill() })
	return c, run
}

// TestUnsupervisedActionsDaemonOutlivesTheRun guards `supervise = false`:
// the daemon an opted-out action starts must survive the run stopping, which
// would otherwise sweep it, and must not carry the enclosing run's tag
// either, or the enclosing run's sweep would kill it.
func TestUnsupervisedActionsDaemonOutlivesTheRun(t *testing.T) {
	c, run := startDaemon(t, false)
	if RunIDFromEnv(c.Env) != "" {
		t.Fatalf("unsupervised command still carries ADMIN_RUN_ID %q", RunIDFromEnv(c.Env))
	}
	run.Stop()
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(c.Process.Pid, 0); err != nil {
		t.Errorf("daemon %d of an unsupervised action died when its run stopped: %v", c.Process.Pid, err)
	}
}
