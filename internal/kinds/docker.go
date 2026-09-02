package kinds

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/reload"
)

// makeDockerReloadLoop is kind="docker-reload-loop"'s factory — the Go port
// of admin_lib/interp.py::_make_docker_reload_loop. `run` is an arbitrary
// shell command (not necessarily Docker-specific — the kind name is
// historical) handed straight to internal/reload.RunWithReload.
//
// Python's factory also builds a column formatter from the project's
// `[output]` table (admin_lib/log_columns.make_column_formatter) before
// passing it to run_with_reload. log_columns.py has no Go port yet — no
// ticket claims it — so output here is unformatted (reload.Options.Formatter
// left nil) until that lands; tracked as a follow-up rather than silently
// dropped.
func makeDockerReloadLoop(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	run, ok := actionString(action, "run")
	if !ok || run == "" {
		return nil, fmt.Errorf(`action %q: kind="docker-reload-loop" requires 'run'`, name)
	}
	return func(args []string) error {
		h := newHost(m)
		h.Info("reload loop: " + run)
		rc := reload.RunWithReload(run, name, reload.Options{
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

// makeServerReload is kind="server-reload"'s factory — the Go port of
// admin_lib/interp.py::_make_server_reload, which calls
// admin_lib/docker.py's reload_running_targets. args[0], when present, is
// the target-name substring filter Python's `args[0] if args else None`
// passes through unchanged.
func makeServerReload(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	return func(args []string) error {
		filter := ""
		if len(args) > 0 {
			filter = args[0]
		}
		h := newHost(m)
		if _, err := reload.SignalAll(reload.PIDPrefix, filter, h); err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		return nil
	}, nil
}

// makeDockerDev is kind="docker-dev"'s factory — the Go port of
// admin_lib/interp.py::_make_docker_dev, which calls
// admin_lib/docker.py's docker_dev_from_config. The `[docker_dev]` manifest
// table (m.DockerDev) is the whole body; the action itself carries no
// per-invocation keys, matching Kinds["docker-dev"].Configless.
func makeDockerDev(name string, action manifest.Action, m *manifest.Manifest, surf *Surface) (Runner, error) {
	cfg := archetypeConfigTable(m, "docker_dev")

	return func(args []string) error {
		h := newHost(m)
		if len(cfg) == 0 {
			h.Err("docker_dev: empty config")
			return fmt.Errorf("action %q: docker_dev: empty config", name)
		}

		if err := makeBindMountDirs(cfg); err != nil {
			h.Warn("docker_dev: could not create bind-mount dir: " + err.Error())
		}

		if truthyCfg(cfg["pre_kill"]) {
			// Mirrors Python's `if callable(kill): kill([])` — best-effort,
			// return value ignored, exactly as docker_dev_from_config does.
			// "kill" is looked up as a declared command (not action) name,
			// the same indirection admin_lib's cmd_kill lookup used.
			if _, ok := resolveCommand(m, "kill"); ok {
				_ = surf.Command("kill", nil)
			}
		}

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

// truthyCfg mirrors Python's bare `if cfg.get(key):` truthiness for the
// handful of config values this file reads that way.
func truthyCfg(v any) bool {
	switch val := v.(type) {
	case nil:
		return false
	case bool:
		return val
	case string:
		return val != ""
	case int64:
		return val != 0
	default:
		return true
	}
}

// asMapSlice reads a []any-of-map[string]any config value (TOML's shape for
// an array of tables) as []map[string]any. A non-list, or a list containing
// a non-table entry, yields nil — the same "just skip it" leniency Python's
// `cfg.get("ports") or ()` loop has for a malformed entry, since manifest
// shape validation is admin-3rgp.8's job, not a kind factory's.
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
// `mkdir` (the Python default is true), skipping absolute paths — those
// belong to system state, not project state. Mirrors
// admin_lib/docker.py::docker_dev_from_config's pre-loop.
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
