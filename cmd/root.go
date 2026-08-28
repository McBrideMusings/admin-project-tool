// Package cmd wires the CLI: the bare `repo` command launches the dashboard;
// `repo new` creates a worktree. Anything that needs to cd the parent shell is
// emitted as a sentinel after the TUI tears down (see internal/shim).
package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/create"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/shim"
	"github.com/McBrideMusings/admin-project-tool/internal/tui"
)

// Flags for --dump-frame, the headless render. They exist so a change can be
// checked at the surface with one command: driving the live TUI needs a pty, and
// piping a keystroke into it races Bubble Tea's first read and hangs.
var (
	dumpFrame  bool
	dumpWidth  int
	dumpHeight int
	dumpKeys   []string
	dumpSettle time.Duration
)

var rootCmd = &cobra.Command{
	Use:           "repo",
	Short:         "Terminal dashboard for the git repo you're in",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if dumpFrame {
			return runDumpFrame(cmd.Context())
		}
		return runDashboard(cmd.Context())
	},
}

// Execute runs the root command.
func Execute() error {
	rootCmd.SetArgs(dispatchArgs(os.Args[1:]))
	return rootCmd.ExecuteContext(context.Background())
}

// dispatchArgs makes a bare `repo <input>` behave like `repo branch <input>`.
// If the first arg isn't a flag and isn't a known subcommand (branch, help,
// completion, …), it's treated as branch input — a URL, issue/PR number, or
// branch name — so pasting a link or typing a name just makes the worktree.
func dispatchArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	first := args[0]
	if strings.HasPrefix(first, "-") {
		return args
	}
	// Cobra registers its built-in help/completion commands lazily during Execute,
	// so force them in now — otherwise `repo completion zsh` (wired in .zshrc) and
	// `repo help` would be misread as branch input.
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()
	for _, c := range rootCmd.Commands() {
		if c.Name() == first || c.HasAlias(first) {
			return args
		}
	}
	// The bare shorthand is the "just make it" path — skip branch's confirm prompt.
	// The explicit `repo branch <input>` still confirms unless -y is passed.
	return append([]string{"branch", "--yes"}, args...)
}

func init() {
	rootCmd.AddCommand(branchCmd)
	rootCmd.AddCommand(prsCmd)
	rootCmd.AddCommand(populateCmd)

	f := rootCmd.Flags()
	f.BoolVar(&dumpFrame, "dump-frame", false, "render one frame to stdout and exit (no TTY needed)")
	f.IntVar(&dumpWidth, "width", 120, "terminal width for --dump-frame")
	f.IntVar(&dumpHeight, "height", 40, "terminal height for --dump-frame")
	f.StringSliceVar(&dumpKeys, "keys", nil, "keys to press before --dump-frame takes the frame, e.g. --keys tab,tab")
	f.DurationVar(&dumpSettle, "settle", 2*time.Second, "how long --dump-frame waits for background loads")
}

// runDumpFrame prints one frame of the dashboard to stdout and exits. Unlike the
// dashboard path this writes the UI to stdout on purpose: the frame *is* the
// output, there is no cd sentinel to keep it clear of, and the repo() shell
// function passes non-sentinel stdout lines straight through.
func runDumpFrame(ctx context.Context) error {
	repoRoot, err := git.RepoRoot(ctx)
	if err != nil {
		return err
	}
	// Colour is decided by probing the renderer's writer, and a captured frame is
	// going somewhere that isn't a terminal. Leave the probe on stdout so a run in
	// a terminal keeps its colours and one piped to a file comes out plain — which
	// is what a diff of two frames wants.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // winds down the watcher and the merged tick the frame abandoned

	nwo, _ := git.OriginNWO(ctx)
	model := tui.New(ctx, repoRoot, nwo, git.CurrentBranch(ctx), git.DefaultBranch(ctx))
	frame := tui.Frame(ctx, model, tui.FrameOptions{
		Width:  dumpWidth,
		Height: dumpHeight,
		Keys:   dumpKeys,
		Settle: dumpSettle,
	})
	fmt.Println(frame)
	return nil
}

func runDashboard(ctx context.Context) error {
	repoRoot, err := git.RepoRoot(ctx)
	if err != nil {
		return err
	}
	nwo, _ := git.OriginNWO(ctx)
	current := git.CurrentBranch(ctx)
	def := git.DefaultBranch(ctx)

	// lipgloss decides how many colors to use by probing its renderer's writer.
	// The TUI draws to stderr, but stdout is captured by the repo() shell function
	// ($(...)), so the default renderer (which probes stdout) sees a pipe, concludes
	// "no color", and flattens the whole UI to one color. Bind the default renderer
	// to stderr — the actual TTY — so the ANSI palette (16-color indices that the
	// terminal theme colors) renders instead of being stripped.
	lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(os.Stderr))

	model := tui.New(ctx, repoRoot, nwo, current, def)
	// Render to stderr so stdout carries only the cd sentinel — the repo() shell
	// function captures stdout via $(...) while the TUI draws on the TTY (stderr).
	// Cell motion reports a click and a drag but not idle movement, which is all
	// the dashboard's own mouse support (click to act, drag to copy) needs.
	prog := tea.NewProgram(model, tea.WithContext(ctx), tea.WithAltScreen(), tea.WithOutput(os.Stderr), tea.WithMouseCellMotion())
	final, err := prog.Run()
	if err != nil {
		return err
	}

	// Execute any deferred action that needs to move the parent shell. All
	// human-readable output goes to stderr; only the cd sentinel goes to stdout.
	if fm, ok := final.(tui.Model); ok && fm.Pending != nil {
		return runPending(ctx, repoRoot, fm.Pending)
	}
	return nil
}

func runPending(ctx context.Context, repoRoot string, p *tui.PendingAction) error {
	switch p.Kind {
	case "cd":
		emitCD(repoRoot, p.Path)
	case "create-cd":
		res, err := create.Worktree(ctx, p.RepoRoot, p.Branch)
		if err != nil {
			return err
		}
		reportPopulate(res)
		emitCD(repoRoot, res.Path)
	case "pr-checkout-cd":
		if err := git.FetchPRHead(ctx, p.PRNum, p.Branch); err != nil {
			return err
		}
		res, err := create.Worktree(ctx, p.RepoRoot, p.Branch)
		if err != nil {
			return err
		}
		reportPopulate(res)
		emitCD(repoRoot, res.Path)
	}
	return nil
}

// emitCD moves the parent shell into path. Inside a herdr pane it opens a
// focused child workspace on the worktree (leaving the current pane put);
// otherwise it emits the cd sentinel for the repo() wrapper to cd.
func emitCD(repoRoot, path string) {
	if shim.OpenInHerdr(repoRoot, path) == nil {
		return
	}
	shim.PrintCD(path)
}

func reportPopulate(res create.Result) {
	reportPopulateResults(res.Populate)
}
