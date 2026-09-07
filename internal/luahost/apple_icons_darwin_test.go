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

func TestRenderDevBannerBottomRightCornerRibbon(t *testing.T) {
	const size = 1024
	img := RenderDevBanner(size)

	// The band sits in the bottom-right corner: a point there is opaque red,
	// and the image's own center — where the old (buggy) diagonal used to
	// run straight through — is untouched.
	r, g, b, a := img.At(3*size/4, 3*size/4).RGBA()
	if a>>8 != 240 || r>>8 < 200 || g>>8 > 100 || b>>8 > 100 {
		t.Errorf("(0.75size,0.75size) = (%d,%d,%d,%d), want opaque banner red", r>>8, g>>8, b>>8, a>>8)
	}
	if _, _, _, a := img.At(size/2, size/2).RGBA(); a != 0 {
		t.Errorf("image center alpha = %d, want fully transparent", a)
	}
}

func TestRenderDevBannerRunsBottomLeftToTopRight(t *testing.T) {
	const size = 1024
	img := RenderDevBanner(size)

	// The midpoint of the painted run in a column is a point on the band's
	// own center line. A larger x must land at a smaller y — the band runs
	// bottom-left to top-right, not top-left to bottom-right.
	midY := func(x int) int {
		min, max := -1, -1
		for y := 0; y < size; y++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0 {
				if min == -1 {
					min = y
				}
				max = y
			}
		}
		if min == -1 {
			t.Fatalf("column x=%d has no painted pixels", x)
		}
		return (min + max) / 2
	}

	yLeft, yRight := midY(300), midY(800)
	if yRight >= yLeft {
		t.Errorf("band center y at x=800 (%d) should be smaller than at x=300 (%d)", yRight, yLeft)
	}
}

func TestRenderDevBannerHasWhiteDEVText(t *testing.T) {
	img := RenderDevBanner(1024)

	found := false
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y && !found; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a != 0 && r>>8 > 230 && g>>8 > 230 && bl>>8 > 230 {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("expected at least one white \"DEV\" pixel inside the band")
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

// --- ApplyTVDevBanner (overlay-in-place, admin-3rgp.49) ------------------

// writeRectPNG is writePNG's non-square sibling — tvOS layers and Top
// Shelf images are rectangular, not square.
func writeRectPNG(t *testing.T, path string, w, h int, c color.Color) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
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

// fakeBrandassets writes just enough of a real-art .brandassets tree —
// Front/Middle/Back PNGs for one imagestack, plus one Top Shelf image — for
// ApplyTVDevBanner to walk, without going through GenerateTVIcons.
func fakeBrandassets(t *testing.T, root string) string {
	t.Helper()
	brand := filepath.Join(root, "App.brandassets")
	writeRectPNG(t, tvLayerPNG(brand, "App Icon.imagestack", "Front", "Front.png"), 400, 240, color.Transparent)
	writeRectPNG(t, tvLayerPNG(brand, "App Icon.imagestack", "Middle", "Middle.png"), 400, 240, color.Transparent)
	writeRectPNG(t, tvLayerPNG(brand, "App Icon.imagestack", "Back", "Back.png"), 400, 240, color.NRGBA{R: 30, G: 30, B: 46, A: 255})
	writeRectPNG(t, filepath.Join(brand, "Top Shelf Image.imageset", "tv.png"), 1920, 720, color.NRGBA{R: 30, G: 30, B: 46, A: 255})
	return brand
}

func TestApplyTVDevBannerTouchesOnlyFrontAndTopShelf(t *testing.T) {
	root := t.TempDir()
	brand := fakeBrandassets(t, root)

	backBefore, err := os.ReadFile(tvLayerPNG(brand, "App Icon.imagestack", "Back", "Back.png"))
	if err != nil {
		t.Fatal(err)
	}
	middleBefore, err := os.ReadFile(tvLayerPNG(brand, "App Icon.imagestack", "Middle", "Middle.png"))
	if err != nil {
		t.Fatal(err)
	}
	frontBefore, err := os.ReadFile(tvLayerPNG(brand, "App Icon.imagestack", "Front", "Front.png"))
	if err != nil {
		t.Fatal(err)
	}

	if ok := ApplyTVDevBanner(testHost(t), brand); !ok {
		t.Fatal("ApplyTVDevBanner returned false")
	}

	backAfter, _ := os.ReadFile(tvLayerPNG(brand, "App Icon.imagestack", "Back", "Back.png"))
	if string(backAfter) != string(backBefore) {
		t.Error("Back layer must be untouched by the overlay")
	}
	middleAfter, _ := os.ReadFile(tvLayerPNG(brand, "App Icon.imagestack", "Middle", "Middle.png"))
	if string(middleAfter) != string(middleBefore) {
		t.Error("Middle layer must be untouched by the overlay")
	}
	frontAfter, _ := os.ReadFile(tvLayerPNG(brand, "App Icon.imagestack", "Front", "Front.png"))
	if string(frontAfter) == string(frontBefore) {
		t.Error("Front layer must carry the banner after overlay")
	}

	topShelf := decodePNG(t, filepath.Join(brand, "Top Shelf Image.imageset", "tv.png"))
	painted := false
	b := topShelf.Bounds()
	for y := b.Min.Y; y < b.Max.Y && !painted; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := topShelf.At(x, y).RGBA()
			if r>>8 > 150 && g>>8 < 100 && bl>>8 < 100 {
				painted = true
				break
			}
		}
	}
	if !painted {
		t.Error("Top Shelf image should carry the red banner stripe somewhere")
	}
}

func TestApplyTVDevBannerPreservesAlphaOnFront(t *testing.T) {
	root := t.TempDir()
	brand := fakeBrandassets(t, root)

	if ok := ApplyTVDevBanner(testHost(t), brand); !ok {
		t.Fatal("ApplyTVDevBanner returned false")
	}

	front := decodePNG(t, tvLayerPNG(brand, "App Icon.imagestack", "Front", "Front.png"))
	// A corner far from the banner's bottom-right stripe should still be
	// transparent — unlike ApplyDevBannerToPNGFile, this overlay must not
	// flatten the Front layer to opaque RGB.
	if _, _, _, a := front.At(0, 0).RGBA(); a != 0 {
		t.Errorf("Front corner alpha = %d, want fully transparent (alpha preserved, not flattened)", a)
	}
}

func TestApplyTVDevBannerSkipsMissingDirs(t *testing.T) {
	root := t.TempDir()
	brand := filepath.Join(root, "Empty.brandassets")
	if ok := ApplyTVDevBanner(testHost(t), brand); !ok {
		t.Error("ApplyTVDevBanner on a brandassets tree with no matching dirs should still return true (no-op, not an error)")
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
