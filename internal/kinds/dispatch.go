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

	"github.com/McBrideMusings/admin-project-tool/internal/guards"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/shim"
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
// own surface. A CLI run's surface routes through RunActionRouted, so a
// nested long-running action still earns its own pane; a hosted run's routes
// through runActionInline, so it cannot. A Runner naming RunActionRouted
// directly would decide that question for its caller and would put
// dispatchToPane back on the hosted entry point's call graph.
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
// whole declared command — the pre-kill hop, which runs the project's own
// "kill" command before starting a dev loop.
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
	return runAction(m, r, name, args, RunActionRouted)
}

// runActionInline is RunAction's pane-free twin: it runs the named action's
// Runner in this process exactly as RunAction does, and builds the Surface
// it passes to the factory from *itself* so a multi-target target's
// `action = "other"` delegation stays inline too. That nested hop is the one
// call-graph hole a shared step loop alone would leave open — RunAction
// builds its Surface from RunActionRouted, so a hosted run whose step
// delegates one level down would otherwise reach the pane path after all.
func runActionInline(m *manifest.Manifest, r *Registry, name string, args []string) error {
	return runAction(m, r, name, args, runActionInline)
}

// runAction is the body both inline entry points share. nested is what the
// Surface built for the Runner resolves to for the duration of this action —
// the single behavioural difference between RunAction and runActionInline.
func runAction(m *manifest.Manifest, r *Registry, name string, args []string, nested stepDispatch) error {
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
		action:  func(n string, a []string) error { return nested(m, r, n, a) },
		command: func(n string, a []string) error { return runSteps(m, r, n, a, nested) },
	}

	runner, err := factory(name, action, m, surf)
	if err != nil {
		return fmt.Errorf("action %q: %w", name, err)
	}

	return runner(args)
}

// RunActionRouted is admin-3rgp.17's lifetime-aware entry point: what the
// plain-CLI producer dispatches with. RunCommand hands it to the shared step
// loop, and RunAction builds the Surface it hands the factory from it, so a
// multi-target target delegating to another action (`action = "other"`)
// routes exactly the same way a top-level command step does.
//
// It is deliberately NOT what every dispatch should use. Which surface a
// dispatch lands on is a property of the producer: the dashboard hosts its
// own commands and dispatches through RunCommandHosted, from which this
// function is unreachable.
//
// When name's declared kind is Lifetime: LongRunning (internal/kinds
// catalog.go) and a herdr pane is available (internal/shim.Available()), it
// dispatches into a new pane and returns immediately — the action's own
// Runner never executes in this process at all; it executes in the pane,
// reached via the hidden "__run-action" re-entry command. That pane's ID is
// recorded in internal/livework's process-wide registry.
//
// In every other case it falls through to RunAction and runs inline, in
// the current frame:
//
//   - A Transient kind, or an undeclared action (RunAction below produces
//     the right "not declared" error for that) — never reached the pane
//     path at all.
//   - A LongRunning kind with shim.ErrHerdrUnavailable — herdr simply
//     isn't usable here (HERDR_ENV unset, or no herdr on PATH). This is
//     the default path in practice and stays completely silent: no
//     warning, nothing printed, nothing logged. Every manifest without
//     HERDR_ENV=1 must see zero behaviour change from before this ticket.
//   - A LongRunning kind where herdr IS available but the split itself
//     failed — daemon not running, workspace gone, split refused
//     (admin-3rgp.46). This is a genuine failure worth knowing about, but
//     "the multiplexer hiccuped" must never be the reason a dev loop dies:
//     it prints exactly one warning line naming the failure, then runs
//     inline exactly like the silent-fallback case.
func RunActionRouted(m *manifest.Manifest, r *Registry, name string, args []string) error {
	if action, ok := m.Actions[name]; ok && actionLifetime(action) == LongRunning {
		kind, _ := actionString(action, "kind")
		paneID, err := dispatchToPane(name, args)
		switch {
		case err == nil:
			recordLiveWork(name, kind, paneID, args)
			return nil
		case err == shim.ErrHerdrUnavailable:
			// Herdr isn't usable here at all — fall through to the inline
			// path silently; this is the default path in practice.
		default:
			// Herdr is available but the split itself failed — degrade to
			// inline with one warning line rather than aborting the
			// action outright (admin-3rgp.46).
			fmt.Fprintf(os.Stderr, "warning: action %q: herdr split failed, running inline: %v\n", name, err)
		}
	}
	return RunAction(m, r, name, args)
}

