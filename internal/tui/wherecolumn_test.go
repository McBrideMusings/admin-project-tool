package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// admin-3rgp.64: the where-column replaces a column that used to print
// wd.live.Handle unconditionally — false for every hosted (Via-empty)
// entry, which is most of them since admin-3rgp.61. These tests pin the
// two values it may now show ("hosted" for a hosted-pty entry, the launcher
// handle for a launcher-dispatched one) and that a hosted row and a
// detached row can never show each other's value.

// TestWhereColumnHostedCommandRowShowsHostedNeverPane covers the "hosted
// running command renders running, its activity, and hosted" AC and its
// negative half: a hosted row must never render a pane identifier.
func TestWhereColumnHostedCommandRowShowsHostedNeverPane(t *testing.T) {
	entry := &livework.Entry{
		ID: "docs", Name: "docs", State: livework.StateRunning,
		Activity: "vitepress dev", Handle: "pty-1", StartedAt: time.Now(),
	}
	// renderWorkRow now reads state through livework.Default.State(e.ID)
	// (admin-ddc8), so the entry must actually be in the process-wide
	// registry rather than merely constructed. Reset to StateExited after so
	// this hosted-and-running row doesn't linger for later tests, e.g.
	// TestRequestQuitWithNothingHostedQuitsImmediately.
	if err := livework.Default.Add(entry); err != nil {
		t.Fatalf("Add(%s): %v", entry.ID, err)
	}
	t.Cleanup(func() {
		livework.Default.SetState(entry.ID, livework.StateExited, nil)
	})
	live := &workRowData{cmdName: "docs", live: entry}
	line := workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: live}, false, 60)
	plain := stripANSI(line)

	if !strings.Contains(plain, "running") {
		t.Errorf("hosted running row missing state word %q: %q", "running", plain)
	}
	if !strings.Contains(plain, "vitepress dev") {
		t.Errorf("hosted running row missing activity: %q", plain)
	}
	if !strings.Contains(plain, "hosted") {
		t.Errorf("hosted running row missing %q: %q", "hosted", plain)
	}
	if strings.Contains(plain, "pane") {
		t.Errorf("hosted row rendered a pane identifier, want none: %q", plain)
	}
}

// TestWhereColumnDetachedWorkerRowShowsPaneNeverHosted covers "a detached
// worker renders its state, its activity, and its pane identifier" and the
// negative half: a detached row must never render the word "hosted".
func TestWhereColumnDetachedWorkerRowShowsPaneNeverHosted(t *testing.T) {
	worker := &livework.Entry{
		ID: "test-wherecol-detached-worker", Name: "test-wherecol-detached-worker",
		State: livework.StateWaiting, Activity: "awaiting approval", Handle: "pane 3", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "some-branch", Worktree: "/tmp/some-branch"},
	}
	line := workTestModel(nil).renderWorkRow(rowItem{kind: rowWorker, work: &workRowData{worker: worker}}, false, 60)
	plain := stripANSI(line)

	if !strings.Contains(plain, "waiting") {
		t.Errorf("detached worker row missing state word %q: %q", "waiting", plain)
	}
	if !strings.Contains(plain, "awaiting approval") {
		t.Errorf("detached worker row missing activity: %q", plain)
	}
	if !strings.Contains(plain, "pane 3") {
		t.Errorf("detached worker row missing its pane id: %q", plain)
	}
	if strings.Contains(plain, "hosted") {
		t.Errorf("detached row rendered %q, want never: %q", "hosted", plain)
	}
}

// TestWhereColumnStateWordsAreVerbatim pins running/waiting/failed exactly
// as written — no abbreviation, no different casing.
func TestWhereColumnStateWordsAreVerbatim(t *testing.T) {
	cases := []struct {
		state livework.State
		word  string
	}{
		{livework.StateRunning, "running"},
		{livework.StateWaiting, "waiting"},
		{livework.StateFailed, "failed"},
	}
	entry := &livework.Entry{
		ID: "wherecol-state-words", Name: "build",
		Activity: "doing the thing", Handle: "pty-2", StartedAt: time.Now(),
	}
	// See TestWhereColumnHostedCommandRowShowsHostedNeverPane: renderWorkRow
	// reads state through the registry now, so the entry must be Added, and
	// each case's state must go through SetState rather than a fresh struct.
	if err := livework.Default.Add(entry); err != nil {
		t.Fatalf("Add(%s): %v", entry.ID, err)
	}
	t.Cleanup(func() {
		livework.Default.SetState(entry.ID, livework.StateExited, nil)
	})
	live := &workRowData{cmdName: "build", live: entry}
	for _, c := range cases {
		livework.Default.SetState(entry.ID, c.state, nil)
		line := stripANSI(workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: live}, false, 60))
		if !strings.Contains(line, c.word) {
			t.Errorf("state %v did not render verbatim word %q: %q", c.state, c.word, line)
		}
	}
}

