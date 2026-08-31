//go:build darwin

package luahost

// Ports admin_lib/apple.py's _detect_project_path / _read_pbxproj /
// _xcode_target_settings / _xcode_setting — the "pbxproj parsing" area
// admin-3rgp.38's NOTES named as remaining. docs/architecture/lua-host-api.md
// previously deferred this whole area as "project-specific convention-
// following, not a general host capability" — true of the higher layer
// (_compose_dev_settings and the dev/prod bundle-suffix scheme, still not
// bound here), but the raw parse and the target/configuration lookup
// underneath it are a general capability: "read what Xcode actually wrote
// into a project's buildSettings" has nothing project-specific about it.
//
// Every function here degrades the same way the Python original did on any
// resolution failure — an empty table / empty string / false, never a raised
// error — because .xcconfig values and $(VAR) substitutions come back as
// literals or are absent entirely, and a caller must already tolerate that.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// DetectProjectPath returns explicit if non-empty, else the first
// *.xcworkspace or *.xcodeproj found in the process's current directory.
// Returns "" (not an error) when nothing is found — mirrors the Python
// original's two outcomes (an explicit path always wins; cwd-globbing is a
// convenience, not a guarantee), except the original called sys.exit(1) on
// failure — a Lua host function raising the process down is wrong for a
// primitive other bindings compose with, so the caller decides what an
// empty result means.
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
// JSON — so callers fall back to manifest-supplied values exactly as the
// Python original's callers already must.
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
// configuration when configuration isn't found, matching the Python
// original), or {} on any resolution failure.
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

// XcodeSetting returns buildSettings[key] for scheme's target, and false if
// the key is missing, non-string, or an unresolved Xcode variable
// substitution like "$(TARGET_NAME)" — the caller can't act on that without
// a proper expansion pass, so it's treated the same as absent.
func XcodeSetting(projectPath, scheme, key string) (string, bool) {
	val, ok := XcodeTargetSettings(projectPath, scheme, "Debug")[key].(string)
	if !ok || val == "" {
		return "", false
	}
	if val[0] == '$' {
		return "", false
	}
	return val, true
}
