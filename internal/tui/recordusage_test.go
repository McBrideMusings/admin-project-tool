package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/workusage"
)

// TestDispatchStartedRecordsUsageOffTheUpdateLoop proves Update does not write
// the usage list itself: the write is one of the Cmds it returns.
func TestDispatchStartedRecordsUsageOffTheUpdateLoop(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)

	m := workTestModel(&manifest.Manifest{
		Custom:  map[string]manifest.Command{"deploy": {Steps: []string{"deploy"}}},
		Actions: map[string]manifest.Action{"deploy": {"kind": "shell"}},
	})
	m.nwo = "o/r"

	_, cmd := m.Update(workDispatchStartedMsg{name: "deploy", args: []string{"grafana"}})
	if got := workusage.Top("o/r", 5); len(got) != 0 {
		t.Fatalf("Update recorded %v inline, want nothing until the Cmd runs", got)
	}
	if cmd == nil {
		t.Fatal("Update returned a nil Cmd")
	}

	runBatched(cmd)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := workusage.Top("o/r", 5); len(got) == 1 && got[0] == "deploy grafana" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, _ := os.ReadDir(filepath.Join(cache))
	t.Fatalf("Cmds never recorded \"deploy grafana\"; cache dir has %v", entries)
}

// runBatched runs cmd, and every Cmd inside any tea.Batch it produces, each on
// its own goroutine. Tick Cmds sleep, so nothing waits on them.
func runBatched(cmd tea.Cmd) {
	go func() {
		if batch, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					runBatched(c)
				}
			}
		}
	}()
}
