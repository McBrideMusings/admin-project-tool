package archetype

import (
	"fmt"
	"sort"

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
// A project with no archetypes (nil or empty project.Archetypes) resolves
// cleanly to project's own content with no archetype directories ever
// consulted — Resolve never fails merely because no archetype directories
// exist on disk.
func Resolve(project *manifest.Manifest, sp SearchPath) (*manifest.Manifest, []string, error) {
	dst := &manifest.Manifest{
		Urls:      map[string]string{},
		Overrides: map[string]manifest.Command{},
		Custom:    map[string]manifest.Command{},
		Actions:   map[string]manifest.Action{},
		Logs:      map[string]manifest.LogSource{},
		Guards:    map[string]manifest.Guard{},

		Apple:           map[string]any{},
		ReactNative:     map[string]any{},
		Electron:        map[string]any{},
		Server:          map[string]any{},
		Output:          map[string]any{},
		Logging:         map[string]any{},
		DevKill:         map[string]any{},
		DevPrecheck:     map[string]any{},
		CleanDev:        map[string]any{},
		ViteDev:         map[string]any{},
		DockerDev:       map[string]any{},
		Deploy:          map[string]any{},
		DockerRun:       map[string]any{},
		DockerBuild:     map[string]any{},
		InstallTemplate: map[string]any{},
		Fixtures:        map[string]any{},
		Docs:            map[string]any{},
		Go:              map[string]any{},
		GoDist:          map[string]any{},
		Raylib:          map[string]any{},
		Launchd:         map[string]any{},
		LogBridge:       map[string]any{},
		Worktree:        map[string]any{},
	}

	var log []string
	o := newOrigins()

	for _, name := range project.Archetypes {
		arch, err := Load(sp, name)
		if err != nil {
			return nil, nil, err
		}
		merge(dst, arch.Partial, fmt.Sprintf("archetype '%s'", name), &log, o)
	}

	// The project always wins (ADR-0009): overlay it last regardless of
	// whether any archetypes were loaded.
	merge(dst, project, "manifest", &log, o)

	// Raw is debugging-only and stays the project's own decoded TOML, never
	// an archetype's.
	dst.Raw = project.Raw
	dst.Archetypes = append([]string(nil), project.Archetypes...)

	return dst, log, nil
}

// merge folds src into dst in place, per ADR-0002: no inheritance, no
// override blocks, last write wins on every key. srcLabel identifies src in
// the merge log ("archetype 'a'" or "manifest").
func merge(dst, src *manifest.Manifest, srcLabel string, log *[]string, o *origins) {
	if src.ProjectName != "" {
		dst.ProjectName = src.ProjectName
	}
	if src.PythonMin != "" {
		dst.PythonMin = src.PythonMin
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
	mergeMap(dst.Overrides, src.Overrides, srcLabel, "command", o.overrides, log)
	mergeMap(dst.Custom, src.Custom, srcLabel, "custom command", o.custom, log)
	mergeMap(dst.Actions, src.Actions, srcLabel, "action", o.actions, log)
	mergeMap(dst.Logs, src.Logs, srcLabel, "log", o.logs, log)
	mergeMap(dst.Guards, src.Guards, srcLabel, "guard", o.guards, log)

	if src.InlineFile != "" {
		dst.InlineFile = src.InlineFile
	}

	for _, pair := range []struct{ dst, src map[string]any }{
		{dst.Apple, src.Apple},
		{dst.ReactNative, src.ReactNative},
		{dst.Electron, src.Electron},
		{dst.Server, src.Server},
		{dst.Output, src.Output},
		{dst.Logging, src.Logging},
		{dst.DevKill, src.DevKill},
		{dst.DevPrecheck, src.DevPrecheck},
		{dst.CleanDev, src.CleanDev},
		{dst.ViteDev, src.ViteDev},
		{dst.DockerDev, src.DockerDev},
		{dst.Deploy, src.Deploy},
		{dst.DockerRun, src.DockerRun},
		{dst.DockerBuild, src.DockerBuild},
		{dst.InstallTemplate, src.InstallTemplate},
		{dst.Fixtures, src.Fixtures},
		{dst.Docs, src.Docs},
		{dst.Go, src.Go},
		{dst.GoDist, src.GoDist},
		{dst.Raylib, src.Raylib},
		{dst.Launchd, src.Launchd},
		{dst.LogBridge, src.LogBridge},
		{dst.Worktree, src.Worktree},
	} {
		mergeTable(pair.dst, pair.src)
	}

	dst.Unknown = mergeStringSet(dst.Unknown, src.Unknown)
	sort.Strings(dst.Unknown)
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

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
