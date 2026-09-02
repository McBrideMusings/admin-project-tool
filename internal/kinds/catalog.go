// Package kinds is the Go port of admin_lib/kinds.py's KINDS catalog plus the
// register_factory/coverage-check half of admin_lib/interp.py. It is the
// closed, single source of truth for valid action kinds: what a kind is
// (Kinds, the catalog) and what runs it (Registry, the factory map).
//
// Two invariants are enforced structurally, not documented:
//
//  1. Registering a factory under a name absent from Kinds is an error
//     (Registry.Register / Registry.MustRegister).
//  2. A declared kind with no registered factory fails at startup, not at
//     dispatch (Registry.Validate, called from factories.go's init() and
//     again from cmd.Execute).
//
// kind = "python" and kind = "python-script" are GONE from this catalog.
// ADR-0010 (docs/adr/0010-lua-and-bash-are-the-authoring-languages.md)
// reverses the earlier reversal recorded above: Bash (kind = "shell") and
// Lua (kind = "lua", dispatching through internal/luahost) are the two
// archetype authoring languages, full stop. No Python kind is declared here,
// even though admin_lib/kinds.py (the Python interpreter being replaced)
// still declares "python" and "python-script" — that file is deleted
// wholesale by admin-3rgp.30, not migrated kind-by-kind, so the two
// catalogs are expected to diverge on exactly this one point until then.
package kinds

import "sort"

// Lifetime is admin-3rgp.17's addition to the catalog: whether a kind's
// dispatch returns control once its own work is done (Transient, running
// inline in the existing frame — today's behaviour for every kind, and the
// zero value so every entry below that doesn't name a Lifetime keeps it) or
// spawns its own herdr pane and returns immediately, registering itself in
// the live-work registry (LongRunning). See dispatch.go's RunActionRouted
// for where that routing actually happens, and internal/livework for the
// registry. With no herdr pane available (internal/shim.Available()),
// a LongRunning kind falls back to running inline exactly like a Transient
// one — that fallback is the default path (HERDR_ENV=1 is usually absent)
// and must never surface as an error, a warning, or a hang.
type Lifetime int

const (
	// Transient runs inline in the current frame/process and returns only
	// once its own work is finished — the shell/build/one-shot kinds, and
	// every kind not explicitly marked LongRunning below.
	Transient Lifetime = iota

	// LongRunning dispatches into its own herdr pane and returns
	// immediately: the dev-loop kinds meant to keep running in the
	// background while the caller's terminal is free for other work.
	LongRunning
)

func (l Lifetime) String() string {
	if l == LongRunning {
		return "long-running"
	}
	return "transient"
}

// Kind is one action kind's validation metadata — name, whether its body is
// a config table rather than a `run` string, which admin_lib-equivalent
// modules it auto-pulls, whether its factory actually reads a target map,
// and its Lifetime (admin-3rgp.17). This is a direct port of
// admin_lib/kinds.py's Kind dataclass, minus the `factory` field: Go keeps
// the factory in the Registry rather than mutating the catalog record, so
// Kinds stays an immutable map.
type Kind struct {
	Name string

	// Configless means the kind's body is driven by a config table, not a
	// `run` string — validators skip the "needs run/targets" check for these.
	Configless bool

	// Modules are the admin_lib-equivalent modules this kind auto-pulls into
	// scope, so a project doesn't have to declare them (using the kind is
	// enough).
	Modules []string

	// ConsumesTargets means the kind's factory actually reads the action's
	// targets; under any other kind, declared targets are built and never
	// run, which is always a mistake.
	ConsumesTargets bool

	// Lifetime is Transient unless set to LongRunning below. Zero value
	// (Transient) means most entries in Kinds need no change at all.
	Lifetime Lifetime
}

