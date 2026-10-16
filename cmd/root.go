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
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/McBrideMusings/admin-project-tool/internal/create"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/shim"
	"github.com/McBrideMusings/admin-project-tool/internal/supervise"
	"github.com/McBrideMusings/admin-project-tool/internal/timing"
	"github.com/McBrideMusings/admin-project-tool/internal/tui"
	"github.com/McBrideMusings/admin-project-tool/internal/worktree"
)

// Flags for --dump-frame, the headless render. They exist so a change can be
// checked at the surface with one command: driving the live TUI needs a pty, and
// piping a keystroke into it races Bubble Tea's first read and hangs.
var (
	dumpFrame   bool
	dumpWidth   int
	dumpHeight  int
	dumpKeys    []string
	dumpSettle  time.Duration
	dumpFixture string

	// compiledInfoFlag backs --compiled-info: ADR-0006's frozen-manifest
	// hazard, made visible from the artifact itself rather than left to a
	// rule a human has to remember. See runCompiledInfo (cmd/compile.go).
	compiledInfoFlag bool

	// worktreeFlag is -w/--worktree's raw value, registered on rootCmd purely
	// so it shows in --help and cobra's own flag parsing doesn't choke on it
	// reaching a command with DisableFlagParsing unset (e.g. `admin --help
	// -w`). The value cobra parses here is never read — Execute strips a
	// literal -w/--worktree out of argv by hand (stripWorktreeFlag) before
	// cobra ever sees it, for the same reason stripTimingFlag exists: a
	// manifest command runs with DisableFlagParsing, so an un-stripped -w
	// would reach the step as one of its own arguments instead of being
	// consumed here.
	worktreeFlag string
)

// worktreeTarget is admin-76tv's single re-target point: nil for an ordinary
// invocation (every os.Getwd() call site below behaves exactly as before),
// or the -w/--worktree flag's resolved directory once resolveWorktreeFlag has
// run. startDir() is what every root-resolution call site in this file reads
// instead of calling os.Getwd() itself, so a worktree target reaches every
// one of them — admin.toml lookup, the manifest's Root (and so every
// subprocess cwd, the tmp/ log dir, ${VAR} resolution — internal/kinds.
// projectRoot), and the dashboard's own repo/manifest roots — without
// threading a parameter through each of their callers individually.
var worktreeTarget *worktree.Target

// worktreePrimaryRoot is the primary checkout of the repo worktreeTarget was
// resolved against, captured alongside it. cmd/manifest.go's
// resolveProjectManifestFromDisk falls back to this checkout's admin.toml
// when the target itself has none — a fresh git worktree usually won't,
// since admin.toml is never committed (see internal/worktree.
// ManifestContentRoot) — while still executing every step inside the target,
// never inside this checkout.
var worktreePrimaryRoot string

// startDir is where every project-root resolution in this package starts:
// worktreeTarget's resolved path when -w was given, else the process's own
// working directory. Every call site cmd/root.go's own doc comment names as
// a root-resolution candidate (unknownCommandError, registerManifestCommand,
// runActionCmd, runCommandCmd, dashboardManifestRoot) calls this instead of
// os.Getwd() directly, which is what makes -w's effect reach every one of
// them from this single function.
func startDir() (string, error) {
	if worktreeTarget != nil {
		return worktreeTarget.Path, nil
	}
	return os.Getwd()
}

// effectiveRepoRoot is git.RepoRoot of startDir: the *main* worktree of
// whatever repo startDir lives in (see internal/git/git.go), so `admin -w
// <target>` with no further args opens the dashboard against the target's
// repo rather than the invoking shell's.
func effectiveRepoRoot(ctx context.Context) (string, error) {
	dir, err := startDir()
	if err != nil {
		return "", err
	}
	return git.RepoRoot(ctx, dir)
}

// cwdRepoRoot is git.RepoRoot of the process's own working directory. The
// tool verbs that act on the invoking shell's repo (branch, worktree,
// populate, archetypes) read this, not effectiveRepoRoot: -w re-targets the
// manifest and the dashboard, not them.
func cwdRepoRoot(ctx context.Context) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return git.RepoRoot(ctx, cwd)
}

