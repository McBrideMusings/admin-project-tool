//go:build darwin

package kinds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
)

// writeFakePbxproj writes a minimal real project.pbxproj (parsed by the real
// `plutil` binary, not a seeded cache) whose single target's Debug
// buildSettings hold bundleIdentifier verbatim — e.g.
// "com.example.app$(PRODUCT_BUNDLE_ID_SUFFIX)" plus a
// PRODUCT_BUNDLE_ID_SUFFIX entry, to exercise appleBundle end to end through
// the real pbxproj-reading path (luahost.ReadPBXProj -> XcodeTargetSettings
// -> XcodeSetting's $(...) expansion), not a seeded in-package cache.
func writeFakePbxproj(t *testing.T, bundleIdentifier, suffixValue string) string {
	t.Helper()
	dir := t.TempDir()
	projDir := filepath.Join(dir, "Fake.xcodeproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	suffixLine := ""
	if suffixValue != "" || bundleIdentifier != "" {
		suffixLine = `PRODUCT_BUNDLE_ID_SUFFIX = "` + suffixValue + `";`
	}
	content := `// !$*UTF8*$!
{
	archiveVersion = 1;
	objectVersion = 46;
	rootObject = ROOT;
	objects = {
		TARGET1 = {
			isa = PBXNativeTarget;
			name = MyApp;
			buildConfigurationList = CFGLIST1;
		};
		CFGLIST1 = {
			isa = XCConfigurationList;
			buildConfigurations = (
				CFG_DEBUG,
			);
		};
		CFG_DEBUG = {
			isa = XCBuildConfiguration;
			name = Debug;
			buildSettings = {
				PRODUCT_BUNDLE_IDENTIFIER = "` + bundleIdentifier + `";
				` + suffixLine + `
			};
		};
	};
}
`
	if err := os.WriteFile(filepath.Join(projDir, "project.pbxproj"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return projDir
}

// TestAppleBundleComposesRealPbxprojSubstitution pins the live-observed bug:
// a target whose raw pbxproj PRODUCT_BUNDLE_IDENTIFIER contains a $(...)
// substitution (a suffix key written into the same buildSettings dict, the
// pattern _compose_dev_settings's docstring describes) must resolve to a
// real, substituted bundle id through appleBundle — not copy the literal
// "$(...)" token into whatever a caller (a log predicate, a subsystem)
// builds on top of it. And the dev variant, composed purely from the
// resolved prod value, must carry the ".dev" suffix.
func TestAppleBundleComposesRealPbxprojSubstitution(t *testing.T) {
	proj := writeFakePbxproj(t, "com.example.app$(PRODUCT_BUNDLE_ID_SUFFIX)", "")
	cfg := map[string]any{"mac_scheme": "MyApp"}

	prod := appleBundle(filepath.Join(proj), cfg, "mac", false)
	if prod != "com.example.app" {
		t.Fatalf("prod bundle = %q, want %q (a $(...) substitution should have been composed, not copied)", prod, "com.example.app")
	}

	dev := appleBundle(proj, cfg, "mac", true)
	if dev != "com.example.app.dev" {
		t.Errorf("dev bundle = %q, want %q", dev, "com.example.app.dev")
	}
}

// TestAppleSubsystemNeverCarriesALiteralSubstitutionToken is the exact
// failure mode reported live: an unexpanded "$(...)" reaching a `log
// stream` predicate. appleSubsystem (used to build that predicate) must
// never return a value containing "$(" for a target using the bundle-suffix
// convention with no explicit manifest bundle id.
func TestAppleSubsystemNeverCarriesALiteralSubstitutionToken(t *testing.T) {
	proj := writeFakePbxproj(t, "com.example.app$(ADMIN_DEV_BUNDLE_SUFFIX)", "")
	cfg := map[string]any{"mac_scheme": "MyApp"}

	subsystem := appleSubsystem(proj, cfg, "mac", true)
	if subsystem == "" {
		// Unresolvable (ADMIN_DEV_BUNDLE_SUFFIX isn't written into the
		// pbxproj — see TestXcodeSettingRejectsSubstitutionOfAbsentKey) is
		// an acceptable outcome: empty, not broken.
		return
	}
	if containsSubstitutionToken(subsystem) {
		t.Errorf("subsystem = %q, contains an unexpanded $(...) token", subsystem)
	}
}

func containsSubstitutionToken(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '$' && s[i+1] == '(' {
			return true
		}
	}
	return false
}

// --- appleComposeSignSettings / appleComposeDevSettings -------------------

func TestAppleComposeSignSettingsEmptyWithNoSigningConfig(t *testing.T) {
	h := luahost.New(t.TempDir())
	got := appleComposeSignSettings(map[string]any{}, h, "", "mac")
	if got != "" {
		t.Errorf("got %q, want empty string with no signing keys configured", got)
	}
}

func TestAppleComposeSignSettingsLiteralIdentity(t *testing.T) {
	h := luahost.New(t.TempDir())
	cfg := map[string]any{
		"code_sign_identity": "Apple Development",
		"development_team":   "ABCDE12345",
	}
	got := appleComposeSignSettings(cfg, h, "", "mac")
	want := `CODE_SIGN_IDENTITY="Apple Development" DEVELOPMENT_TEAM="ABCDE12345"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAppleComposeDevSettingsRejectsExplicitBundleIdentifierOverride(t *testing.T) {
	h := luahost.New(t.TempDir())
	cfg := map[string]any{
		"mac_bundle":         "com.example.app",
		"dev_build_settings": "PRODUCT_BUNDLE_IDENTIFIER=com.example.app.dev",
	}
	if _, err := appleComposeDevSettings(cfg, h, "", "mac"); err == nil {
		t.Error("expected an error naming PRODUCT_BUNDLE_IDENTIFIER in dev_build_settings")
	}
}

func TestAppleComposeDevSettingsRejectsUnexpressibleDevBundle(t *testing.T) {
	h := luahost.New(t.TempDir())
	cfg := map[string]any{
		"mac_bundle":     "com.example.app",
		"mac_dev_bundle": "com.other.app",
	}
	if _, err := appleComposeDevSettings(cfg, h, "", "mac"); err == nil {
		t.Error("expected an error: mac_dev_bundle isn't the prod bundle plus a suffix")
	}
}

func TestAppleComposeDevSettingsEmitsDefaultDevSuffix(t *testing.T) {
	h := luahost.New(t.TempDir())
	cfg := map[string]any{"mac_bundle": "com.example.app"}
	got, err := appleComposeDevSettings(cfg, h, "", "mac")
	if err != nil {
		t.Fatalf("appleComposeDevSettings: %v", err)
	}
	if got != "ADMIN_DEV_BUNDLE_SUFFIX=.dev" {
		t.Errorf("got %q, want %q", got, "ADMIN_DEV_BUNDLE_SUFFIX=.dev")
	}
}

func TestAppleComposeDevSettingsCustomSuffix(t *testing.T) {
	h := luahost.New(t.TempDir())
	cfg := map[string]any{
		"mac_bundle":     "com.example.app",
		"mac_dev_bundle": "com.example.app.staging",
	}
	got, err := appleComposeDevSettings(cfg, h, "", "mac")
	if err != nil {
		t.Fatalf("appleComposeDevSettings: %v", err)
	}
	if got != "ADMIN_DEV_BUNDLE_SUFFIX=.staging" {
		t.Errorf("got %q, want %q", got, "ADMIN_DEV_BUNDLE_SUFFIX=.staging")
	}
}

func TestAppleComposeDevSettingsAppendsDevBuildSettingsBeforeSuffix(t *testing.T) {
	h := luahost.New(t.TempDir())
	cfg := map[string]any{
		"mac_bundle":         "com.example.app",
		"dev_build_settings": "OTHER_SETTING=1",
	}
	got, err := appleComposeDevSettings(cfg, h, "", "mac")
	if err != nil {
		t.Fatalf("appleComposeDevSettings: %v", err)
	}
	if got != "OTHER_SETTING=1 ADMIN_DEV_BUNDLE_SUFFIX=.dev" {
		t.Errorf("got %q, want %q", got, "OTHER_SETTING=1 ADMIN_DEV_BUNDLE_SUFFIX=.dev")
	}
}

// --- appleEntitlementsPaths / appleTargetNeedsProvisioningProfile ---------

func TestAppleTargetNeedsProvisioningProfileDeviceAlwaysTrue(t *testing.T) {
	h := luahost.New(t.TempDir())
	if !appleTargetNeedsProvisioningProfile(map[string]any{}, h, "", "ios") {
		t.Error("ios must always need a provisioning profile")
	}
	if !appleTargetNeedsProvisioningProfile(map[string]any{}, h, "", "tv") {
		t.Error("tv must always need a provisioning profile")
	}
}

func TestAppleTargetNeedsProvisioningProfileMacReadsEntitlements(t *testing.T) {
	root := t.TempDir()
	h := luahost.New(root)

	projDir := filepath.Join(root, "Fake.xcodeproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entPath := filepath.Join(root, "App.entitlements")
	entContent := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>com.apple.developer.icloud-container-identifiers</key>
	<array/>
</dict>
</plist>
`
	if err := os.WriteFile(entPath, []byte(entContent), 0o644); err != nil {
		t.Fatal(err)
	}
	content := `// !$*UTF8*$!
{
	archiveVersion = 1;
	objectVersion = 46;
	rootObject = ROOT;
	objects = {
		TARGET1 = {
			isa = PBXNativeTarget;
			name = MyApp;
			buildConfigurationList = CFGLIST1;
		};
		CFGLIST1 = {
			isa = XCConfigurationList;
			buildConfigurations = (
				CFG_DEBUG,
			);
		};
		CFG_DEBUG = {
			isa = XCBuildConfiguration;
			name = Debug;
			buildSettings = {
				CODE_SIGN_ENTITLEMENTS = "App.entitlements";
			};
		};
	};
}
`
	if err := os.WriteFile(filepath.Join(projDir, "project.pbxproj"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := map[string]any{"mac_scheme": "MyApp"}
	paths := appleEntitlementsPaths(cfg, h, projDir)
	if len(paths) != 1 || paths[0] != entPath {
		t.Fatalf("appleEntitlementsPaths = %v, want [%q]", paths, entPath)
	}

	if !appleTargetNeedsProvisioningProfile(cfg, h, projDir, "mac") {
		t.Error("expected true: entitlements declare com.apple.developer.icloud-container-identifiers")
	}
}

func TestAppleTargetNeedsProvisioningProfileMacPlainSandboxIsFalse(t *testing.T) {
	root := t.TempDir()
	h := luahost.New(root)
	// No mac_scheme at all -> appleEntitlementsPaths finds nothing -> false.
	cfg := map[string]any{}
	if appleTargetNeedsProvisioningProfile(cfg, h, "", "mac") {
		t.Error("expected false: no entitlements to read at all")
	}
}
