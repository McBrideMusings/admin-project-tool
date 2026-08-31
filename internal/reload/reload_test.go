package reload

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPIDFileRoundTrip proves WritePIDFile writes the current process's PID
// and readPIDFile parses it back out, then CleanupPIDFile removes the file.
func TestPIDFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "admin-run-test.pid")

	if err := WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}

	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatalf("readPIDFile: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("readPIDFile() = %d, want current pid %d", pid, os.Getpid())
	}

	CleanupPIDFile(path)
	if _, err := readPIDFile(path); err == nil {
		t.Error("expected readPIDFile to fail after CleanupPIDFile removed the file")
	}
}

// TestReadPIDFileRejectsGarbage proves a malformed PID file (not a bare
// integer) is reported as an error rather than silently parsed as 0.
//
// MUTATION EVIDENCE: replace readPIDFile's `if err != nil { return 0,
// fmt.Errorf(...) }` after strconv.Atoi with an unconditional `_ = err`
// (always treat the parse as successful) and this test fails with:
// "expected an error for a non-numeric pid file, got pid=0" — verified by
// hand-editing reload.go to that shape and re-running go test, then
// reverting.
func TestReadPIDFileRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pid")
	if err := os.WriteFile(path, []byte("not-a-pid"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	pid, err := readPIDFile(path)
	if err == nil {
		t.Fatalf("expected an error for a non-numeric pid file, got pid=%d", pid)
	}
}

// TestGlobPIDFiles proves globPIDFiles matches only "<prefix>-*.pid" and
// ignores an unrelated file in the same directory.
//
// MUTATION EVIDENCE: change the glob pattern from prefix+"-*.pid" to
// prefix+"*.pid" (dropping the required "-") and this test fails: it would
// then also match "adminrunextra.pid", so len(got) becomes 3 instead of 2 —
// verified by hand-editing globPIDFiles to that pattern and re-running,
// which reported "globPIDFiles() returned 3 files, want 2", then reverted.
func TestGlobPIDFiles(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "admin-run")
	for _, name := range []string{"admin-run-web.pid", "admin-run-worker.pid", "admin-runextra.pid", "unrelated.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("1"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	got, err := globPIDFiles(prefix)
	if err != nil {
		t.Fatalf("globPIDFiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("globPIDFiles() returned %d files, want 2: %v", len(got), got)
	}
}