// stripWorktreeFlag removes a -w/--worktree flag and its value from args
// (in every form cobra itself would accept: "-w x", "--worktree x",
// "-w=x", "--worktree=x") before dispatchArgs or cobra's own parser ever see
// them — the same reason stripTimingFlag exists, and for the same root
// cause: a manifest command's DisableFlagParsing means cobra would otherwise
// hand -w straight to the step as one of its own arguments. found is false
// when neither form appears, in which case value is meaningless.
func stripWorktreeFlag(args []string) (out []string, value string, found bool) {
	out = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-w" || a == "--worktree":
			if i+1 >= len(args) {
				// No value follows — leave it in place so cobra's own flag
				// parser reports the missing argument rather than this
				// function silently swallowing a malformed invocation.
				out = append(out, a)
				continue
			}
			value, found = args[i+1], true
			i++
		case strings.HasPrefix(a, "--worktree="):
			value, found = strings.TrimPrefix(a, "--worktree="), true
		case strings.HasPrefix(a, "-w="):
			value, found = strings.TrimPrefix(a, "-w="), true
		default:
			out = append(out, a)
		}
	}
	return out, value, found
}

// resolveWorktreeFlag resolves -w/--worktree's value against the worktrees of
// the repo containing the invoking shell's own working directory — real
// os.Getwd(), the one and only time this file calls it directly, since
// nothing has set worktreeTarget yet and internal/worktree.Resolve needs to
// know which repo's `git worktree list` to read. On success it records both
// package vars startDir/effectiveRepoRoot/resolveProjectManifestFromDisk
// read from then on. On failure (ambiguous or no match) it returns the error
// as-is: *worktree.AmbiguousError and *worktree.NotFoundError already format
// every candidate, one per line, and main.go prints an Execute error
// verbatim to stderr before exiting non-zero.
func resolveWorktreeFlag(ctx context.Context, value string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	t, err := worktree.Resolve(ctx, cwd, value)
	if err != nil {
		return err
	}
	if abs, absErr := filepath.Abs(t.Path); absErr == nil {
		t.Path = abs
	}
	worktreeTarget = &t
	if primary, perr := git.RepoRoot(ctx, cwd); perr == nil {
		worktreePrimaryRoot = primary
	}
	return nil
}

var rootCmd = &cobra.Command{
	Use: "admin",
	// "repo" is a cobra alias, not a second identity: ADR-0011 §1. It exists
	// only because `repo completion zsh` is wired into the user's .zshrc, and
	// may be deleted once nothing invokes it under that name.
	Aliases:       []string{"repo"},
	Short:         "Terminal dashboard for the git repo you're in, and the project's admin.toml runner",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if compiledInfoFlag {
			return runCompiledInfo(cmd)
		}
		if dumpFixture != "" && !dumpFrame {
			return fmt.Errorf("--fixture only applies to --dump-frame")
		}
		if dumpFrame {
			return runDumpFrame(cmd.Context(), "")
		}
		return runDashboard(cmd.Context(), "")
	},
}

// Execute runs the root command. It first validates the kind catalog is
// fully covered by registered factories (internal/kinds) — this makes an
// incomplete catalog a clean startup error in addition to the init-time
// panic factories.go raises, and gives that check a path reachable without
// killing the process.
//
// Timing is enabled here, before dispatchArgs, because dispatchArgs is
// where manifest find/parse/resolve actually run (registerManifestCommand
// -> resolveProjectManifest) — enabling it any later would miss those
// phases entirely. --timing is stripped out of the argv handed to
// rootCmd.SetArgs for the same reason dispatchArgs' own early return
// matters: a manifest command uses DisableFlagParsing, so an un-stripped
// --timing would be passed straight through to the step as one of its own
// arguments instead of being consumed here.
func Execute() error {
	supervise.Enable()
	if err := kinds.Validate(); err != nil {
		return err
	}
	args, timingRequested := stripTimingFlag(os.Args[1:])
	if timingRequested || timing.EnvRequests() {
		timing.Enable()
	}
	defer timing.Total()

	args, wtValue, wtFound := stripWorktreeFlag(args)
	if wtFound {
		if err := resolveWorktreeFlag(context.Background(), wtValue); err != nil {
			return err
		}
	}

	rootCmd.SetArgs(dispatchArgs(args))
	return rootCmd.ExecuteContext(context.Background())
}