// Kinds is the canonical action-kind catalog — the single source of truth.
// Adding a kind is one entry here plus a Register call in factories.go's
// registerBuiltins; the init-time coverage check in factories.go rejects a
// declared kind left without a factory, so the catalog and the factories
// cannot drift. This mirrors admin_lib/kinds.py's KINDS dict exactly,
// including python and python-script.
// Lifetime classification (admin-3rgp.17): the epic names five long-running
// dev-loop kinds explicitly — docker-dev, docker-reload-loop, vite-dev,
// server-reload, apple-dev-loop ("apple dev loops") — and says everything
// else is transient ("shell, go, npm, http, sync and friends"). Every other
// kind below is transient for that reason, with two worth calling out:
//
//   - interactive-shell is long-lived in wall-clock terms but is NOT marked
//     LongRunning: its whole job is reading the user's own keystrokes
//     (Modules includes "keybindings") to drive live-reload from the
//     terminal the user is sitting at. Routing it into a herdr pane would
//     silently disconnect it from the keyboard it exists to read — worse
//     than today's behaviour, not an improvement — so it stays inline.
//   - bridge-listen (`tail -f /dev/null`, parking a shell so the log
//     bridge's HTTP listener stays up for the browser-console forwarder)
//     takes no input at all and is meant to be started and left running
//     while the caller goes on to do other things — exactly what a herdr
//     pane is for. It IS marked LongRunning despite sharing
//     interactive-shell's module list, because the module list describes
//     what the *action* pulls into scope, not what its kind's factory does
//     with the terminal.
var Kinds = map[string]Kind{
	"shell":             {Name: "shell"},
	"shell-passthrough": {Name: "shell-passthrough"},
	"multi-target":      {Name: "multi-target", ConsumesTargets: true},
	"apple-dev-loop":    {Name: "apple-dev-loop", ConsumesTargets: true, Lifetime: LongRunning},
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

	"http":               {Name: "http"},
	"npm":                {Name: "npm"},
	"go":                 {Name: "go"},
	"raylib":             {Name: "raylib"},
	"sync":               {Name: "sync"},
	"apple":              {Name: "apple"},
	"clean-dev":          {Name: "clean-dev", Configless: true},
	"vite-dev":           {Name: "vite-dev", Configless: true, Lifetime: LongRunning},
	"print-urls":         {Name: "print-urls", Configless: true},
	"docs":               {Name: "docs", Configless: true},
	"docker-reload-loop": {Name: "docker-reload-loop", Modules: []string{"docker"}, Lifetime: LongRunning},
	"docker-dev":         {Name: "docker-dev", Configless: true, Modules: []string{"docker"}, Lifetime: LongRunning},
	"server-reload":      {Name: "server-reload", Configless: true, Modules: []string{"docker"}, Lifetime: LongRunning},
	"kill-port":          {Name: "kill-port", Modules: []string{"ports", "ports_nuke"}},
	"nuke-dev":           {Name: "nuke-dev", Configless: true, Modules: []string{"ports", "ports_nuke"}},
	"dev-precheck":       {Name: "dev-precheck", Configless: true, Modules: []string{"ports", "ports_nuke"}},
	"fixtures-generate":  {Name: "fixtures-generate", Configless: true, Modules: []string{"cleanup", "fixtures"}},
	"fixtures-reset":     {Name: "fixtures-reset", Configless: true, Modules: []string{"cleanup", "fixtures"}},
	"install-template":   {Name: "install-template", Configless: true, Modules: []string{"remote", "unraid"}},
	"interactive-shell":  {Name: "interactive-shell", Modules: []string{"ports", "log_server", "log_bridge", "keybindings", "log_format"}},
	"bridge-listen":      {Name: "bridge-listen", Configless: true, Modules: []string{"ports", "log_server", "log_bridge", "keybindings", "log_format"}, Lifetime: LongRunning},

	// workflow (admin-3rgp.24): "run this same thing over many targets",
	// declared via `over` rather than a `run` string, so Configless: true —
	// the same reason clean-dev/vite-dev/docs/etc. above are. Lifetime is
	// Transient (admin-spjp.2), NOT LongRunning as originally classified: a
	// workflow's Runner (internal/kinds/workflowcli.go) has to run in the
	// terminal the user typed into, because it prints the resolved target
	// set and reads a yes/no confirmation from that terminal's stdin before
	// it dispatches anything — RunActionRouted's LongRunning branch would
	// pane-split the action itself and return nil before the Runner is ever
	// built, which is exactly the silent no-op admin-spjp.2 fixed (the
	// Runner never even ran). The fan-out into panes still happens, just one
	// level down: DispatchTargets (workflowdispatch.go) opens its own
	// shim.SplitPane per target once the set is confirmed, which is a
	// per-target pane, not one pane for the workflow action as a whole.
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

// KindModules is the admin_lib-equivalent modules a kind auto-pulls into
// scope, keyed by kind name — only kinds with at least one module are
// present, matching admin_lib/kinds.py's KIND_MODULES. Derived from Kinds.
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

// LongRunningKinds is every kind marked Lifetime: LongRunning, sorted —
// derived from Kinds, mirroring the other derived views above.
var LongRunningKinds = func() []string {
	out := make([]string, 0)
	for name, k := range Kinds {
		if k.Lifetime == LongRunning {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}()

// Declared reports whether name is a declared kind, returning its catalog
// record.
func Declared(name string) (Kind, bool) {
	k, ok := Kinds[name]
	return k, ok
}
