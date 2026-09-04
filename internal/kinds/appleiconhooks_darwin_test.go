//go:build darwin

package kinds

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppleIconDirsOnlyConfiguredEntries(t *testing.T) {
	cfg := map[string]any{
		"icons": map[string]any{
			"mac_asset_dir": "Mac/Assets.xcassets/AppIcon.appiconset",
			"tv_asset_dir":  "TV/Assets.xcassets/App Icon.brandassets",
		},
	}
	dirs := appleIconDirs(cfg)
	if dirs["mac"] != "Mac/Assets.xcassets/AppIcon.appiconset" {
		t.Fatalf("mac dir = %q, want the configured mac_asset_dir", dirs["mac"])
	}
	if _, ok := dirs["ios"]; ok {
		t.Fatalf("dirs[ios] present with no ios_asset_dir configured: %+v", dirs)
	}
	if _, ok := dirs["tv"]; ok {
		t.Fatalf("dirs[tv] present — appleIconDirs only mirrors mac/ios, tv is out of scope: %+v", dirs)
	}
}

func TestAppleIconDirsEmptyWithNoIconsConfigured(t *testing.T) {
	dirs := appleIconDirs(map[string]any{})
	if len(dirs) != 0 {
		t.Fatalf("dirs = %+v, want empty with no [apple.icons] table", dirs)
	}
}

func TestAppleResolveDevDisplayNameExplicitOverride(t *testing.T) {
	cfg := map[string]any{"ios_dev_display_name": "Custom Dev Name"}
	got := appleResolveDevDisplayName(cfg, "ios", "MyApp")
	if got != "Custom Dev Name" {
		t.Fatalf("got %q, want the explicit ios_dev_display_name override", got)
	}
}

func TestAppleResolveDevDisplayNameDefaultsToProcessPlusDev(t *testing.T) {
	got := appleResolveDevDisplayName(map[string]any{}, "ios", "MyApp")
	if got != "MyApp Dev" {
		t.Fatalf("got %q, want %q", got, "MyApp Dev")
	}
}

func TestAppleResolveDevDisplayNameEmptyWithNoProcess(t *testing.T) {
	got := appleResolveDevDisplayName(map[string]any{}, "ios", "")
	if got != "" {
		t.Fatalf("got %q, want empty (no-op) with no process name and no override", got)
	}
}

func TestIsAppleBackupDirPopulatedFalseWhenMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	if isAppleBackupDirPopulated(dir) {
		t.Fatal("reported populated for a directory that does not exist")
	}
}

func TestIsAppleBackupDirPopulatedFalseWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	if isAppleBackupDirPopulated(dir) {
		t.Fatal("reported populated for an empty directory")
	}
}

func TestIsAppleBackupDirPopulatedTrueWithEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "icon.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isAppleBackupDirPopulated(dir) {
		t.Fatal("reported not populated for a directory holding a stranded backup file")
	}
}

func TestAppleBuildIconHooksNoOpWithNoIconsConfigured(t *testing.T) {
	pre, post := appleBuildIconHooks(map[string]any{}, nil)
	if err := pre(); err != nil {
		t.Fatalf("pre() = %v, want nil no-op with no [apple.icons] configured", err)
	}
	if err := post(); err != nil {
		t.Fatalf("post() = %v, want nil no-op with no [apple.icons] configured", err)
	}
}
