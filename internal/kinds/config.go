package kinds

import "github.com/McBrideMusings/admin-project-tool/internal/manifest"

// archetypeConfigTable returns m's settings table named key (e.g. "go",
// "apple", or any name a project's own admin.toml invents), or an empty
// (never nil) map when m is nil or the table simply wasn't set. A built-in
// kind reads its own table by its own literal name — that's the kind
// describing its own mechanism (ADR-0019), not the tool dictating what a
// project may declare. Before admin-tlhw.4 this dispatched through a fixed
// ~20-name registry (archetypeConfigNames) that made any other table
// invisible to every kind; it now reads manifest.Manifest.Settings directly,
// which parse populates for every top-level table the tool doesn't already
// handle structurally, so a project's own [my_thing] table is exactly as
// reachable as [apple] with no change here. Nil-safety here is what lets a
// Factory be called with m == nil in a unit test that only wants to
// exercise one factory in isolation (registry_test.go's stubFactory,
// goaction_test.go's existing calls) without constructing a full Manifest.
func archetypeConfigTable(m *manifest.Manifest, key string) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	if tbl, ok := m.Settings[key]; ok && tbl != nil {
		return tbl
	}
	return map[string]any{}
}

// ArchetypeConfigTable is archetypeConfigTable exported for callers outside
// this package (cmd/check.go's checkPreRunRefs).
func ArchetypeConfigTable(m *manifest.Manifest, key string) map[string]any {
	return archetypeConfigTable(m, key)
}

// archetypeConfigTables returns every non-empty settings table on m, keyed
// by its admin.toml name. This is the table makeLua exposes to a Lua body as
// sub-tables of the `config` global — see that file's doc comment for why
// nesting-by-domain-name, not a flat merge, is the shape that generalizes to
// "any archetype's Lua body" without needing per-action attribution of which
// archetype an action came from.
func archetypeConfigTables(m *manifest.Manifest) map[string]any {
	out := map[string]any{}
	if m == nil {
		return out
	}
	for key, tbl := range m.Settings {
		if len(tbl) > 0 {
			out[key] = tbl
		}
	}
	return out
}
