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

// admin-3rgp.64 gave a running Work-tab row a where-column: "hosted" for a
// hosted-pty entry, the launcher handle for a launcher-dispatched one.
// admin-3rgp.72.11 ported the Commands section (flat commands and targets)
// onto the Dock design's fixed column grid, whose running-row shape is
// marker+name+state+elapsed+activity with no room left for a where-value
// (docs/spikes/admin-tui-desktop/commands-running.txt:15 shows no such
// text) — so the where-column is gone from THAT section's rows. It survives
// unchanged on the Workers section below (out of scope for that ticket;
// admin-3rgp.72.16 owns porting Workers onto this same grid) — the tests
// below that still exercise a rowWorker row are untouched.

// TestCommandRowRunningShowsNoWhereColumn pins the Commands section's own
// running-row shape: state, elapsed and activity, and never a where-value —
// replacing TestWhereColumnHostedCommandRowShowsHostedNeverPane, which pinned
// the opposite (a "hosted" marker) before this ticket removed the column
// from command rows.
func TestCommandRowRunningShowsNoWhereColumn(t *testing.T) {
	freshLivework(t)
	entry := &livework.Entry{
		ID: "docs", Name: "docs", State: livework.StateRunning,
		Activity: "vitepress dev", Handle: "pty-1", StartedAt: time.Now(),
	}
	// renderWorkRow now reads state through livework.Default.State(e.ID)
	// (admin-ddc8), so the entry must actually be in the process-wide
	// registry rather than merely constructed. freshLivework(t) above gives
	// this test its own registry, so this entry can never linger for a later
	// test, e.g. TestRequestQuitWithNothingHostedQuitsImmediately.
	if err := livework.Default.Add(entry); err != nil {
		t.Fatalf("Add(%s): %v", entry.ID, err)
	}
	live := &workRowData{cmdName: "docs", live: entry}
	line := workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: live}, false, 60)
	plain := stripANSI(line)

	if !strings.Contains(plain, "running") {
		t.Errorf("hosted running row missing state word %q: %q", "running", plain)
	}
	if !strings.Contains(plain, "vitepress dev") {
		t.Errorf("hosted running row missing activity: %q", plain)
	}
	if strings.Contains(plain, "hosted") {
		t.Errorf("command row rendered %q, want none — the where-column no longer applies to Commands rows: %q", "hosted", plain)
	}
	if strings.Contains(plain, "pty-1") {
		t.Errorf("command row rendered a where value %q, want none: %q", "pty-1", plain)
	}
}

// TestWhereColumnDetachedWorkerRowShowsPaneNeverHosted covers "a detached
// worker renders its state, its activity, and its pane identifier" and the
// negative half: a detached row must never render the word "hosted".
func TestWhereColumnDetachedWorkerRowShowsPaneNeverHosted(t *testing.T) {
	freshLivework(t)
	worker := &livework.Entry{
		ID: "test-wherecol-detached-worker", Name: "test-wherecol-detached-worker",
		State: livework.StateWaiting, Activity: "awaiting approval", Handle: "pane 3", Via: "terminal",
		Worker: &livework.WorkerInfo{Branch: "some-branch", Worktree: "/tmp/some-branch"},
	}
	// renderWorkRow now reads activity through livework.Default.Activity(e.ID)
	// (admin-8hlz.1, mirroring admin-ddc8's State fix), so the entry must
	// actually be in the process-wide registry rather than merely
	// constructed. See TestWhereColumnHostedCommandRowShowsHostedNeverPane.
	if err := livework.Default.Add(worker); err != nil {
		t.Fatalf("Add(%s): %v", worker.ID, err)
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
	freshLivework(t)
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
	live := &workRowData{cmdName: "build", live: entry}
	for _, c := range cases {
		livework.Default.SetState(entry.ID, c.state, nil)
		line := stripANSI(workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: live}, false, 60))
		if !strings.Contains(line, c.word) {
			t.Errorf("state %v did not render verbatim word %q: %q", c.state, c.word, line)
		}
	}
}

// TestCommandRowTruncatesLongActivity covers "a long activity truncates
// with an ellipsis" for the Commands section's own running row — replacing
// TestWhereColumnTruncatesActivityKeepsWherewholeAt, whose "the where-value
// stays whole at the end of the row" half no longer applies now that the
// where-column is gone from command rows (see the file doc comment above).
func TestCommandRowTruncatesLongActivity(t *testing.T) {
	freshLivework(t)
	longActivity := strings.Repeat("writing a very long activity line ", 4)
	entry := &livework.Entry{
		ID: "wherecol-truncate-activity", Name: "build", State: livework.StateRunning,
		Activity: longActivity, Handle: "pty-3", StartedAt: time.Now(),
	}
	// renderWorkRow now reads activity through livework.Default.Activity(e.ID)
	// (admin-8hlz.1, mirroring admin-ddc8's State fix), so the entry must
	// actually be in the process-wide registry rather than merely
	// constructed. See TestCommandRowRunningShowsNoWhereColumn.
	if err := livework.Default.Add(entry); err != nil {
		t.Fatalf("Add(%s): %v", entry.ID, err)
	}
	live := &workRowData{cmdName: "build", live: entry}
	line := stripANSI(workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: live}, false, 40))
	if !strings.Contains(line, "…") {
		t.Errorf("long activity at a narrow width did not truncate with an ellipsis: %q", line)
	}
}

// TestWhereColumnIdleRowUnchanged is the "an idle command row shows its
// description, nothing else" AC, updated for the Commands grid
// (admin-3rgp.72.11): the description starts right after the name column,
// with no separate where segment.
func TestWhereColumnIdleRowUnchanged(t *testing.T) {
	idle := &workRowData{cmdName: "build", cmdDesc: "compile the binary", cmdKind: "shell"}
	got := workTestModel(nil).renderWorkRow(rowItem{kind: rowCommand, work: idle}, false, 60)

	marker := faintStyle.Render("◇ ")
	name := padEnd(truncR("build", wName), wName)
	desc := truncR("compile the binary", maxi(60-wMarker-wName, 6))
	want := marker + name + faintStyle.Render(desc)

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
	freshLivework(t)
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
			Keys:   []string{"2"},
			Settle: 200 * time.Millisecond,
		})
		plain := stripANSI(out)

		// The hosted entry is a Commands-section row: admin-3rgp.72.11 removed
		// the where-column from that section (see the file doc comment
		// above), so the styled " hosted" where-value (a leading space, from
		// whereSeg) no longer appears anywhere in the frame — only the
		// detached WORKER's own pane id, which the Workers section still
		// carries unchanged, is asserted here. " hosted" (leading space),
		// not "hosted" bare: the fixture's own command name is
		// "wherecolfixturehosted" and the detail pane (out of scope, still
		// shows the full name at this width) would otherwise false-positive
		// on that name's own suffix.
		if strings.Contains(plain, " hosted") {
			t.Errorf("width %d: frame rendered a where-value, want none — the where-column no longer applies to Commands rows: %q", width, plain)
		}
		if !strings.Contains(plain, "pane 7") {
			t.Errorf("width %d: frame missing the detached worker's pane id: %q", width, plain)
		}
	}
}
