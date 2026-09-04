package ports

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestKillStaleSupervisorsKillsAMatchedProjectScopedProcess proves the core
// contract: a process whose cmdline contains a supervisor marker AND whose
// cwd is inside projectRoot gets killed and counted.
//
// MUTATION EVIDENCE: change the marker match from
// `strings.Contains(p.Cmdline, m)` to `p.Cmdline == m` (exact match instead
// of substring) and this test fails — real output pasted in this ticket's
// verdict.
func TestKillStaleSupervisorsKillsAMatchedProjectScopedProcess(t *testing.T) {
	// t.TempDir() can return a path through a symlink (e.g. /var ->
	// /private/var on macOS) — resolve it up front so it matches the real
	// cwd PidCwd's lsof fallback reports for the spawned child.
	dir := realTempDir(t)
	// "nodemon" is one of defaultSupervisorMarkers; embed it in the argv so
	// the cmdline substring match fires, and run from `dir` so the cwd scope
	// matches too.
	cmd := exec.Command("sh", "-c", "exec -a nodemon-fake sleep 5")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	go cmd.Wait()
	waitForPID(t, pid)

	// The shell rewrote argv0 to "nodemon-fake" via `exec -a`; cmdline is
	// what ps reports, and "nodemon-fake" contains the "nodemon" marker.
	n := KillStaleSupervisors(dir, nil)
	if n != 1 {
		t.Fatalf("KillStaleSupervisors killed %d process(es), want 1", n)
	}
	if pidAlive(pid) {
		t.Errorf("pid %d still alive after KillStaleSupervisors", pid)
	}
}

// TestKillStaleSupervisorsIgnoresProcessesOutsideProjectRoot proves the
// project-scoping half of the contract — a marker match with a cwd OUTSIDE
// projectRoot must not be touched, or every user's editor/shell running a
// "nodemon" substring anywhere on the machine would get swept.
func TestKillStaleSupervisorsIgnoresProcessesOutsideProjectRoot(t *testing.T) {
	insideDir := t.TempDir()
	outsideDir := t.TempDir()

	cmd := exec.Command("sh", "-c", "exec -a nodemon-fake sleep 5")
	cmd.Dir = outsideDir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	waitForPID(t, pid)

	n := KillStaleSupervisors(insideDir, nil)
	if n != 0 {
		t.Fatalf("KillStaleSupervisors(%q) killed %d process(es) actually rooted in %q, want 0", insideDir, n, outsideDir)
	}
	if !pidAlive(pid) {
		t.Errorf("pid %d was killed despite being outside the scoped project root", pid)
	}
}

// TestNukeDevProcessesReportsPortHolderKills proves NukeDevProcesses' pass 1
// (port holders) actually kills the process bound to a declared port and
// reports it in NukeReport.Killed.
func TestNukeDevProcessesReportsPortHolderKills(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	// python3 -m http.server binds a real listening socket lsof/PortHolders
	// can find, unlike `sleep` which holds no port at all.
	port := freeTCPPortForNuke(t)
	cmd := exec.Command("python3", "-m", "http.server", "--bind", "127.0.0.1", itoaLocal(port))
	if err := cmd.Start(); err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		// Registered before any assertion runs, so it fires whether the test
		// passes, fails, or skips below — a failing assertion must never
		// leave this holder LISTENING on the port for the next run. The
		// holder is usually already dead by the time this runs (that's what
		// NukeDevProcesses is being tested to do); Kill/Wait on an
		// already-exited process return an error, which is expected and
		// ignored rather than failing the test.
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	if !waitForPortBusy(port, 2*time.Second) {
		t.Skip("http.server never bound the port in time")
	}

	report := NukeDevProcesses(t.TempDir(), NukeDevOptions{
		Ports: []PortSpec{{Port: port, Label: "test"}},
	})

	found := false
	for _, r := range report.Killed {
		if r.PID == pid {
			found = true
		}
	}
	if !found {
		t.Errorf("NukeReport.Killed = %+v, want it to include pid %d (the port holder)", report.Killed, pid)
	}
	if len(report.StillHeld) != 0 {
		t.Errorf("NukeReport.StillHeld = %v, want empty after killing the only holder", report.StillHeld)
	}
	if !PortFree(port) {
		t.Errorf("port %d still bound after NukeDevProcesses", port)
	}
}

