package kinds

import (
	"fmt"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// notImplemented returns a Factory whose Runner always fails with a clear
// not-yet-implemented error. Implementing kind behaviour is out of scope for
// this ticket (later tickets port shell, apple, docker, node, ports, and the
// rest); this keeps invariant 2 honest — every declared kind has a bound
// factory — without pre-empting that work. The error surfaces at dispatch
// time, when the Runner is actually invoked, never at startup.
func notImplemented(kind string) Factory {
	return func(name string, action manifest.Action) (Runner, error) {
		return func(args []string) error {
			return fmt.Errorf("kind %q is not implemented in the Go runtime yet", kind)
		}, nil
	}
}

// registerBuiltins binds every declared kind's factory into r. Kinds are
// listed one at a time, explicitly — mirroring admin_lib/interp.py's own
// register_factory block — rather than looped over the Kinds catalog:
// looping would make invariant 2 ("a declared kind with no registered
// factory fails at startup") vacuously true and unfalsifiable, since every
// declared kind would always end up registered by construction.
func registerBuiltins(r *Registry) {
	r.MustRegister("shell", notImplemented("shell"))
	r.MustRegister("shell-passthrough", notImplemented("shell-passthrough"))
	r.MustRegister("multi-target", notImplemented("multi-target"))
	r.MustRegister("apple-dev-loop", notImplemented("apple-dev-loop"))
	r.MustRegister("python", notImplemented("python"))
	r.MustRegister("python-script", notImplemented("python-script"))
	r.MustRegister("http", notImplemented("http"))
	r.MustRegister("npm", notImplemented("npm"))
	r.MustRegister("go", notImplemented("go"))
	r.MustRegister("raylib", notImplemented("raylib"))
	r.MustRegister("sync", notImplemented("sync"))
	r.MustRegister("apple", notImplemented("apple"))
	r.MustRegister("clean-dev", notImplemented("clean-dev"))
	r.MustRegister("vite-dev", notImplemented("vite-dev"))
	r.MustRegister("print-urls", notImplemented("print-urls"))
	r.MustRegister("docs", notImplemented("docs"))
	r.MustRegister("docker-reload-loop", notImplemented("docker-reload-loop"))
	r.MustRegister("docker-dev", notImplemented("docker-dev"))
	r.MustRegister("server-reload", notImplemented("server-reload"))
	r.MustRegister("kill-port", notImplemented("kill-port"))
	r.MustRegister("nuke-dev", notImplemented("nuke-dev"))
	r.MustRegister("dev-precheck", notImplemented("dev-precheck"))
	r.MustRegister("fixtures-generate", notImplemented("fixtures-generate"))
	r.MustRegister("fixtures-reset", notImplemented("fixtures-reset"))
	r.MustRegister("install-template", notImplemented("install-template"))
	r.MustRegister("interactive-shell", notImplemented("interactive-shell"))
	r.MustRegister("bridge-listen", notImplemented("bridge-listen"))
}

// init registers every declared kind's (stub) factory into Default, then
// immediately validates coverage and panics if any declared kind was left
// unbound. Because cmd/root.go imports internal/kinds, this init runs during
// program start — before cobra parses argv — so an incomplete catalog is a
// startup failure, not a dispatch-time surprise. Registration and the
// coverage check live in the same init() function (not split across files
// relying on Go's filename-based init ordering) so the "register everything,
// then check everything" sequence is structural rather than incidental.
func init() {
	registerBuiltins(Default)
	if err := Default.Validate(); err != nil {
		panic(err)
	}
}
