// hostedptyparity_test.go covers the two admin-3rgp.61 acceptance criteria
// that landed with no test (admin-3rgp.70): the hosted pty's log file must
// match a plain-CLI run of the same Command byte-for-byte (same path, same
// ANSI stripping), and a Command declaring `urls`/`footer` must still emit
// its pinned footer — into the pty, not a second implementation, since the
// design's whole point is that the child treats the pty as its own
// terminal.
//
// Both properties only exist in the pty CHILD, which StartHostedPTY re-execs
// as selfExecutable() with argv ["__run-command", name, args...] — the
// existing hosted-pty tests stub selfExecutable with a bare `#!/bin/sh`
// fixture, which can never write a log file or draw a footer. So these two
// tests stub selfExecutable with a wrapper that re-execs THIS TEST BINARY
// into a guarded helper test (TestHostedPTYChildProcess), the standard
// os/exec "helper process" pattern — that helper calls
// kinds.RunCommandHosted directly, exactly what cmd/root.go's __run-command
// verb does (cmd/root.go:284), so it never needs a real admin.toml on disk.
package kinds

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// hostedParityRun is deliberately a printf writing real ANSI escapes (via
// printf's own \033 octal escape, interpreted by the child shell, not by Go)
// so the log-file assertion actually exercises luahost's ANSI-stripping —
// not just "this file happens to contain no escape codes because nothing
// wrote any".
const hostedParityRun = `printf '\033[31m12:00:00 [ERROR] boom\033[0m\n'`

// hostedParityManifest returns a fresh manifest declaring one shell action
// WITH urls — load-bearing for both tests, not just the footer one:
// internal/kinds/frame.go's enterFrame is the only caller of
// h.SetLogContext for kind="shell" (frame.go:39), so an action with no
// urls/footer never materializes a log file at all.
func hostedParityManifest() *manifest.Manifest {
	return &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"hosted-frame": {
				"kind": "shell",
				"run":  hostedParityRun,
				"urls": []any{"http://127.0.0.1:9999"},
			},
		},
		Custom: map[string]manifest.Command{
			"hosted-frame-cmd": {Steps: []string{"hosted-frame"}},
		},
	}
}

// TestHostedPTYChildProcess is not a real test — it is the "helper process"
// half of the os/exec re-exec pattern the two tests below drive through a
// fixtureAdminScript wrapper. It skips unless ADMIN_TEST_HOSTED_CHILD=1 is
// set (which only the wrapper's exec'd environment sets), so a normal `go
// test ./...` run never executes RunCommandHosted here.
func TestHostedPTYChildProcess(t *testing.T) {
	if os.Getenv("ADMIN_TEST_HOSTED_CHILD") != "1" {
		t.Skip("helper process for the hosted-pty parity tests — not a real test")
	}
	cmdName := os.Getenv("ADMIN_TEST_HOSTED_CMD")
	if cmdName == "" {
		t.Fatal("ADMIN_TEST_HOSTED_CMD not set")
	}
	if err := RunCommandHosted(hostedParityManifest(), Default, cmdName, nil); err != nil {
		t.Fatalf("RunCommandHosted(%q): %v", cmdName, err)
	}
}

// hostedChildTestBin resolves the path to the currently-running test binary
// — captured before any t.Chdir, per this test file's own known risk: if
// os.Executable() ever returned something unusable under this harness, the
// whole re-exec approach would stall silently, so a bad value is reported
// loudly here instead.
func hostedChildTestBin(t *testing.T) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v — the hosted-pty parity tests need to re-exec this test binary as the pty's child", err)
	}
	return bin
}

// stubSelfExecutableToChildHarness points selfExecutable at a fixture
// script that execs testBin back into TestHostedPTYChildProcess — argv is
// ignored (fixtureAdminScript's own contract), so hostedReentryArgs never
// reaches the wrapper at all; the child instead learns which command to run
// from ADMIN_TEST_HOSTED_CMD, which the caller must include in env.
func stubSelfExecutableToChildHarness(t *testing.T, testBin string) {
	t.Helper()
	stubSelfExecutable(t, fixtureAdminScript(t,
		`exec "`+testBin+`" -test.run '^TestHostedPTYChildProcess$'`,
	))
}

