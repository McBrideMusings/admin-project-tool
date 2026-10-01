//go:build darwin

package kinds

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// repoRoot walks up from the current test file's directory looking for
// go.mod — the same technique internal/detect/detect_test.go uses to find
// this repo's own archetypes/ directory rather than a synthetic fixture, so
// these tests exercise the real Lua body shipped in
// archetypes/apple/archetype.toml instead of a copy that could drift from
// it (admin-vnnw).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from cwd")
		}
		dir = parent
	}
}

// loadIconsAction loads the real [actions.icons] body from
// archetypes/apple/archetype.toml.
func loadIconsAction(t *testing.T) manifest.Action {
	t.Helper()
	root := repoRoot(t)
	sp := archetype.SearchPath{{Label: "repo", FS: os.DirFS(filepath.Join(root, "archetypes"))}}
	arch, err := archetype.Load(sp, "apple")
	if err != nil {
		t.Fatalf("archetype.Load(apple): %v", err)
	}
	action, ok := arch.Partial.Actions["icons"]
	if !ok {
		t.Fatal("apple archetype has no [actions.icons]")
	}
	return action
}

// writeFakeIcon writes a small, real, decodable PNG standing in for a
// committed app icon — a solid maroon square, distinguishable from anything
// apple.generate_icons would render (a background-color square in a
// different color, or a glyph over one) and decodable by the swap-dev/
// restore path, which round-trips the file through image/png.
func writeFakeIcon(t *testing.T, path string) []byte {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	maroon := color.RGBA{R: 128, G: 0, B: 32, A: 255}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, maroon)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	content := buf.Bytes()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return content
}

func iconsManifest(t *testing.T, projectRoot string, iconsCfg map[string]any) *manifest.Manifest {
	t.Helper()
	return &manifest.Manifest{
		Root:     projectRoot,
		Settings: map[string]map[string]any{"apple": {"icons": iconsCfg}},
	}
}

