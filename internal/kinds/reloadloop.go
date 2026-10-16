package kinds

import (
	"fmt"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/reload"
)

// makeReloadLoop is kind="reload-loop"'s factory: run one arbitrary shell
// command through internal/reload.RunWithReload, so `r` at the terminal (or a
// SIGUSR1 from `admin reload`) restarts it without tearing the loop down.
//
// Every existing caller of RunWithReload is welded to a stack — docker.go's
// docker-reload-loop, vitedev.go's vite-dev, appledevloop_darwin.go. A
// compiled backend with no container and no vite in front of it (a bare `go
// run .`, a cargo binary) had no way to reach the loop at all and had to fall
// back to kind="interactive-shell", which can only be killed and re-run. That
// is the gap this kind closes.
//
// `target` is the name RunWithReload records the loop under, which is what
// `admin reload <target>` matches; it defaults to the action's own name.
func makeReloadLoop(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	run, ok := actionString(action, "run")
	if !ok || run == "" {
		return nil, fmt.Errorf(`action %q: kind="reload-loop" requires 'run'`, name)
	}
	target := orDefault(func() string { s, _ := actionString(action, "target"); return s }(), name)

	return func(args []string) error {
		h := newHost(m)
		rc := reload.RunWithReload(manifest.ResolveComputed(run), target, reload.Options{
			Dir:          h.ProjectRoot,
			Root:         h.ProjectRoot,
			Unsupervised: !superviseEnabled(action),
			Stdout:       h.Stdout,
			LogFn:        h.AppendLog,
			Info:         h.Info,
			Warn:         h.Warn,
		})
		// 130 is a user Ctrl-C, which is how this loop is meant to end.
		if rc != 0 && rc != 130 {
			return fmt.Errorf("action %q: exited %d", name, rc)
		}
		return nil
	}, nil
}
