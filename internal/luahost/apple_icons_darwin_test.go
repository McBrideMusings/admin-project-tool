//go:build darwin

package luahost

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// --- backup / restore (appiconset) --------------------------------------

func writePNG(t *testing.T, path string, size int, c color.Color) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestBackupAndRestoreIconsRoundTrip(t *testing.T) {
	root := t.TempDir()
	macDir := filepath.Join(root, "mac.appiconset")
	iosDir := filepath.Join(root, "ios.appiconset")
	writePNG(t, filepath.Join(macDir, "128.png"), 4, color.White)
	writePNG(t, filepath.Join(iosDir, "60.png"), 4, color.White)

	iconDirs := map[string]string{"mac": macDir, "ios": iosDir}
	backupDir := filepath.Join(root, "backup")

	if err := BackupIcons(iconDirs, backupDir); err != nil {
		t.Fatalf("BackupIcons: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backupDir, "mac", "128.png")); err != nil {
		t.Fatalf("backup missing mac/128.png: %v", err)
	}

	// Mutate the "live" icon to simulate swap_dev_icons having run.
	writePNG(t, filepath.Join(macDir, "128.png"), 4, color.Black)

	if err := RestoreIcons(iconDirs, backupDir); err != nil {
		t.Fatalf("RestoreIcons: %v", err)
	}

	f, err := os.Open(filepath.Join(macDir, "128.png"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(0, 0).RGBA()
	if r>>8 != 255 || g>>8 != 255 || b>>8 != 255 {
		t.Errorf("restored pixel = (%d,%d,%d), want white (the backed-up original)", r>>8, g>>8, b>>8)
	}
	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Error("RestoreIcons should remove backupDir when done")
	}
}

func TestBackupIconsSkipsMissingSourceDir(t *testing.T) {
	root := t.TempDir()
	iconDirs := map[string]string{"mac": filepath.Join(root, "does-not-exist")}
	if err := BackupIcons(iconDirs, filepath.Join(root, "backup")); err != nil {
		t.Fatalf("BackupIcons should tolerate a missing source dir, got: %v", err)
	}
}

func TestBackupTVIconsCopiesWholeTree(t *testing.T) {
	root := t.TempDir()
	brand := filepath.Join(root, "App.brandassets")
	writePNG(t, filepath.Join(brand, "Layered Icon.imagestack", "Front.imagestacklayer", "content.png"), 4, color.White)

	backupDir := filepath.Join(root, "backup")
	if err := BackupTVIcons(brand, backupDir); err != nil {
		t.Fatalf("BackupTVIcons: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backupDir, tvBackupLabel, "Layered Icon.imagestack", "Front.imagestacklayer", "content.png")); err != nil {
		t.Fatalf("nested file not copied: %v", err)
	}

	if err := os.RemoveAll(brand); err != nil {
		t.Fatal(err)
	}
	if err := RestoreTVIcons(brand, backupDir); err != nil {
		t.Fatalf("RestoreTVIcons: %v", err)
	}
	if _, err := os.Stat(filepath.Join(brand, "Layered Icon.imagestack", "Front.imagestacklayer", "content.png")); err != nil {
		t.Fatalf("restored tree missing nested file: %v", err)
	}
	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Error("RestoreTVIcons should remove backupDir when done")
	}
}

// --- dev banner rendering ------------------------------------------------

func TestRenderDevBannerCornersUnpaintedCenterInBand(t *testing.T) {
	img := RenderDevBanner(200)

	// The top-left corner should be untouched (transparent) — the banner is
	// confined to a diagonal band, not the whole canvas.
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Errorf("top-left alpha = %d, want fully transparent", a)
	}

	// Somewhere along the band's own diagonal near the shifted center should
	// be painted with the banner color.
	painted := false
	for i := 0; i < 200; i++ {
		if _, _, _, a := img.At(i, i).RGBA(); a != 0 {
			painted = true
			break
		}
	}
	if !painted {
		t.Error("expected at least one painted pixel on the main diagonal")
	}
}

func TestRenderDevBannerSizeMatchesCanvas(t *testing.T) {
	img := RenderDevBanner(64)
	b := img.Bounds()
	if b.Dx() != 64 || b.Dy() != 64 {
		t.Errorf("bounds = %v, want 64x64", b)
	}
}

func TestApplyDevBannerToPNGFileFlattensAlpha(t *testing.T) {
	path := filepath.Join(t.TempDir(), "icon.png")
	writePNG(t, path, 64, color.NRGBA{R: 10, G: 200, B: 10, A: 255})

	if err := ApplyDevBannerToPNGFile(path); err != nil {
		t.Fatalf("ApplyDevBannerToPNGFile: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	// Untouched corner should keep the original green, fully opaque.
	r, g, b, a := img.At(0, 0).RGBA()
	if a>>8 != 255 {
		t.Errorf("corner alpha = %d, want fully opaque after flatten", a>>8)
	}
	if g>>8 < 150 || r>>8 > 50 {
		t.Errorf("corner color = (%d,%d,%d), want the original green untouched", r>>8, g>>8, b>>8)
	}
}

// --- Icon Composer (.icon) banner inject / strip -------------------------

func TestIconComposerInjectBannerIsIdempotent(t *testing.T) {
	doc := map[string]any{
		"groups": []any{
			map[string]any{"layers": []any{map[string]any{"name": "base"}}},
		},
	}

	IconComposerInjectBanner(doc)
	groups := doc["groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("after one inject: %d groups, want 2", len(groups))
	}

	// Re-injecting (a re-swap) must not accumulate a second banner group.
	IconComposerInjectBanner(doc)
	groups = doc["groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("after re-inject: %d groups, want 2 (idempotent)", len(groups))
	}

	bannerCount := 0
	for _, g := range groups {
		if isBannerGroup(g.(map[string]any)) {
			bannerCount++
		}
	}
	if bannerCount != 1 {
		t.Errorf("banner groups = %d, want exactly 1", bannerCount)
	}
}

func TestIconComposerStripBannerRemovesOnlyBannerGroup(t *testing.T) {
	doc := map[string]any{
		"groups": []any{
			map[string]any{"layers": []any{map[string]any{"name": "base"}}},
		},
	}
	IconComposerInjectBanner(doc)
	IconComposerStripBanner(doc)

	groups := doc["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("after strip: %d groups, want 1 (just base)", len(groups))
	}
	if isBannerGroup(groups[0].(map[string]any)) {
		t.Error("the remaining group should be the base group, not the banner")
	}
}

func TestSwapAndRestoreIconComposerRoundTrip(t *testing.T) {
	root := t.TempDir()
	iconPath := filepath.Join(root, "AppIcon.icon")
	jsonPath := iconComposerJSONPath(iconPath)
	if err := os.MkdirAll(iconPath, 0o755); err != nil {
		t.Fatal(err)
	}
	original := map[string]any{
		"groups": []any{map[string]any{"layers": []any{map[string]any{"name": "base"}}}},
	}
	b, _ := json.Marshal(original)
	if err := os.WriteFile(jsonPath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	backupDir := filepath.Join(root, "backup")
	ok, err := SwapDevIconComposer(iconPath, backupDir)
	if err != nil || !ok {
		t.Fatalf("SwapDevIconComposer: ok=%v err=%v", ok, err)
	}

	// The banner asset should exist and the json should carry the banner group.
	if _, err := os.Stat(filepath.Join(iconComposerAssetsDir(iconPath), devBannerAsset)); err != nil {
		t.Fatalf("banner asset not written: %v", err)
	}
	swapped, err := readJSONDoc(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	groups := swapped["groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("swapped json has %d groups, want 2", len(groups))
	}

	if err := RestoreIconComposer(iconPath, backupDir); err != nil {
		t.Fatalf("RestoreIconComposer: %v", err)
	}
	restored, err := readJSONDoc(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredBytes, _ := json.Marshal(restored)
	originalBytes, _ := json.Marshal(original)
	if string(restoredBytes) != string(originalBytes) {
		t.Errorf("restored json = %s, want the byte-exact original %s", restoredBytes, originalBytes)
	}
	if _, err := os.Stat(filepath.Join(iconComposerAssetsDir(iconPath), devBannerAsset)); !os.IsNotExist(err) {
		t.Error("RestoreIconComposer should remove the generated banner asset")
	}
	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Error("RestoreIconComposer should remove backupDir")
	}
}

func TestSwapDevIconComposerSkipsWhenNoIconJSON(t *testing.T) {
	root := t.TempDir()
	ok, err := SwapDevIconComposer(filepath.Join(root, "NotAnIcon.icon"), filepath.Join(root, "backup"))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ok {
		t.Error("expected false when icon.json is missing")
	}
}
