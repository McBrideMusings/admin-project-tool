package kinds

import (
	"fmt"
	"sort"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// Runner is the closure a Factory returns — the thing that actually runs
// when the kind dispatches.
type Runner func(args []string) error

// Factory builds a Runner for one [actions.X] entry. name is the kind name
// (so one Factory value can be registered under more than one kind and
// still know which it's running as); action is the manifest.Action being
// dispatched; m is the fully resolved project Manifest the action lives in.
//
// admin-3rgp.43: m is what the earlier (name, action) shape lacked. A
// Factory may need any manifest-level table by name, not just its own
// action's inline keys — the gap this ticket closes. m is
// threaded through rather than a narrower "just the config" struct because
// the same gap already applies to other manifest-level tables a Factory may
// need (guards for admin-3rgp.10, urls for admin-3rgp.11): one foundational
// parameter now avoids a second signature break for each of those. A
// Factory that doesn't need m (shell, npm, http, sync, multi-target today)
// simply ignores it — Go's blank identifier costs nothing at the call site.
//
// m may be nil in tests that exercise a single factory directly without
// building a full Manifest; every factory that reads from m must do so
// through archetypeConfigTable (or an equivalent nil-safe accessor), never
// by dereferencing m directly.
//
// surf is the nested-dispatch handle (admin-3rgp.69): the surface the
// Runner this Factory builds should use if it needs to call back into
// another declared action (Surface.Action) or command (Surface.Command) —
// the multi-target `action = "other"` delegation, the interactive-shell/
// bridge-listen keybinding hooks, and the docker-dev/vite-dev/clean-dev
// pre-kill hop. It is built fresh by runAction immediately before the
// factory call, so two concurrent dispatches each close over their own
// surface rather than reaching through shared process state. surf is nil
// when a Factory's Runner is invoked directly in a test, outside any
// dispatch — Surface's methods tolerate a nil receiver and return the
// existing "requires running through kinds.RunAction/RunCommand" error in
// that case. A Factory that never delegates (shell, npm, http, sync today)
// simply ignores it.
type Factory func(name string, action manifest.Action, m *manifest.Manifest, surf *Surface) (Runner, error)

// Registry maps a declared kind name to the Factory that executes it.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{factories: map[string]Factory{}}
}

// Register binds f as the factory for the declared kind name. It errors if
// name is not in Kinds — you can't register a factory for a kind absent from
// the catalog — or if name already has a factory bound (a copy-paste double
// registration).
func (r *Registry) Register(name string, f Factory) error {
	if _, ok := Kinds[name]; !ok {
		return fmt.Errorf(
			"kinds.Register: unknown kind %q — declare it in internal/kinds/catalog.go first",
			name,
		)
	}
	if _, ok := r.factories[name]; ok {
		return fmt.Errorf(
			"kinds.Register: kind %q already has a factory bound (double registration)",
			name,
		)
	}
	r.factories[name] = f
	return nil
}

// MustRegister calls Register and panics on error. Used by factories.go's
// init() block, where a registration failure is a programming error that
// must stop the binary from starting, not a runtime condition to recover
// from.
func (r *Registry) MustRegister(name string, f Factory) {
	if err := r.Register(name, f); err != nil {
		panic(err)
	}
}

// Validate returns an error naming every declared kind with no registered
// factory, sorted ("action kinds with no registered factory: [...]"). This is
// the function that makes invariant 2 ("a declared kind with no registered
// factory fails at startup, not at dispatch") true: factories.go's init()
// panics on this, and cmd.Execute calls it as its first statement so the
// same check is also reachable as a clean startup error.
func (r *Registry) Validate() error {
	var unbound []string
	for name := range Kinds {
		if _, ok := r.factories[name]; !ok {
			unbound = append(unbound, name)
		}
	}
	if len(unbound) == 0 {
		return nil
	}
	sort.Strings(unbound)
	return fmt.Errorf("action kinds with no registered factory: %v", unbound)
}

// Lookup returns the factory bound to name, if any.
func (r *Registry) Lookup(name string) (Factory, bool) {
	f, ok := r.factories[name]
	return f, ok
}

// Default is the registry factories.go's init() populates with every
// declared kind's (stub, for now) factory — the one the shipped binary
// dispatches through.
var Default = NewRegistry()

// Register binds f as the factory for name on the Default registry.
func Register(name string, f Factory) error {
	return Default.Register(name, f)
}

// Validate checks the Default registry's coverage.
func Validate() error {
	return Default.Validate()
}

// Lookup returns the Default registry's factory for name, if any.
func Lookup(name string) (Factory, bool) {
	return Default.Lookup(name)
}
