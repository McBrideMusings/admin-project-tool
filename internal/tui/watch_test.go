package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestIsGitPath(t *testing.T) {
	sep := string(os.PathSeparator)
	yes := []string{
		"/repo" + sep + ".git",
		"/repo" + sep + ".git" + sep + "index",
		"/repo" + sep + ".git" + sep + "refs" + sep + "heads",
	}
	no := []string{
		"/repo" + sep + "main.go",
		"/repo" + sep + "internal" + sep + "git" + sep + "tracked.go",
		"/repo" + sep + ".github" + sep + "workflows",
	}
	for _, p := range yes {
		if !isGitPath(p) {
			t.Errorf("isGitPath(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if isGitPath(p) {
			t.Errorf("isGitPath(%q) = true, want false", p)
		}
	}
}

// TestDebounceLoopFiresOnWrite proves the watcher mechanics end to end: a file
// write in a watched directory produces a coalesced signal on the channel.
func TestDebounceLoopFiresOnWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ch := make(chan struct{}, 1)
	go debounceLoop(w, ch)

	// A burst of writes must coalesce into a single signal after the quiet period.
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte{byte(i)}, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not signal after a file write")
	}
	// No second signal should arrive from the same coalesced burst.
	select {
	case <-ch:
		t.Error("watcher signaled twice for one coalesced burst")
	case <-time.After(fsDebounce * 2):
	}
}

// TestDebounceLoopIgnoresChmod proves an attribute-only change (what a reader
// updating atime produces) does not signal a reload.
func TestDebounceLoopIgnoresChmod(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ch := make(chan struct{}, 1)
	go debounceLoop(w, ch)

	if err := os.Chmod(f, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	select {
	case <-ch:
		t.Error("watcher signaled for a chmod-only change")
	case <-time.After(fsDebounce * 3):
	}
}
