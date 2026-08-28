package kinds

import (
	"fmt"
	"sort"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// Runner is the closure a Factory returns — the Go shape of Python's
// _make_x(cmd, ns) -> fn(args): the thing that actually runs when the kind
// dispatches.
type Runner func(args []string) error

// Factory builds a Runner for one [actions.X] entry. name is the kind name
// (so one Factory value, like the Python apple-dev-loop/multi-target pair,
// can be registered under more than one kind and still know which it's
// running as); action is the manifest.Action being dispatched.
type Factory func(name string, action manifest.Action) (Runner, error)

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
// registration). This is the Go port of admin_lib/kinds.py's
// register_factory.
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
// factory, sorted — the Go port of admin_lib/interp.py's import-time
// coverage check ("action kinds with no registered factory: [...]"). This is
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
