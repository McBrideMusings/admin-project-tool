package kinds

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// TestHostedLauncherHelper is the stand-in dev launcher: it starts a child in
// a new session (what Node's `detached: true` does), records that child's
// pid, then stays alive. It re-executes the test binary because a system
// binary such as /bin/sleep hides its environment on macOS.
func TestHostedLauncherHelper(t *testing.T) {
	switch os.Getenv("KINDS_TEST_HELPER") {
	case "launcher":
		c := exec.Command(os.Args[0], "-test.run=^TestHostedLauncherHelper$")
		c.Env = append(os.Environ(), "KINDS_TEST_HELPER=grandchild")
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(os.Getenv("KINDS_TEST_PIDFILE"), []byte(strconv.Itoa(c.Process.Pid)), 0o644)
		time.Sleep(60 * time.Second)
	case "grandchild":
		time.Sleep(60 * time.Second)
	default:
		t.Skip("helper process only")
	}
}

// TestStoppingAHostedRunKillsDetachedGrandchildren guards the leak behind
// dev ports held for hours: stopping a hosted run killed only the child's
// process group, so a grandchild that called setsid (Node's `detached:
// true`) kept running and holding its ports.
func TestStoppingAHostedRunKillsDetachedGrandchildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	const id = "detached-sweep-test"
	_, err := StartHostedWorker(id,
		[]string{os.Args[0], "-test.run=^TestHostedLauncherHelper$"},
		t.TempDir(),
		[]string{"KINDS_TEST_HELPER=launcher", "KINDS_TEST_PIDFILE=" + pidFile},
		24, 80, nil)
	if err != nil {
		t.Fatalf("StartHostedWorker: %v", err)
	}

	var grandchild int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				grandchild = n
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if grandchild == 0 {
		t.Fatal("launcher never reported its detached child")
	}
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })
	// Give the sweep something to find: the grandchild must be running.
	if err := syscall.Kill(grandchild, 0); err != nil {
		t.Fatalf("detached child %d not running: %v", grandchild, err)
	}

	e, ok := livework.Default.Get(id)
	if !ok {
		t.Fatal("no live-work entry registered")
	}
	if err := e.Ops.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(grandchild, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("detached grandchild %d survived stopping the hosted run", grandchild)
}
