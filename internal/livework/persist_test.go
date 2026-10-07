package livework

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// twoProcesses returns two registries sharing one state dir, standing in for
// two admin processes on one machine, and installs an ops builder that marks
// every entry it rebuilds.
func twoProcesses(t *testing.T) (a, b *Registry, rebuilt *[]string) {
	t.Helper()
	dir := t.TempDir()
	var ids []string
	prev := opsBuilder
	SetOpsBuilder(func(e *Entry) Ops {
		ids = append(ids, e.ID+"@"+e.Handle)
		return Ops{Kill: func() error { return nil }}
	})
	t.Cleanup(func() { opsBuilder = prev })
	return NewSharedRegistry(dir), NewSharedRegistry(dir), &ids
}

func sharedWorker(id, handle string) *Entry {
	return &Entry{
		ID: id, Name: "worktree", Kind: "workflow", Handle: handle, Via: "terminal", Root: "/repo",
		Worker: &WorkerInfo{Branch: "fix-it", Worktree: "/wt/fix-it", Target: "fix-it"},
	}
}

func TestSyncShowsAnEntryAnotherProcessAdded(t *testing.T) {
	a, b, rebuilt := twoProcesses(t)
	if err := a.Add(sharedWorker("worktree-fix-it", "w1:p2")); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync("/repo"); err != nil {
		t.Fatal(err)
	}
	e, ok := b.Get("worktree-fix-it")
	if !ok {
		t.Fatal("b does not see the worker a added")
	}
	if e.State != StateRunning || e.Handle != "w1:p2" || e.Worker == nil || e.Worker.Worktree != "/wt/fix-it" {
		t.Fatalf("loaded entry = %+v, worker %+v", e, e.Worker)
	}
	if got := strings.Join(*rebuilt, ","); got != "worktree-fix-it@w1:p2" {
		t.Fatalf("ops rebuilt for %q, want worktree-fix-it@w1:p2", got)
	}
	if err := b.Kill("worktree-fix-it"); err != nil {
		t.Fatalf("loaded entry's rebuilt kill: %v", err)
	}
	if got := b.WorkerStates()["fix-it"]; got != StateRunning {
		t.Fatalf("b.WorkerStates()[fix-it] = %q, want running", got)
	}
}

func TestStateChangesCrossProcessesAndExitRemovesTheFile(t *testing.T) {
	a, b, _ := twoProcesses(t)
	if err := a.Add(sharedWorker("w", "h")); err != nil {
		t.Fatal(err)
	}
	b.Sync("/repo")

	b.SetState("w", StateWaiting, nil)
	b.SetNote("w", "needs input")
	a.Sync("/repo")
	if st, _ := a.State("w"); st != StateWaiting {
		t.Fatalf("a sees %q after b set waiting", st)
	}
	if line, _ := a.Line("w"); line != "needs input" {
		t.Fatalf("a sees note %q", line)
	}

	a.SetState("w", StateExited, nil)
	if _, err := os.Stat(filepath.Join(a.RepoDir("/repo"), "w.json")); !os.IsNotExist(err) {
		t.Fatalf("exited entry's file still there: %v", err)
	}
	b.Sync("/repo")
	if st, _ := b.State("w"); st != StateExited {
		t.Fatalf("b sees %q after a's entry exited, want exited", st)
	}
	if _, live := b.WorkerStates()["fix-it"]; live {
		t.Fatal("an exited worker still counts as live in b")
	}
}

func TestAddRefusesAnIDAnotherProcessHoldsLive(t *testing.T) {
	a, b, _ := twoProcesses(t)
	if err := a.Add(sharedWorker("w", "h1")); err != nil {
		t.Fatal(err)
	}
	err := b.Add(sharedWorker("w", "h2"))
	if err == nil || !strings.Contains(err.Error(), "another admin process") {
		t.Fatalf("b.Add of a's live id = %v, want refusal", err)
	}
	if _, ok := b.Get("w"); ok {
		t.Fatal("a refused Add left the entry in b")
	}
	a.SetState("w", StateExited, nil)
	if err := b.Add(sharedWorker("w", "h2")); err != nil {
		t.Fatalf("b.Add after a's exited: %v", err)
	}
}

