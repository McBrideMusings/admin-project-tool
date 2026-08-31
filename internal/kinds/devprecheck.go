package kinds

import (
	"fmt"
	"os"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/ports"
)

// resolveEnvList resolves every entry of a `[dev_precheck]` string list
// through manifest.ResolveEnv, dropping any that resolve to empty —
// matches admin_lib/ports_nuke.py::dev_precheck_from_config's container and
// reap_pidfiles handling. Returns an error (rather than exiting the
// process, as Python's resolve_env does) on an unresolvable required var.
func resolveEnvList(cfg map[string]any, key string) ([]string, error) {
	var out []string
	for _, raw := range cfgStrings(cfg, key) {
		v, err := manifest.ResolveEnv(raw)
		if err != nil {
			return nil, err
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

// makeDevPrecheck is kind="dev-precheck"'s factory — the Go port of
// admin_lib/interp.py::_make_dev_precheck, reading the manifest's
// [dev_precheck] table and running ports.DevPrecheck. A live session or a
// running declared container aborts (non-nil error); orphaned processes are
// reaped and reported, never treated as a failure.
func makeDevPrecheck(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	cfg := archetypeConfigTable(m, "dev_precheck")

	return func(args []string) error {
		h := newHost()

		containers, err := resolveEnvList(cfg, "containers")
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		reapPidfiles, err := resolveEnvList(cfg, "reap_pidfiles")
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}

		opts := ports.DevPrecheckOptions{Containers: containers, ReapPidfiles: reapPidfiles}
		if rt := cfgStrings(cfg, "runtimes"); len(rt) > 0 {
			opts.Runtimes = rt
		}
		if ar := cfgStrings(cfg, "accept_runtimes"); len(ar) > 0 {
			opts.AcceptRuntimes = ar
		}
		label := cfgString(cfg, "label", "dev")
		opts.Label = label

		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		result := ports.DevPrecheck(cwd, opts)

		if len(result.Reaped) > 0 {
			h.Info(fmt.Sprintf("[%s] reaping %d orphaned process(es): %v", label, len(result.Reaped), result.Reaped))
		}
		for _, c := range result.RunningContainers {
			h.Err(fmt.Sprintf("[%s] ABORT: container %q is running — Ctrl-C its foreground session first.", label, c))
		}
		if len(result.Live) > 0 {
			h.Err(fmt.Sprintf("[%s] ABORT: a live %s session owns these PIDs: %v — Ctrl-C that session first.", label, label, result.Live))
		}
		if result.Aborted {
			return fmt.Errorf("action %q: dev precheck aborted", name)
		}
		return nil
	}, nil
}
