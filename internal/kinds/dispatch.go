// dispatch.go is the Go-level entry point this ticket (admin-3rgp.35) is
// scoped to drive the factories through — CLI wiring (argv shape,
// root-command identity) is admin-3rgp.7's job and is deliberately not
// here: RunCommand takes an already-resolved *manifest.Manifest (the output
// of internal/archetype.Resolve) and a command name, nothing shaped like
// os.Args.
package kinds

import (
	"fmt"
	"sort"
	"strings"

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

// currentDispatch lets a running action's Runner invoke another declared
// action by name — the multi-target kind's `action = "other"` delegation
// (ADR-0003's whole point: "one action can back multiple commands" and
// multi-target dispatches to other named actions "only possible because
// actions are first-class addressable units"). A Factory's signature is
// (name, action manifest.Action) — no Manifest, no Registry — so a target
// delegation cannot look up and run another action from inside the
// multi-target closure directly. RunAction sets this for the duration of
// the Runner it invokes, exactly the shape admin_lib/core.py's own
// run_command uses (a module-level registry a nested call reaches through),
// not a design invented for Go.
var currentDispatch func(name string, args []string) error

// RunAction dispatches one named action: resolves its kind (defaulting to
// "shell", matching admin_lib/validate.py's own `data.get("kind", "shell")"),
// looks up that kind's factory in r, builds the Runner, and invokes it with
// args. While the Runner runs, currentDispatch is set so a multi-target
// target's `action = "other"` can call back into RunAction for another step
// without needing its own Manifest/Registry reference.
func RunAction(m *manifest.Manifest, r *Registry, name string, args []string) error {
	action, ok := m.Actions[name]
	if !ok {
		return fmt.Errorf("action %q: not declared in [actions]", name)
	}
	kind, _ := actionString(action, "kind")
	if kind == "" {
		kind = "shell"
	}
	factory, ok := r.Lookup(kind)
	if !ok {
		return fmt.Errorf("action %q: unknown kind %q", name, kind)
	}
	runner, err := factory(name, action)
	if err != nil {
		return fmt.Errorf("action %q: %w", name, err)
	}

	prev := currentDispatch
	currentDispatch = func(n string, a []string) error { return RunAction(m, r, n, a) }
	defer func() { currentDispatch = prev }()

	return runner(args)
}

// RunCommand executes name's steps against m using r, in declared order,
// stopping at the first step whose Runner returns a non-nil error — the
// acceptance criteria's "a command with several steps runs them in order
// and stops at the first failure" and "a non-zero exit from an action
// produces a non-zero result" (that "non-zero result" surfaces here as a
// non-nil error; mapping it to a process exit code is CLI wiring, out of
// this ticket's scope).
func RunCommand(m *manifest.Manifest, r *Registry, name string, args []string) error {
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
	for _, step := range cmd.Steps {
		if err := RunAction(m, r, step, args); err != nil {
			return fmt.Errorf("command %q: %w", name, err)
		}
	}
	return nil
}
