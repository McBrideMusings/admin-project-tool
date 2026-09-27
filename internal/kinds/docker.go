package kinds

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/logcolumns"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/reload"
)

// makeDockerReloadLoop is kind="docker-reload-loop"'s factory. `run` is an
// arbitrary shell command (not necessarily Docker-specific — the kind name
// is historical) handed straight to internal/reload.RunWithReload.
//
// This builds a column formatter from the project's `[output]` table via
// internal/logcolumns.MakeColumnFormatter("docker", ...), reading the
// manifest's [output] table exactly the way every other kind reads its own
// settings table. reload.Options.Formatter is therefore non-nil whenever a
// project declares [output] (or even when it doesn't — the "docker" source
// default still applies), and RunWithReload's own always-on HH:MM:SS.mmm
// stamping (logformat.StampLine, wired as the pump's DisplayTransform) is
// skipped for a pump that already stamps its own lines via FormatLine.
func makeDockerReloadLoop(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	run, ok := actionString(action, "run")
	if !ok || run == "" {
		return nil, fmt.Errorf(`action %q: kind="docker-reload-loop" requires 'run'`, name)
	}
	return func(args []string) error {
		h := newHost(m)
		h.Info("reload loop: " + run)
		// A fresh formatter per invocation (not built once at factory time,
		// shared across every future call), so each `admin dev` run gets
		// its own header print.
		formatter := logcolumns.MakeColumnFormatter("docker", archetypeConfigTable(m, "output"))
		rc := reload.RunWithReload(run, name, reload.Options{
			Dir:       h.ProjectRoot,
			Stdout:    h.Stdout,
			LogFn:     h.AppendLog,
			Info:      h.Info,
			Warn:      h.Warn,
			Formatter: formatter,
		})
		if rc != 0 {
			return fmt.Errorf("action %q: exited %d", name, rc)
		}
		return nil
	}, nil
}

// makeServerReload is kind="server-reload"'s factory. It signals loops this
// project started, never another project's; which of them, first match wins:
//
//   - args[0], when present: every loop whose target name contains it.
//   - dispatched from a Work-tab row key binding (ADMIN_ROW_KIND is command,
//     commandTarget or worker): only the loops that row's run started
//     (reload.MatchRow on ADMIN_ROW_ID) — none, for an agent worker.
//   - otherwise: every loop.
func makeServerReload(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	return func(args []string) error {
		root := projectRoot(m)
		match := reload.MatchTarget(root, "")
		switch rowKind := os.Getenv("ADMIN_ROW_KIND"); {
		case len(args) > 0:
			match = reload.MatchTarget(root, args[0])
		case rowKind == "command" || rowKind == "commandTarget" || rowKind == "worker":
			match = reload.MatchRow(root, os.Getenv("ADMIN_ROW_ID"))
		}
		h := newHost(m)
		if _, err := reload.Signal(reload.PIDPrefix, match, h); err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		return nil
	}, nil
}

// makeDockerDev is kind="docker-dev"'s factory. The `[docker_dev]` manifest
// table (m.Settings["docker_dev"]) is the whole body; the action itself carries no
// per-invocation keys, matching Kinds["docker-dev"].Configless.
func makeDockerDev(name string, action manifest.Action, m *manifest.Manifest, surf *Surface) (Runner, error) {
	cfg := archetypeConfigTable(m, "docker_dev")
	// pre_kill (admin-tlhw.3) names whatever command or action the project
	// wants run before this dev loop starts — no default, no "kill" fallback
	// when it's absent.
	preRun := cfgString(cfg, "pre_kill", "")

	return func(args []string) error {
		h := newHost(m)
		if len(cfg) == 0 {
			h.Err("docker_dev: empty config")
			return fmt.Errorf("action %q: docker_dev: empty config", name)
		}

		if err := makeBindMountDirs(cfg); err != nil {
			h.Warn("docker_dev: could not create bind-mount dir: " + err.Error())
		}

		runPreHook(m, surf, preRun, nil, h.Warn)

		cmd, err := buildDockerDevCmd(cfg)
		if err != nil {
			h.Err("docker_dev: " + err.Error())
			return fmt.Errorf("action %q: %w", name, err)
		}

		target, _ := cfg["target"].(string)
		if target == "" {
			target = "dev"
		}

		rc := reload.RunWithReload(cmd, target, reload.Options{
			Dir:    h.ProjectRoot,
			Stdout: h.Stdout,
			LogFn:  h.AppendLog,
			Info:   h.Info,
			Warn:   h.Warn,
		})
		if rc != 0 {
			return fmt.Errorf("action %q: exited %d", name, rc)
		}
		return nil
	}, nil
}

// asMapSlice reads a []any-of-map[string]any config value (TOML's shape for
// an array of tables) as []map[string]any. A non-list, or a list containing
// a non-table entry, yields nil — a malformed entry is simply skipped,
// since manifest shape validation is admin-3rgp.8's job, not a kind
// factory's.
func asMapSlice(v any) []map[string]any {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// cfgNumberString renders m[key] — an int64, float64, or string in TOML's
// decoded shape — as a plain decimal string, for a value used to build a
// shell argv (a port number). Returns "", false for anything else, including
// absent.
func cfgNumberString(m map[string]any, key string) (string, bool) {
	switch v := m[key].(type) {
	case int64:
		return strconv.FormatInt(v, 10), true
	case string:
		return v, v != ""
	default:
		return "", false
	}
}

// makeBindMountDirs pre-creates every bind-mount host dir that opted into
// `mkdir` (the default is true), skipping absolute paths — those
// belong to system state, not project state.
func makeBindMountDirs(cfg map[string]any) error {
	for _, m := range asMapSlice(cfg["bind_mounts"]) {
		host := cfgString(m, "host", "")
		if host == "" || strings.HasPrefix(host, "/") {
			continue
		}
		mkdir, present := m["mkdir"].(bool)
		if present && !mkdir {
			continue
		}
		if err := os.MkdirAll(host, 0o755); err != nil {
			return err
		}
	}
	return nil
}
