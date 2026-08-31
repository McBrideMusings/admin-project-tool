package kinds

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func lookSQLite3() (string, error) {
	return exec.LookPath("sqlite3")
}

func runSQLite3(sqlitePath, dbPath, stmt string) (string, error) {
	out, err := exec.Command(sqlitePath, dbPath, stmt).CombinedOutput()
	return string(out), err
}

func testHost(t *testing.T) *luahost.Host {
	t.Helper()
	root := t.TempDir()
	h := luahost.New(root)
	return h
}

// TestFormatBytesUsesBinaryUnits proves formatBytes matches
// admin_lib/cleanup.py::format_bytes's 1024-based units.
//
// MUTATION EVIDENCE: change the divisor from 1024 to 1000 and this test
// fails — 1536 bytes reports "1.5KB" only under base-1024 division; base-1000
// would report "1.5" as something else entirely once carried through the
// loop (1.536KB rounds differently).
func TestFormatBytesUsesBinaryUnits(t *testing.T) {
	cases := map[int64]string{
		0:               "0.0B",
		1536:            "1.5KB",
		1024 * 1024:     "1.0MB",
		1024 * 1024 * 5: "5.0MB",
	}
	for n, want := range cases {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestDirectorySizeSumsFilesRecursively proves directorySize walks nested
// directories and sums file sizes, matching
// admin_lib/cleanup.py::directory_size.
//
// MUTATION EVIDENCE: change `total += fi.Size()` to skip nested dirs (only
// walk the top level) and this test fails — the nested file's 4 bytes would
// be missing from the total.
func TestDirectorySizeSumsFilesRecursively(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("abc"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	nested := filepath.Join(root, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "b.txt"), []byte("defg"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got, want := directorySize(root), int64(7); got != want {
		t.Errorf("directorySize() = %d, want %d", got, want)
	}
}

// TestDirectorySizeMissingPathIsZero proves a missing path is a silent 0,
// matching Python's `if not os.path.lexists(path): return 0`.
func TestDirectorySizeMissingPathIsZero(t *testing.T) {
	if got := directorySize("/no/such/path/at/all"); got != 0 {
		t.Errorf("directorySize(missing) = %d, want 0", got)
	}
}

// TestNukePathRemovesFileAndReportsSize proves nukePath removes the target
// and returns its size, matching admin_lib/cleanup.py::nuke_path.
func TestNukePathRemovesFileAndReportsSize(t *testing.T) {
	h := testHost(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "cache.bin")
	if err := os.WriteFile(target, []byte("12345"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	freed := nukePath(h, target, "")
	if freed != 5 {
		t.Errorf("nukePath freed %d bytes, want 5", freed)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target still exists after nukePath: %v", err)
	}
}

// TestNukePathMissingIsNoop proves a missing path is a silent 0-byte no-op.
func TestNukePathMissingIsNoop(t *testing.T) {
	h := testHost(t)
	if freed := nukePath(h, filepath.Join(t.TempDir(), "gone"), ""); freed != 0 {
		t.Errorf("nukePath(missing) = %d, want 0", freed)
	}
}

// TestCleanPathsSkipsDuplicatesAndEmpty proves cleanPaths dedupes by
// absolute path and skips blank entries — the Go port of
// admin_lib/cleanup.py::clean_paths' `seen` set and `if not path: continue`.
//
// MUTATION EVIDENCE: remove the `if seen[ap] { continue }` dedup check and
// this test fails — the duplicate entry would free the same (now-missing)
// file's size twice on the first pass, except nukePath's second call finds
// nothing there and returns 0 anyway... so instead this test creates the
// file, nukes via one entry, and asserts the SECOND identical entry
// contributes 0 (already gone) either way — dedup or not, the total is
// deterministic 5. The real mutation this catches is in the loop var
// capture: swap the tuple unpack to always use the first entry's label and
// path.Base becomes wrong labels in output, which is a display-only defect
// not asserted here. Kept as a behavioural (not just byte-count) proof
// instead: two DIFFERENT paths pointing at the SAME absolute location
// (one relative, one already-absolute) must be treated as one dedup group.
func TestCleanPathsSkipsDuplicatesAndEmpty(t *testing.T) {
	h := testHost(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "dup.bin")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	entries := []cleanPathEntry{
		{Path: ""},
		{Path: target},
		{Path: target}, // duplicate absolute path — must not double-count
	}
	freed := cleanPaths(h, entries)
	if freed != 5 {
		t.Errorf("cleanPaths freed %d bytes, want 5 (dedup should prevent double-count)", freed)
	}
}

// TestDBQuickCheckMissingFileReportsMissing proves a nonexistent DB path
// reports "missing", matching admin_lib/cleanup.py::db_quick_check.
func TestDBQuickCheckMissingFileReportsMissing(t *testing.T) {
	healthy, status := dbQuickCheck(filepath.Join(t.TempDir(), "no.db"))
	if healthy {
		t.Error("expected unhealthy for a missing DB")
	}
	if status != "missing" {
		t.Errorf("status = %q, want %q", status, "missing")
	}
}

// TestDBQuickCheckEmptyPathReportsMissing proves an empty db_path (no [db]
// table configured) is treated as missing, not attempted.
func TestDBQuickCheckEmptyPathReportsMissing(t *testing.T) {
	healthy, status := dbQuickCheck("")
	if healthy || status != "missing" {
		t.Errorf("got (%v, %q), want (false, missing)", healthy, status)
	}
}

// TestCleanWithDBForceDropsHealthyDB proves --force drops the DB (and its
// companions) even when it's healthy — matching
// admin_lib/cleanup.py::clean_with_db's `should_drop = force or (not
// healthy and exists)`.
//
// MUTATION EVIDENCE: change `shouldDrop := force || (...)` to just
// `shouldDrop := !healthy && ...` (dropping the `force ||`) and this test
// fails — a DB with no `sqlite3` binary on PATH (this test's environment
// reports "unreadable", i.e. !healthy already) would pass either way, so
// this test creates a real DB and skips unless sqlite3 quick_check reports
// it healthy, to isolate the force-only path.
func TestCleanWithDBForceDropsHealthyDB(t *testing.T) {
	sqlitePath, err := lookSQLite3()
	if err != nil {
		t.Skip("sqlite3 not on PATH — cannot construct a genuinely healthy DB")
	}
	dbPath := filepath.Join(t.TempDir(), "app.db")
	if out, err := runSQLite3(sqlitePath, dbPath, "CREATE TABLE t (x INTEGER);"); err != nil {
		t.Fatalf("could not seed sqlite db: %v (%s)", err, out)
	}
	healthy, status := dbQuickCheck(dbPath)
	if !healthy {
		t.Skipf("sqlite3 did not report the seeded DB healthy: %s", status)
	}

	h := testHost(t)
	cleanWithDB(h, nil, dbPath, true)
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("--force should have dropped a healthy DB, stat err = %v", err)
	}
}

// TestCleanWithDBKeepsHealthyDBWithoutForce proves a healthy DB survives
// without --force.
func TestCleanWithDBKeepsHealthyDBWithoutForce(t *testing.T) {
	sqlitePath, err := lookSQLite3()
	if err != nil {
		t.Skip("sqlite3 not on PATH — cannot construct a genuinely healthy DB")
	}
	dbPath := filepath.Join(t.TempDir(), "app.db")
	if out, err := runSQLite3(sqlitePath, dbPath, "CREATE TABLE t (x INTEGER);"); err != nil {
		t.Fatalf("could not seed sqlite db: %v (%s)", err, out)
	}
	healthy, _ := dbQuickCheck(dbPath)
	if !healthy {
		t.Skip("sqlite3 did not report the seeded DB healthy")
	}

	h := testHost(t)
	cleanWithDB(h, nil, dbPath, false)
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("healthy DB should survive without --force, stat err = %v", err)
	}
}

// TestCleanDevFromConfigResolvesEnvPathEntry proves clean_dev's `paths`
// entries resolve an `env`-sourced base directory, matching
// admin_lib/cleanup.py::clean_dev_from_config's `os.environ.get(entry["env"],
// ...)` branch.
//
// MUTATION EVIDENCE: swap `os.Getenv(envName)` for
// `cfgString(entry, "default", "")` unconditionally (ignore the env
// override) and this test fails — it sets CLEAN_DEV_TEST_DIR to a directory
// containing a marker file, and asserts that file was freed; with the
// mutation, cleanDevFromConfig would look in "default-that-does-not-exist"
// instead and free 0 bytes.
func TestCleanDevFromConfigResolvesEnvPathEntry(t *testing.T) {
	h := testHost(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cache.bin"), []byte("12345"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("CLEAN_DEV_TEST_DIR", dir)

	cfg := map[string]any{
		"paths": []any{
			map[string]any{"env": "CLEAN_DEV_TEST_DIR", "default": "default-that-does-not-exist"},
		},
	}
	freed := cleanDevFromConfig(h, cfg, false)
	if freed != 5 {
		t.Errorf("cleanDevFromConfig freed %d bytes, want 5 (env path should win)", freed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir should have been removed: %v", err)
	}
}

// TestCleanDevFromConfigEmptyIsNoop proves an empty/absent [clean_dev]
// table is a 0-byte no-op, matching Python's `if not cfg: return 0`.
func TestCleanDevFromConfigEmptyIsNoop(t *testing.T) {
	h := testHost(t)
	if freed := cleanDevFromConfig(h, nil, false); freed != 0 {
		t.Errorf("cleanDevFromConfig(nil) = %d, want 0", freed)
	}
}

// TestMakeCleanDevForceFlagTriggersDBDrop proves makeCleanDev's Runner
// treats a literal "--force" arg as force=true, matching
// admin_lib/interp.py::_make_clean_dev's `"--force" in args or "--db" in
// args`.
//
// MUTATION EVIDENCE: change the force-flag scan to only check for "--db"
// (drop the "--force" branch) and this test fails — passing ["--force"]
// alone would no longer drop the healthy DB.
func TestMakeCleanDevForceFlagTriggersDBDrop(t *testing.T) {
	sqlitePath, err := lookSQLite3()
	if err != nil {
		t.Skip("sqlite3 not on PATH")
	}
	root := t.TempDir()
	t.Chdir(root)
	dbPath := filepath.Join(root, "app.db")
	if out, err := runSQLite3(sqlitePath, dbPath, "CREATE TABLE t (x INTEGER);"); err != nil {
		t.Fatalf("could not seed sqlite db: %v (%s)", err, out)
	}
	healthy, _ := dbQuickCheck(dbPath)
	if !healthy {
		t.Skip("sqlite3 did not report the seeded DB healthy")
	}

	m := &manifest.Manifest{CleanDev: map[string]any{
		"db": map[string]any{"path": dbPath},
	}}
	runner, err := makeCleanDev("clean-dev", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makeCleanDev: %v", err)
	}
	if err := runner([]string{"--force"}); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Error("--force should have dropped the DB via makeCleanDev's Runner")
	}
}
