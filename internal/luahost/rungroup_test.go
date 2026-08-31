package luahost

import (
	"strings"
	"testing"
	"time"
)

// TestRunGroupTagsEachProcessOutput proves run_group runs several commands
// concurrently and prefixes each one's output with its own tag — the shape
// forge-engine's workspace dev loop needs (N sample-game processes, each
// line labelled by which one produced it).
func TestRunGroupTagsEachProcessOutput(t *testing.T) {
	h, out, _ := newTestHost(t)
	err := h.Exec(`
local code = run_group({
  {tag = "one", cmd = "echo hello-one"},
  {tag = "two", cmd = "echo hello-two"},
})
info("exit=" .. tostring(code))
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "[one] hello-one") {
		t.Fatalf("expected tagged output for 'one', got %q", got)
	}
	if !strings.Contains(got, "[two] hello-two") {
		t.Fatalf("expected tagged output for 'two', got %q", got)
	}
	if !strings.Contains(got, "exit=0") {
		t.Fatalf("expected exit=0 for two successful commands, got %q", got)
	}
}

// TestRunGroupReturnsHighestExitCode proves the aggregate exit code is the
// max across every child, matching the Python original's sys.exit(max(rcs)).
//
// MUTATION EVIDENCE: change RunGroup's `if r.code > maxCode` to `if false`
// (never updating maxCode), and this test fails: exit=0 is reported instead
// of exit=3, since the failing child's code 3 is never folded in.
func TestRunGroupReturnsHighestExitCode(t *testing.T) {
	h, out, _ := newTestHost(t)
	err := h.Exec(`
local code = run_group({
  {tag = "good", cmd = "exit 0"},
  {tag = "bad", cmd = "exit 3"},
})
info("exit=" .. tostring(code))
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "exit=3") {
		t.Fatalf("expected exit=3 (the failing child's code), got %q", out.String())
	}
}

// TestRunGroupWithNoCommandsReturnsZero is the degenerate case: no crash,
// exit code 0.
func TestRunGroupWithNoCommandsReturnsZero(t *testing.T) {
	h, out, _ := newTestHost(t)
	err := h.Exec(`
local code = run_group({})
info("exit=" .. tostring(code))
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "exit=0") {
		t.Fatalf("expected exit=0 for an empty group, got %q", out.String())
	}
}

// TestRunGroupDoesNotHangPastCompletion is a coarse regression guard: two
// quick commands must not block run_group beyond a couple of seconds (a
// deadlocked wait-group or an un-closed done channel would hang forever).
func TestRunGroupDoesNotHangPastCompletion(t *testing.T) {
	h, _, _ := newTestHost(t)
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- h.Exec(`run_group({{tag="a", cmd="true"}, {tag="b", cmd="true"}})`, nil)
	}()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run_group did not return within 5s for two trivially-successful commands")
	}
}