// TestWhereColumnTruncatesActivityKeepsWherewholeAt covers "a long activity
// truncates with an ellipsis while the where-value stays whole" — the
// where-value is reserved first and never passed through truncR.
func TestWhereColumnTruncatesActivityKeepsWherewholeAt(t *testing.T) {
	longActivity := strings.Repeat("writing a very long activity line ", 4)
	live := &workRowData{
		cmdName: "build",
		live: &livework.Entry{
			ID: "build", Name: "build", State: livework.StateRunning,
			Activity: longActivity, Handle: "pane 12345", Via: "terminal", StartedAt: time.Now(),
		},
	}
	line := stripANSI(workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: live}, false, 40))
	if !strings.Contains(line, "…") {
		t.Errorf("long activity at a narrow width did not truncate with an ellipsis: %q", line)
	}
	if !strings.HasSuffix(line, "pane 12345") {
		t.Errorf("where-value was not left whole at the end of the row: %q", line)
	}
}

// TestWhereColumnIdleRowUnchanged is the "an idle command row is
// byte-identical to today's" AC: a row with no live entry must render
// exactly as it did before this change — kind + description, nothing else.
func TestWhereColumnIdleRowUnchanged(t *testing.T) {
	idle := &workRowData{cmdName: "build", cmdDesc: "compile the binary", cmdKind: "shell"}
	got := workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: idle}, false, 60)

	marker := faintStyle.Render("◇ ")
	name := padEnd(truncR("build", 16), 16)
	kind := padEnd(truncR("shell", 9), 9)
	desc := truncR("compile the binary", maxi(60-2-16-9, 4))
	want := marker + name + faintStyle.Render(kind+desc)

	if got != want {
		t.Errorf("idle row changed shape:\n got  %q\n want %q", got, want)
	}
}

// TestWhereColumnFrameAtMultipleWidths is the "asserted through the
// dashboard's headless frame renderer at more than one width" AC: seeds one
// hosted-pty (Via empty) entry and one launcher-dispatched (Via set) worker
// entry into the process-wide registry and asserts each row's where-value at two
// different widths. livework.Default is a process-wide singleton shared
// with every other test in this package, so IDs/names here are unique and
// the assertions only ever look at these two rows, never at the absence of
// anything else.
func TestWhereColumnFrameAtMultipleWidths(t *testing.T) {
	const cmdName = "wherecolfixturehosted"
	dir := t.TempDir()
	toml := "project_name = \"fixture\"\n\n[commands." + cmdName + "]\ndesc = \"fixture\"\nsteps = [\"" + cmdName + "\"]\n\n[actions." + cmdName + "]\nkind = \"shell\"\nrun = \"true\"\n"
	if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte(toml), 0o644); err != nil {
		t.Fatalf("writing fixture admin.toml: %v", err)
	}

	hosted := &livework.Entry{
		ID: cmdName, Name: cmdName, State: livework.StateRunning,
		Activity: "running the fixture", Handle: "pty-wherecol-fixture", StartedAt: time.Now(),
	}
	if err := livework.Default.Add(hosted); err != nil {
		t.Fatalf("Add hosted: %v", err)
	}

	detached := &livework.Entry{
		ID: "wherecolfixturedetachedworker", Name: "wherecolfixturedetachedworker",
		State: livework.StateWaiting, Activity: "needs input", Handle: "pane 7", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "wherecol-branch", Worktree: "/tmp/wherecol-branch"},
	}
	if err := livework.Default.Add(detached); err != nil {
		t.Fatalf("Add detached: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, width := range []int{100, 140} {
		m := New(ctx, dir, "owner/repo", "main", "main")
		m.herdr = false

		out := Frame(ctx, m, FrameOptions{
			Width: width, Height: 30,
			Settle: 200 * time.Millisecond,
		})
		plain := stripANSI(out)

		if !strings.Contains(plain, "hosted") {
			t.Errorf("width %d: frame missing %q for the hosted entry: %q", width, "hosted", plain)
		}
		if !strings.Contains(plain, "pane 7") {
			t.Errorf("width %d: frame missing the detached worker's pane id: %q", width, plain)
		}
	}
}
