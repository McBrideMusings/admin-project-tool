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
	return func(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
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
//
// Every declared kind now has a real factory — no stub registrations remain.
// They arrived in stages: the eight transient kinds with admin-3rgp.35 (the
// set ADR-0010 settles on — nine minus the deleted python/python-script, plus
// lua), then docker and remote (.13), node and web (.14), ports and process
// cleanup (.15), interactive-shell and bridge-listen (.18), workflow (.24),
// and apple (.12).
func registerBuiltins(r *Registry) {
	r.MustRegister("shell", makeShell)
	r.MustRegister("shell-passthrough", makeShellPassthrough)
	r.MustRegister("multi-target", makeMultiTarget)
	r.MustRegister("apple-dev-loop", makeMultiTarget) // same shape as multi-target (admin-3rgp.12)
	r.MustRegister("lua", makeLua)
	r.MustRegister("electron", makeElectron)
	r.MustRegister("reload-loop", makeReloadLoop)
	r.MustRegister("launchd", makeLaunchd)
	r.MustRegister("go-dist", makeGoDist)
	r.MustRegister("agent-skill", makeAgentSkill)
	r.MustRegister("http", makeHTTP)
	r.MustRegister("npm", makeNpm)
	r.MustRegister("go", makeGoKind)
	r.MustRegister("raylib", makeRaylib)
	r.MustRegister("sync", makeSync)
	r.MustRegister("apple", makeApple)
	r.MustRegister("clean-dev", makeCleanDev)
	r.MustRegister("vite-dev", makeViteDev)
	r.MustRegister("print-urls", makePrintURLs)
	r.MustRegister("docs", makeDocs)
	r.MustRegister("docker-reload-loop", makeDockerReloadLoop)
	r.MustRegister("docker-dev", makeDockerDev)
	r.MustRegister("server-reload", makeServerReload)
	r.MustRegister("kill-port", makeKillPort)
	r.MustRegister("nuke-dev", makeNukeDev)
	r.MustRegister("dev-precheck", makeDevPrecheck)
	r.MustRegister("fixtures-generate", makeFixturesGenerate)
	r.MustRegister("fixtures-reset", makeFixturesReset)
	r.MustRegister("install-template", makeInstallTemplate)
	r.MustRegister("interactive-shell", makeInteractiveShell)
	r.MustRegister("bridge-listen", makeBridgeListen)
	r.MustRegister("workflow", makeWorkflow)
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