func TestHostedAndRootlessEntriesStayInProcess(t *testing.T) {
	a, _, _ := twoProcesses(t)
	hosted := &Entry{ID: "dev", Handle: "/dev/ttys004", Root: "/repo"}
	rootless := &Entry{ID: "x", Handle: "p", Via: "terminal"}
	for _, e := range []*Entry{hosted, rootless} {
		if err := a.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	if names, _ := os.ReadDir(a.RepoDir("/repo")); len(names) != 0 {
		t.Fatalf("wrote %d files for entries that must stay in process", len(names))
	}
}

func TestSyncNamesAMalformedFileAndLoadsTheRest(t *testing.T) {
	a, b, _ := twoProcesses(t)
	if err := a.Add(sharedWorker("good", "h")); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(a.RepoDir("/repo"), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := b.Sync("/repo")
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("Sync error = %v, want one naming %s", err, bad)
	}
	if _, ok := b.Get("good"); !ok {
		t.Fatal("a malformed neighbour stopped a good entry loading")
	}
}

func TestSyncDoesNotReviveAnEntryThisProcessEnded(t *testing.T) {
	a, _, _ := twoProcesses(t)
	running := func(handle string) record {
		rec := toRecord(sharedWorker("w", handle))
		rec.State = StateRunning
		return rec
	}
	w := sharedWorker("w", "h")
	if err := a.Add(w); err != nil {
		t.Fatal(err)
	}
	a.SetState("w", StateExited, nil)
	// The file outlives the entry: a removal that failed, or a crash.
	if err := writeFile(filepath.Join(a.RepoDir("/repo"), "w.json"), running("h"), false); err != nil {
		t.Fatal(err)
	}
	a.Sync("/repo")
	if st, _ := a.State("w"); st != StateExited {
		t.Fatalf("a leftover file revived the ended entry as %q", st)
	}
	// A new pane under the same id is a re-dispatch, and does load.
	if err := writeFile(filepath.Join(a.RepoDir("/repo"), "w.json"), running("h2"), false); err != nil {
		t.Fatal(err)
	}
	a.Sync("/repo")
	if e, _ := a.Get("w"); e.Handle != "h2" || e.State != StateRunning {
		t.Fatalf("re-dispatched entry = %s %s, want h2 running", e.Handle, e.State)
	}
}

func TestSyncKeepsThisProcessesLiveEntryUnderTheSameID(t *testing.T) {
	a, b, _ := twoProcesses(t)
	if err := b.Add(&Entry{ID: "w", Handle: "/dev/ttys004", Root: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Add(sharedWorker("w", "h")); err != nil {
		t.Fatal(err)
	}
	b.Sync("/repo")
	if e, _ := b.Get("w"); e.Handle != "/dev/ttys004" {
		t.Fatalf("b's hosted entry was replaced by %s", e.Handle)
	}
}

func TestSyncReportsAMalformedFileOnce(t *testing.T) {
	a, b, _ := twoProcesses(t)
	os.MkdirAll(a.RepoDir("/repo"), 0o755)
	os.WriteFile(filepath.Join(a.RepoDir("/repo"), "bad.json"), []byte("{"), 0o644)
	if err := b.Sync("/repo"); err == nil {
		t.Fatal("first Sync did not report the malformed file")
	}
	if err := b.Sync("/repo"); err != nil {
		t.Fatalf("second Sync reported it again: %v", err)
	}
}

func TestInMemoryRegistryWritesNothing(t *testing.T) {
	r := NewRegistry()
	if err := r.Add(sharedWorker("w", "h")); err != nil {
		t.Fatal(err)
	}
	if r.RepoDir("/repo") != "" {
		t.Fatal("an in-memory registry reports a state dir")
	}
}
