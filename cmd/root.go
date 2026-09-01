// Package cmd wires the CLI: the bare `admin` command launches the
// dashboard; `admin branch` / `admin worktree` create a branch or a worktree
// from an issue, a PR or a name; `admin <command>` runs a command declared in
// the project's admin.toml (ADR-0011). A word that is neither is an error;
// there is no fall-through (ADR-0014). Anything that needs to cd the parent
// shell is emitted as a sentinel after the TUI tears down (see
// internal/shim).
package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
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

// dispatchArgs resolves a bare first argument in two steps:
//
//  1. A tool verb or an existing cmd/ verb (branch, worktree, prs, populate,
//     plus cobra's help/completion) — handled by the loop below.
//  2. A command declared in the current project's admin.toml — handled by
//     registerManifestCommand.
//
// A word matching neither is an error naming what it could have been. There
// is deliberately no third step: `admin <anything>` used to mean `admin
// branch --yes <anything>`, so in any project without an admin.toml — which
// is every fresh clone, since the manifest is never committed — a typed
// command name silently created and checked out a git branch wearing it
// (ADR-0011 amendment, admin-oeqo). Creating a branch or a worktree is
// what the reserved `branch` and `worktree` verbs are for, and typing the
// verb is the whole cost of never guessing again.
//
// An admin.toml that is present but fails to parse or resolve reports the
// manifest problem rather than "unknown command" (admin-3rgp.52) — see
// registerManifestCommand.
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
	// `repo help` would be reported as unknown commands.
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
	registerUnknownCommand(first)
	return args
}

// registerUnknownCommand wires a hidden command under name whose RunE is the
// "this word means nothing here" error. It is registered rather than left to
// cobra's own unknown-command error so the message can name the actual
// remedy: the project's real command list, or the fact that there is no
// admin.toml here at all, plus the two verbs that create git refs — which is
// what a word like "dev" used to do to a repo by accident.
func registerUnknownCommand(name string) {
	rootCmd.AddCommand(&cobra.Command{
		Use:                name,
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			return unknownCommandError(name)
		},
	})
}

// unknownCommandError builds that message. The two cases differ in what is
// worth saying: with a manifest, the fix is usually a name in it; without
// one, the project has no task runner at all and no command name could have
// worked.
func unknownCommandError(name string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "unknown command %q", name)

	wd, wdErr := os.Getwd()
	if wdErr == nil {
		if m, ok, err := resolveProjectManifest(context.Background(), wd); ok && err == nil {
			if names := kinds.CommandNames(m); len(names) > 0 {
				fmt.Fprintf(&b, " — admin.toml declares: %s", strings.Join(names, ", "))
			} else {
				b.WriteString(" — admin.toml declares no commands")
			}
		} else if !ok {
			fmt.Fprintf(&b, " — no admin.toml in %s, so this project has no admin commands", wd)
		}
	}

	fmt.Fprintf(&b, "\nto create a branch or worktree named %q: admin branch %s | admin worktree %s", name, name, name)
	return fmt.Errorf("%s", b.String())
}

// registerManifestCommand resolves the project manifest at the current
// working directory and, if it declares a command named name, adds a cobra
// command for it to rootCmd (so the normal SetArgs/Execute flow below runs
// it) and returns true.
//
// It returns false — meaning "this name is not a project command" — in two
// cases, both of which leave the caller to report an unknown command: there
// is no admin.toml here at all, or the manifest resolves fine and simply
// doesn't declare that name.
//
// The third case returns true without the name being a command: the manifest
// EXISTS but fails to parse or resolve. That used to be silently treated as
// "no match", which sent every bare argument in a broken-manifest project
// straight to branch shorthand — admin-3rgp.52, which mutated git state with
// no warning the manifest was even invalid. It registers an error command
// under name whose RunE reports the manifest problem, so a broken manifest is
// never reported as a typo.
func registerManifestCommand(name string) bool {
	wd, err := os.Getwd()
	if err != nil {
		return false
	}
	m, ok, err := resolveProjectManifest(context.Background(), wd)
	if !ok {
		return false // no admin.toml at all — nothing here declares commands
	}
	if err != nil {
		// admin.toml exists but failed to parse/resolve: a hard error naming
		// the problem, never a typo report (admin-3rgp.52).
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

// runCommandCmd is admin-3rgp.61's hidden re-entry point: what the pty child
// a dashboard-hosted command dispatches into actually runs. It is the one
// caller that invokes kinds.RunCommandHosted directly — deliberately: unlike
// runActionCmd's __run-action (which reaches RunAction, a lifetime-aware
// entry point that CAN open a further herdr pane for a nested long-running
// step), RunCommandHosted's whole point is that no step it dispatches can
// open a pane, whatever that step's own kind declares. Hidden and
// double-underscore prefixed for the same reason as __run-action: plumbing,
// not something a person types, and never collidable with a project's own
// admin.toml commands.
var runCommandCmd = &cobra.Command{
	Use:                "__run-command <name> [args...]",
	Hidden:             true,
	DisableFlagParsing: true, // flags belong to the command's own steps, not to cobra
	Args:               cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Applied first, before resolveProjectManifest can log anything or
		// any step can render a style: this child's stdout is a pty for the
		// first time, and an explicit profile is what stops
		// lipgloss/termenv from issuing the OSC 11 background-color probe
		// that otherwise stalls 5s waiting for a reply the dashboard never
		// sends (admin-3rgp.61's Cause B). See HostedColorProfileEnv's doc
		// comment in internal/kinds/hostedpty.go.
		applyHostedColorProfile()

		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		// context.Background(), not cmd.Context(): see runActionCmd's own
		// comment on the same call.
		m, ok, err := resolveProjectManifest(context.Background(), wd)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("__run-command: no admin.toml found in %s", wd)
		}
		return kinds.RunCommandHosted(m, kinds.Default, args[0], args[1:])
	},
}

// applyHostedColorProfile reads the color profile the parent dashboard
// resolved (StartHostedPTY set it in the child's env) and applies it to
// this process's own termenv/lipgloss renderers before anything can render
// a style — a no-op when the vars are absent, so a plain `admin
// __run-command` invocation outside a hosted pty behaves exactly as before.
func applyHostedColorProfile() {
	rawProfile, ok := os.LookupEnv(kinds.HostedColorProfileEnv)
	if !ok {
		return
	}
	n, err := strconv.Atoi(rawProfile)
	if err != nil {
		return
	}
	profile := termenv.Profile(n)

	termenv.SetDefaultOutput(termenv.NewOutput(os.Stdout, termenv.WithProfile(profile)))
	lipgloss.SetColorProfile(profile)

	if rawDark, ok := os.LookupEnv(kinds.HostedDarkBackgroundEnv); ok {
		if dark, err := strconv.ParseBool(rawDark); err == nil {
			lipgloss.SetHasDarkBackground(dark)
		}
	}
}

func init() {
	rootCmd.AddCommand(branchCmd)
	rootCmd.AddCommand(worktreeCmd)
	rootCmd.AddCommand(prsCmd)
	rootCmd.AddCommand(populateCmd)
	rootCmd.AddCommand(runActionCmd)
	rootCmd.AddCommand(runCommandCmd)

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
