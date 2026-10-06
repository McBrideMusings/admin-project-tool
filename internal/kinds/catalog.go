// Package kinds is the closed, single source of truth for valid action
// kinds: what a kind is (Kinds, the catalog) and what runs it (Registry, the
// factory map).
//
// Two invariants are enforced structurally, not documented:
//
//  1. Registering a factory under a name absent from Kinds is an error
//     (Registry.Register / Registry.MustRegister).
//  2. A declared kind with no registered factory fails at startup, not at
//     dispatch (Registry.Validate, called from factories.go's init() and
//     again from cmd.Execute).
//
// Bash (kind = "shell") and Lua (kind = "lua", dispatching through
// internal/luahost) are the two archetype authoring languages (ADR-0010,
// docs/adr/0010-lua-and-bash-are-the-authoring-languages.md). No kind runs
// Python.
package kinds

import "sort"

// Kind is one action kind's validation metadata — name, whether its body is
// a config table rather than a `run` string, which modules it auto-pulls,
// and whether its factory actually reads a target map. The factory itself
// lives in the Registry rather than on the catalog record, so Kinds stays
// an immutable map.
//
// Whether a kind's dispatch reaches its own terminal instead of running
// inline is no longer a property of the kind at all — ADR-0020 moved that
// to a per-action `locale` key (internal/kinds/locale.go), which any kind
// may declare and which the tool never assumes for a project that didn't
// ask.
type Kind struct {
	Name string

	// Configless means the kind's body is driven by a config table, not a
	// `run` string — validators skip the "needs run/targets" check for these.
	Configless bool

	// Modules are the modules this kind auto-pulls into scope, so a project
	// doesn't have to declare them (using the kind is enough).
	Modules []string

	// ConsumesTargets means the kind's factory actually reads the action's
	// targets; under any other kind, declared targets are built and never
	// run, which is always a mistake.
	ConsumesTargets bool
}

