//go:build darwin

package luahost

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testHost returns a Host with a real Stdout/Stderr (so Err/Warn don't panic
// on a nil writer) and logging inert — no set_log_context call means
// logWrite's materializeLogContext no-ops, so no log file is created.
func testHost(t *testing.T) *Host {
	t.Helper()
	return New(t.TempDir())
}

func decodePNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return img
}

// minAlpha returns the minimum alpha value found in img — the same
// diagnostic ADR-0007 names as the only reliable full-bleed-vs-inset check.
func minAlpha(img image.Image) uint32 {
	b := img.Bounds()
	min := uint32(1 << 16)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a < min {
				min = a
			}
		}
	}
	return min
}

// --- hexToNRGBA -----------------------------------------------------------

func TestHexToNRGBAParsesWithAndWithoutHash(t *testing.T) {
	c, err := hexToNRGBA("#1E1E2E")
	if err != nil {
		t.Fatalf("hexToNRGBA: %v", err)
	}
	if c.R != 0x1E || c.G != 0x1E || c.B != 0x2E || c.A != 255 {
		t.Errorf("got %+v, want R=0x1E G=0x1E B=0x2E A=255", c)
	}
	c2, err := hexToNRGBA("FFFFFF")
	if err != nil || c2.R != 255 {
		t.Errorf("bare hex (no #) should parse: %+v, %v", c2, err)
	}
}

func TestHexToNRGBARejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "#ZZZZZZ", "#FFF", "not-a-color"} {
		if _, err := hexToNRGBA(bad); err == nil {
			t.Errorf("hexToNRGBA(%q) should have failed", bad)
		}
	}
}

// --- GenerateIcons ----------------------------------------------------------

func TestGenerateIconsWritesOpaqueRGBAtEachSize(t *testing.T) {
	root := t.TempDir()
	ok := GenerateIcons(testHost(t), []int{16, 128}, root, IconOptions{
		BGColor:   "#1E1E2E",
		GlyphText: "A",
	})
	if !ok {
		t.Fatal("GenerateIcons returned false")
	}

	for _, size := range []int{16, 128} {
		path := filepath.Join(root, strconv.Itoa(size)+".png")
		img := decodePNG(t, path)
		b := img.Bounds()
		if b.Dx() != size || b.Dy() != size {
			t.Errorf("size %d: bounds = %v, want %dx%d", size, b, size, size)
		}
		if minAlpha(img) != 0xffff {
			t.Errorf("size %d: min alpha = %#x, want fully opaque (ADR-0007)", size, minAlpha(img))
		}
	}
}

func TestGenerateIconsCustomFilenameFormat(t *testing.T) {
	root := t.TempDir()
	ok := GenerateIcons(testHost(t), []int{32}, root, IconOptions{
		FilenameFmt: "icon-{size}x{size}.png",
	})
	if !ok {
		t.Fatal("GenerateIcons returned false")
	}
	if _, err := os.Stat(filepath.Join(root, "icon-32x32.png")); err != nil {
		t.Errorf("custom filename_format not honored: %v", err)
	}
}

// TestGenerateIconsGlyphSVGFailsWithClearMessage exercises admin-3rgp.48's
// hard requirement: a configured glyph_svg must fail loudly, naming the
// dropped capability, never silently render a blank or wrong icon.
func TestGenerateIconsGlyphSVGFailsWithClearMessage(t *testing.T) {
	root := t.TempDir()
	h := testHost(t)
	var stderr bytes.Buffer
	h.Stderr = &stderr

	ok := GenerateIcons(h, []int{64}, root, IconOptions{
		BGColor:  "#000000",
		GlyphSVG: "/some/glyph.svg",
	})
	if ok {
		t.Fatal("GenerateIcons should fail when glyph_svg is configured")
	}
	if _, err := os.Stat(filepath.Join(root, "64.png")); err == nil {
		t.Error("no icon should be written when glyph_svg is rejected")
	}
	msg := stderr.String()
	if !strings.Contains(msg, "glyph_svg") || !strings.Contains(msg, "admin-3rgp.48") {
		t.Errorf("error message %q does not name the dropped capability", msg)
	}
}

