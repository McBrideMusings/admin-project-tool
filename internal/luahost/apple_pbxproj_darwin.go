//go:build darwin

package luahost

// Implements project path detection, pbxproj parsing, and target/
// configuration build-setting lookup — the "pbxproj parsing" area
// admin-3rgp.38's NOTES named as remaining. docs/architecture/lua-host-api.md
// previously deferred this whole area as "project-specific convention-
// following, not a general host capability" — true of the higher layer
// (the dev/prod bundle-suffix scheme, still not bound here), but the raw
// parse and the target/configuration lookup underneath it are a general
// capability: "read what Xcode actually wrote into a project's
// buildSettings" has nothing project-specific about it.
//
// Every function here degrades the same way on any resolution failure — an
// empty table / empty string / false, never a raised error — because
// .xcconfig values and $(VAR) substitutions come back as literals or are
// absent entirely, and a caller must already tolerate that.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// DetectProjectPath returns explicit if non-empty, else the first
// *.xcworkspace or *.xcodeproj found in the process's current directory.
// Returns "" (not an error) when nothing is found: an explicit path always
// wins, cwd-globbing is a convenience, not a guarantee, and a Lua host
// function raising the process down is wrong for a primitive other
// bindings compose with, so the caller decides what an empty result means.
func DetectProjectPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	for _, pat := range []string{"*.xcworkspace", "*.xcodeproj"} {
		matches, err := filepath.Glob(pat)
		if err != nil {
			return "", err
		}
		if len(matches) > 0 {
			return matches[0], nil
		}
	}
	return "", nil
}

var (
	pbxprojCacheMu sync.Mutex
	pbxprojCache   = map[string]map[string]any{}
)

// resetPBXProjCache clears the per-process pbxproj parse cache. Test-only:
// production callers share one process per admin invocation, so the cache
// never needs invalidating in real use.
func resetPBXProjCache() {
	pbxprojCacheMu.Lock()
	defer pbxprojCacheMu.Unlock()
	pbxprojCache = map[string]map[string]any{}
}

// ReadPBXProj parses projectPath's project.pbxproj (an OpenStep ASCII plist)
// into a map via `plutil -convert json -o -`, caching per projectPath so one
// admin invocation parses at most once. Returns {} on any failure — missing
// file, a workspace with no obvious sibling .xcodeproj, a plutil error, bad
// JSON — so callers fall back to manifest-supplied values.
func ReadPBXProj(projectPath string) map[string]any {
	pbxprojCacheMu.Lock()
	if cached, ok := pbxprojCache[projectPath]; ok {
		pbxprojCacheMu.Unlock()
		return cached
	}
	pbxprojCacheMu.Unlock()

	data := readPBXProjUncached(projectPath)

	pbxprojCacheMu.Lock()
	pbxprojCache[projectPath] = data
	pbxprojCacheMu.Unlock()
	return data
}

func readPBXProjUncached(projectPath string) map[string]any {
	actual := projectPath
	if filepath.Ext(projectPath) == ".xcworkspace" {
		base := trimSuffix(filepath.Base(projectPath), ".xcworkspace")
		sibling := filepath.Join(filepath.Dir(projectPath), base+".xcodeproj")
		if isDir(sibling) {
			actual = sibling
		} else {
			return map[string]any{}
		}
	}

	pbxproj := filepath.Join(actual, "project.pbxproj")
	if !isFile(pbxproj) {
		return map[string]any{}
	}

	out, err := exec.Command("plutil", "-convert", "json", "-o", "-", pbxproj).Output()
	if err != nil {
		return map[string]any{}
	}
	var data map[string]any
	if err := json.Unmarshal(out, &data); err != nil {
		return map[string]any{}
	}
	return data
}

func trimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// XcodeTargetSettings returns the buildSettings map for the PBXNativeTarget
// named scheme at configuration (falling back to the target's first build
// configuration when configuration isn't found), or {} on any resolution
// failure.
func XcodeTargetSettings(projectPath, scheme, configuration string) map[string]any {
	if scheme == "" {
		return map[string]any{}
	}
	proj := ReadPBXProj(projectPath)
	objects, _ := proj["objects"].(map[string]any)
	if len(objects) == 0 {
		return map[string]any{}
	}

	var target map[string]any
	for _, obj := range objects {
		o, ok := obj.(map[string]any)
		if !ok {
			continue
		}
		if o["isa"] == "PBXNativeTarget" && o["name"] == scheme {
			target = o
			break
		}
	}
	if target == nil {
		return map[string]any{}
	}

	cfgListID, _ := target["buildConfigurationList"].(string)
	cfgList, _ := objects[cfgListID].(map[string]any)
	cfgIDs, _ := cfgList["buildConfigurations"].([]any)

	var chosen map[string]any
	for _, idAny := range cfgIDs {
		id, ok := idAny.(string)
		if !ok {
			continue
		}
		c, ok := objects[id].(map[string]any)
		if ok && c["name"] == configuration {
			chosen = c
			break
		}
	}
	if chosen == nil && len(cfgIDs) > 0 {
		if id, ok := cfgIDs[0].(string); ok {
			chosen, _ = objects[id].(map[string]any)
		}
	}
	if chosen == nil {
		return map[string]any{}
	}
	settings, _ := chosen["buildSettings"].(map[string]any)
	if settings == nil {
		return map[string]any{}
	}
	return settings
}

// xcodeVarRefRe matches a single $(KEY) build-setting reference — the form
// PRODUCT_BUNDLE_IDENTIFIER takes in a project that follows the dev/prod
// bundle-suffix convention (docs/architecture):
// "com.foo.app$(ADMIN_DEV_BUNDLE_SUFFIX)".
var xcodeVarRefRe = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)`)

// expandXcodeValue resolves every $(KEY) reference in val by substituting
// settings[KEY]'s own (recursively expanded) value, so a bundle id like
// "com.foo.app$(PRODUCT_BUNDLE_ID_SUFFIX)" composes into a real value when
// the referenced setting is itself written into the same buildSettings dict
// (a common pattern: the suffix key sits right next to the templated one).
// Returns ok=false when any reference can't be resolved this way — a key
// absent from buildSettings entirely (e.g. ADMIN_DEV_BUNDLE_SUFFIX, which is
// only ever supplied on the xcodebuild command line, never written into the
// project) or a cycle (a setting that references itself, directly or
// through another setting — visited guards this exactly the way Xcode's own
// evaluator refuses to expand a cycle further). A value with no "$(" at all
// is returned unchanged.
//
// This deliberately rejects a value with an embedded, unresolved "$(...)"
// anywhere in the string, not just one that *starts* with "$" — a value
// with an embedded reference elsewhere (e.g. after a literal prefix) that
// passed straight through was the exact bug observed live: an uncomposed
// bundle id containing a literal "$(...)" token reaching a `log stream`
// predicate. Treating any unresolved reference — not just a leading one —
// as "value absent" is what makes the caller fall back to composing the id
// instead of copying it.
func expandXcodeValue(settings map[string]any, val string, visited map[string]bool) (string, bool) {
	if !strings.Contains(val, "$(") {
		return val, true
	}
	ok := true
	expanded := xcodeVarRefRe.ReplaceAllStringFunc(val, func(match string) string {
		if !ok {
			return match
		}
		key := xcodeVarRefRe.FindStringSubmatch(match)[1]
		if visited[key] {
			ok = false
			return match
		}
		raw, exists := settings[key].(string)
		if !exists {
			ok = false
			return match
		}
		next := make(map[string]bool, len(visited)+1)
		for k, v := range visited {
			next[k] = v
		}
		next[key] = true
		resolved, subOk := expandXcodeValue(settings, raw, next)
		if !subOk {
			ok = false
			return match
		}
		return resolved
	})
	if !ok {
		return "", false
	}
	return expanded, true
}

// XcodeSetting returns buildSettings[key] for scheme's target — expanding
// any $(OTHER_KEY) reference against the same buildSettings dict — and false
// if the key is missing, non-string, or still contains an unresolved
// reference after expansion. See expandXcodeValue for what "unresolved"
// covers.
func XcodeSetting(projectPath, scheme, key string) (string, bool) {
	settings := XcodeTargetSettings(projectPath, scheme, "Debug")
	val, ok := settings[key].(string)
	if !ok || val == "" {
		return "", false
	}
	return expandXcodeValue(settings, val, map[string]bool{key: true})
}
