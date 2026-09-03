package luahost

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func newTestLogContext(t *testing.T) (*LogContext, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	c := NewLogContext(dir)
	var errOut bytes.Buffer
	c.Stderr = &errOut
	return c, &errOut
}

// TestDeferredNamingCreatesNothing proves naming is deferred: SetLogContext
// alone must not touch disk, and LogFile() (which does not count as a use)
// must not either.
func TestDeferredNamingCreatesNothing(t *testing.T) {
	c, _ := newTestLogContext(t)
	c.SetLogContext([]string{"dev"}, RetainUnset)
	if c.LogFile() != "" {
		t.Fatalf("LogFile() should be empty before any use, got %q", c.LogFile())
	}
	if _, err := os.Stat(c.LogDir); err == nil {
		t.Fatal("LogDir should not exist yet — nothing has been created")
	}
}

// TestMaterializeSettlesNameAndCreatesExactlyOneFile proves that asking for
// the path (MaterializeLogFile) is itself a "use" that settles the name and
// creates exactly one file.
func TestMaterializeSettlesNameAndCreatesExactlyOneFile(t *testing.T) {
	c, errOut := newTestLogContext(t)
	c.SetLogContext([]string{"dev"}, RetainUnset)
	path := c.MaterializeLogFile()
	if path == "" {
		t.Fatal("MaterializeLogFile should settle and return a path")
	}
	if !strings.HasPrefix(filepath.Base(path), "dev.") {
		t.Fatalf("path = %q, want stem \"dev.\"", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("materialized file should exist on disk: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(c.LogDir, "dev.*.log"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 file, got %d: %v", len(matches), matches)
	}
	if !strings.Contains(errOut.String(), LogAnnouncePrefix) {
		t.Fatalf("expected the announce prefix on Stderr, got %q", errOut.String())
	}
	if got, ok := ParseLogAnnounce(errOut.String()); !ok || got != path {
		t.Fatalf("ParseLogAnnounce(%q) = %q, %v; want %q, true", errOut.String(), got, ok, path)
	}
}

// TestSecondSetLogContextIsNoop proves the one-file-per-invocation
// guarantee: once the file exists, a later SetLogContext call (a second
// step's own naming) must not repoint the log.
func TestSecondSetLogContextIsNoop(t *testing.T) {
	c, _ := newTestLogContext(t)
	c.SetLogContext([]string{"dev"}, RetainUnset)
	first := c.MaterializeLogFile()
	if first == "" {
		t.Fatal("expected a materialized file")
	}
	c.SetLogContext([]string{"something-else"}, RetainUnset)
	second := c.MaterializeLogFile()
	if second != first {
		t.Fatalf("a second SetLogContext after materialization repointed the log: %q -> %q", first, second)
	}
}

// TestNestedNamingInnermostWins proves nested naming: parts join with "-"
// and the LAST SetLogContext call before first use wins — the innermost
// caller, since it runs closer to the actual write.
func TestNestedNamingInnermostWins(t *testing.T) {
	c, _ := newTestLogContext(t)
	c.SetLogContext([]string{"dev"}, RetainUnset)       // outer: the command
	c.SetLogContext([]string{"dev", "cf"}, RetainUnset) // inner: the picked target
	path := c.MaterializeLogFile()
	if !strings.HasPrefix(filepath.Base(path), "dev-cf.") {
		t.Fatalf("path = %q, want stem \"dev-cf.\" (innermost name should win)", path)
	}
}

// TestNestedNamingJoinsAtThreeLevels proves arbitrary nesting depth: a
// three-level path joins to "a-b-c" with no special case.
func TestNestedNamingJoinsAtThreeLevels(t *testing.T) {
	c, _ := newTestLogContext(t)
	c.SetLogContext([]string{"a", "b", "c"}, RetainUnset)
	path := c.MaterializeLogFile()
	if !strings.HasPrefix(filepath.Base(path), "a-b-c.") {
		t.Fatalf("path = %q, want stem \"a-b-c.\"", path)
	}
}

// TestRetainDefaultPrunesOldest proves the default retain of 5 prunes
// older same-stem logs beyond that count.
func TestRetainDefaultPrunesOldest(t *testing.T) {
	c, _ := newTestLogContext(t)
	if err := os.MkdirAll(c.LogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := []string{
		"dev.2026-01-01_100000.log",
		"dev.2026-01-02_100000.log",
		"dev.2026-01-03_100000.log",
		"dev.2026-01-04_100000.log",
		"dev.2026-01-05_100000.log",
		"dev.2026-01-06_100000.log",
	}
	for _, name := range existing {
		if err := os.WriteFile(filepath.Join(c.LogDir, name), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// retain is RetainUnset -> falls back to the configured default (5).
	c.SetLogContext([]string{"dev"}, RetainUnset)
	c.MaterializeLogFile()

	matches, _ := filepath.Glob(filepath.Join(c.LogDir, "dev.*.log"))
	sort.Strings(matches)
	if len(matches) != 5 {
		t.Fatalf("expected 5 files after default pruning (6 pre-existing + 1 new, pruned to 5), got %d: %v", len(matches), matches)
	}
	// The two oldest pre-existing files must be gone.
	for _, name := range existing[:2] {
		if _, err := os.Stat(filepath.Join(c.LogDir, name)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be pruned", name)
		}
	}
}

// TestRetainZeroDoesNotPrune proves an EXPLICIT retain of 0 disables
// pruning entirely (distinct from RetainUnset, which uses the default).
func TestRetainZeroDoesNotPrune(t *testing.T) {
	c, _ := newTestLogContext(t)
	if err := os.MkdirAll(c.LogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := []string{
		"dev.2026-01-01_100000.log",
		"dev.2026-01-02_100000.log",
	}
	for _, name := range existing {
		if err := os.WriteFile(filepath.Join(c.LogDir, name), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c.SetLogContext([]string{"dev"}, 0)
	c.MaterializeLogFile()

	matches, _ := filepath.Glob(filepath.Join(c.LogDir, "dev.*.log"))
	if len(matches) != 3 {
		t.Fatalf("expected 3 files (no pruning with explicit retain=0), got %d: %v", len(matches), matches)
	}
}

// TestPathContextNumberedRotation proves set_log_context_path uses numbered
// rotation (.1, .2, ...) rather than timestamped pruning.
func TestPathContextNumberedRotation(t *testing.T) {
	dir := t.TempDir()
	c := NewLogContext(dir)
	c.Stderr = &bytes.Buffer{}
	path := filepath.Join(dir, "run.log")

	// First run: nothing to rotate, just creates run.log.
	c.SetLogContextPath("run.log", RetainUnset)
	got := c.MaterializeLogFile()
	if got != path {
		t.Fatalf("MaterializeLogFile() = %q, want %q", got, path)
	}
	if err := os.WriteFile(path, []byte("run 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Second run: rotates run.log -> run.log.1.
	c2 := NewLogContext(dir)
	c2.Stderr = &bytes.Buffer{}
	c2.SetLogContextPath("run.log", RetainUnset)
	c2.MaterializeLogFile()
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected %s.1 after second run's rotation: %v", path, err)
	}
	data, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "run 1\n" {
		t.Fatalf("run.log.1 = %q, want the first run's content", string(data))
	}
}

// TestSetLogContextPathDeferredAndOneFile proves the path variant is
// deferred and creates exactly one file per invocation, same as the stem
// variant.
func TestSetLogContextPathDeferredAndOneFile(t *testing.T) {
	c, _ := newTestLogContext(t)
	c.SetLogContextPath("custom/run.log", RetainUnset)
	if c.LogFile() != "" {
		t.Fatal("expected deferred naming — nothing materialized yet")
	}
	c.Write("line one")
	c.Write("line two")
	path := c.LogFile()
	if path == "" {
		t.Fatal("expected a materialized path after the first write")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "line one") || !strings.Contains(got, "line two") {
		t.Fatalf("log file missing entries: %q", got)
	}
	matches, _ := filepath.Glob(path + "*")
	// Exactly one file with no .1/.2 rotation siblings on a first run.
	count := 0
	for _, m := range matches {
		if m == path {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 file at %q, matches: %v", path, matches)
	}
}

// TestConfigureAppliesLoggingTable proves Configure applies [logging]
// dir/enabled/retain overrides.
func TestConfigureAppliesLoggingTable(t *testing.T) {
	root := t.TempDir()
	c := NewLogContext(root)
	c.Stderr = &bytes.Buffer{}
	c.Configure(root, map[string]any{"dir": "logs", "retain": int64(2)})
	if want := filepath.Join(root, "logs"); c.LogDir != want {
		t.Fatalf("LogDir = %q, want %q", c.LogDir, want)
	}

	// Pre-seed 3 files, then materialize a 4th with the configured
	// default retain (2) applied via RetainUnset.
	if err := os.MkdirAll(c.LogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dev.2026-01-01_100000.log", "dev.2026-01-02_100000.log", "dev.2026-01-03_100000.log"} {
		if err := os.WriteFile(filepath.Join(c.LogDir, name), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c.SetLogContext([]string{"dev"}, RetainUnset)
	c.MaterializeLogFile()
	matches, _ := filepath.Glob(filepath.Join(c.LogDir, "dev.*.log"))
	if len(matches) != 2 {
		t.Fatalf("expected 2 files after pruning to configured retain=2, got %d: %v", len(matches), matches)
	}
}

// TestConfigureDisablesLogging proves `[logging] enabled = false` blocks
// SetLogContext entirely.
func TestConfigureDisablesLogging(t *testing.T) {
	root := t.TempDir()
	c := NewLogContext(root)
	c.Stderr = &bytes.Buffer{}
	c.Configure(root, map[string]any{"enabled": false})
	c.SetLogContext([]string{"dev"}, RetainUnset)
	if path := c.MaterializeLogFile(); path != "" {
		t.Fatalf("expected no log file when [logging] enabled = false, got %q", path)
	}
}

// TestSetEnabledFalseBlocksLogging proves SetEnabled(false) — the
// per-command `log = false` path — blocks SetLogContext exactly like a
// disabled [logging] config.
func TestSetEnabledFalseBlocksLogging(t *testing.T) {
	c, _ := newTestLogContext(t)
	c.SetEnabled(false)
	c.SetLogContext([]string{"dev"}, RetainUnset)
	if path := c.MaterializeLogFile(); path != "" {
		t.Fatalf("expected no log file with logging disabled, got %q", path)
	}
}

// TestParseLogAnnounceTolerant proves ParseLogAnnounce tolerates ANSI
// escapes and trailing control bytes.
func TestParseLogAnnounceTolerant(t *testing.T) {
	line := "\x1b[2m" + LogAnnouncePrefix + "/tmp/x/dev.2026-01-01_100000.log\x1b[0m\r\n"
	path, ok := ParseLogAnnounce(line)
	if !ok {
		t.Fatal("expected ParseLogAnnounce to find the prefix")
	}
	if path != "/tmp/x/dev.2026-01-01_100000.log" {
		t.Fatalf("path = %q", path)
	}
}

// TestParseLogAnnounceAbsent proves a line with no announce prefix returns
// ok=false rather than a garbage match.
func TestParseLogAnnounceAbsent(t *testing.T) {
	if _, ok := ParseLogAnnounce("just some regular output\n"); ok {
		t.Fatal("expected ok=false for a line with no announce prefix")
	}
}

// TestAnnounceEndPrintsOnceAfterMaterialize proves AnnounceEnd (the "→
// wrote:" line at the end of a run, admin-krvc.1) prints the materialized
// path once a context has actually been used — the Go port of
// admin_lib/core.py's _announce_log_end.
func TestAnnounceEndPrintsOnceAfterMaterialize(t *testing.T) {
	c, errOut := newTestLogContext(t)
	c.SetLogContext([]string{"dev"}, RetainUnset)
	path := c.MaterializeLogFile()
	errOut.Reset() // drop the start-of-run announce materialize already wrote
	c.AnnounceEnd()
	if !strings.Contains(errOut.String(), LogAnnouncePrefix) {
		t.Fatalf("expected the announce prefix on Stderr, got %q", errOut.String())
	}
	if got, ok := ParseLogAnnounce(errOut.String()); !ok || got != path {
		t.Fatalf("ParseLogAnnounce(%q) = %q, %v; want %q, true", errOut.String(), got, ok, path)
	}
}

// TestAnnounceEndNoopWithoutMaterialize proves a context that never
// materialized a file (e.g. a `branch`/`worktree` verb dispatch, which
// never touches logging) prints nothing.
func TestAnnounceEndNoopWithoutMaterialize(t *testing.T) {
	c, errOut := newTestLogContext(t)
	c.AnnounceEnd()
	if errOut.Len() != 0 {
		t.Fatalf("expected no output, got %q", errOut.String())
	}
}

// TestAnnounceEndIsIdempotent proves a second AnnounceEnd call — the
// explicit call plus a hypothetical safety-net call racing it, mirroring
// Python's flag-consuming _announce_log_end — does not print twice.
func TestAnnounceEndIsIdempotent(t *testing.T) {
	c, errOut := newTestLogContext(t)
	c.SetLogContext([]string{"dev"}, RetainUnset)
	c.MaterializeLogFile()
	errOut.Reset()
	c.AnnounceEnd()
	first := errOut.String()
	c.AnnounceEnd()
	if errOut.String() != first {
		t.Fatalf("expected no additional output on second call, got %q after %q", errOut.String(), first)
	}
}