// TestGenerateTVIconsGlyphSVGFailsWithClearMessage is the tvOS analog: the
// Front-layer render is the one that touches the glyph, and it must reject
// glyph_svg the same way generate_icons does.
func TestGenerateTVIconsGlyphSVGFailsWithClearMessage(t *testing.T) {
	root := t.TempDir()
	brand := filepath.Join(root, "App.brandassets")
	h := testHost(t)
	var stderr bytes.Buffer
	h.Stderr = &stderr

	ok := GenerateTVIcons(h, brand, IconOptions{BGColor: "#123456", GlyphSVG: "/some/glyph.svg"})
	if ok {
		t.Fatal("GenerateTVIcons should fail when glyph_svg is configured")
	}
	if !strings.Contains(stderr.String(), "glyph_svg") {
		t.Errorf("error message %q does not name the dropped capability", stderr.String())
	}
}

func TestGenerateIconsDevBannerStaysOpaque(t *testing.T) {
	root := t.TempDir()
	ok := GenerateIcons(testHost(t), []int{100}, root, IconOptions{
		GlyphText: "A",
		Dev:       true,
	})
	if !ok {
		t.Fatal("GenerateIcons returned false")
	}
	img := decodePNG(t, filepath.Join(root, "100.png"))
	if minAlpha(img) != 0xffff {
		t.Errorf("dev-bannered icon min alpha = %#x, want fully opaque", minAlpha(img))
	}

	plainRoot := t.TempDir()
	GenerateIcons(testHost(t), []int{100}, plainRoot, IconOptions{GlyphText: "A"})
	plain := decodePNG(t, filepath.Join(plainRoot, "100.png"))

	// The bottom-right corner (where the banner lands) must differ between
	// the dev and non-dev renders.
	r1, g1, b1, _ := img.At(95, 95).RGBA()
	r2, g2, b2, _ := plain.At(95, 95).RGBA()
	if r1 == r2 && g1 == g2 && b1 == b2 {
		t.Error("dev banner did not change the bottom-right corner pixel")
	}
}

func TestGenerateIconsInvalidBGColorFailsGracefully(t *testing.T) {
	ok := GenerateIcons(testHost(t), []int{16}, t.TempDir(), IconOptions{BGColor: "not-a-color"})
	if ok {
		t.Error("expected false for an invalid bg_color")
	}
}

// --- GenerateTVIcons ---------------------------------------------------------

func tvLayerPNG(brand, stack, layer, fname string) string {
	return filepath.Join(brand, stack, layer+".imagestacklayer", "Content.imageset", fname)
}