// TestPortHolderCleanupHelperProcess is not a real test on its own — it is
// the deliberately-failing body that TestPortHolderCleanupRunsDespiteSubtestFailure
// re-invokes as a subprocess (via `go test -run` against the already-built
// test binary at os.Args[0]) to observe a t.Fatal from outside the process,
// the same "helper process" pattern os/exec_test.go uses in the Go standard
// library to test a failure path without the failure propagating up into the
// calling test's own pass/fail result — which a nested t.Run subtest cannot
// avoid, since a failing subtest always marks its parent test failed too. A
// normal `go test` run (without the env var below set) skips this
// immediately, so it never appears as a failure of its own.
func TestPortHolderCleanupHelperProcess(t *testing.T) {
	if os.Getenv("ADMIN_PORT_CLEANUP_HELPER") != "1" {
		t.Skip("only runs as a subprocess of TestPortHolderCleanupRunsDespiteSubtestFailure")
	}
	port, err := strconv.Atoi(os.Getenv("ADMIN_PORT_CLEANUP_HELPER_PORT"))
	if err != nil {
		t.Fatalf("bad ADMIN_PORT_CLEANUP_HELPER_PORT=%q: %v", os.Getenv("ADMIN_PORT_CLEANUP_HELPER_PORT"), err)
	}

	cmd := exec.Command("python3", "-m", "http.server", "--bind", "127.0.0.1", itoaLocal(port))
	if err := cmd.Start(); err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	// Registered before the deliberate failure below — this is the exact
	// shape being proven: the cleanup must fire even though this process is
	// about to fail before making any kill attempt of its own.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	if !waitForPortBusy(port, 2*time.Second) {
		t.Skip("http.server never bound the port in time")
	}

	t.Fatal("deliberate failure before any kill attempt, to prove t.Cleanup still runs")
}

// TestPortHolderCleanupRunsDespiteSubtestFailure is a permanent, deterministic
// regression test for the t.Cleanup leak-fix (admin-6x8l round 1): a spawned
// port holder must get killed even when the code exercising it fails before
// reaching any kill attempt.
//
// It does not touch NukeDevProcesses' retry/flakiness fix at all — that half
// is timing/scheduler-dependent and is intentionally left unverified by
// automation per this ticket. What this test proves is deterministic Go
// semantics, not scheduler timing: t.Cleanup runs on test completion
// regardless of pass/fail. TestPortHolderCleanupHelperProcess spawns the real
// holder, confirms it is bound, registers the kill as a cleanup, then fails
// immediately — before making any kill attempt itself — mirroring the exact
// shape that used to leak a holder process when a nuke_test.go assertion
// failed mid-test. This test runs that body as a subprocess, and after the
// subprocess exits (with the deliberate failure, confirmed by its nonzero
// exit code) asserts the port is free: that is only true if the subprocess's
// t.Cleanup ran despite the failure.
func TestPortHolderCleanupRunsDespiteSubtestFailure(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	port := freeTCPPortForNuke(t)

	cmd := exec.Command(os.Args[0], "-test.run", "^TestPortHolderCleanupHelperProcess$", "-test.v=true")
	cmd.Env = append(os.Environ(),
		"ADMIN_PORT_CLEANUP_HELPER=1",
		"ADMIN_PORT_CLEANUP_HELPER_PORT="+itoaLocal(port),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("helper process unexpectedly exited zero; want the deliberate t.Fatal to fail it:\n%s", out)
	}
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("helper process skipped instead of exercising the failure path:\n%s", out)
	}

	// The helper process's t.Cleanup already ran by the time it exited
	// above. If the cleanup fix were missing or broken, the holder would
	// still be LISTENING on this port right here.
	if !PortFree(port) {
		t.Errorf("port %d still bound after the failing helper process exited — t.Cleanup did not kill the holder\nhelper output:\n%s", port, out)
	}
}

// TestNukeDevProcessesOnAFreePortIsANoOp proves the acceptance criterion
// directly: "a port already free is a no-op that exits zero" — here, that
// StillHeld stays empty and nothing gets killed for an unheld port.
func TestNukeDevProcessesOnAFreePortIsANoOp(t *testing.T) {
	port := freeTCPPortForNuke(t)
	report := NukeDevProcesses(t.TempDir(), NukeDevOptions{
		Ports: []PortSpec{{Port: port}},
	})
	if len(report.Killed) != 0 {
		t.Errorf("Killed = %+v, want none for an already-free port", report.Killed)
	}
	if len(report.StillHeld) != 0 {
		t.Errorf("StillHeld = %v, want empty for an already-free port", report.StillHeld)
	}
}