// stripTimingFlag removes a literal "--timing" argument from args (it is
// never a value cobra's flag parser needs to see — DisableFlagParsing on
// every manifest command means cobra never gets a chance to consume it
// itself) and reports whether it was present.
func stripTimingFlag(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == "--timing" {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

// dispatchArgs resolves a bare first argument in two steps:
//
//  1. Any verb registered on rootCmd, aliases and cobra's help/completion
//     included — handled by the loop below. reservedCmdVerbs reads the same
//     set, so admin check rejects a manifest command it would shadow.
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
		Annotations:        map[string]string{dynamicCommand: ""},
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

	wd, wdErr := startDir()
	if wdErr == nil {
		if m, ok, err := resolveProjectManifest(context.Background(), wd); ok && err == nil {
			if names := kinds.CommandNames(m); len(names) > 0 {
				fmt.Fprintf(&b, " — admin.toml declares: %s", strings.Join(names, ", "))
			} else {
				b.WriteString(" — admin.toml declares no commands")
			}
		} else if !ok {
			fmt.Fprintf(&b, " — no admin.toml in %s or any directory above it, so this project has no admin commands", wd)
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
// is no admin.toml at or above the cwd at all, or the manifest resolves fine and simply
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
	wd, err := startDir()
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
			Annotations:        map[string]string{dynamicCommand: ""},
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
		Annotations:        map[string]string{dynamicCommand: ""},
		RunE: func(cmd *cobra.Command, cmdArgs []string) error {
			// AnnounceEnd's defer fires on both a normal return and an
			// error return, so one registration covers the explicit
			// call and the safety net.
			defer luahost.Process().AnnounceEnd()
			return kinds.RunCommandInline(m, kinds.Default, name, cmdArgs)
		},
	})
	return true
}

// runActionCmd is admin-3rgp.17's hidden re-entry point: what a herdr pane
// a long-running kind dispatched into actually runs. It invokes
// kinds.RunAction, which — like every dispatch reached from the plain CLI or
// from inside an already-launched run — executes exactly the one named
// action's Runner in this process, with no further lifetime routing, so the
// pane does the real work instead of trying to open a second pane for
// itself. Hidden
// because it is plumbing, not something a person types; double-underscore
// prefixed so a project's own admin.toml commands (which would otherwise
// need "reserved name" handling like the tool verbs get) can never collide
// with it in practice.
//
// kinds.MarkReentry() runs before anything else: this verb IS by definition
// a re-entry into an already-launched dispatch, so the fact is asserted
// here rather than depending on the launched process having inherited an
// out-of-band signal from its parent. It is what keeps a nested delegation
// reached from inside this action (a multi-target target's `action =
// "other"` hop) from trying to reach a second launcher from inside the
// first (admin-ysxr.4/.7).
var runActionCmd = &cobra.Command{
	Use:                "__run-action <name> [args...]",
	Hidden:             true,
	DisableFlagParsing: true, // flags belong to the action's own steps, not to cobra
	Args:               cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// See registerManifestCommand's RunE comment: AnnounceEnd's defer
		// covers this verb's own outermost dispatch boundary.
		defer luahost.Process().AnnounceEnd()
		kinds.MarkReentry()
		// No command runs here, so the action itself owns the log.
		luahost.Process().ClaimOwner(args[0])
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
			return fmt.Errorf("__run-action: no admin.toml in %s or any directory above it", wd)
		}
		return kinds.RunAction(m, kinds.Default, args[0], args[1:])
	},
}

// runCommandCmd is admin-3rgp.61's hidden re-entry point: what the pty child
// a dashboard-hosted command dispatches into actually runs, and (as of
// admin-ysxr.4) what a window-locale Command's launcher dispatch re-enters into
// as well. It calls kinds.RunCommandInline — `locale` is dashboard-producer-only
// again (admin-wwrg), so this re-entry point never consults it either way.
// kinds.MarkReentry() below is kept regardless: nested dispatch reached from
// inside this run (a multi-target target's `action = "other"` hop, or any
// step that dispatches a command of its own) still needs inReentry() true so
// it can never reach a second launcher, whatever RunCommand it goes through.
// Hidden and double-underscore prefixed for the same reason as __run-action:
// plumbing, not something a person types, and never collidable with a
// project's own admin.toml commands.
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
		// See registerManifestCommand's RunE comment: AnnounceEnd's defer
		// covers this verb's own outermost dispatch boundary.
		defer luahost.Process().AnnounceEnd()
		kinds.MarkReentry()

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
			return fmt.Errorf("__run-command: no admin.toml in %s or any directory above it", wd)
		}
		return kinds.RunCommandInline(m, kinds.Default, args[0], args[1:])
	},
}

