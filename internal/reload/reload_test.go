package reload

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// resetOwner clears SetOwner's first-call-wins state around a test.
func resetOwner(t *testing.T) {
	t.Helper()
	ownerMu.Lock()
	ownerRoot, ownerLive = "", ""
	ownerMu.Unlock()
	t.Cleanup(func() {
		ownerMu.Lock()
		ownerRoot, ownerLive = "", ""
		ownerMu.Unlock()
	})
}

// TestRecordRoundTrip proves writeRecord writes this process's PID, the
// target, and the owner set by SetOwner, and readRecord parses them back;
// removeRecord then deletes the file.
func TestRecordRoundTrip(t *testing.T) {
	resetOwner(t)
	SetOwner("/proj", "dev:web")
	prefix := filepath.Join(t.TempDir(), "nested", "admin-run")

	path, err := writeRecord(prefix, "dev", "/ignored")
	if err != nil {
		t.Fatalf("writeRecord: %v", err)
	}
	rec, err := readRecord(path)
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	want := Record{PID: os.Getpid(), Target: "dev", Root: "/proj", Live: "dev:web"}
	if rec != want {
		t.Errorf("readRecord() = %+v, want %+v", rec, want)
	}

	removeRecord(path)
	if _, err := readRecord(path); err == nil {
		t.Error("expected readRecord to fail after removeRecord deleted the file")
	}
}

// TestRecordRootFallsBackToDir proves a loop started with no SetOwner (a
// kind called outside runSteps) still records its project, from its own
// working directory.
func TestRecordRootFallsBackToDir(t *testing.T) {
	resetOwner(t)
	path, err := writeRecord(filepath.Join(t.TempDir(), "admin-run"), "dev", "/from-dir")
	if err != nil {
		t.Fatalf("writeRecord: %v", err)
	}
	rec, err := readRecord(path)
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if rec.Root != "/from-dir" || rec.Live != "" {
		t.Errorf("readRecord() = %+v, want Root=/from-dir and no Live", rec)
	}
}

// TestSetOwnerFirstCallWins proves a nested command's runSteps cannot
// re-attribute the loop to itself.
func TestSetOwnerFirstCallWins(t *testing.T) {
	resetOwner(t)
	SetOwner("/proj", "dev")
	SetOwner("/proj", "nested")
	if root, live := owner(); root != "/proj" || live != "dev" {
		t.Errorf("owner() = (%q, %q), want (/proj, dev)", root, live)
	}
}

// TestRecordPathCarriesPID proves two loops on the same target name get two
// files — the collision a bare "<prefix>-<target>.pid" had.
func TestRecordPathCarriesPID(t *testing.T) {
	resetOwner(t)
	prefix := filepath.Join(t.TempDir(), "admin-run")
	path, err := writeRecord(prefix, "dev", "")
	if err != nil {
		t.Fatalf("writeRecord: %v", err)
	}
	if want := prefix + "-dev-" + strconv.Itoa(os.Getpid()) + ".pid"; path != want {
		t.Errorf("writeRecord path = %q, want %q", path, want)
	}
}

// TestReadRecordRejectsGarbage proves a malformed PID file — the bare-integer
// shape an older binary wrote, or no pid at all — is an error rather than a
// record with PID 0.
func TestReadRecordRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"bare": "1234", "nopid": `{"target":"dev"}`} {
		path := filepath.Join(dir, name+".pid")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if rec, err := readRecord(path); err == nil {
			t.Errorf("%s: expected an error, got %+v", name, rec)
		}
	}
}

// TestMatchRow proves a command row matches its own loop and its targets'
// loops, and nothing from another project or another command.
func TestMatchRow(t *testing.T) {
	match := MatchRow("/proj", "dev")
	cases := []struct {
		rec  Record
		want bool
	}{
		{Record{Root: "/proj", Live: "dev"}, true},
		{Record{Root: "/proj", Live: "dev:web"}, true},
		{Record{Root: "/other", Live: "dev"}, false},
		{Record{Root: "/proj", Live: "devtools"}, false},
		{Record{Root: "/proj", Live: ""}, false},
	}
	for _, c := range cases {
		if got := match(c.rec); got != c.want {
			t.Errorf("MatchRow(/proj, dev)(%+v) = %v, want %v", c.rec, got, c.want)
		}
	}
}

// TestMatchTargetStaysInItsProject proves `admin reload [filter]` never
// reaches another project's loop, filter or not.
func TestMatchTargetStaysInItsProject(t *testing.T) {
	cases := []struct {
		filter string
		rec    Record
		want   bool
	}{
		{"", Record{Root: "/proj", Target: "dev"}, true},
		{"", Record{Root: "/other", Target: "dev"}, false},
		{"ev", Record{Root: "/proj", Target: "dev"}, true},
		{"web", Record{Root: "/proj", Target: "dev"}, false},
	}
	for _, c := range cases {
		if got := MatchTarget("/proj", c.filter)(c.rec); got != c.want {
			t.Errorf("MatchTarget(/proj, %q)(%+v) = %v, want %v", c.filter, c.rec, got, c.want)
		}
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
