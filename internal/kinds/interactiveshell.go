// interactiveshell.go implements kind="interactive-shell" and
// kind="bridge-listen" — the Go port of admin_lib/interp.py's
// _make_interactive_shell and _make_bridge_listen. Both dispatch through
// internal/keybindings.Run, which owns the subprocess loop, the shared
// pump, and the log bridge lifecycle; this file's job is purely the
// manifest-to-Options translation each kind needs.
package kinds

import (
	"fmt"
	"os"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/keybindings"
	"github.com/McBrideMusings/admin-project-tool/internal/logbridge"
	"github.com/McBrideMusings/admin-project-tool/internal/logformat"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// actionKeys reads and validates the raw `keys` table off action, matching
// admin_lib/archetypes.py's `if "keys" in data: parse_keys(...)` — an
// absent key is not an error and yields a nil map (no bindings declared).
func actionKeys(action manifest.Action, name string) (map[string]validate.KeyBinding, error) {
	raw, present := action["keys"]
	if !present {
		return nil, nil
	}
	keys, verr := validate.ParseKeys(raw, fmt.Sprintf("action %q", name))
	if verr != nil {
		return nil, verr
	}
	return keys, nil
}

// buildLogBridgeConfig reads m's [log_bridge] table into a *logbridge.Config,
// resolving each host through manifest.ResolveEnv — the Go port of
// admin_lib/interp.py's boot-time `configure_log_bridge` call, which does
// the same `resolve_env(h) for h in hosts`. Returns nil when the table is
// absent or declares no hosts, matching log_bridge_enabled()'s "opt-in"
// semantics: no [log_bridge] table means no listener, ever.
func buildLogBridgeConfig(m *manifest.Manifest) *logbridge.Config {
	if m == nil || len(m.LogBridge) == 0 {
		return nil
	}
	rawHosts, _ := m.LogBridge["hosts"].([]any)
	var hosts []string
	for _, h := range rawHosts {
		s, ok := h.(string)
		if !ok {
			continue
		}
		resolved, err := manifest.ResolveEnv(s)
		if err != nil {
			// A bad ${VAR} in a log_bridge host degrades the bridge, not the
			// action — the wrapped process may not even need it to succeed.
			continue
		}
		hosts = append(hosts, resolved)
	}
	if len(hosts) == 0 {
		return nil
	}
	cfg := &logbridge.Config{Hosts: hosts, Project: m.ProjectName}
	if rawRange, ok := m.LogBridge["port_range"].([]any); ok && len(rawRange) == 2 {
		start, sOk := toInt(rawRange[0])
		end, eOk := toInt(rawRange[1])
		if sOk && eOk {
			cfg.PortRange = [2]int{start, end}
		}
	}
	return cfg
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
}

// makeInteractiveShell is kind="interactive-shell"'s factory — the Go port
// of admin_lib/interp.py::_make_interactive_shell. It stays Transient
// (internal/kinds/catalog.go): its whole job is reading the user's own
// keystrokes to drive live-reload from the terminal they're sitting at, so
// routing it into a herdr pane would silently disconnect it from the
// keyboard it exists to read.
func makeInteractiveShell(name string, action manifest.Action, m *manifest.Manifest) (Runner, error) {
	run, _ := actionString(action, "run")
	if run == "" {
		return nil, fmt.Errorf("interactive-shell action %q needs 'run'", name)
	}
	keys, err := actionKeys(action, name)
	if err != nil {
		return nil, err
	}
	rawURLs := actionURLs(action)
	footerVal, hasFooter := actionBool(action, "footer")
	bridgeCfg := buildLogBridgeConfig(m)

	return func(args []string) error {
		resolved := manifest.ResolveComputed(run)
		h := newHost()
		f := enterFrame(h, name, rawURLs, hasFooter, footerVal, m)
		defer frame.Deactivate(f)
		h.Info("running: " + resolved)
		fmtFn := logformat.MakeFormatter(logformat.DefaultOptions())
		rc, rerr := keybindings.Run(resolved, keys, keybindings.Options{
			Label:     name,
			Formatter: fmtFn,
			Stdout:    h.Stdout,
			LogFn:     h.AppendLog,
			LogPath:   h.MaterializeLogFile(),
			LogBridge: bridgeCfg,
			RunAction: func(target string) error { return dispatchNested(target, nil) },
			Info:      h.Info,
			Warn:      h.Warn,
			Err:       h.Err,
		})
		if rerr != nil {
			return fmt.Errorf("action %q: %w", name, rerr)
		}
		if rc != 0 && rc != 130 {
			h.Err(name + " failed")
			return fmt.Errorf("action %q failed", name)
		}
		h.Ok(name + " complete")
		return nil
	}, nil
}

// bridgePark mirrors admin_lib/interp.py's _BRIDGE_PARK: the shell command
// bridge-listen wraps is irrelevant beyond "must not exit" — the log
// bridge's HTTP listener is the whole point, not the parked shell.
const bridgePark = "tail -f /dev/null"

// scopeLogBridgeHosts resolves hosts (a raw `hosts` string off a bridge-
// listen action, possibly carrying ${VAR} placeholders) and sets
// ADMIN_LOG_BRIDGE_HOSTS to the result — the env var
// internal/logbridge.Start reads to narrow which hosts THIS listener
// advertises, so `logs live` and `logs dev` can run side by side with the
// userscript routing each page to the right one. Split out from
// makeBridgeListen's closure so the resolve-then-set-env-var step is
// testable without driving the (deliberately infinite, until interrupted)
// bridgePark subprocess it precedes.
func scopeLogBridgeHosts(hosts string) (string, error) {
	resolved, err := manifest.ResolveEnv(hosts)
	if err != nil {
		return "", err
	}
	os.Setenv("ADMIN_LOG_BRIDGE_HOSTS", resolved)
	return resolved, nil
}

// makeBridgeListen is kind="bridge-listen"'s factory — the Go port of
// admin_lib/interp.py::_make_bridge_listen. `hosts` on the action narrows
// what THIS listener advertises (via ADMIN_LOG_BRIDGE_HOSTS, read by
// internal/logbridge.Start), so `logs live` and `logs dev` can run side by
// side with the userscript routing each page to the right one.
func makeBridgeListen(name string, action manifest.Action, m *manifest.Manifest) (Runner, error) {
	keys, err := actionKeys(action, name)
	if err != nil {
		return nil, err
	}
	hosts, _ := actionString(action, "hosts")
	bridgeCfg := buildLogBridgeConfig(m)

	return func(args []string) error {
		h := newHost()
		if hosts != "" {
			resolved, rerr := scopeLogBridgeHosts(hosts)
			if rerr != nil {
				return fmt.Errorf("action %q: %w", name, rerr)
			}
			h.Info("log bridge scoped to: " + resolved)
		}
		h.Info("waiting for browser console output (Ctrl-C to stop)")
		fmtFn := logformat.MakeFormatter(logformat.DefaultOptions())
		rc, rerr := keybindings.Run(bridgePark, keys, keybindings.Options{
			Label:     name,
			Formatter: fmtFn,
			Stdout:    h.Stdout,
			LogFn:     h.AppendLog,
			LogPath:   h.MaterializeLogFile(),
			LogBridge: bridgeCfg,
			RunAction: func(target string) error { return dispatchNested(target, nil) },
			Info:      h.Info,
			Warn:      h.Warn,
			Err:       h.Err,
		})
		if rerr != nil {
			return fmt.Errorf("action %q: %w", name, rerr)
		}
		if rc != 0 && rc != 130 {
			h.Err(name + " failed")
			return fmt.Errorf("action %q failed", name)
		}
		h.Ok(name + " complete")
		return nil
	}, nil
}