// superviseCmd is the hidden watcher internal/supervise.Begin starts for each
// action run: it blocks on fd 3 (a pipe whose write end only the owning admin
// process holds) and, at EOF, kills every process carrying the run's
// ADMIN_RUN_ID. Hidden and double-underscore prefixed like __run-action.
var superviseCmd = &cobra.Command{
	Use:    "__supervise <run-id> <root> <owner-pid> <owner-pgid>",
	Hidden: true,
	Args:   cobra.ExactArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		ownerPID, err := strconv.Atoi(args[2])
		if err != nil {
			return fmt.Errorf("__supervise: owner pid %q: %w", args[2], err)
		}
		ownerPGID, err := strconv.Atoi(args[3])
		if err != nil {
			return fmt.Errorf("__supervise: owner pgid %q: %w", args[3], err)
		}
		return supervise.Watch(args[0], args[1], ownerPID, ownerPGID)
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

// colorfgbgDarkBackground classifies a terminal's background as dark or
// light from the COLORFGBG env var, without ever issuing a live OSC 11
// query. COLORFGBG holds "<fg>;<bg>" (or "<fg>;<default>;<bg>" for some
// terminals), so the background is always the last ';'-separated field.
// termenv's own HasDarkBackground converts the resolved ANSIColor to RGB
// (via its ansiHex table) and tests HSL lightness < 0.5 — indices 0-6 (the
// non-bright colors other than white) land below that line, but index 7
// ("#c0c0c0", light gray) and every index from 8 up (bright black at
// "#808080" has lightness ~0.502, and every other bright color is lighter
// still) land at or above it. So the dark set is strictly idx < 7, not
// idx < 8. When the var is unset, has no ';', or the last field doesn't
// parse as an int, this defaults to dark=true — the same value termenv
// falls back to (ANSIColor(0), black) when an OSC 11 query goes unanswered.
//
// This is not a lossless stand-in for the live query: a terminal that
// supports OSC 11 but doesn't export COLORFGBG (iTerm2, Kitty, Alacritty,
// WezTerm and Ghostty don't by default) would have its real background
// correctly detected by the query and instead gets the dark=true default
// here. Skipping the query trades that case's correctness for never
// blocking on an unanswered probe. As of this writing nothing in this repo
// consumes lipgloss.AdaptiveColor or otherwise branches on HasDarkBackground,
// so the trade has no visible effect yet — but the first caller that adds
// one should know this function is a heuristic, not termenv's query in
// disguise.
func colorfgbgDarkBackground(raw string) bool {
	sep := strings.LastIndex(raw, ";")
	if sep < 0 {
		return true
	}
	idx, err := strconv.Atoi(strings.TrimSpace(raw[sep+1:]))
	if err != nil {
		return true
	}
	return idx < 7
}

func init() {
	rootCmd.AddCommand(branchCmd)
	rootCmd.AddCommand(worktreeCmd)
	rootCmd.AddCommand(showCmd)
	rootCmd.AddCommand(populateCmd)
	rootCmd.AddCommand(runActionCmd)
	rootCmd.AddCommand(runCommandCmd)
	rootCmd.AddCommand(superviseCmd)

	// Registered purely so `admin --help` lists it — Execute strips a literal
	// --timing out of argv by hand (see stripTimingFlag) before cobra ever
	// parses flags, since every manifest command runs with
	// DisableFlagParsing and would otherwise receive it as one of the
	// step's own arguments. A manifest command still only responds to
	// ADMIN_TIMING=1 for that reason — the help string says so.
	rootCmd.PersistentFlags().Bool("timing", false, "print elapsed time per dispatch phase to stderr (also: ADMIN_TIMING=1 — required for a manifest command, since its flags are its own)")

	addDumpFrameFlags(rootCmd.Flags())
	rootCmd.Flags().BoolVar(&compiledInfoFlag, "compiled-info", false, "report which admin.toml this binary was compiled from and when, then exit")

	// --version is a flag, not a verb: a `version` subcommand would join
	// reservedCmdVerbs and shadow any admin.toml command of that name. It
	// prints "<revision> <sourceDir>" so a caller can look the revision up in
	// the checkout that holds it.
	rootCmd.Version = versionLine(buildRevision(), sourceDir)
	rootCmd.SetVersionTemplate("{{.Version}}\n")

	rootCmd.PersistentFlags().StringVarP(&worktreeFlag, "worktree", "w", "",
		"run against another git worktree instead of the cwd — a directory path, a worktree's basename, its branch, or a substring of either")
}

// addDumpFrameFlags registers --dump-frame and the flags that shape it on f.
// The bare dashboard and every tab verb (cmd/tabverb.go) share the same
// variables, so one headless render path serves them all.
func addDumpFrameFlags(f *pflag.FlagSet) {
	f.BoolVar(&dumpFrame, "dump-frame", false, "render one frame to stdout and exit (no TTY needed)")
	f.IntVar(&dumpWidth, "width", 120, "terminal width for --dump-frame")
	f.IntVar(&dumpHeight, "height", 40, "terminal height for --dump-frame")
	f.StringSliceVar(&dumpKeys, "keys", nil, "keys to press before --dump-frame takes the frame, e.g. --keys tab,tab")
	f.DurationVar(&dumpSettle, "settle", 2*time.Second, "how long --dump-frame waits for background loads")
	f.StringVar(&dumpFixture, "fixture", "", "seed live work before --dump-frame draws: "+strings.Join(tui.FrameFixtures, ", "))
	_ = f.MarkHidden("fixture")
}

// buildRevision is the commit `go build` stamped into this binary from the
// checkout it ran in — "+dirty" when that tree had uncommitted changes — so a
// caller can ask whether an installed admin carries a given fix. A build with
// no VCS stamp (go test, admin compile's temp copy) reports "unknown".
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return revisionFromSettings(info.Settings)
}

