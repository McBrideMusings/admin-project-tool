package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/vt"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// FrameFixtures names the live-work fixtures SeedFrameFixture builds. A
// headless frame has no worker and no hosted pty to look at, so these put the
// same four workers (running, waiting, failed, stopped) into the registry a
// real swarm would, and optionally one hosted-pty worker to attach to:
//
//	swarm         four launcher-dispatched workers; the failed one carries a
//	              pane snapshot for the band to show
//	hosted        the same, but the waiting worker is a hosted pty with a
//	              prompt on its screen
//	hosted-empty  the same, but the hosted pty has printed nothing yet
//
// Drive them through `admin --dump-frame --fixture <name> --keys …`: `2` opens
// the Workers tab, `down` twice puts the cursor on the waiting worker, `a`
// attaches it.
var FrameFixtures = []string{"swarm", "hosted", "hosted-empty"}

// hostedPrompt is what the hosted fixture's pty has printed: a worker blocked
// on a question.
const hostedPrompt = "the manifest declares no top-level command name —\r\n" +
	"every existing project sets one (admin.toml's\r\n" +
	"[identity] or the repo directory name), and this\r\n" +
	"fixture repo has neither.\r\n" +
	"\r\n" +
	"what should the root command be called? this\r\n" +
	"decides the CLI verb (\"admin <name>\") and the\r\n" +
	"dashboard's header line.\r\n" +
	"\r\n" +
	"needs input: root command name\r\n"

// SeedFrameFixture adds the named fixture's entries to livework.Default. It
// is for a headless frame only: the entries run nothing, and the process
// exits right after the frame.
func SeedFrameFixture(name string) error {
	hosted := false
	switch name {
	case "swarm":
	case "hosted", "hosted-empty":
		hosted = true
	default:
		return fmt.Errorf("unknown fixture %q (have %s)", name, strings.Join(FrameFixtures, ", "))
	}
	now := time.Now()
	workers := []*livework.Entry{
		{ID: "admin-3rgp.36", Name: "admin-3rgp.36", Handle: "pane 3", Via: "terminal",
			State: livework.StateRunning, StartedAt: now.Add(-18 * time.Minute),
			Worker: &livework.WorkerInfo{
				Branch:   "feat/admin-3rgp.36-lua-host",
				Worktree: "/Users/dev/.worktrees/admin-project-tool/admin-3rgp.36",
			}},
		{ID: "admin-3rgp.7", Name: "admin-3rgp.7", Handle: "pane 4", Via: "terminal",
			State: livework.StateWaiting, StartedAt: now.Add(-6 * time.Minute),
			Worker: &livework.WorkerInfo{
				Branch:   "feat/admin-3rgp.7-root-command",
				Worktree: "/Users/dev/.worktrees/admin-project-tool/admin-3rgp.7",
			}},
		{ID: "admin-3rgp.33", Name: "admin-3rgp.33", Handle: "pane 5", Via: "terminal",
			State: livework.StateFailed, StartedAt: now.Add(-2 * time.Minute),
			Worker: &livework.WorkerInfo{
				Branch:   "fix/admin-3rgp.33-detect",
				Worktree: "/Users/dev/.worktrees/admin-project-tool/admin-3rgp.33",
			}},
		{ID: "admin-3rgp.41", Name: "admin-3rgp.41", Handle: "pane 6", Via: "terminal",
			State: livework.StateStopped, StartedAt: now.Add(-47 * time.Minute),
			Worker: &livework.WorkerInfo{
				Branch:   "fix/admin-3rgp.41-footer",
				Worktree: "/Users/dev/.worktrees/admin-project-tool/admin-3rgp.41",
			}},
	}
	for _, e := range workers {
		if hosted && e.ID == "admin-3rgp.7" {
			e.Via, e.Handle, e.Kind = "", "admin-3rgp.7-pty", "hosted-pty"
			e.Output = livework.NewRing(1 << 16)
			e.Screen = vt.NewSafeEmulator(80, 24)
			e.Ops = livework.Ops{
				Kill:   func() error { return nil },
				Write:  func([]byte) (int, error) { return 0, nil },
				Resize: func(rows, cols int) error { e.Screen.Resize(cols, rows); return nil },
			}
			if name == "hosted" {
				_, _ = e.Output.Write([]byte(hostedPrompt))
				_, _ = e.Screen.Write([]byte(hostedPrompt))
			}
		}
		if err := livework.Default.Add(e); err != nil {
			return err
		}
	}
	livework.Default.SetActivity("admin-3rgp.36", "writing internal/lua/host.go")
	livework.Default.SetActivity("admin-3rgp.7", "needs input: root command name")
	livework.Default.SetActivity("admin-3rgp.33", "go test ./internal/detect FAIL")
	livework.Default.SetNote("admin-3rgp.41", "pane closed before done passed")
	livework.Default.SetLogPath("admin-3rgp.36", "/Users/dev/admin-project-tool/.admin/logs/worker-admin-3rgp.36.log")
	livework.Default.SetLogPath("admin-3rgp.7", "/Users/dev/admin-project-tool/.admin/logs/worker-admin-3rgp.7.log")
	livework.Default.SetLogPath("admin-3rgp.33", "/Users/dev/admin-project-tool/.admin/logs/worker-admin-3rgp.33.log")
	livework.Default.SetSnapshot("admin-3rgp.33", "    detect_test.go:88: expected archetype\n"+
		"        \"react-native\", got \"\"\n"+
		"FAIL\n"+
		"FAIL    github.com/.../internal/detect    0.014s\n")
	return nil
}
