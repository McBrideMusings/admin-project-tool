package kinds

import "github.com/McBrideMusings/admin-project-tool/internal/manifest"

// archetypeConfigNames mirrors admin_lib/interp.py's _CONFIG_GLOBALS: the set
// of manifest-level archetype config tables a kind body may need, keyed by
// the same snake_case name the table carries in admin.toml (matching
// internal/manifest.Manifest's own field set). Python exposed each of these
// unconditionally to every kind="python" body as a distinct global
// (_APPLE_CONFIG, _GO_CONFIG, ...); a body simply read whichever name it was
// authored against. This is the Go port of that same "every table is always
// reachable by its own name" design, kept as one ordered list so
// archetypeConfigTables and archetypeConfigTable share a single source of
// truth for "which manifest field does this TOML key project to".
var archetypeConfigNames = map[string]func(m *manifest.Manifest) map[string]any{
	"apple":            func(m *manifest.Manifest) map[string]any { return m.Apple },
	"electron":         func(m *manifest.Manifest) map[string]any { return m.Electron },
	"react_native":     func(m *manifest.Manifest) map[string]any { return m.ReactNative },
	"server":           func(m *manifest.Manifest) map[string]any { return m.Server },
	"output":           func(m *manifest.Manifest) map[string]any { return m.Output },
	"dev_kill":         func(m *manifest.Manifest) map[string]any { return m.DevKill },
	"dev_precheck":     func(m *manifest.Manifest) map[string]any { return m.DevPrecheck },
	"clean_dev":        func(m *manifest.Manifest) map[string]any { return m.CleanDev },
	"vite_dev":         func(m *manifest.Manifest) map[string]any { return m.ViteDev },
	"docker_dev":       func(m *manifest.Manifest) map[string]any { return m.DockerDev },
	"docker_run":       func(m *manifest.Manifest) map[string]any { return m.DockerRun },
	"docker_build":     func(m *manifest.Manifest) map[string]any { return m.DockerBuild },
	"deploy":           func(m *manifest.Manifest) map[string]any { return m.Deploy },
	"install_template": func(m *manifest.Manifest) map[string]any { return m.InstallTemplate },
	"fixtures":         func(m *manifest.Manifest) map[string]any { return m.Fixtures },
	"docs":             func(m *manifest.Manifest) map[string]any { return m.Docs },
	"go":               func(m *manifest.Manifest) map[string]any { return m.Go },
	"go_dist":          func(m *manifest.Manifest) map[string]any { return m.GoDist },
	"raylib":           func(m *manifest.Manifest) map[string]any { return m.Raylib },
	"launchd":          func(m *manifest.Manifest) map[string]any { return m.Launchd },
}

// archetypeConfigTable returns m's manifest-level config table named key
// (e.g. "go", "apple"), or an empty (never nil) map when m is nil, key is
// unknown, or the table simply wasn't set in admin.toml. Nil-safety here is
// what lets a Factory be called with m == nil in a unit test that only wants
// to exercise one factory in isolation (registry_test.go's stubFactory,
// goaction_test.go's existing calls) without constructing a full Manifest.
func archetypeConfigTable(m *manifest.Manifest, key string) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	get, ok := archetypeConfigNames[key]
	if !ok {
		return map[string]any{}
	}
	if tbl := get(m); tbl != nil {
		return tbl
	}
	return map[string]any{}
}

// ArchetypeConfigTable is archetypeConfigTable exported for callers outside
// this package (cmd/check.go's checkPreRunRefs) that read one manifest-level
// archetype config table by its admin.toml name, so the name-to-field
// mapping stays owned by archetypeConfigNames alone rather than being
// re-listed per caller.
func ArchetypeConfigTable(m *manifest.Manifest, key string) map[string]any {
	return archetypeConfigTable(m, key)
}

// archetypeConfigTables returns every non-empty manifest-level archetype
// config table on m, keyed by its admin.toml name. This is the table makeLua
// exposes to a Lua body as sub-tables of the `config` global — see that
// file's doc comment for why nesting-by-domain-name, not a flat merge, is
// the shape that generalizes to "any archetype's Lua body" without needing
// per-action attribution of which archetype an action came from.
func archetypeConfigTables(m *manifest.Manifest) map[string]any {
	out := map[string]any{}
	if m == nil {
		return out
	}
	for key, get := range archetypeConfigNames {
		if tbl := get(m); len(tbl) > 0 {
			out[key] = tbl
		}
	}
	return out
}