// versionLine is what `admin --version` prints: the revision, then the source
// checkout when install.sh stamped one in.
func versionLine(rev, src string) string {
	if src == "" {
		return rev
	}
	return rev + " " + src
}

func revisionFromSettings(settings []debug.BuildSetting) string {
	rev, dirty := "", false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "unknown"
	}
	if dirty {
		rev += "+dirty"
	}
	return rev
}

// dashboardManifestRoot is where the dashboard should read admin.toml from.
//
// git.RepoRoot returns the MAIN worktree from inside a linked one, which is what
// branches, worktrees and PRs want — they are repo-wide. The manifest is not:
// admin.toml is per-checkout and never committed, so a worktree that declares
// one would otherwise lose its Work tab while `admin <cmd>` in that same
// directory ran its commands fine. Resolve it from the working directory, the
// way resolveProjectManifest already does for the CLI, so the dashboard lists
// exactly the commands a bare `admin <name>` would run here.
func dashboardManifestRoot() string {
	cwd, err := startDir()
	if err != nil {
		return ""
	}
	if root := tui.FindManifestRoot(cwd); root != "" {
		return root
	}
	// -w's fallback (cmd/manifest.go's own resolveProjectManifestFromDisk
	// comment has the full rationale): the target has no admin.toml of its
	// own, so fall back to the primary checkout's for the dashboard's Work
	// tab content too, same as a plain `admin <cmd>` in the target would.
	if worktreeTarget != nil {
		if root, ok := worktree.ManifestContentRoot(cwd, worktreePrimaryRoot); ok {
			return root
		}
	}
	return ""
}

// dashboardCheckout is the top level of the checkout startDir sits in — the
// directory the dashboard's git commands run in (tui.WithCheckout). It is
// repoRoot itself unless startDir is inside a linked worktree.
func dashboardCheckout(ctx context.Context, repoRoot string) string {
	dir, err := startDir()
	if err != nil {
		return repoRoot
	}
	top, err := git.TopLevel(ctx, dir)
	if err != nil {
		return repoRoot
	}
	return top
}