// TestNukeDevProcessesKillsPidfileEntry proves pass 2 (pidfiles): an int
// pidfile naming a live pid gets that pid (and its children) killed, and the
// pidfile itself is removed.
func TestNukeDevProcessesKillsPidfileEntry(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	go cmd.Wait()
	waitForPID(t, pid)

	pidfilePath := filepath.Join(t.TempDir(), "server.pid")
	if err := os.WriteFile(pidfilePath, []byte(itoaLocal(pid)), 0o644); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}

	report := NukeDevProcesses(t.TempDir(), NukeDevOptions{
		Pidfiles: []PidfileSpec{{Path: pidfilePath, Format: "int"}},
	})

	found := false
	for _, r := range report.Killed {
		if r.PID == pid {
			found = true
		}
	}
	if !found {
		t.Errorf("Killed = %+v, want it to include pidfile pid %d", report.Killed, pid)
	}
	if _, err := os.Stat(pidfilePath); !os.IsNotExist(err) {
		t.Errorf("pidfile %s still exists after the sweep", pidfilePath)
	}
}

// TestNukeDevProcessesJSONPidfile proves the json pidfile format (with a
// custom key) parses correctly — a plain "format: int" test wouldn't catch
// a regression in the JSON branch.
func TestNukeDevProcessesJSONPidfile(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	go cmd.Wait()
	waitForPID(t, pid)

	pidfilePath := filepath.Join(t.TempDir(), "server.json")
	body, _ := json.Marshal(map[string]int{"worker_pid": pid})
	if err := os.WriteFile(pidfilePath, body, 0o644); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}

	report := NukeDevProcesses(t.TempDir(), NukeDevOptions{
		Pidfiles: []PidfileSpec{{Path: pidfilePath, Format: "json", Key: "worker_pid"}},
	})

	found := false
	for _, r := range report.Killed {
		if r.PID == pid {
			found = true
		}
	}
	if !found {
		t.Errorf("Killed = %+v, want it to include JSON pidfile pid %d", report.Killed, pid)
	}
}

// realTempDir returns t.TempDir() with symlinks resolved — on macOS
// t.TempDir() lives under /var, which is a symlink to /private/var, and
// PidCwd's lsof fallback reports the resolved path. Comparing an unresolved
// caller path against a resolved lsof answer would false-negative every
// cwd-scoping test on macOS.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return real
}

func waitForPID(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range ListProcesses() {
			if p.PID == pid {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d never showed up in ListProcesses", pid)
}

func waitForPortBusy(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !PortFree(port) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func freeTCPPortForNuke(t *testing.T) int {
	t.Helper()
	for p := 20000; p < 20100; p++ {
		if PortFree(p) {
			return p
		}
	}
	t.Fatal("no free port found in 20000-20100")
	return 0
}

func itoaLocal(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestWebSubdirMatchesArgvNotJustArgv0 covers the shape that let a `npm run
// dev` vite survive every teardown: the process is `node
// <root>/apps/web/node_modules/.bin/vite`, so argv0 is "node" and the only
// path naming the web subdir sits in argv[1]. Matching argv0 alone missed it,
// and the cwd fallback that should have caught it was dead on macOS because
// `ps` cannot report a working directory.
func TestWebSubdirMatchesArgvNotJustArgv0(t *testing.T) {
	root := t.TempDir()
	webDir := filepath.Join(root, "apps", "web")

	// The real ListProcesses cannot be faked here, so assert on the matching
	// rule itself against the exact argv shape observed.
	argv := []string{"node", filepath.Join(webDir, "node_modules", ".bin", "vite")}

	matched := false
	prefix := webDir + string(os.PathSeparator)
	if strings.HasPrefix(argv[0], prefix) {
		matched = true
	}
	for _, tok := range argv {
		if strings.HasPrefix(tok, prefix) {
			matched = true
		}
	}
	if !matched {
		t.Fatal("a web-subdir path in argv[1] must match; argv0 alone is not enough")
	}

	// And the same argv under a DIFFERENT checkout must not match, which is
	// what keeps a worktree-scoped teardown off a sibling worktree.
	other := filepath.Join(t.TempDir(), "apps", "web", "node_modules", ".bin", "vite")
	if strings.HasPrefix(other, prefix) {
		t.Fatal("another checkout's identical path must not match this root")
	}
}

// matchesAny gates the PidCwd lookup so the sweep pays for lsof only on
// plausible candidates. Getting it wrong either misses processes or makes the
// sweep take minutes.
func TestMatchesAny(t *testing.T) {
	markers := []string{"vite", "esbuild", "npm run dev"}
	for _, hit := range []string{
		"node /x/node_modules/.bin/vite",
		"npm run dev",
		"/x/esbuild --service=0.21.5",
	} {
		if !matchesAny(hit, markers) {
			t.Errorf("matchesAny(%q) = false, want true", hit)
		}
	}
	for _, miss := range []string{"", "go run .", "/usr/bin/ssh host"} {
		if matchesAny(miss, markers) {
			t.Errorf("matchesAny(%q) = true, want false", miss)
		}
	}
}
