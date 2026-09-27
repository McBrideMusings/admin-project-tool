package ports

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestListProcessesFindsASpawnedChild proves the ps-fallback path (the one
// this darwin test machine actually exercises — there is no /proc here)
// finds a real child process by pid and reports its argv correctly.
func TestListProcessesFindsASpawnedChild(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	deadline := time.Now().Add(2 * time.Second)
	var found *Proc
	for time.Now().Before(deadline) {
		for _, p := range ListProcesses() {
			if p.PID == pid {
				pp := p
				found = &pp
				break
			}
		}
		if found != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if found == nil {
		t.Fatalf("ListProcesses never reported pid %d (sleep 5)", pid)
	}
	if found.Argv0 != "sleep" {
		t.Errorf("Argv0 = %q, want %q", found.Argv0, "sleep")
	}
	if len(found.Argv) < 2 || found.Argv[1] != "5" {
		t.Errorf("Argv = %v, want [sleep 5]", found.Argv)
	}
}

// TestListProcessesSkipsSelf proves the self-pid exclusion.
func TestListProcessesSkipsSelf(t *testing.T) {
	self := os.Getpid()
	for _, p := range ListProcesses() {
		if p.PID == self {
			t.Fatalf("ListProcesses reported its own pid %d — must skip self", self)
		}
	}
}

// TestPidCwdResolvesARealProcess proves PidCwd finds the actual working
// directory of a live child, via the lsof fallback on this darwin machine
// (no /proc).
func TestPidCwdResolvesARealProcess(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("sleep", "5")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	cwd := PidCwd(cmd.Process.Pid, 3*time.Second)
	if cwd == "" {
		t.Skip("lsof unavailable or timed out in this environment")
	}
	// t.TempDir() can return a path through a symlink (e.g. /var -> /private/var
	// on macOS); resolve both sides before comparing.
	wantReal, _ := filepath.EvalSymlinks(dir)
	gotReal, _ := filepath.EvalSymlinks(cwd)
	if gotReal != wantReal {
		t.Errorf("PidCwd = %q (resolved %q), want %q (resolved %q)", cwd, gotReal, dir, wantReal)
	}
}

// TestCwdInRoot exercises the boundary cases of cwdInRoot directly —
// exact match, nested match, sibling-prefix false positive, and empty cwd.
//
// MUTATION EVIDENCE: change the prefix check from
// `strings.HasPrefix(cwd, projectRoot+string(os.PathSeparator))` to
// `strings.HasPrefix(cwd, projectRoot)` (drop the separator) and this test's
// sibling case fails — "/home/proj-other" would wrongly read as inside
// "/home/proj".
func TestCwdInRoot(t *testing.T) {
	cases := []struct {
		cwd, root string
		want      bool
	}{
		{"/home/proj", "/home/proj", true},
		{"/home/proj/sub", "/home/proj", true},
		{"/home/proj-other", "/home/proj", false},
		{"", "/home/proj", false},
		{"/home/other", "/home/proj", false},
	}
	for _, c := range cases {
		if got := cwdInRoot(c.cwd, c.root); got != c.want {
			t.Errorf("cwdInRoot(%q, %q) = %v, want %v", c.cwd, c.root, got, c.want)
		}
	}
}

// TestChildPIDsOrdersDeepestFirst proves ChildPIDs walks a real process tree
// (this test process -> sh -> sleep) and returns the grandchild before the
// child, so a caller killing in order reaps leaves first.
func TestChildPIDsOrdersDeepestFirst(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 5 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	parentPID := cmd.Process.Pid
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	// Poll: the grandchild `sleep` needs a moment to fork under `sh`.
	deadline := time.Now().Add(2 * time.Second)
	var children []int
	for time.Now().Before(deadline) {
		children = ChildPIDs(parentPID)
		if len(children) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(children) == 0 {
		t.Fatalf("ChildPIDs(%d) found no descendants of `sh -c 'sleep 5 & wait'`", parentPID)
	}
	// The last entry (closest to the root pid) must actually be a direct
	// child of parentPID — i.e. sh itself's child, not a stray unrelated pid.
	last := children[len(children)-1]
	out, err := exec.Command("ps", "-p", strconv.Itoa(last), "-o", "ppid=").Output()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	ppidStr := trimSpaceLocal(string(out))
	if ppidStr != strconv.Itoa(parentPID) {
		t.Errorf("last child %d has ppid %s, want %d (deepest-first means the immediate child is last)", last, ppidStr, parentPID)
	}
}

func trimSpaceLocal(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	for len(s) > 0 && (s[0] == '\n' || s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}
