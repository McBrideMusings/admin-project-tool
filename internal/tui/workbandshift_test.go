// workbandshift_test.go ports TestBandDoesNotShift from the design
// prototype (docs/spikes/admin-tui-desktop/key_test.go:273-326) against the
// app's real types: rowItem/workRowData/livework.Entry/Model.renderWorkBand
// and the headless internal/tui.Frame seam, rather than the prototype's own
// commandEntry/liveRows/render(State{...}).
//
// This is the epic's single most important invariant (fieldRowsH's own doc
// comment in workview.go): the live band's field block is a fixed-height
// slot for every row kind, so moving the cursor between rows in different
// states never moves a row of the band beneath it.
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestBandDoesNotShift has two halves, both ported from the prototype:
//
//  1. every field block — idle-never-run, idle-has-run, live-command-with-
//     url, live-command-without-url, worker — is exactly fieldRowsH rows.
//  2. the row directly BELOW the field block — the output tail's own
//     content, bottom-aligned within the band, for a running command or a
//     worker; the "output appears here while it runs" placeholder for an
//     idle command — lands on the same rendered line index whatever the
//     cursor sits on. That is the line fieldRowsH actually protects: the
//     band's title rule sits at a fixed band-local offset (0) no matter
//     what the field block does, so asserting ITS position proves nothing —
//     it is the boundary between the field block and whatever follows it
//     that moves when the block grows or shrinks.
func TestBandDoesNotShift(t *testing.T) {
	t.Run("field blocks are always fieldRowsH rows", func(t *testing.T) {
		// idle, never run: no logPath at all, so bandIdle has only kind/desc
		// to say and falls back to "never"/"—" for last run/last log.
		neverRun := bandIdle(&workRowData{cmdName: "dev", cmdDesc: "start dev", cmdKind: "shell"}, 60)
		if n := len(neverRun); n != fieldRowsH {
			t.Errorf("bandIdle for a never-run entry is %d rows, want exactly %d", n, fieldRowsH)
		}

		// idle, HAS run: a real log file on disk so bandIdle computes a real
		// "last run" age from its mtime — the case the whole invariant
		// exists for, since it is the one that used to grow a row.
		logPath := filepath.Join(t.TempDir(), "dev.log")
		if err := os.WriteFile(logPath, []byte("log\n"), 0o644); err != nil {
			t.Fatalf("writing fixture log: %v", err)
		}
		hasRun := bandIdle(&workRowData{cmdName: "dev", cmdDesc: "start dev", cmdKind: "shell", logPath: logPath}, 60)
		if n := len(hasRun); n != fieldRowsH {
			t.Errorf("bandIdle for an entry that HAS run is %d rows, want exactly %d", n, fieldRowsH)
		}

		// live command, with a url.
		withURL := bandFieldsCommand(&workRowData{
			cmdName: "dev", logPath: logPath,
			urls: []frame.URLEntry{{Label: "local", URL: "http://localhost:8787"}},
		}, 60)
		if n := len(withURL); n != fieldRowsH {
			t.Errorf("bandFieldsCommand (with a url) is %d rows, want exactly %d", n, fieldRowsH)
		}

		// live command, with NO url.
		noURL := bandFieldsCommand(&workRowData{cmdName: "dev", logPath: logPath}, 60)
		if n := len(noURL); n != fieldRowsH {
			t.Errorf("bandFieldsCommand (no url) is %d rows, want exactly %d", n, fieldRowsH)
		}

		// worker: always five real rows, no padding.
		worker := bandFieldsWorker(&livework.Entry{
			ID: "w1", LogPath: logPath,
			Worker: &livework.WorkerInfo{Branch: "feature/x", Worktree: "/tmp/wt"},
		}, 60)
		if n := len(worker); n != fieldRowsH {
			t.Errorf("bandFieldsWorker is %d rows, want exactly %d", n, fieldRowsH)
		}
	})

	t.Run("the row directly below the field block lands on the same line whatever the cursor is on", func(t *testing.T) {
		freshLivework(t)

		dir := t.TempDir()
		toml := "project_name = \"fixture\"\n\n" +
			"[commands.idle-cmd]\ndesc = \"idle command\"\nsteps = [\"idle-cmd\"]\n\n[actions.idle-cmd]\nkind = \"shell\"\nrun = \"true\"\n\n" +
			"[commands.running-cmd]\ndesc = \"running command\"\nsteps = [\"running-cmd\"]\n\n[actions.running-cmd]\nkind = \"shell\"\nrun = \"true\"\n"
		if err := os.WriteFile(filepath.Join(dir, manifest.Filename), []byte(toml), 0o644); err != nil {
			t.Fatalf("writing fixture admin.toml: %v", err)
		}

		// A single-line marker in each live entry's Output ring: bandTail
		// bottom-aligns a short tail (a gap goes above it, never below), so
		// with exactly one line of content the marker always renders on the
		// band's OWN last row when the field block above it is sized
		// correctly — the same last row the idle placeholder below occupies
		// (TestBandIdleOutputPlaceholderOnLastLine). That shared landing
		// spot is what makes the three states comparable at all: an
		// oversized or undersized field block shifts avail (the tail's
		// available height, computed from the fixed fieldRowsH constant
		// rather than the field block's own actual length) and the marker
		// moves off the last row.
		runningRing := livework.NewRing(4096)
		runningRing.Write([]byte("RUNNING-TAIL-MARKER\r\n"))
		live := &livework.Entry{
			ID: "running-cmd", Name: "running-cmd", Handle: "pty-running-cmd",
			State: livework.StateRunning, StartedAt: time.Now(), Output: runningRing,
		}
		if err := livework.Default.Add(live); err != nil {
			t.Fatalf("Add(running): %v", err)
		}
		workerRing := livework.NewRing(4096)
		workerRing.Write([]byte("WORKER-TAIL-MARKER\r\n"))
		worker := &livework.Entry{
			ID: "worker-1", Name: "worker-1", Handle: "pane-worker-1", Via: "herdr",
			State: livework.StateRunning, StartedAt: time.Now(), Output: workerRing,
			Worker: &livework.WorkerInfo{Branch: "worker-1", Worktree: "/tmp/worker-1"},
		}
		if err := livework.Default.Add(worker); err != nil {
			t.Fatalf("Add(worker): %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		base := New(ctx, dir, "owner/repo", "main", "main")
		base.herdr = false

		// lineContaining renders the whole Work tab with the cursor on
		// cursorOn and returns the index of the one line carrying want —
		// the idle placeholder text, or a tail marker unique to that entry.
		lineContaining := func(t *testing.T, cursorOn, want string) int {
			t.Helper()
			m := base
			m.active = tabWork
			m.focus = focusBody
			rows := m.workRows()
			idx := -1
			for i, r := range rows {
				switch {
				case r.kind == rowCommand && r.work != nil && r.work.cmdName == cursorOn:
					idx = i
				case r.kind == rowWorker && r.work != nil && r.work.worker != nil && r.work.worker.ID == cursorOn:
					idx = i
				}
			}
			if idx < 0 {
				t.Fatalf("no row found for %q in %v", cursorOn, rows)
			}
			m.cursor[tabWork] = idx

			out := Frame(ctx, m, FrameOptions{Width: 120, Height: 40, Settle: 300 * time.Millisecond})
			lines := strings.Split(stripANSI(out), "\n")
			for i, l := range lines {
				if strings.Contains(l, want) {
					return i
				}
			}
			t.Fatalf("%s: %q not found in:\n%s", cursorOn, want, out)
			return -1
		}

		idleLine := lineContaining(t, "idle-cmd", "output appears here while it runs")
		if got := lineContaining(t, "running-cmd", "RUNNING-TAIL-MARKER"); got != idleLine {
			t.Errorf("cursor on a running command: tail marker on line %d, want %d (idle command's placeholder line) — the field block above it moved the row below", got, idleLine)
		}
		if got := lineContaining(t, "worker-1", "WORKER-TAIL-MARKER"); got != idleLine {
			t.Errorf("cursor on a worker: tail marker on line %d, want %d (idle command's placeholder line) — the field block above it moved the row below", got, idleLine)
		}
	})
}