// runHostedFixture dispatches cmdName through StartHostedPTY against m and
// waits (up to 10s) for its entry — keyed on the command's first step name,
// per StartHostedPTY's own doc comment — to report StateExited, going
// through livework.Default.State rather than reading an Entry's State field
// directly (the field is mutated under the registry's own lock by the wait
// goroutine's SetState call). On StateFailed it dumps the child's own ring
// output into t.Fatalf, since a failing hosted child's only visible signal
// is otherwise an opaque "reached StateFailed".
func runHostedFixture(t *testing.T, m *manifest.Manifest, cmdName string, env []string) string {
	t.Helper()
	cmd, ok := resolveCommand(m, cmdName)
	if !ok || len(cmd.Steps) == 0 {
		t.Fatalf("fixture manifest declares no steps for command %q", cmdName)
	}
	entryID := cmd.Steps[0]

	if err := StartHostedPTY(m, cmdName, nil, 24, 80, env); err != nil {
		t.Fatalf("StartHostedPTY(%q): %v", cmdName, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state, ok := livework.Default.State(entryID)
		if ok {
			switch state {
			case livework.StateExited:
				return entryID
			case livework.StateFailed:
				var ring []byte
				if e, ok := livework.Default.Get(entryID); ok && e.Output != nil {
					ring = e.Output.Bytes()
				}
				t.Fatalf("hosted child %q reported StateFailed — its own output:\n%s", entryID, ring)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("hosted child %q never reported exited within the deadline", entryID)
	return ""
}

// TestHostedPTYLogFileMatchesAPlainCLIRun is admin-3rgp.70's first gap: a
// Command dispatched onto the hosted pty must write its log file at the
// same path and in the same format — timestamping and ANSI stripping — as a
// plain-CLI run of the same Command.
//
// The plain-CLI leg runs in-process via RunCommand (dispatch.go:315's
// documented "plain-CLI entry point"); stdout is not a tty there, so
// frame.Activate declines, but enterFrame already called h.SetLogContext
// and h.MaterializeLogFile() before that decision is even made — so the log
// file is still written. That asymmetry (log file: always; footer: only on
// a real terminal) is exactly what this test pins.
//
// MUTATION EVIDENCE (run and reverted, see the commit this test landed in):
//
//   - Dropping the `plain := ansiEscape.ReplaceAllString(...)` strip in
//     internal/luahost/host.go's logWrite fails both the "no raw ESC byte"
//     assertion and the byte-equality assertion below.
//   - Deleting the `h.SetLogContext` call in internal/kinds/frame.go's
//     enterFrame fails this test with "hosted leg produced 0 new log
//     files" — no hosted log file at all.
func TestHostedPTYLogFileMatchesAPlainCLIRun(t *testing.T) {
	testBin := hostedChildTestBin(t)

	dir := t.TempDir()
	t.Chdir(dir)

	m := hostedParityManifest()

	if err := RunCommand(m, Default, "hosted-frame-cmd", nil); err != nil {
		t.Fatalf("RunCommand (plain-CLI leg): %v", err)
	}
	plainLogs, err := filepath.Glob(filepath.Join(dir, "tmp", "hosted-frame.*.log"))
	if err != nil {
		t.Fatalf("globbing plain-leg logs: %v", err)
	}
	if len(plainLogs) != 1 {
		t.Fatalf("plain-CLI leg produced %d log files, want 1: %v", len(plainLogs), plainLogs)
	}
	plainContent, err := os.ReadFile(plainLogs[0])
	if err != nil {
		t.Fatalf("reading plain-leg log %s: %v", plainLogs[0], err)
	}

	// luahost's materializeLogContext (internal/luahost/host.go:159) names a
	// stem-based log file with a SECOND-granularity timestamp
	// ("2006-01-02_150405") and no other disambiguator. Two dispatches of
	// the same action stem inside the same wall-clock second therefore
	// collide on the identical filename and the second overwrites the
	// first — a real defect independent of this test, worth its own
	// ticket. Sleeping past the second boundary here keeps this test's own
	// "two distinct files" assertion meaningful rather than flaking on
	// which side of a second boundary the run landed.
	time.Sleep(1100 * time.Millisecond)

	stubSelfExecutableToChildHarness(t, testBin)
	childEnv := []string{
		"ADMIN_TEST_HOSTED_CHILD=1",
		"ADMIN_TEST_HOSTED_CMD=hosted-frame-cmd",
	}
	entryID := runHostedFixture(t, m, "hosted-frame-cmd", childEnv)
	t.Cleanup(func() {
		if e, ok := livework.Default.Get(entryID); ok && e.Ops.Kill != nil {
			_ = e.Ops.Kill()
		}
	})

	hostedLogs, err := filepath.Glob(filepath.Join(dir, "tmp", "hosted-frame.*.log"))
	if err != nil {
		t.Fatalf("globbing hosted-leg logs: %v", err)
	}
	var newLogs []string
	for _, p := range hostedLogs {
		if p != plainLogs[0] {
			newLogs = append(newLogs, p)
		}
	}
	if len(newLogs) != 1 {
		t.Fatalf("hosted leg produced %d new log files, want 1: %v", len(newLogs), newLogs)
	}
	hostedLogPath := newLogs[0]

	if gotDir, wantDir := filepath.Dir(hostedLogPath), filepath.Dir(plainLogs[0]); gotDir != wantDir {
		t.Errorf("hosted log dir = %q, want %q (same as the plain-CLI run)", gotDir, wantDir)
	}
	gotStem := strings.SplitN(filepath.Base(hostedLogPath), ".", 2)[0]
	wantStem := strings.SplitN(filepath.Base(plainLogs[0]), ".", 2)[0]
	if gotStem != wantStem {
		t.Errorf("hosted log stem = %q, want %q", gotStem, wantStem)
	}

	hostedContent, err := os.ReadFile(hostedLogPath)
	if err != nil {
		t.Fatalf("reading hosted-leg log %s: %v", hostedLogPath, err)
	}
	if bytes.IndexByte(hostedContent, 0x1b) != -1 {
		t.Errorf("hosted log %s contains a raw ESC byte — ANSI stripping did not happen: %q", hostedLogPath, hostedContent)
	}
	if !bytes.Equal(plainContent, hostedContent) {
		t.Errorf("hosted log content differs from the plain-CLI run's log:\nplain:  %q\nhosted: %q", plainContent, hostedContent)
	}
}

// TestHostedPTYFooterIsWrittenIntoThePTY is admin-3rgp.70's second gap: a
// Command declaring `urls` (or `footer`) must still produce its pinned
// footer, written into the pty, even though no second footer implementation
// exists for the hosted path — the design turns on the child treating the
// pty as its own terminal and emitting its own scroll region and footer
// there (admin-3rgp.61's design note).
//
// MUTATION EVIDENCE (run and reverted, see the commit this test landed in):
//
//   - Removing the `urls` key from hostedParityManifest's action (so
//     frame.ShouldActivate sees zero resolved URLs and no footer key) fails
//     every assertion below while TestStartHostedPTYRegistersALiveEntry and
//     its siblings stay green — the entry still runs, it just carries no
//     footer.
//   - Making internal/kinds/frame.go's enterFrame return nil
//     unconditionally produces the same failure.
func TestHostedPTYFooterIsWrittenIntoThePTY(t *testing.T) {
	testBin := hostedChildTestBin(t)
	// Explicit rather than assumed: frame.Activate declines outright when
	// ADMIN_NO_FRAME is set to anything non-empty (frame.go:296), and this
	// test's whole point is that Activate succeeds.
	t.Setenv("ADMIN_NO_FRAME", "")

	dir := t.TempDir()
	t.Chdir(dir)

	m := hostedParityManifest()

	stubSelfExecutableToChildHarness(t, testBin)
	childEnv := []string{
		"ADMIN_TEST_HOSTED_CHILD=1",
		"ADMIN_TEST_HOSTED_CMD=hosted-frame-cmd",
	}
	entryID := runHostedFixture(t, m, "hosted-frame-cmd", childEnv)
	t.Cleanup(func() {
		if e, ok := livework.Default.Get(entryID); ok && e.Ops.Kill != nil {
			_ = e.Ops.Kill()
		}
	})

	e, ok := livework.Default.Get(entryID)
	if !ok || e.Output == nil {
		t.Fatal("no live-work entry (or no Output ring) for the hosted run")
	}
	ring := e.Output.Bytes()

	logs, err := filepath.Glob(filepath.Join(dir, "tmp", "hosted-frame.*.log"))
	if err != nil {
		t.Fatalf("globbing hosted-leg logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("hosted leg produced %d log files, want 1: %v", len(logs), logs)
	}
	logBase := filepath.Base(logs[0])

	// Scroll region (DECSTBM): "\x1b[1;Nr". N (the region's bottom row) is
	// asserted loosely — StartHostedPTY's frame.SetWinsize on the master
	// races the child's own frame reading its winsize at Activate, so the
	// exact row count can flake; the shape is what's load-bearing.
	if !regexp.MustCompile(`\x1b\[1;\d+r`).Match(ring) {
		t.Errorf("pty output missing a DECSTBM scroll-region sequence (\\x1b[1;Nr): %q", ring)
	}
	// A footer repaint: cursor-address to a row, then clear-line.
	if !regexp.MustCompile(`\x1b\[\d+;1H\x1b\[2K`).Match(ring) {
		t.Errorf("pty output missing a footer repaint sequence (cursor-address + clear-line): %q", ring)
	}
	// The status row's label.
	if !bytes.Contains(ring, []byte("hosted-frame")) {
		t.Errorf("pty output missing the status row's label %q: %q", "hosted-frame", ring)
	}
	// The log row: an OSC 8 file:// hyperlink naming the actual log file
	// this same run just wrote (not merely "some file:// link exists").
	if !bytes.Contains(ring, []byte("\x1b]8;;file://")) {
		t.Errorf("pty output missing the log row's OSC 8 file hyperlink: %q", ring)
	}
	if !bytes.Contains(ring, []byte(logBase)) {
		t.Errorf("pty output's file hyperlink doesn't name this run's own log file %q: %q", logBase, ring)
	}
	// One row per declared URL.
	if !bytes.Contains(ring, []byte("127.0.0.1:9999")) {
		t.Errorf("pty output missing the declared URL row: %q", ring)
	}
}
