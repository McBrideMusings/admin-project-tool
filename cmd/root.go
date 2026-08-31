// Package cmd wires the CLI: the bare `admin` command launches the
// dashboard; `admin new` creates a worktree; `admin <command>` runs a
// command declared in the project's admin.toml (ADR-0011). Anything that
// needs to cd the parent shell is emitted as a sentinel after the TUI tears
// down (see internal/shim).
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
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
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
	Use:   "admin",
	// "repo" is a cobra alias, not a second identity: ADR-0011 §1. It exists
	// only because `repo completion zsh` is wired into the user's .zshrc, and
	// may be deleted once nothing invokes it under that name.
	Aliases:       []string{"repo"},
	Short:         "Terminal dashboard for the git repo you're in, and the project's admin.toml runner",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if dumpFrame {
			return runDumpFrame(cmd.Context())
		}
		return runDashboard(cmd.Context())
	},
}

// Execute runs the root command. It first validates the kind catalog is
// fully covered by registered factories (internal/kinds) — this makes an
// incomplete catalog a clean startup error in addition to the init-time
// panic factories.go raises, and gives that check a path reachable without
// killing the process.
func Execute() error {
	if err := kinds.Validate(); err != nil {
		return err
	}
	rootCmd.SetArgs(dispatchArgs(os.Args[1:]))
	return rootCmd.ExecuteContext(context.Background())
}

// dispatchArgs resolves a bare first argument per ADR-0011 §2's order:
//
//  1. A tool verb or an existing cmd/ verb (branch, prs, populate, plus
//     cobra's help/completion) — handled by the loop below.
//  2. A command declared in the current project's admin.toml — handled by
//     manifestCommandArgs.
//  3. Otherwise, branch shorthand, exactly as before this ticket: `admin
//     <input>` behaves like `admin branch <input>`.
//
// Step 2 is skipped (falling straight to step 3) whenever the project has no
// admin.toml at all, so a project with no manifest keeps exactly today's
// behaviour — every unrecognized bare argument is branch input. An
// admin.toml that IS present but fails to parse or resolve is not skipped:
// it blocks the fall-through and reports the manifest problem instead
// (admin-3rgp.52) — see registerManifestCommand.
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
	if registerManifestCommand(first) {
		return args
	}
	// The bare shorthand is the "just make it" path — skip branch's confirm prompt.
	// The explicit `admin branch <input>` still confirms unless -y is passed.
	return append([]string{"branch", "--yes"}, args...)
}

// registerManifestCommand resolves the project manifest at the current
// working directory and, if it declares a command named name, adds a cobra
// command for it to rootCmd (so the normal SetArgs/Execute flow below runs
// it) and returns true.
//
// It returns false — meaning "keep looking, fall through to branch
// shorthand" — only in the one case ADR-0011 §2 reserves for that: there is
// no admin.toml here at all. Two other cases both return true, which blocks
// the branch-shorthand fall-through, but differ in what gets registered:
//
//   - The manifest resolves fine but doesn't declare a command called name:
//     no command is registered, and dispatchArgs' caller falls through
//     anyway (registerManifestCommand still returns false here — a resolved
//     manifest without a matching name is exactly the "not for me" case a
//     bare argument like a branch name legitimately hits).
//   - The manifest EXISTS but fails to parse or resolve: this used to be
//     silently treated as "no match", which sent every bare argument in a
//     broken-manifest project straight to branch shorthand — admin-3rgp.52,
//     which mutated git state with no warning the manifest was even
//     invalid. Now a present-but-broken manifest registers an error command
//     under name whose RunE reports the manifest problem, and returns true
//     so the caller never reaches the branch fallback.
func registerManifestCommand(name string) bool {
	wd, err := os.Getwd()
	if err != nil {
		return false
	}
	m, ok, err := resolveProjectManifest(context.Background(), wd)
	if !ok {
		return false // no admin.toml at all — ADR-0011 §2's one fall-through case
	}
	if err != nil {
		// admin.toml exists but failed to parse/resolve: a hard error naming
		// the problem, never a fall-through to branch shorthand (admin-3rgp.52).
		rootCmd.AddCommand(&cobra.Command{
			Use:                name,
			Short:              "admin.toml is invalid",
			DisableFlagParsing: true,
			RunE: func(cmd *cobra.Command, cmdArgs []string) error {
				return fmt.Errorf("admin.toml is invalid: %w", err)
			},
		})
		return true
	}
	if !kinds.HasCommand(m, name) {
		return false
	}
	rootCmd.AddCommand(&cobra.Command{
		Use:                name,
		Short:              "admin.toml command: " + name,
		DisableFlagParsing: true, // flags belong to the underlying steps, not to cobra
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			return kinds.RunCommand(m, kinds.Default, name, cmdArgs)
		},
	})
	return true
}

// runActionCmd is admin-3rgp.17's hidden re-entry point: what a herdr pane
// a long-running kind dispatched into actually runs. It is the one caller
// that invokes kinds.RunAction directly rather than kinds.RunActionRouted —
// deliberately: RunAction executes exactly the one named action's Runner in
// this process, with no further lifetime routing, so the pane does the
// real work instead of trying to open a second pane for itself. Hidden
// because it is plumbing, not something a person types; double-underscore
// prefixed so a project's own admin.toml commands (which would otherwise
// need "reserved name" handling like the tool verbs get) can never collide
// with it in practice.
var runActionCmd = &cobra.Command{
	Use:                "__run-action <name> [args...]",
	Hidden:             true,
	DisableFlagParsing: true, // flags belong to the action's own steps, not to cobra
	Args:               cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		// context.Background(), not cmd.Context(): this verb is reached both
		// through cobra's normal Execute (a real context) and by a test or
		// tool calling RunE directly (cmd.Context() is nil then) — the same
		// reason registerManifestCommand's own resolveProjectManifest call
		// uses context.Background() rather than depending on cobra's.
		m, ok, err := resolveProjectManifest(context.Background(), wd)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("__run-action: no admin.toml found in %s", wd)
		}
		return kinds.RunAction(m, kinds.Default, args[0], args[1:])
	},
}

func init() {
	rootCmd.AddCommand(branchCmd)
	rootCmd.AddCommand(worktreeCmd)
	rootCmd.AddCommand(prsCmd)
	rootCmd.AddCommand(populateCmd)
	rootCmd.AddCommand(runActionCmd)

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