// actionLifetime resolves name's declared kind (defaulting to "shell",
// matching RunAction's own default) and returns its catalog Lifetime,
// Transient for an undeclared kind — RunAction is what reports "unknown
// kind" as a real error; this helper only decides routing and must never be
// the place that surfaces that error.
func actionLifetime(action manifest.Action) Lifetime {
	kind, _ := actionString(action, "kind")
	if kind == "" {
		kind = "shell"
	}
	k, ok := Declared(kind)
	if !ok {
		return Transient
	}
	return k.Lifetime
}

// RunCommand is the plain-CLI entry point: it runs name's steps through the
// shared loop below with the lifetime-aware router, so a step whose kind is
// LongRunning opens a herdr pane when one is available. Steps run in
// declared order and stop at the first one whose Runner returns a non-nil
// error — the acceptance criteria's "a command with several steps runs them
// in order and stops at the first failure" and "a non-zero exit from an
// action produces a non-zero result" (that "non-zero result" surfaces here
// as a non-nil error; mapping it to a process exit code is CLI wiring).
//
// The dashboard wants the opposite surface and calls RunCommandHosted.
func RunCommand(m *manifest.Manifest, r *Registry, name string, args []string) error {
	return runSteps(m, r, name, args, RunActionRouted)
}

// RunCommandHosted is admin-3rgp.59's second entry point: the same command,
// the same shared step loop, but every step goes straight to the inline
// action runner. dispatchToPane is not on its call graph at any depth — not
// through the step loop (which calls runActionInline, never
// RunActionRouted) and not through a multi-target target's `action =
// "other"` delegation (runActionInline builds its Surface from itself). So
// no step can open a herdr pane regardless of its kind's Lifetime or
// whether herdr is available.
//
// This exists because which surface a dispatch lands on is a property of
// the *producer*, not of the action's kind. Three producers dispatch work: a
// Command run from the dashboard, Start Work on an issue, and a workflow's
// agent worker. The last two want a pane; the first is hosted by the
// dashboard itself and must never take one. A flag on RunCommand would
// leave the pane path reachable from the dashboard, guarded only by a field
// being set right — a separate entry point removes the condition instead of
// guarding it.
//
// Lifetime consequently narrows to exactly one meaning: whether a plain-CLI
// dispatch opens a pane. RunCommandHosted never consults it.
func RunCommandHosted(m *manifest.Manifest, r *Registry, name string, args []string) error {
	return runSteps(m, r, name, args, runActionInline)
}

// stepDispatch is how one step (or one nested delegation) is dispatched —
// the single thing that differs between the two command entry points.
type stepDispatch func(m *manifest.Manifest, r *Registry, name string, args []string) error

// runSteps is the body RunCommand and RunCommandHosted share: the global
// preflight — guards.RunGlobalPreflight, the always-on deps-fresh check
// ADR-0005 requires before every command, skipped for the exempt diagnostic
// verbs (logs, kill) and memoized so a multi-step command pays for it once
// per invocation, not once per step — plus the ADR-0003
// pure-routing check, the empty-steps check, declared-order execution,
// stop-at-first-failure, and the error wrapping that names the command.
func runSteps(m *manifest.Manifest, r *Registry, name string, args []string, dispatch stepDispatch) error {
	if err := guards.RunGlobalPreflight(name); err != nil {
		return err
	}
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
	nameCommandLog(m, name, cmd)
	for _, step := range cmd.Steps {
		if err := dispatch(m, r, step, args); err != nil {
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
	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	ctx := luahost.Process()
	ctx.Configure(root, m.Logging)

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
