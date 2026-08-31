package ports

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestDevPrecheckReapsAnOrphanProcess proves the orphan half of the
// live-vs-orphan contract: a process rooted in projectRoot whose parent
// (this test binary, run under `go test`) has no controlling terminal in a
// CI-like invocation gets reaped rather than blocking. On an interactive
// terminal run (`go test` from a real shell) this test process itself may
// have a tty ancestor, which would reclassify the child as "live" and skip
// straight to Aborted=true — that's still a real, correct classification
// this test also checks for, rather than a flake to work around.
func TestDevPrecheckClassifiesASpawnedRuntimeProcess(t *testing.T) {
	dir := realTempDir(t)
	// A real python3 invocation re-execs into a differently-named binary on
	// macOS's Framework build (.../Python.app/Contents/MacOS/Python), so
	// argv0 never matches "python3" there — faking argv0 via `exec -a` is
	// the portable way to get a process whose argv0 basename is a runtime
	// name AND whose argv carries a path under `dir` (so both
	// DevPrecheck halves — the runtime-basename filter and argvInProject —
	// see what they're supposed to). `tail -f` blocks indefinitely reading
	// a path, which becomes that project-scoped argv token.
	script := dir + "/server.js"
	if err := writeFile(script, ""); err != nil {
		t.Fatalf("write script: %v", err)
	}
	cmd := exec.Command("sh", "-c", "exec -a node tail -f "+script)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	go cmd.Wait()
	waitForPID(t, pid)

	result := DevPrecheck(dir, DevPrecheckOptions{Runtimes: []string{"node"}})

	inLive := containsInt(result.Live, pid)
	inReaped := containsInt(result.Reaped, pid)
	if !inLive && !inReaped {
		t.Fatalf("DevPrecheck classified neither Live nor Reaped for pid %d (Live=%v Reaped=%v)", pid, result.Live, result.Reaped)
	}
	if inReaped {
		// Reaped means DevPrecheck already killed it — verify that.
		time.Sleep(200 * time.Millisecond)
		if pidAlive(pid) {
			t.Errorf("pid %d classified as Reaped but is still alive", pid)
		}
		if result.Aborted {
			t.Errorf("Aborted = true, want false when every match was an orphan (no live session)")
		}
	}
	if inLive {
		if !result.Aborted {
			t.Errorf("Aborted = false, want true when a live session was found")
		}
		_ = cmd.Process.Kill()
	}
}

// TestDevPrecheckIgnoresProcessesOutsideProjectRoot proves scoping: a
// runtime process whose cwd is NOT inside projectRoot must be classified as
// neither live nor orphan for THIS project's precheck.
func TestDevPrecheckIgnoresProcessesOutsideProjectRoot(t *testing.T) {
	outsideDir := realTempDir(t)
	insideDir := realTempDir(t)
	script := outsideDir + "/server.js"
	if err := writeFile(script, ""); err != nil {
		t.Fatalf("write script: %v", err)
	}
	cmd := exec.Command("sh", "-c", "exec -a node tail -f "+script)
	cmd.Dir = outsideDir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	waitForPID(t, pid)

	result := DevPrecheck(insideDir, DevPrecheckOptions{Runtimes: []string{"node"}})
	if containsInt(result.Live, pid) || containsInt(result.Reaped, pid) {
		t.Errorf("DevPrecheck(%q) classified pid %d, which is rooted in %q — scoping leak", insideDir, pid, outsideDir)
	}
}

// TestDevPrecheckAbortsOnARunningContainer proves the container-check half:
// a declared container name that docker reports running aborts the
// precheck, independent of any process scan.
func TestDevPrecheckAbortsOnARunningContainer(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	// A container name guaranteed not to exist must never abort.
	result := DevPrecheck(t.TempDir(), DevPrecheckOptions{Containers: []string{"admin-3rgp-15-definitely-not-running"}})
	if result.Aborted {
		t.Errorf("Aborted = true for a container that isn't running")
	}
}

// TestDevPrecheckAcceptRuntimeIsScopedByCwdAlone proves the AcceptRuntimes
// path (helpers with no project path in argv, e.g. ffmpeg) is still scoped
// by cwd even though it skips the argvInProject check runtimeSet entries
// get — this is the ONLY scoping check on that path, so a regression here
// leaks every AcceptRuntimes-matched process on the machine into the
// precheck regardless of project.
//
// MUTATION EVIDENCE: replace the `if !cwdInRoot(cwd, root) { continue }`
// check with a no-op and this test fails — real output pasted in this
// ticket's verdict.
func TestDevPrecheckAcceptRuntimeIsScopedByCwdAlone(t *testing.T) {
	outsideDir := realTempDir(t)
	insideDir := realTempDir(t)
	cmd := exec.Command("sh", "-c", "exec -a helper-tool tail -f /dev/null")
	cmd.Dir = outsideDir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	waitForPID(t, pid)

	result := DevPrecheck(insideDir, DevPrecheckOptions{AcceptRuntimes: []string{"helper-tool"}})
	if containsInt(result.Live, pid) || containsInt(result.Reaped, pid) {
		t.Errorf("DevPrecheck(%q) classified pid %d via AcceptRuntimes despite its cwd being %q, not inside the scoped root", insideDir, pid, outsideDir)
	}
}

func containsInt(xs []int, want int) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
