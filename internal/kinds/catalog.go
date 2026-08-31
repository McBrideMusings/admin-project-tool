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

// Kind is one action kind's validation metadata — name, whether its body is
// a config table rather than a `run` string, which admin_lib-equivalent
// modules it auto-pulls, and whether its factory actually reads a target
// map. This is a direct port of admin_lib/kinds.py's Kind dataclass, minus
// the `factory` field: Go keeps the factory in the Registry rather than
// mutating the catalog record, so Kinds stays an immutable map.
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
}

// Kinds is the canonical action-kind catalog — the single source of truth.
// Adding a kind is one entry here plus a Register call in factories.go's
// registerBuiltins; the init-time coverage check in factories.go rejects a
// declared kind left without a factory, so the catalog and the factories
// cannot drift. This mirrors admin_lib/kinds.py's KINDS dict exactly,
// including python and python-script.
var Kinds = map[string]Kind{
	"shell":              {Name: "shell"},
	"shell-passthrough":  {Name: "shell-passthrough"},
	"multi-target":       {Name: "multi-target", ConsumesTargets: true},
	"apple-dev-loop":     {Name: "apple-dev-loop", ConsumesTargets: true},
	"lua":                {Name: "lua"},
	"http":               {Name: "http"},
	"npm":                {Name: "npm"},
	"go":                 {Name: "go"},
	"raylib":             {Name: "raylib"},
	"sync":               {Name: "sync"},
	"apple":              {Name: "apple"},
	"clean-dev":          {Name: "clean-dev", Configless: true},
	"vite-dev":           {Name: "vite-dev", Configless: true},
	"print-urls":         {Name: "print-urls", Configless: true},
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

// Declared reports whether name is a declared kind, returning its catalog
// record.
func Declared(name string) (Kind, bool) {
	k, ok := Kinds[name]
	return k, ok
}
