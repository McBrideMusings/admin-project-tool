//go:build darwin

package luahost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectProjectPathExplicitWins(t *testing.T) {
	got, err := DetectProjectPath("/explicit/Foo.xcodeproj")
	if err != nil {
		t.Fatalf("DetectProjectPath: %v", err)
	}
	if got != "/explicit/Foo.xcodeproj" {
		t.Errorf("got %q, want the explicit path unchanged", got)
	}
}

func TestDetectProjectPathGlobsCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Foo.xcodeproj"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	got, err := DetectProjectPath("")
	if err != nil {
		t.Fatalf("DetectProjectPath: %v", err)
	}
	if got != "Foo.xcodeproj" {
		t.Errorf("got %q, want Foo.xcodeproj", got)
	}
}

func TestDetectProjectPathNoneFound(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	got, err := DetectProjectPath("")
	if err != nil {
		t.Fatalf("DetectProjectPath: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestXcodeTargetSettingsResolvesConfigurationByName(t *testing.T) {
	objects := map[string]any{
		"TARGET1": map[string]any{
			"isa":                     "PBXNativeTarget",
			"name":                    "MyApp",
			"buildConfigurationList": "CFGLIST1",
		},
		"CFGLIST1": map[string]any{
			"buildConfigurations": []any{"CFG_DEBUG", "CFG_RELEASE"},
		},
		"CFG_DEBUG": map[string]any{
			"name":          "Debug",
			"buildSettings": map[string]any{"PRODUCT_BUNDLE_IDENTIFIER": "com.example.app"},
		},
		"CFG_RELEASE": map[string]any{
			"name":          "Release",
			"buildSettings": map[string]any{"PRODUCT_BUNDLE_IDENTIFIER": "com.example.app.release"},
		},
	}
	proj := map[string]any{"objects": objects}

	dir := t.TempDir()
	fakeProjectPath := filepath.Join(dir, "Fake.xcodeproj")
	// Seed the cache directly rather than shelling out to plutil, so this
	// test exercises the lookup logic without depending on a real pbxproj
	// file or the plutil binary.
	pbxprojCacheMu.Lock()
	pbxprojCache[fakeProjectPath] = proj
	pbxprojCacheMu.Unlock()
	defer resetPBXProjCache()

	settings := XcodeTargetSettings(fakeProjectPath, "MyApp", "Release")
	if settings["PRODUCT_BUNDLE_IDENTIFIER"] != "com.example.app.release" {
		t.Errorf("Release settings = %v, want the release bundle id", settings)
	}

	settings = XcodeTargetSettings(fakeProjectPath, "MyApp", "Debug")
	if settings["PRODUCT_BUNDLE_IDENTIFIER"] != "com.example.app" {
		t.Errorf("Debug settings = %v, want the debug bundle id", settings)
	}

	// Unknown configuration falls back to the target's first configuration
	// (Python original's behaviour when the requested name isn't found).
	settings = XcodeTargetSettings(fakeProjectPath, "MyApp", "Nonexistent")
	if settings["PRODUCT_BUNDLE_IDENTIFIER"] != "com.example.app" {
		t.Errorf("fallback settings = %v, want the first configuration's bundle id", settings)
	}
}

func TestXcodeTargetSettingsUnknownScheme(t *testing.T) {
	pbxprojCacheMu.Lock()
	pbxprojCache["/fake/none.xcodeproj"] = map[string]any{"objects": map[string]any{}}
	pbxprojCacheMu.Unlock()
	defer resetPBXProjCache()

	if got := XcodeTargetSettings("/fake/none.xcodeproj", "Nope", "Debug"); len(got) != 0 {
		t.Errorf("got %v, want empty map for an unknown scheme", got)
	}
}

func TestXcodeSettingRejectsVariableSubstitution(t *testing.T) {
	objects := map[string]any{
		"TARGET1": map[string]any{
			"isa":                     "PBXNativeTarget",
			"name":                    "MyApp",
			"buildConfigurationList": "CFGLIST1",
		},
		"CFGLIST1": map[string]any{"buildConfigurations": []any{"CFG_DEBUG"}},
		"CFG_DEBUG": map[string]any{
			"name": "Debug",
			"buildSettings": map[string]any{
				"PRODUCT_BUNDLE_IDENTIFIER": "$(PRODUCT_BUNDLE_IDENTIFIER)",
				"PRODUCT_NAME":              "MyApp",
			},
		},
	}
	path := "/fake/var-sub.xcodeproj"
	pbxprojCacheMu.Lock()
	pbxprojCache[path] = map[string]any{"objects": objects}
	pbxprojCacheMu.Unlock()
	defer resetPBXProjCache()

	if _, ok := XcodeSetting(path, "MyApp", "PRODUCT_BUNDLE_IDENTIFIER"); ok {
		t.Error("expected a $(...) substitution to be rejected as unresolved")
	}
	if val, ok := XcodeSetting(path, "MyApp", "PRODUCT_NAME"); !ok || val != "MyApp" {
		t.Errorf("got (%q, %v), want (\"MyApp\", true)", val, ok)
	}
}

func TestReadPBXProjMissingFileReturnsEmpty(t *testing.T) {
	defer resetPBXProjCache()
	got := ReadPBXProj(filepath.Join(t.TempDir(), "Nonexistent.xcodeproj"))
	if len(got) != 0 {
		t.Errorf("got %v, want empty map for a missing project", got)
	}
}

func TestReadPBXProjCaches(t *testing.T) {
	defer resetPBXProjCache()
	path := "/fake/cached.xcodeproj"
	pbxprojCacheMu.Lock()
	pbxprojCache[path] = map[string]any{"marker": "seeded"}
	pbxprojCacheMu.Unlock()

	got := ReadPBXProj(path)
	if got["marker"] != "seeded" {
		t.Errorf("ReadPBXProj bypassed the cache: got %v", got)
	}
}
