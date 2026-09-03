// dispatch.go is the Go-level entry point this ticket (admin-3rgp.35) is
// scoped to drive the factories through — CLI wiring (argv shape,
// root-command identity) is admin-3rgp.7's job and is deliberately not
// here: RunCommand takes an already-resolved *manifest.Manifest (the output
// of internal/archetype.Resolve) and a command name, nothing shaped like
// os.Args.
package kinds

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/guards"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// pureRoutingKeys are the action-only fields ADR-0003
// (docs/adr/0003-commands-are-pure-routing.md) forbids on a [commands.X]
// table. manifest.toCommand already stashes anything outside the known
// Command field set into Command.Raw without raising — rejecting these is
// explicitly left to "the later validation ticket" per that function's own
// doc comment. admin-3rgp.8 (schema validation) is that ticket and is still
// open, so this dispatch entry point carries the one check its own
// acceptance criteria names ("a command carrying kind or run is rejected
// with an error naming the rule") rather than leaving RunCommand to silently
// ignore a misplaced kind/run/targets.
var pureRoutingKeys = []string{"kind", "run", "targets"}

// checkPureRouting returns an error naming ADR-0003 if cmd carries any
// action-only key, sorted for a deterministic message.
func checkPureRouting(name string, cmd manifest.Command) error {
	var bad []string
	for _, k := range pureRoutingKeys {
		if _, ok := cmd.Raw[k]; ok {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf(
		"command %q carries %s — ADR-0003: commands are pure routing "+
			"(desc + steps only); move %s to [actions.%s]",
		name, strings.Join(bad, ", "), strings.Join(bad, "/"), name,
	)
}

// resolveCommand looks up name against the merged manifest's custom
// commands first, then its (archetype + manifest-override) commands —
// mirroring admin_lib/archetypes.py::resolve's "custom shadows
// archetype-provided" rule. Menu-level concerns beyond that — hide_commands,
// group/priority ordering — belong to the Work-tab tickets (admin-3rgp.20/
// .21), not to running one named command, so they are not reproduced here.
func resolveCommand(m *manifest.Manifest, name string) (manifest.Command, bool) {
	if c, ok := m.Custom[name]; ok {
		return c, true
	}
	if c, ok := m.Overrides[name]; ok {
		return c, true
	}
	return manifest.Command{}, false
}

// runPreHook dispatches a project-declared pre-run reference — the string
// behind [docker_dev]/[clean_dev]/[vite_dev]'s `pre_kill` key
// (admin-tlhw.3) — before that dev loop's own body runs. The project names
// what to run; this function only resolves the name, exactly the way
// dispatching a command or action by name works everywhere else. An empty
// name means the project declared no hook at all, which is a no-op: "a
// project that declares neither behaves correctly: no pre-run hook fires."
// A non-empty name that resolves to neither a command nor an action is also
// a no-op here — admin check's checkPreRunRefs (cmd/check.go) is what
// reports that as a validation error at check time, not the dispatch path,
// which must never guess or fall back to a name of its own.
func runPreHook(m *manifest.Manifest, surf *Surface, name string, args []string, warn func(string)) {
	if name == "" {
		return
	}
	if _, ok := resolveCommand(m, name); ok {
		if err := surf.Command(name, args); err != nil {
			warn(name + ": " + err.Error())
		}
		return
	}
	if _, ok := m.Actions[name]; ok {
		if err := RunAction(m, Default, name, args); err != nil {
			warn(name + ": " + err.Error())
		}
	}
}

// HasCommand reports whether m declares a command named name (custom or
// archetype-provided) — the read-only half of resolveCommand exposed for
// callers outside this package that only need to know whether a name would
// dispatch, not run it. cmd.dispatchArgs (admin-3rgp.7) is the first real
// caller: it needs this to decide, for a bare CLI argument, whether the
// project's admin.toml claims that name — and if not, to report an unknown
// command (ADR-0014).
func HasCommand(m *manifest.Manifest, name string) bool {
	_, ok := resolveCommand(m, name)
	return ok
}

// CommandNames returns every command name m declares — custom and
// archetype-provided, de-duplicated and sorted. It exists so the CLI can name
// what a project actually offers when a typed word matches nothing: an
// unknown command is a dead end, and the list of live ones is the whole
// remedy. The set itself is m.CommandNames()'s — derived, not walked a
// second time — so the two cannot drift out of agreement.
func CommandNames(m *manifest.Manifest) []string {
	seen := m.CommandNames()
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Surface lets a running action's Runner invoke another declared action or
// command by name — the multi-target kind's `action = "other"` delegation
// (ADR-0003's whole point: "one action can back multiple commands" and
// multi-target dispatches to other named actions "only possible because
// actions are first-class addressable units"), the keybinding hooks in
// interactive-shell and bridge-listen, and the pre-kill hop in
// docker-dev/vite-dev/clean-dev. A Factory's other parameters are (name,
// action manifest.Action, m *manifest.Manifest) — no Registry, no entry
// point — so a Runner cannot look those up itself; runAction builds a
// Surface immediately before calling the factory and passes it in, so each
// Runner closes over the surface belonging to its own dispatch rather than
// reaching through shared process state.
//
// Both halves are built together because both must name the SAME surface: a
// hosted run whose step delegates one level down must stay hosted whether it
// delegates to an action or to a command. Two independent values would let
// one be set without the other, which is exactly the drift this struct
// removes rather than guards.
type Surface struct {
	action  func(name string, args []string) error
	command func(name string, args []string) error
}

// Action runs another declared action from inside a running Runner, on s's
// own surface. Every inline dispatch — the hosted pty child, a nested
// delegation — routes through RunAction, which never reaches the launcher;
// only launchCommand, reached from RunCommand's top-level dispatch before any
// Runner is invoked, does that, once, whichever producer asked for it.
//
// A nil receiver is legal in Go and is exactly the case where a factory's
// Runner is invoked directly, outside any dispatch — s.Action still returns
// the same error in that case.
func (s *Surface) Action(name string, args []string) error {
	if s == nil {
		return fmt.Errorf(
			"action %q: dispatching a nested action requires running through "+
				"kinds.RunAction/RunCommand rather than invoking a factory's Runner directly", name)
	}
	return s.action(name, args)
}

// Command is Action's command-level twin, for a Runner that needs to run a
// whole declared command — the pre-run hop (runPreHook), which runs
// whatever command the project named in `pre_kill` before starting a dev
// loop.
func (s *Surface) Command(name string, args []string) error {
	if s == nil {
		return fmt.Errorf(
			"command %q: dispatching a nested command requires running through "+
				"kinds.RunAction/RunCommand rather than invoking a factory's Runner directly", name)
	}
	return s.command(name, args)
}

// RunAction dispatches one named action: resolves its kind (defaulting to
// "shell", matching admin_lib/validate.py's own `data.get("kind", "shell")"),
// looks up that kind's factory in r, builds a Surface, and invokes the
// factory with it so a multi-target target's `action = "other"` can call
// back into RunAction for another step without needing its own
// Manifest/Registry reference.
//
// Every non-multi-target action is wrapped with its own `requires` check
// before the Runner fires — the Go port of admin_lib/interp.py's "wraps
// each flat action with a run_requires([...]) check" (ADR-0005). A
// kind="multi-target" action is deliberately excluded here: its `requires`
// is only the *default* the picked target inherits, and which target gets
// picked isn't known until the Runner itself runs, so the multi-target
// factory resolves and checks per-target requires inside its own closure
// (internal/kinds/multitarget.go) instead.
func RunAction(m *manifest.Manifest, r *Registry, name string, args []string) error {
	action, ok := m.Actions[name]
	if !ok {
		return fmt.Errorf("action %q: not declared in [actions]", name)
	}
	// guards.Register is idempotent (it just re-adds the same shell-backed
	// funcs to the registry), so registering m's user-defined guards on
	// every RunAction call — rather than needing a separate one-time
	// "interpreter setup" phase, which the Go CLI doesn't have yet
	// (admin-3rgp.7) — keeps every requires check below reachable without
	// requiring a caller to remember a setup step.
	guards.Register(m.Guards)
	kind, _ := actionString(action, "kind")
	if kind == "" {
		kind = "shell"
	}
	factory, ok := r.Lookup(kind)
	if !ok {
		return fmt.Errorf("action %q: unknown kind %q", name, kind)
	}

	if kind != "multi-target" {
		reqs, err := guards.RequiresOf(action)
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if err := guards.RunRequires(reqs); err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
	}

	surf := &Surface{
		action:  func(n string, a []string) error { return RunAction(m, r, n, a) },
		command: func(n string, a []string) error { return runSteps(m, r, n, a) },
	}

	runner, err := factory(name, action, m, surf)
	if err != nil {
		return fmt.Errorf("action %q: %w", name, err)
	}

	return runner(args)
}

// CommandProducer distinguishes a command dispatch whose producer cannot
// host the run itself from one that can. A nil *CommandProducer passed to
// RunCommand means "this process would otherwise host the run inline" — the
// plain CLI, which already owns a terminal — but that no longer exempts it
// from `detach`: both producers resolve the command's own launcher/detach
// declaration once, up front, in launchCommand, and either dispatch through
// the project's declared [terminal] launcher or fall back to running inline
// (the CLI) / Launch (the dashboard's hosted pty).
//
// A non-nil producer is the dashboard: it cannot run a Command's step loop
// on its own terminal at all (that would block its alt-screen for the run's
// whole duration), so its fallback is Launch rather than an inline run.
type CommandProducer struct {
	// Launch runs name (with args) on the producer's own default surface —
	// StartHostedPTY for the dashboard — when the command's first step
	// declares no `detach`, or the manifest declares no [terminal] launcher.
	Launch func(m *manifest.Manifest, name string, args []string) error
	// Env carries extra KEY=VALUE pairs the producer would have handed its
	// own launcher — a row-scoped key binding's ADMIN_ROW_KIND/ADMIN_ROW_ID/
	// ADMIN_ROW_URL (internal/tui/rowkeys.go's rowEnv) — since the dashboard
	// process holds these as an explicit env slice, not in its own
	// os.Environ(). launchCtxFrom reads it; nil behaves like the plain-CLI
	// case (fall back to the process environment).
	Env []string
}

// RunCommand is the single command-dispatch entry point. Steps run in
// declared order and stop at the first one whose Runner returns a non-nil
// error — the acceptance criteria's "a command with several steps runs them
// in order and stops at the first failure" and "a non-zero exit from an
// action produces a non-zero result" (that "non-zero result" surfaces here
// as a non-nil error; mapping it to a process exit code is CLI wiring).
//
// Two cases, checked in this order:
//
//  1. inReentry() — every step runs inline through runSteps/RunAction, and
//     `detach` is never consulted. This is __run-command's own RunCommand
//     call (cmd/root.go): a process already inside a launched dispatch must
//     never reach a second launcher, however many levels of delegation
//     separate it from the dispatch that opened that run.
//  2. Otherwise — a plain-CLI dispatch (p == nil) or a dashboard dispatch (p
//     non-nil) alike — launchCommand resolves the command's own
//     launcher/detach declaration once, up front, and either dispatches to
//     the project's declared [terminal] launcher or falls back to running
//     the command on this process's own surface: inline (runSteps) for the
//     CLI, p.Launch (the hosted pty) for the dashboard.
func RunCommand(m *manifest.Manifest, r *Registry, name string, args []string, p *CommandProducer) error {
	if inReentry() {
		return runSteps(m, r, name, args)
	}
	var env []string
	fallback := func() error { return runSteps(m, r, name, args) }
	if p != nil {
		env = p.Env
		fallback = func() error { return p.Launch(m, name, args) }
	}
	return launchCommand(m, name, args, env, fallback)
}

// launchCommand is RunCommand's launcher-resolving half: reached whenever
// this process is not already inside a launched dispatch, for both the
// plain CLI and the dashboard. It resolves name once, up front — unlike the
// old CLI-only shape, which let runSteps discover an unknown command or an
// ADR-0003 pure-routing violation mid-dispatch — because a dispatch that
// ends up spawning through the launcher has no runSteps call on this path to
// catch that: dispatchToLauncher is a distinct spawn (an exec, a pty), not
// this process running the command's own step loop. A typo'd or malformed
// command must still be reported here rather than silently spawning
// nothing, or (for the CLI) silently falling back to fallback with no error
// at all.
//
// env carries the extra ADMIN_ROW_* pairs a row-bound dashboard dispatch
// would have handed its own launcher (CommandProducer.Env) — nil for the
// CLI, which has no such row context and falls back to the process
// environment (launchCtxFrom). fallback runs the command on the caller's own
// default surface when no launcher dispatch happens: runSteps for the CLI,
// p.Launch for the dashboard.
//
// The launcher decision looks only at the command's FIRST step's action,
// keyed by cmd.Steps[0] — the same key StartHostedPTY uses — so
// internal/tui/work.go's liveEntryFor resolves the same row whichever
// surface the command actually landed on, and `a` (attach) finds it either
// way.
//
// When that first step is itself kind="multi-target", its own top-level
// `detach` is only the default a picked target inherits — the effective
// value belongs to the picked target, and dispatchDetach (detach.go)
// resolves parent-default-plus-target-override the same way
// multitarget.go's own Runner does, from args[0] (splitTargetArg). A
// multi-target command dispatched with no target argument can't have its
// effective detach known up front in general — the Runner itself raises
// the target picker once it runs — unless the action declares exactly one
// target, which the Runner auto-picks deterministically; dispatchDetach
// resolves that case the same way. Otherwise dispatchDetach reports the
// dispatch as unresolvable and this function falls through to fallback,
// same as `detach` being absent.
func launchCommand(m *manifest.Manifest, name string, args []string, env []string, fallback func() error) error {
	cmd, ok := resolveCommand(m, name)
	if !ok {
		return fmt.Errorf("unknown command %q", name)
	}
	if err := checkPureRouting(name, cmd); err != nil {
		return err
	}
	if len(cmd.Steps) == 0 {
		return fmt.Errorf("command %q has no steps", name)
	}

	entryID := cmd.Steps[0]
	if action, ok := m.Actions[entryID]; ok {
		if detach, ok := dispatchDetach(action, args); ok && detach {
			cfg, _ := config.FromManifest(m)
			if cfg.Terminal.Spawn != "" {
				ctx := launchCtxFrom(name, env)
				root := projectRoot(m)
				handle, err := dispatchToLauncher("__run-command", name, args, ctx, cfg.Terminal, root)
				if err == nil {
					kind, _ := actionString(action, "kind")
					recordLiveWork(entryID, kind, handle, "__run-command", name, args, ctx, cfg.Terminal, root)
					return nil
				}
				// A launcher IS configured but spawning through it failed —
				// degrade to the caller's own default surface with one
				// warning line, rather than aborting the command outright.
				fmt.Fprintf(os.Stderr, "warning: command %q: launcher spawn failed, running on the default surface: %v\n", name, err)
			}
		}
	}
	return fallback()
}

// runSteps is the body an inline run shares with itself for every step —
// reached from RunCommand directly when already inReentry, and from
// launchCommand's fallback when the CLI dispatches a command that carries no
// `detach` (or has nowhere to send it): the global preflight —
// guards.RunGlobalPreflight, the always-on deps-fresh check ADR-0005
// requires before every command, skipped when the command itself declares
// `preflight = false` (admin-tlhw.3 — no name is hardcoded here) and
// memoized so a multi-step command pays for it once per invocation, not
// once per step — plus the ADR-0003 pure-routing check, the empty-steps
// check, declared-order execution, stop-at-first-failure, and the error
// wrapping that names the command. Every step dispatches through RunAction,
// which never reaches the launcher.
func runSteps(m *manifest.Manifest, r *Registry, name string, args []string) error {
	cmd, ok := resolveCommand(m, name)
	if !ok {
		return fmt.Errorf("unknown command %q", name)
	}
	exempt := cmd.HasPreflight && !cmd.Preflight
	if err := guards.RunGlobalPreflight(exempt); err != nil {
		return err
	}
	if err := checkPureRouting(name, cmd); err != nil {
		return err
	}
	if len(cmd.Steps) == 0 {
		return fmt.Errorf("command %q has no steps", name)
	}
	nameCommandLog(m, name, cmd)
	for _, step := range cmd.Steps {
		if err := RunAction(m, r, step, args); err != nil {
			return fmt.Errorf("command %q: %w", name, err)
		}
	}
	return nil
}

// nameCommandLog is the Go port of admin_lib/interp.py's _make_step_runner
// opening block: every command names a log unconditionally, before its
// first step runs — admin-rc82's fix. Go had no equivalent of this call at
// all, so a command whose step bodies never called set_log_context
// themselves (directly, or via a multi-target target or a framed action)
// wrote no log at all.
//
// This only NAMES the log (deferred, on the process-wide luahost.Process()
// every kind-factory Host shares — see internal/kinds/helpers.go's
// newHost). Nothing is created here: the first step's first Info/Ok/Warn/
// Err call, or a call to MaterializeLogFile, is what actually materializes
// it. A step's own kind factory can still refine the name before that
// happens — SetLogContext/SetLogContextPath are no-ops only once the
// context has materialized, so whichever call is still pending at first use
// (typically the innermost — a multi-target target, a framed action) wins,
// exactly as in the Python original.
func nameCommandLog(m *manifest.Manifest, name string, cmd manifest.Command) {
	// The project root, not the cwd: LogDir is "<root>/tmp" (or the
	// [logging].dir under it), and rooting it at the invocation directory put
	// a run's log under whatever subdirectory the command was typed in — a
	// different file per directory for the same command, and none of them
	// where `admin logs` looks.
	ctx := luahost.Process()
	ctx.Configure(projectRoot(m), m.Logging)

	retain := luahost.RetainUnset
	if cmd.HasRetain {
		retain = cmd.LogRetain
	}

	switch {
	case cmd.HasLog && logIsFalse(cmd.Log):
		// Python's _make_step_runner (admin_lib/interp.py:855) is a bare
		// `pass` here: `log = false` means this command names no context,
		// not that logging is switched off process-wide. Calling
		// SetEnabled(false) instead let a nested `log = false` command —
		// reached through dispatch.go:217's surf.Command hop — silence the
		// outer command's logging for the rest of the run. Go has no
		// tmp/run.log fallback, so for a top-level command "name nothing"
		// and "disable" produce the same empty result anyway.
	case cmd.HasLogFile:
		ctx.SetLogContextPath(cmd.LogFile, retain)
	default:
		ctx.SetLogContext([]string{name}, retain)
	}
}

// logIsFalse reports whether v — manifest.Command.Log's untyped `log` value
// — is the boolean false, mirroring Python's `log_cfg.get("log") is False`
// (a string value, e.g. a log-source name, is never "false").
func logIsFalse(v any) bool {
	b, ok := v.(bool)
	return ok && !b
}