// TestIconsRegenerateWithoutGlyphRefusesAndLeavesArtworkUntouched is
// acceptance criterion 1 of admin-vnnw: `admin icons regenerate` in a
// project whose [apple.icons] configures neither glyph_text nor glyph_svg
// must refuse and change nothing on disk, rather than overwriting a
// committed app icon with a flat generated square.
//
// MUTATION EVIDENCE: revert the glyph_text/glyph_svg guard added to
// do_regenerate in archetypes/apple/archetype.toml and this test fails —
// the runner returns nil instead of an error, and 1024.png's bytes change
// from the committed fixture to a generated flat-color square.
func TestIconsRegenerateWithoutGlyphRefusesAndLeavesArtworkUntouched(t *testing.T) {
	dir := t.TempDir()
	iconPath := filepath.Join(dir, "AppIcon.appiconset", "1024.png")
	before := writeFakeIcon(t, iconPath)

	action := loadIconsAction(t)
	t.Chdir(dir)
	m := iconsManifest(t, dir, map[string]any{
		"mac_asset_dir": "AppIcon.appiconset",
		"sizes":         []any{1024},
		"bg_color":      "#336699",
	})
	runner, err := makeLua("icons", action, m, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}

	runErr := runner([]string{"regenerate"})
	if runErr == nil {
		t.Fatal("runner([regenerate]) = nil, want an error — no glyph_text/glyph_svg configured")
	}
	if got := runErr.Error(); !bytes.Contains([]byte(got), []byte("[apple.icons] needs glyph_text or glyph_svg")) {
		t.Errorf("error = %q, want it to contain %q", got, "[apple.icons] needs glyph_text or glyph_svg")
	}

	after, err := os.ReadFile(iconPath)
	if err != nil {
		t.Fatalf("read icon after run: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("1024.png changed: committed artwork was overwritten despite the glyph guard")
	}
}

// TestIconsBareArgPrintsUsageAndChangesNothing is acceptance criterion 2:
// `admin icons` with no subcommand must print the usage line rather than
// silently defaulting to the destructive `regenerate` verb.
//
// MUTATION EVIDENCE: revert `local sub = args[1]` / `if not sub then ...`
// back to `local sub = args[1] or "regenerate"` and this test fails — the
// icon gets regenerated (bytes change, guard error fires instead of the
// usage line landing on stdout).
func TestIconsBareArgPrintsUsageAndChangesNothing(t *testing.T) {
	dir := t.TempDir()
	iconPath := filepath.Join(dir, "AppIcon.appiconset", "1024.png")
	before := writeFakeIcon(t, iconPath)

	action := loadIconsAction(t)
	t.Chdir(dir)
	m := iconsManifest(t, dir, map[string]any{
		"mac_asset_dir": "AppIcon.appiconset",
		"sizes":         []any{1024},
	})
	runner, err := makeLua("icons", action, m, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}

	var runErr error
	out := captureStdoutTo(t, dir, func() {
		runErr = runner(nil)
	})
	if runErr != nil {
		t.Fatalf("runner(nil) = %v, want nil (bare arg just prints usage)", runErr)
	}
	if !bytes.Contains([]byte(out), []byte("usage: admin icons [regenerate|swap-dev|restore]")) {
		t.Errorf("stdout = %q, want it to contain the usage line", out)
	}

	after, err := os.ReadFile(iconPath)
	if err != nil {
		t.Fatalf("read icon after run: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("1024.png changed on a bare `admin icons` call")
	}
}

// TestIconsRegenerateWithGlyphStillWorks is acceptance criterion 3: a
// project that DOES configure glyph_text keeps regenerating successfully —
// the guard must not gate the legitimate case.
func TestIconsRegenerateWithGlyphStillWorks(t *testing.T) {
	dir := t.TempDir()
	iconPath := filepath.Join(dir, "AppIcon.appiconset", "1024.png")
	before := writeFakeIcon(t, iconPath)

	action := loadIconsAction(t)
	t.Chdir(dir)
	m := iconsManifest(t, dir, map[string]any{
		"mac_asset_dir": "AppIcon.appiconset",
		"sizes":         []any{1024},
		"bg_color":      "#336699",
		"glyph_text":    "A",
	})
	runner, err := makeLua("icons", action, m, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}

	if runErr := runner([]string{"regenerate"}); runErr != nil {
		t.Fatalf("runner([regenerate]) = %v, want nil (glyph_text is configured)", runErr)
	}

	after, err := os.ReadFile(iconPath)
	if err != nil {
		t.Fatalf("read icon after run: %v", err)
	}
	if bytes.Equal(before, after) {
		t.Error("1024.png unchanged: regeneration did not actually run")
	}
}

// TestIconsSwapDevAndRestoreWorkWithoutGlyphConfigured is acceptance
// criterion 4: swap-dev/restore overlay a dev badge onto committed art and
// must keep working in a no-glyph project — the glyph guard belongs only to
// do_regenerate, never to these two subcommands.
func TestIconsSwapDevAndRestoreWorkWithoutGlyphConfigured(t *testing.T) {
	dir := t.TempDir()
	iconPath := filepath.Join(dir, "AppIcon.appiconset", "1024.png")
	writeFakeIcon(t, iconPath)

	action := loadIconsAction(t)
	t.Chdir(dir)
	m := iconsManifest(t, dir, map[string]any{
		"mac_asset_dir": "AppIcon.appiconset",
		"sizes":         []any{1024},
	})
	runner, err := makeLua("icons", action, m, nil)
	if err != nil {
		t.Fatalf("makeLua: %v", err)
	}

	if runErr := runner([]string{"swap-dev"}); runErr != nil {
		t.Fatalf("runner([swap-dev]) = %v, want nil (not gated by the glyph guard)", runErr)
	}
	if runErr := runner([]string{"restore"}); runErr != nil {
		t.Fatalf("runner([restore]) = %v, want nil (not gated by the glyph guard)", runErr)
	}
}