// Kinds is the canonical action-kind catalog — the single source of truth.
// Adding a kind is one entry here plus a Register call in factories.go's
// registerBuiltins; the init-time coverage check in factories.go rejects a
// declared kind left without a factory, so the catalog and the factories
// cannot drift.
//
// Whether an action carrying a given kind dispatches through a project's
// [terminal] launcher is no longer a catalog property (ADR-0020): a project
// declares `locale = "window"` on whichever actions it wants dispatched that
// way — a dev-loop kind is a common case, not a reserved one, and nothing
// here assumes it.
var Kinds = map[string]Kind{
	"shell":             {Name: "shell"},
	"shell-passthrough": {Name: "shell-passthrough"},
	"multi-target":      {Name: "multi-target", ConsumesTargets: true},
	"apple-dev-loop":    {Name: "apple-dev-loop", ConsumesTargets: true},
	"lua":               {Name: "lua"},

	// Capability kinds whose Go implementations existed with no way to reach
	// them. internal/electron was a complete port with zero callers;
	// internal/reload's loop was reachable only through docker-dev/vite-dev;
	// [launchd] and [go_dist] were parsed into the manifest and read by no
	// factory at all. Each of these is the missing dispatch shape, not new
	// behaviour — see each factory's doc comment.
	"electron": {Name: "electron", Modules: []string{"electron", "reload"}},
	// reload-loop is Transient for interactive-shell's reason, not
	// docker-reload-loop's: RunWithReload restarts the child on SIGUSR1 *or an
	// 'r'/'R' keypress when stdin is a terminal*, so routing it into a herdr
	// pane would silently disconnect it from the keyboard half of its own
	// contract.
	"reload-loop": {Name: "reload-loop", Modules: []string{"reload"}},
	"launchd":     {Name: "launchd", Configless: true, Modules: []string{"launchd"}},
	"go-dist":     {Name: "go-dist", Modules: []string{"godist"}},
	"agent-skill": {Name: "agent-skill"},
	// clip records a looping demo through the recorder its [clip] table
	// names (internal/clip); its body is that table, not a `run` string.
	"clip": {Name: "clip", Configless: true},

	"http":               {Name: "http"},
	"npm":                {Name: "npm"},
	"go":                 {Name: "go"},
	"raylib":             {Name: "raylib"},
	"sync":               {Name: "sync"},
	"apple":              {Name: "apple"},
	"clean-dev":          {Name: "clean-dev", Configless: true},
	"vite-dev":           {Name: "vite-dev", Configless: true},
	"print-status":       {Name: "print-status", Configless: true},
	"docs":               {Name: "docs", Configless: true},
	"docker-reload-loop": {Name: "docker-reload-loop", Modules: []string{"docker"}},
	"docker-dev":         {Name: "docker-dev", Configless: true, Modules: []string{"docker"}},
	"server-reload":      {Name: "server-reload", Configless: true, Modules: []string{"docker"}},
	"kill-port":          {Name: "kill-port", Modules: []string{"ports", "ports_nuke"}},
	"nuke-dev":           {Name: "nuke-dev", Configless: true, Modules: []string{"ports", "ports_nuke"}},
	"dev-precheck":       {Name: "dev-precheck", Configless: true, Modules: []string{"ports", "ports_nuke"}},
	"fixtures-generate":  {Name: "fixtures-generate", Configless: true, Modules: []string{"cleanup", "fixtures"}},
	"fixtures-reset":     {Name: "fixtures-reset", Configless: true, Modules: []string{"cleanup", "fixtures"}},
	"install-template":   {Name: "install-template", Configless: true, Modules: []string{"remote", "unraid"}},
	"interactive-shell":  {Name: "interactive-shell", Modules: []string{"ports", "log_server", "log_bridge", "keybindings", "log_format"}},
	"bridge-listen":      {Name: "bridge-listen", Configless: true, Modules: []string{"ports", "log_server", "log_bridge", "keybindings", "log_format"}},

	// workflow (admin-3rgp.24): "run this same thing over many targets",
	// declared via `over` rather than a `run` string, so Configless: true —
	// the same reason clean-dev/vite-dev/docs/etc. above are. A workflow
	// action itself never runs in a window (admin-spjp.2's fix, still true under
	// ADR-0020; dispatchLocale resolves a workflow to hosted whatever it
	// declares): its Runner (internal/kinds/workflowcli.go)
	// has to run in the terminal the user typed into, because it prints the
	// resolved target set and reads a yes/no confirmation from that
	// terminal's stdin before it dispatches anything — dispatching the
	// workflow action itself through a launcher would hand off before the
	// Runner is ever built, the exact silent no-op admin-spjp.2 fixed (the
	// Runner never even ran). The fan-out still happens, just one level down:
	// DispatchTargets (workflowdispatch.go) runs each target once the set is
	// confirmed — in its own pane through the project's declared [terminal]
	// launcher (ADR-0020), or on a hosted pty when the workflow says
	// locale = "hosted" — which is a per-target run, not one dispatch for
	// the workflow action as a whole.
	// ConsumesTargets stays false: that field means "reads the action's
	// `targets` table" (multi-target's shape), and a workflow's targets come
	// from `over` (a row selection or a resolved query), never a declared
	// `targets` table.
	"workflow": {Name: "workflow", Configless: true},
}

// KnownKinds is every valid kind name, sorted — derived from Kinds so a new
// kind needs no separate edit here.
var KnownKinds = func() []string {
	out := make([]string, 0, len(Kinds))
	for name := range Kinds {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}()

// ConfiglessKinds is every kind whose body is a config table, not a `run`
// string, sorted — derived from Kinds.
var ConfiglessKinds = func() []string {
	out := make([]string, 0)
	for name, k := range Kinds {
		if k.Configless {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}()

// TargetConsumingKinds is every kind whose factory actually reads the
// action's targets — under any other kind, declared targets are built and
// never run, which is always a mistake. Sorted, derived from Kinds.
var TargetConsumingKinds = func() []string {
	out := make([]string, 0)
	for name, k := range Kinds {
		if k.ConsumesTargets {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}()

// KindModules is the modules a kind auto-pulls into scope, keyed by kind
// name — only kinds with at least one module are present. Derived from
// Kinds.
var KindModules = func() map[string][]string {
	out := map[string][]string{}
	for name, k := range Kinds {
		if len(k.Modules) > 0 {
			mods := make([]string, len(k.Modules))
			copy(mods, k.Modules)
			out[name] = mods
		}
	}
	return out
}()

// Declared reports whether name is a declared kind, returning its catalog
// record.
func Declared(name string) (Kind, bool) {
	k, ok := Kinds[name]
	return k, ok
}