// runDumpFrame prints one frame of the dashboard to stdout and exits. Unlike the
// dashboard path this writes the UI to stdout on purpose: the frame *is* the
// output, there is no cd sentinel to keep it clear of, and the repo() shell
// function passes non-sentinel stdout lines straight through.
//
// tabVerb is the verb that opened one tab alone ("" for the bare dashboard).
func runDumpFrame(ctx context.Context, tabVerb string) error {
	repoRoot, err := effectiveRepoRoot(ctx)
	if err != nil {
		return err
	}
	if dumpFixture != "" {
		if err := tui.SeedFrameFixture(dumpFixture); err != nil {
			return err
		}
	}
	// Colour is decided by probing the renderer's writer, and a captured frame is
	// going somewhere that isn't a terminal. Leave the probe on stdout so a run in
	// a terminal keeps its colours and one piped to a file comes out plain — which
	// is what a diff of two frames wants.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // winds down the watcher and the merged tick the frame abandoned

	checkout := dashboardCheckout(ctx, repoRoot)
	nwo, _ := git.OriginNWO(ctx, checkout)
	model := tui.New(ctx, repoRoot, nwo, git.CurrentBranch(ctx, checkout), git.DefaultBranch(ctx, checkout),
		dashboardOptions(ctx, checkout, nwo, tabVerb)...)
	if err := requireDrawn(model, tabVerb); err != nil {
		return err
	}
	frame := tui.Frame(ctx, model, tui.FrameOptions{
		Width:  dumpWidth,
		Height: dumpHeight,
		Keys:   dumpKeys,
		Settle: dumpSettle,
	})
	fmt.Println(frame)
	return nil
}

// tabVerb is the verb that opened one tab alone ("" for the bare dashboard).
func runDashboard(ctx context.Context, tabVerb string) error {
	// The TUI draws its own alt screen on stderr — the same stream timing
	// lines go to — so a phase line written while the program is running
	// would be wiped by Bubble Tea's next repaint. Buffer until prog.Run
	// returns, then Flush once the alt screen has torn down.
	timing.BufferUntilFlush()
	defer timing.Flush()

	start := timing.Start()
	repoRoot, err := effectiveRepoRoot(ctx)
	if err != nil {
		return err
	}
	timing.Phase("git repo root", start)

	start = timing.Start()
	checkout := dashboardCheckout(ctx, repoRoot)
	nwo, _ := git.OriginNWO(ctx, checkout)
	current := git.CurrentBranch(ctx, checkout)
	def := git.DefaultBranch(ctx, checkout)
	timing.Phase("git branch context", start)

	// lipgloss decides how many colors to use by probing its renderer's writer.
	// The TUI draws to stderr, but stdout is captured by the repo() shell function
	// ($(...)), so the default renderer (which probes stdout) sees a pipe, concludes
	// "no color", and flattens the whole UI to one color. Bind the default renderer
	// to stderr — the actual TTY — so the ANSI palette (16-color indices that the
	// terminal theme colors) renders instead of being stripped.
	renderer := lipgloss.NewRenderer(os.Stderr)
	lipgloss.SetDefaultRenderer(renderer)

	// termenv's Output.backgroundColor() always tries a live OSC 11 status
	// report first (up to a 5s timeout) and only falls back to COLORFGBG if
	// that query errors — so an unread OSC reply (e.g. inside a pty layer
	// that never answers one) stalls dashboard startup for the length of the
	// timeout. lipgloss only runs that query when a renderer's
	// explicitBackgroundColor is unset; calling SetHasDarkBackground on the
	// stderr-bound renderer here sets it explicitly, so the query never
	// fires. This mirrors applyHostedColorProfile's use of the same
	// mechanism for the hosted-pty child, and is otherwise unrelated to it.
	renderer.SetHasDarkBackground(colorfgbgDarkBackground(os.Getenv("COLORFGBG")))

	start = timing.Start()
	model := tui.New(ctx, repoRoot, nwo, current, def, dashboardOptions(ctx, checkout, nwo, tabVerb)...)
	timing.Phase("tui.New", start)
	if err := requireDrawn(model, tabVerb); err != nil {
		return err
	}

	// Render to stderr so stdout carries only the cd sentinel — the repo() shell
	// function captures stdout via $(...) while the TUI draws on the TTY (stderr).
	// Cell motion reports a click and a drag but not idle movement, which is all
	// the dashboard's own mouse support (click to act, drag to copy) needs.
	prog := tea.NewProgram(model, tea.WithContext(ctx), tea.WithAltScreen(), tea.WithOutput(os.Stderr), tea.WithMouseCellMotion(), tea.WithReportFocus())
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
		if err := git.FetchPRHead(ctx, p.RepoRoot, p.PRNum, p.Branch); err != nil {
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
	if res.PostStartLog != "" {
		fmt.Fprintf(os.Stderr, "post_start: detached, log %s\n", res.PostStartLog)
	}
}
