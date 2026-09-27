package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/ports"
)

// resolvePortValue resolves one `[dev_kill].ports[]` scalar entry to an int
// — an int/int64 straight from TOML, or a string resolved through
// manifest.ResolveEnv first (so `"${PORT:-3300}"` works the same way a
// pidfile path does).
func resolvePortValue(v any) (int, error) {
	switch x := v.(type) {
	case int64:
		return int(x), nil
	case int:
		return x, nil
	case string:
		resolved, err := manifest.ResolveEnv(x)
		if err != nil {
			return 0, err
		}
		resolved = strings.TrimSpace(resolved)
		n, err := strconv.Atoi(resolved)
		if err != nil {
			return 0, fmt.Errorf("[dev_kill].ports: %q resolved to %q, which is not a port", x, resolved)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("[dev_kill].ports: invalid port entry %v", v)
	}
}

// buildPortSpecs reads `[dev_kill].ports[]` — each entry either a bare
// port (string or int) or a {port=, label=} table — into []ports.PortSpec.
func buildPortSpecs(cfg map[string]any) ([]ports.PortSpec, error) {
	var out []ports.PortSpec
	for _, raw := range cfgList(cfg, "ports") {
		if entry, ok := raw.(map[string]any); ok {
			n, err := resolvePortValue(entry["port"])
			if err != nil {
				return nil, err
			}
			out = append(out, ports.PortSpec{Port: n, Label: cfgString(entry, "label", "")})
			continue
		}
		n, err := resolvePortValue(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, ports.PortSpec{Port: n})
	}
	return out, nil
}

// buildPidfileSpecs reads `[dev_kill].pidfiles[]` — each a {path=} or
// {env=, default=} table, plus optional format/key — into
// []ports.PidfileSpec. An entry that resolves to an empty path is dropped.
func buildPidfileSpecs(cfg map[string]any) []ports.PidfileSpec {
	var out []ports.PidfileSpec
	for _, raw := range cfgList(cfg, "pidfiles") {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var path string
		if envName, ok := entry["env"].(string); ok {
			path = os.Getenv(envName)
			if path == "" {
				path = manifest.ResolveComputed(cfgString(entry, "default", ""))
			}
		} else {
			path = manifest.ResolveComputed(cfgString(entry, "path", ""))
		}
		if path == "" {
			continue
		}
		out = append(out, ports.PidfileSpec{
			Path:   path,
			Format: cfgString(entry, "format", "int"),
			Key:    cfgString(entry, "key", "pid"),
		})
	}
	return out
}

// buildPatternSpecs reads `[dev_kill].patterns[]` — a bare string or a
// {match=, regex=, global=} table — into []ports.PatternSpec.
//
// A GLOBAL pattern given a relative, non-regex path is anchored to projectRoot
// before matching. Global means "match this anywhere, not just under the
// working directory", which is the only way to reach a process whose own cwd
// is elsewhere (an Electron dev app launched out of dist-dev). Left relative,
// such a pattern also matches the identical path inside every OTHER checkout
// of the same repo — so a worktree-scoped teardown would kill a sibling
// worktree's dev app. Anchoring keeps the reach global while keeping the
// target this checkout's.
func buildPatternSpecs(cfg map[string]any, projectRoot string) []ports.PatternSpec {
	var out []ports.PatternSpec
	for _, raw := range cfgList(cfg, "patterns") {
		switch v := raw.(type) {
		case string:
			out = append(out, ports.PatternSpec{Match: manifest.ResolveComputed(v)})
		case map[string]any:
			match := manifest.ResolveComputed(cfgString(v, "match", ""))
			if match == "" {
				continue
			}
			regex := cfgBool(v, "regex", false)
			global := cfgBool(v, "global", false)
			if global && !regex && !filepath.IsAbs(match) && strings.Contains(match, "/") {
				match = filepath.Join(projectRoot, match)
			}
			out = append(out, ports.PatternSpec{
				Match:  match,
				Regex:  regex,
				Global: global,
			})
		}
	}
	return out
}

// makeNukeDev is kind="nuke-dev"'s factory, reading the manifest's
// [dev_kill] table and running ports.NukeDevProcesses. A manifest with no
// [dev_kill] table configured is a no-op that reports "nothing was running"
// and exits zero — a nuke-dev command declared but never configured must
// never be treated as an error.
//
// An inline `[actions.X.config]` table REPLACES [dev_kill] for that action
// (ADR-0005's "inheritance is replace-not-merge"). One project-wide teardown
// spec is the common case, but not the only one: a worktree-scoped teardown
// has to kill this checkout's own dev processes while deliberately declaring
// NO ports, so that the port-based passes — the only ones not scoped to the
// working directory — cannot reach a standing service every other client
// depends on. Merging would make that impossible to express, since [dev_kill]'s
// ports would always survive into it.
func makeNukeDev(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	cfg := archetypeConfigTable(m, "dev_kill")
	if inline, ok := action["config"].(map[string]any); ok {
		cfg = inline
	}

	return func(args []string) error {
		h := newHost(m)
		if len(cfg) == 0 {
			h.Info("nothing was running")
			return nil
		}

		portSpecs, err := buildPortSpecs(cfg)
		if err != nil {
			h.Err(err.Error())
			return fmt.Errorf("action %q: %w", name, err)
		}

		root, err := os.Getwd()
		if err != nil {
			root = "."
		}

		opts := ports.NukeDevOptions{
			Ports:      portSpecs,
			Pidfiles:   buildPidfileSpecs(cfg),
			JSFiles:    cfgStrings(cfg, "js_files"),
			WebSubdirs: cfgStrings(cfg, "web_subdirs"),
			Patterns:   buildPatternSpecs(cfg, root),
		}
		if rt := cfgStrings(cfg, "runtimes"); len(rt) > 0 {
			opts.Runtimes = rt
		}
		if sm := cfgStrings(cfg, "supervisor_markers"); len(sm) > 0 {
			opts.SupervisorMarkers = sm
		}

		report := ports.NukeDevProcesses(root, opts)

		for _, r := range report.Killed {
			label := "TERM"
			if r.Escalated {
				label = "TERM then KILL"
			}
			h.Info(fmt.Sprintf("  killed pid %d (%s)", r.PID, label))
		}
		if len(report.Killed) > 0 {
			h.Ok(fmt.Sprintf("dev server stopped (%d process(es))", len(report.Killed)))
		} else if len(report.StillHeld) == 0 {
			h.Info("nothing was running")
		}
		for _, port := range report.StillHeld {
			h.Err(fmt.Sprintf("port %d is STILL held after the sweep", port))
		}
		if len(report.StillHeld) > 0 {
			return fmt.Errorf("action %q: port(s) %v still held after the sweep", name, report.StillHeld)
		}
		return nil
	}, nil
}
