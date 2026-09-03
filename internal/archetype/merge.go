package archetype

import (
	"fmt"
	"sort"

	"github.com/McBrideMusings/admin-project-tool/internal/guards"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// origins tracks, per merged key, the label of whichever source last
// contributed it — so a later collision can name what it overrode ("command
// 'build' from 'archetype a' overridden by archetype 'b'"), matching
// admin_lib/archetypes.py::resolve's merge_log.
type origins struct {
	urls, overrides, custom, actions, logs, guards map[string]string
}

func newOrigins() *origins {
	return &origins{
		urls:      map[string]string{},
		overrides: map[string]string{},
		custom:    map[string]string{},
		actions:   map[string]string{},
		logs:      map[string]string{},
		guards:    map[string]string{},
	}
}

// Resolve loads every archetype named in project.Archetypes (in list order)
// off sp, merges their partial manifests together, and overlays project on
// top — per ADR-0002 (partial manifests, list order, last wins, no
// inheritance, no override blocks) and ADR-0009 (the manifest references, the
// archetype implements: the project always wins). It returns the merged
// manifest and a human-readable merge log of every collision.
//
// Before any of that, BaseName is merged in implicitly — first, so every
// declared archetype and the project itself can override it — unless the
// project already lists "base" in `archetypes`, in which case the explicit
// pass covers it and the implicit one is skipped to avoid merging it twice
// and doubling its collision log lines. A project never has to write
// `archetypes = ["base"]` for this to happen (admin-mb5q's whole point: 53
// manifests need no edit), and a search path with no base at all (LoadBase,
// in archetype.go) is a silent no-op, not an error.
//
// A project with no archetypes (nil or empty project.Archetypes) resolves
// cleanly to project's own content plus base — Resolve never fails merely
// because no other archetype directories exist on disk.
func Resolve(project *manifest.Manifest, sp SearchPath) (*manifest.Manifest, []string, error) {
	dst := &manifest.Manifest{
		// Root is the project's, not an archetype's: an archetype fragment
		// has no directory of its own, and the resolved manifest is what
		// every action's working directory is taken from.
		Root: project.Root,

		Urls:      map[string]string{},
		Overrides: map[string]manifest.Command{},
		Custom:    map[string]manifest.Command{},
		Actions:   map[string]manifest.Action{},
		Logs:      map[string]manifest.LogSource{},
		Guards:    map[string]manifest.Guard{},

		Settings:  map[string]map[string]any{},
		Logging:   map[string]any{},
		LogBridge: map[string]any{},
		Worktree:  map[string]any{},
		Keys:      map[string]any{},
		Identity:  map[string]any{},
		Worktrees: map[string]any{},
		Beads:     map[string]any{},
		Workflow:  map[string]any{},
		Terminal:  map[string]any{},
	}

	var log []string
	o := newOrigins()

	declaresBase := false
	for _, name := range project.Archetypes {
		if name == BaseName {
			declaresBase = true
			break
		}
	}
	if !declaresBase {
		if base, ok, err := LoadBase(sp); err != nil {
			return nil, nil, err
		} else if ok {
			if err := merge(dst, base.Partial, fmt.Sprintf("archetype '%s'", BaseName), false, &log, o); err != nil {
				return nil, nil, err
			}
		}
	}

	for _, name := range project.Archetypes {
		arch, err := Load(sp, name)
		if err != nil {
			return nil, nil, err
		}
		if err := merge(dst, arch.Partial, fmt.Sprintf("archetype '%s'", name), false, &log, o); err != nil {
			return nil, nil, err
		}
	}

	// The project always wins (ADR-0009): overlay it last regardless of
	// whether any archetypes were loaded.
	if err := merge(dst, project, "manifest", true, &log, o); err != nil {
		return nil, nil, err
	}

	// Raw is debugging-only and stays the project's own decoded TOML, never
	// an archetype's.
	dst.Raw = project.Raw
	dst.Archetypes = append([]string(nil), project.Archetypes...)

	// ADR-0005's resolve-phase audit: every `requires` reference (action-level
	// and per multi-target target) must resolve to a known guard now that
	// archetype merging is done and dst.Guards holds every user-defined one.
	if err := guards.ValidateRequires(dst); err != nil {
		return nil, nil, err
	}

	return dst, log, nil
}

// merge folds src into dst in place, per ADR-0002: no inheritance, no
// override blocks, last write wins on every key. srcLabel identifies src in
// the merge log ("archetype 'a'" or "manifest").
//
// isManifest distinguishes the two callers of merge: archetype-to-archetype
// [commands.X] collisions keep whole-value replacement (Python:145-151,
// ADR-0002 last-wins), while the final manifest overlay merges each
// [commands.X] override onto the archetype-provided command field by field
// (admin_lib/archetypes.py:155-185) — ADR-0009: the manifest references, the
// archetype implements, so naming one field must not delete the rest.
func merge(dst, src *manifest.Manifest, srcLabel string, isManifest bool, log *[]string, o *origins) error {
	if src.ProjectName != "" {
		dst.ProjectName = src.ProjectName
	}
	if src.PythonMin != "" {
		dst.PythonMin = src.PythonMin
	}
	if src.HasTracker {
		dst.Trackers, dst.HasTracker = src.Trackers, true
	}
	if src.HasUsesPullRequests {
		dst.UsesPullRequests, dst.HasUsesPullRequests = src.UsesPullRequests, true
	}

	dst.Modules = mergeStringSet(dst.Modules, src.Modules)
	dst.ExcludeModules = mergeStringSet(dst.ExcludeModules, src.ExcludeModules)
	dst.HideCommands = mergeStringSet(dst.HideCommands, src.HideCommands)
	dst.HideUrls = mergeStringSet(dst.HideUrls, src.HideUrls)

	// A non-empty Order REPLACES dst's, matching Python's archetype_order
	// behaviour (the last archetype/manifest to declare an order wins
	// outright rather than being appended to).
	if len(src.Order) > 0 {
		dst.Order = append([]string(nil), src.Order...)
	}

	mergeMap(dst.Urls, src.Urls, srcLabel, "url", o.urls, log)
	if isManifest {
		if err := mergeCommandOverrides(dst.Overrides, src.Overrides, srcLabel, o.overrides, log); err != nil {
			return err
		}
	} else {
		mergeMap(dst.Overrides, src.Overrides, srcLabel, "command", o.overrides, log)
	}
	mergeMap(dst.Custom, src.Custom, srcLabel, "custom command", o.custom, log)
	mergeMap(dst.Actions, src.Actions, srcLabel, "action", o.actions, log)
	mergeMap(dst.Logs, src.Logs, srcLabel, "log", o.logs, log)
	mergeMap(dst.Guards, src.Guards, srcLabel, "guard", o.guards, log)

	if src.InlineFile != "" {
		dst.InlineFile = src.InlineFile
	}

	for _, pair := range []struct{ dst, src map[string]any }{
		{dst.Logging, src.Logging},
		{dst.LogBridge, src.LogBridge},
		{dst.Worktree, src.Worktree},
		{dst.Identity, src.Identity},
		{dst.Worktrees, src.Worktrees},
		{dst.Beads, src.Beads},
		{dst.Workflow, src.Workflow},
		{dst.Terminal, src.Terminal},
	} {
		mergeTable(pair.dst, pair.src)
	}

	mergeSettings(dst.Settings, src.Settings)

	mergeRowKeys(dst.Keys, src.Keys)

	dst.Populate = mergePopulate(dst.Populate, src.Populate)

	dst.Unknown = mergeStringSet(dst.Unknown, src.Unknown)
	sort.Strings(dst.Unknown)

	return nil
}

// mergeStringSet appends src's entries onto dst, de-duplicated, preserving
// dst's existing order and then src's — the "append" merge for
// Modules/ExcludeModules/HideCommands/HideUrls/Unknown.
func mergeStringSet(dst, src []string) []string {
	seen := make(map[string]bool, len(dst)+len(src))
	out := make([]string, 0, len(dst)+len(src))
	for _, s := range dst {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range src {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// mergeTable shallow-merges src's keys into dst, src winning on conflict —
// the per-key merge every ~22 archetype-specific config table uses. Unlike
// mergeMap, a collision here is not logged: these tables are configuration
// fragments (e.g. [apple] scheme/bundle_id), not named commands/actions/urls
// worth surfacing individually.
func mergeTable(dst, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}

// mergeSettings folds every settings table src declares into dst, one
// mergeTable per name. Unlike the fixed pair list mergeTable's callers used
// to enumerate for the ~20 built-in archetype config tables, this walks
// whatever names src.Settings actually holds — apple, go, or a name only one
// project's own admin.toml invents — so an archetype can contribute default
// values for a project-invented table exactly as it already can for a
// built-in one, with no registry naming it in advance (admin-tlhw.4).
func mergeSettings(dst, src map[string]map[string]any) {
	for key, srcTbl := range src {
		dstTbl, ok := dst[key]
		if !ok {
			dstTbl = map[string]any{}
			dst[key] = dstTbl
		}
		mergeTable(dstTbl, srcTbl)
	}
}

// mergePopulate appends src's [[populate]] entries after dst's, de-duplicated
// by pattern with the last occurrence's mode winning — an archetype can
// contribute a default set of populate patterns and a project can extend it,
// or override one entry's mode by repeating its pattern, without either
// silently dropping the other's contribution. This is the one raw table that
// cannot go through mergeTable: it is a list, not a map.
func mergePopulate(dst, src []map[string]any) []map[string]any {
	out := append([]map[string]any(nil), dst...)
	for _, entry := range src {
		pattern, _ := entry["pattern"].(string)
		replaced := false
		if pattern != "" {
			for i, existing := range out {
				if p, _ := existing["pattern"].(string); p == pattern {
					out[i] = entry
					replaced = true
					break
				}
			}
		}
		if !replaced {
			out = append(out, entry)
		}
	}
	return out
}

// mergeRowKeys merges [keys.<row-kind>] two levels deep — the one raw
// archetype table that does not take mergeTable's whole-value replacement.
// Every other raw table ([apple], [deploy], and the ~22 others merged via
// mergeTable above) is a configuration fragment: replacing it wholesale is
// fine because the fragment is one coherent unit. A row-kind's binding table
// is different — it is a namespace of independent bindings ("o" -> a
// command, "enter" -> another), and a project declaring [keys.bead] to bind
// one key of its own must not silently delete an archetype's other bindings
// for that same row kind. See ADR-0018's Consequences for the resolution
// order this produces (archetypes in list order, then the project, later
// wins per individual key).
func mergeRowKeys(dst, src map[string]any) {
	for k, sv := range src {
		srcTable, srcOK := sv.(map[string]any)
		dstTable, dstOK := dst[k].(map[string]any)
		if !srcOK || !dstOK {
			// Either side isn't a table (or dst has no entry yet): fall back
			// to whole-value assignment. If the shape is actually invalid,
			// validate.RowKeys reports "keys.<kind>: must be a table" as it
			// does today — this helper never needs to judge that itself.
			dst[k] = sv
			continue
		}

		// Build a fresh map rather than mutating either side's parsed table
		// in place — an archetype's manifest may be reused across multiple
		// Resolve calls, and dst's existing table may be that same shared
		// map from an earlier merge step.
		merged := make(map[string]any, len(dstTable)+len(srcTable))
		for bk, bv := range dstTable {
			merged[bk] = bv
		}
		for bk, bv := range srcTable {
			merged[bk] = bv
		}
		dst[k] = merged
	}
}

// mergeMap merges src into dst key by key, src always winning, recording a
// human-readable line in *log the first (and every subsequent) time a key is
// contributed by more than one source. origin remembers who last won each
// key so the log line can name what got overridden.
func mergeMap[V any](dst, src map[string]V, srcLabel, label string, origin map[string]string, log *[]string) {
	for _, k := range sortedKeys(src) {
		if prev, exists := origin[k]; exists {
			*log = append(*log, fmt.Sprintf("%s '%s' from '%s' overridden by %s", label, k, prev, srcLabel))
		}
		dst[k] = src[k]
		origin[k] = srcLabel
	}
}

// mergeCommandOverrides merges the project manifest's [commands.X] overrides
// onto dst, one field at a time, per admin_lib/archetypes.py:155-185 — the
// manifest-only counterpart to mergeMap's whole-value replacement.
//
// When a command name already exists (contributed by an archetype), each
// field of the override wins when present and otherwise falls back to dst's
// existing value, so naming only `desc` cannot delete `steps`. When the name
// does not already exist, the override must carry `steps` — an override with
// no archetype behind it and no steps of its own resolves to nothing, which
// Python treats as a manifest error rather than a silently empty command.
func mergeCommandOverrides(dst, src map[string]manifest.Command, srcLabel string, origin map[string]string, log *[]string) error {
	for _, k := range sortedKeys(src) {
		override := src[k]
		base, exists := dst[k]
		if !exists {
			if override.Steps == nil {
				return fmt.Errorf(
					"manifest [commands.%s] must include 'steps' (a list of action names). "+
						"No archetype provides this command. Move run/kind/targets to [actions].", k)
			}
			dst[k] = override
			origin[k] = srcLabel
			continue
		}

		if prev, exists := origin[k]; exists {
			*log = append(*log, fmt.Sprintf("command '%s' from '%s' overridden by %s", k, prev, srcLabel))
		}

		merged := manifest.Command{Raw: override.Raw}

		if override.HasDesc {
			merged.Desc, merged.HasDesc = override.Desc, true
		} else {
			merged.Desc, merged.HasDesc = base.Desc, base.HasDesc
		}

		if override.Steps != nil {
			merged.Steps = append([]string(nil), override.Steps...)
		} else {
			merged.Steps = append([]string(nil), base.Steps...)
		}

		if override.HasGroup {
			merged.Group, merged.HasGroup = override.Group, true
		} else {
			merged.Group, merged.HasGroup = base.Group, base.HasGroup
		}

		if override.HasPriority {
			merged.Priority, merged.HasPriority = override.Priority, true
		} else {
			merged.Priority, merged.HasPriority = base.Priority, base.HasPriority
		}

		if override.HasLogFile {
			merged.LogFile, merged.HasLogFile = override.LogFile, true
		} else {
			merged.LogFile, merged.HasLogFile = base.LogFile, base.HasLogFile
		}

		if override.HasLog {
			merged.Log, merged.HasLog = override.Log, true
		} else {
			merged.Log, merged.HasLog = base.Log, base.HasLog
		}

		if override.HasRetain {
			merged.LogRetain, merged.HasRetain = override.LogRetain, true
		} else {
			merged.LogRetain, merged.HasRetain = base.LogRetain, base.HasRetain
		}

		if override.Env != nil {
			merged.Env = override.Env
		} else if base.Env != nil {
			merged.Env = base.Env
		}

		if override.HasPreflight {
			merged.Preflight, merged.HasPreflight = override.Preflight, true
		} else {
			merged.Preflight, merged.HasPreflight = base.Preflight, base.HasPreflight
		}

		dst[k] = merged
		origin[k] = srcLabel
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