func TestGenerateTVIconsWritesFullBrandassetsSchema(t *testing.T) {
	root := t.TempDir()
	brand := filepath.Join(root, "App.brandassets")
	ok := GenerateTVIcons(testHost(t), brand, IconOptions{BGColor: "#123456", GlyphText: "X"})
	if !ok {
		t.Fatal("GenerateTVIcons returned false")
	}

	b, err := os.ReadFile(filepath.Join(brand, "Contents.json"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, a := range top["assets"].([]any) {
		names[a.(map[string]any)["filename"].(string)] = true
	}
	for _, want := range []string{
		"App Icon.imagestack", "App Icon - App Store.imagestack",
		"Top Shelf Image.imageset", "Top Shelf Image Wide.imageset",
	} {
		if !names[want] {
			t.Errorf("top-level Contents.json missing %q", want)
		}
	}

	for _, layer := range []string{"Front", "Middle", "Back"} {
		for _, fname := range []string{layer + ".png", layer + "@2x.png"} {
			if _, err := os.Stat(tvLayerPNG(brand, "App Icon.imagestack", layer, fname)); err != nil {
				t.Errorf("missing %s: %v", fname, err)
			}
		}
	}
	if _, err := os.Stat(tvLayerPNG(brand, "App Icon - App Store.imagestack", "Back", "Back.png")); err != nil {
		t.Errorf("App Store Back.png missing: %v", err)
	}
	if _, err := os.Stat(tvLayerPNG(brand, "App Icon - App Store.imagestack", "Back", "Back@2x.png")); err == nil {
		t.Error("App Store icon should be 1x only")
	}

	for _, fname := range []string{"tv.png", "tv@2x.png", "tv-marketing.png", "tv-marketing@2x.png"} {
		if _, err := os.Stat(filepath.Join(brand, "Top Shelf Image.imageset", fname)); err != nil {
			t.Errorf("Top Shelf missing %s: %v", fname, err)
		}
	}
}

func TestGenerateTVIconsLayerSizesAndOpacity(t *testing.T) {
	root := t.TempDir()
	brand := filepath.Join(root, "brand.brandassets")
	GenerateTVIcons(testHost(t), brand, IconOptions{BGColor: "#123456", GlyphText: "X"})

	back := decodePNG(t, tvLayerPNG(brand, "App Icon.imagestack", "Back", "Back.png"))
	if bb := back.Bounds(); bb.Dx() != 400 || bb.Dy() != 240 {
		t.Errorf("Back size = %v, want 400x240", bb)
	}
	if minAlpha(back) != 0xffff {
		t.Error("Back layer must be fully opaque — tvOS errors otherwise")
	}

	front := decodePNG(t, tvLayerPNG(brand, "App Icon.imagestack", "Front", "Front.png"))
	if fb := front.Bounds(); fb.Dx() != 400 || fb.Dy() != 240 {
		t.Errorf("Front size = %v, want 400x240", fb)
	}
	if minAlpha(front) == 0xffff {
		t.Error("Front layer must carry transparency for the parallax effect")
	}

	storeBack := decodePNG(t, tvLayerPNG(brand, "App Icon - App Store.imagestack", "Back", "Back.png"))
	if sb := storeBack.Bounds(); sb.Dx() != 1280 || sb.Dy() != 768 {
		t.Errorf("App Store Back size = %v, want 1280x768", sb)
	}

	top1x := decodePNG(t, filepath.Join(brand, "Top Shelf Image.imageset", "tv.png"))
	top2x := decodePNG(t, filepath.Join(brand, "Top Shelf Image.imageset", "tv@2x.png"))
	if b := top1x.Bounds(); b.Dx() != 1920 || b.Dy() != 720 {
		t.Errorf("top shelf 1x = %v, want 1920x720", b)
	}
	if b := top2x.Bounds(); b.Dx() != 3840 || b.Dy() != 1440 {
		t.Errorf("top shelf 2x = %v, want 3840x1440", b)
	}
	wide1x := decodePNG(t, filepath.Join(brand, "Top Shelf Image Wide.imageset", "tv.png"))
	if b := wide1x.Bounds(); b.Dx() != 2320 || b.Dy() != 720 {
		t.Errorf("wide top shelf 1x = %v, want 2320x720", b)
	}
}

func TestGenerateTVIconsDevBannerOnlyTouchesFrontLayer(t *testing.T) {
	root := t.TempDir()
	prod := filepath.Join(root, "prod.brandassets")
	dev := filepath.Join(root, "dev.brandassets")
	GenerateTVIcons(testHost(t), prod, IconOptions{BGColor: "#123456", GlyphText: "X", Dev: false})
	GenerateTVIcons(testHost(t), dev, IconOptions{BGColor: "#123456", GlyphText: "X", Dev: true})

	backProd, _ := os.ReadFile(tvLayerPNG(prod, "App Icon.imagestack", "Back", "Back.png"))
	backDev, _ := os.ReadFile(tvLayerPNG(dev, "App Icon.imagestack", "Back", "Back.png"))
	if string(backProd) != string(backDev) {
		t.Error("the DEV banner must not touch the opaque Back layer")
	}

	frontProd, _ := os.ReadFile(tvLayerPNG(prod, "App Icon.imagestack", "Front", "Front.png"))
	frontDev, _ := os.ReadFile(tvLayerPNG(dev, "App Icon.imagestack", "Front", "Front.png"))
	if string(frontProd) == string(frontDev) {
		t.Error("the DEV banner must be baked into the Front layer")
	}
}

// --- makeDevBannerRect -------------------------------------------------------

func TestMakeDevBannerRectMatchesRequestedRectangle(t *testing.T) {
	if b := makeDevBannerRect(400, 240).Bounds(); b.Dx() != 400 || b.Dy() != 240 {
		t.Errorf("bounds = %v, want 400x240", b)
	}
	if b := makeDevBannerRect(512, 512).Bounds(); b.Dx() != 512 || b.Dy() != 512 {
		t.Errorf("bounds = %v, want 512x512", b)
	}
	// The taller-than-wide case exercises the negative-x-offset clip path.
	if b := makeDevBannerRect(240, 400).Bounds(); b.Dx() != 240 || b.Dy() != 400 {
		t.Errorf("bounds = %v, want 240x400", b)
	}
}
