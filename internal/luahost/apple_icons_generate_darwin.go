//go:build darwin

package luahost

// Glyph-based fresh Apple icon generation. GenerateIcons and
// GenerateTVIcons composite a configured glyph_text over a background at
// every declared size, for both the flat mac/iOS appiconset ladder and the
// tvOS layered .brandassets stack.
//
// # The dependency decision (admin-3rgp.48)
//
// Two rendering capabilities were considered: drawing text with a font, and
// rasterizing an SVG glyph.
//
//   - Text: golang.org/x/image's font/opentype + font/gofont/goregular —
//     the Go team's own extended-stdlib module. Pure Go, no cgo, actively
//     maintained (tagged releases through 2026), and font/gofont embeds a
//     real TTF (Go Regular) as Go source, so there is no system-font lookup
//     to port and no font file to bundle separately. Clears the bar easily;
//     taken.
//   - SVG: github.com/fyne-io/oksvg + github.com/srwiley/rasterx were taken
//     briefly (admin-3rgp.38) and then dropped (admin-3rgp.48). oksvg pulls
//     in srwiley/rasterx, which has had no commits since 2022 — an
//     unmaintained dependency reached transitively rather than chosen
//     directly, for a narrow feature (compositing an SVG glyph onto an app
//     icon) that text glyphs already cover for this tool's own use. The
//     user's decision (admin-3rgp.48 NOTES): drop SVG glyph support
//     entirely rather than vendor around the unmaintained dependency. A
//     configured glyph_svg now fails loudly (see renderGlyph) instead of
//     silently rendering nothing.
//   - github.com/tdewolff/canvas — pure Go and does SVG rasterization, but
//     its go.mod pulls in Fyne, Gio, go-gl (OpenGL bindings), a MySQL
//     driver, a MongoDB driver, and dozens of other unrelated packages
//     having nothing to do with rendering a PNG. Rejected on dependency
//     footprint alone — a static binary that vendors an OpenGL binding to
//     draw a 1024x1024 icon is the opposite of what taking a dependency was
//     supposed to buy here.
//   - Hand-rolled SVG path parsing + golang.org/x/image/vector rasterizing —
//     zero third-party dependency, but reimplements a real parser (path
//     commands including arcs, transforms, viewBox, style attributes).
//     CLAUDE.md's "name existing candidates before a custom parser" rule
//     argues directly against this for a feature this narrow — and the
//     feature itself was dropped rather than rebuilt.
//
// # One shared rendering path
//
// The flat mac/iOS case and the tvOS layered case could each compute their
// own font-pixel-size formula and center a glyph raster differently. Instead
// both go through one renderGlyph: always render the glyph (SVG or text)
// into a shortSide*scale square sub-image, centered, and composite it onto
// the background — the same visual contract (a centered glyph occupying
// roughly `scale` of the icon) with one code path instead of two.
//
// Backed by the same alpha-flattening rule ADR-0007 documents: every PNG
// this writes for a mac/iOS icon (and every opaque tvOS layer) is flattened
// to zero-alpha RGB before saving.

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const defaultGlyphScale = 0.55

// hexToNRGBA parses a "#RRGGBB" (leading # optional) into an opaque NRGBA.
func hexToNRGBA(hex string) (color.NRGBA, error) {
	h := strings.TrimPrefix(hex, "#")
	if len(h) != 6 {
		return color.NRGBA{}, fmt.Errorf("invalid hex color %q (want #RRGGBB)", hex)
	}
	var rgb [3]uint64
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(h[i*2:i*2+2], 16, 8)
		if err != nil {
			return color.NRGBA{}, fmt.Errorf("invalid hex color %q: %w", hex, err)
		}
		rgb[i] = v
	}
	return color.NRGBA{R: uint8(rgb[0]), G: uint8(rgb[1]), B: uint8(rgb[2]), A: 255}, nil
}

var (
	goFontOnce sync.Once
	goFont     *opentype.Font
	goFontErr  error

	goBoldFontOnce sync.Once
	goBoldFont     *opentype.Font
	goBoldFontErr  error
)

// parsedGoFont parses the embedded Go Regular TTF once per process; a Face
// (which bakes in a rasterization size) is created fresh per call since
// sizes vary per icon.
func parsedGoFont() (*opentype.Font, error) {
	goFontOnce.Do(func() {
		goFont, goFontErr = opentype.Parse(goregular.TTF)
	})
	return goFont, goFontErr
}

// parsedGoBoldFont parses the embedded Go Bold TTF once per process, the same
// way parsedGoFont does for Go Regular. Used only where a heavier stroke
// weight is needed (the dev banner's "DEV" label) — every configured glyph
// still renders in Go Regular via drawCenteredText/parsedGoFont, unchanged.
func parsedGoBoldFont() (*opentype.Font, error) {
	goBoldFontOnce.Do(func() {
		goBoldFont, goBoldFontErr = opentype.Parse(gobold.TTF)
	})
	return goBoldFont, goBoldFontErr
}

// drawCenteredText renders text in fg onto dst (w×h), centered on the text's
// own bounding box at the given pixel size, using the Go Regular face.
func drawCenteredText(dst draw.Image, w, h int, text string, fg color.Color, pixelSize int) error {
	f, err := parsedGoFont()
	if err != nil {
		return err
	}
	return drawCenteredTextWithFont(dst, w, h, text, fg, pixelSize, f)
}

// drawCenteredTextBold is drawCenteredText's bold-face counterpart, used only
// by the dev banner (RenderDevBanner in apple_icons_darwin.go) so its "DEV"
// label reads with a bold system-font weight, without changing
// renderGlyph's regular-weight glyph text.
func drawCenteredTextBold(dst draw.Image, w, h int, text string, fg color.Color, pixelSize int) error {
	f, err := parsedGoBoldFont()
	if err != nil {
		return err
	}
	return drawCenteredTextWithFont(dst, w, h, text, fg, pixelSize, f)
}

// drawCenteredTextWithFont is the shared implementation behind
// drawCenteredText and drawCenteredTextBold: render text in fg onto dst
// (w×h), centered on the text's own bounding box at the given pixel size,
// using whichever parsed font face is passed in.
func drawCenteredTextWithFont(dst draw.Image, w, h int, text string, fg color.Color, pixelSize int, f *opentype.Font) error {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    float64(pixelSize),
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return err
	}
	defer face.Close()

	d := &font.Drawer{Dst: dst, Src: image.NewUniform(fg), Face: face}
	bounds, _ := d.BoundString(text)
	bw := bounds.Max.X - bounds.Min.X
	bh := bounds.Max.Y - bounds.Min.Y
	d.Dot = fixed.Point26_6{
		X: (fixed.I(w)-bw)/2 - bounds.Min.X,
		Y: (fixed.I(h)-bh)/2 - bounds.Min.Y,
	}
	d.DrawString(text)
	return nil
}

// errSVGGlyphDropped is returned whenever a caller configures glyph_svg —
// SVG glyph rendering was dropped in admin-3rgp.48 (github.com/fyne-io/oksvg
// and its unmaintained-since-2022 github.com/srwiley/rasterx dependency were
// removed entirely), and the user's decision there was explicit: a
// configured glyph_svg must fail loudly naming the dropped capability, never
// silently render nothing or fall back to a blank icon.
var errSVGGlyphDropped = fmt.Errorf("glyph_svg is no longer supported: SVG glyph rendering was dropped (admin-3rgp.48) because it depended on an unmaintained package; use glyph_text instead")

// renderGlyph paints bg (which may itself carry alpha — a.0 renders a
// transparent w×h canvas, used for tvOS's transparent layers) over a w×h
// canvas, then centers a glyph on top: glyphText renders as text, or a
// plain bg-colored square when both glyph inputs are empty. glyphSVG is no
// longer a supported input — it always fails with errSVGGlyphDropped rather
// than silently rendering nothing (admin-3rgp.48). Returns an error when a
// requested glyph fails to render (glyph_svg configured at all, or an
// unparseable font size) — callers treat that as "generation failed for
// this size" and warn, not a hard crash.
func renderGlyph(w, h int, bg color.NRGBA, glyphSVG, glyphText string, fg color.NRGBA, scale float64) (*image.NRGBA, error) {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)

	if glyphSVG != "" {
		return nil, errSVGGlyphDropped
	}

	if glyphText == "" {
		return img, nil
	}

	shortSide := w
	if h < shortSide {
		shortSide = h
	}
	glyphSize := int(float64(shortSide) * scale)
	if glyphSize < 1 {
		glyphSize = 1
	}

	glyph := image.NewNRGBA(image.Rect(0, 0, glyphSize, glyphSize))
	fontSize := int(float64(glyphSize) * 0.9)
	if fontSize < 1 {
		fontSize = 1
	}
	if err := drawCenteredText(glyph, glyphSize, glyphSize, glyphText, fg, fontSize); err != nil {
		return nil, err
	}

	offX, offY := (w-glyphSize)/2, (h-glyphSize)/2
	draw.Draw(img, image.Rect(offX, offY, offX+glyphSize, offY+glyphSize), glyph, image.Point{}, draw.Over)
	return img, nil
}

// flattenOpaqueRGB composites img over bg and forces every pixel's alpha to
// 255 — ADR-0007's non-optional rule: any PNG with an alpha channel (even at
// full-opacity in every pixel but one) risks macOS rendering the icon inset
// instead of full-bleed, so every icon path flattens before saving.
func flattenOpaqueRGB(img *image.NRGBA, bg color.NRGBA) *image.NRGBA {
	flat := image.NewNRGBA(img.Bounds())
	draw.Draw(flat, flat.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), img, img.Bounds().Min, draw.Over)
	for i := 3; i < len(flat.Pix); i += 4 {
		flat.Pix[i] = 255
	}
	return flat
}

func savePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// IconOptions is the shared {bg_color=, glyph_svg=, glyph_text=,
// glyph_color=, glyph_scale=, dev=} shape apple.generate_icons and
// apple.generate_tv_icons both take as their trailing options table.
// GlyphScale <= 0 means "use the 0.55 default" — 0 is not a meaningful
// scale, so it doubles as the Lua-side "unset" value with no separate
// optionality flag needed.
type IconOptions struct {
	BGColor     string
	GlyphSVG    string
	GlyphText   string
	GlyphColor  string
	GlyphScale  float64
	Dev         bool
	FilenameFmt string // generate_icons only
}

func (o IconOptions) resolved() (bg, fg color.NRGBA, scale float64, err error) {
	bgHex := o.BGColor
	if bgHex == "" {
		bgHex = "#1E1E2E"
	}
	fgHex := o.GlyphColor
	if fgHex == "" {
		fgHex = "#FFFFFF"
	}
	bg, err = hexToNRGBA(bgHex)
	if err != nil {
		return
	}
	fg, err = hexToNRGBA(fgHex)
	if err != nil {
		return
	}
	scale = o.GlyphScale
	if scale <= 0 {
		scale = defaultGlyphScale
	}
	return
}

// GenerateIcons renders a full ladder of flat mac/iOS app-icon PNGs, one per
// size, into outputDir. Returns false (after warning via h) if any size
// fails to render — generation is all-or-nothing per call: warn and let the
// build continue without custom icons, rather than leave a partial ladder.
func GenerateIcons(h *Host, sizes []int, outputDir string, opts IconOptions) bool {
	bg, fg, scale, err := opts.resolved()
	if err != nil {
		h.Err(fmt.Sprintf("apple.generate_icons: %v", err))
		return false
	}
	filenameFormat := opts.FilenameFmt
	if filenameFormat == "" {
		filenameFormat = "{size}.png"
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		h.Err(fmt.Sprintf("apple.generate_icons: %v", err))
		return false
	}

	for _, size := range sizes {
		img, err := renderGlyph(size, size, bg, opts.GlyphSVG, opts.GlyphText, fg, scale)
		if err != nil {
			h.Err(fmt.Sprintf("Icon generation failed: %v", err))
			h.Err("Build will continue without custom icons.")
			return false
		}
		if opts.Dev {
			banner := RenderDevBanner(size)
			draw.Draw(img, img.Bounds(), banner, image.Point{}, draw.Over)
		}
		flat := flattenOpaqueRGB(img, bg)

		fname := strings.ReplaceAll(filenameFormat, "{size}", strconv.Itoa(size))
		if err := savePNG(filepath.Join(outputDir, fname), flat); err != nil {
			h.Err(fmt.Sprintf("Icon generation failed: %v", err))
			h.Err("Build will continue without custom icons.")
			return false
		}
	}
	return true
}

// --- tvOS layered icon stack (see apple_icons_darwin.go's file doc comment
// for the schema this describes in full) --------------------------------

type tvScale struct {
	Label string
	W, H  int
}

var tvIconStacks = []struct {
	Name   string
	Scales []tvScale
}{
	{"App Icon.imagestack", []tvScale{{"1x", 400, 240}, {"2x", 800, 480}}},
	{"App Icon - App Store.imagestack", []tvScale{{"1x", 1280, 768}}},
}

var tvTopShelfSets = []struct {
	Name   string
	Scales []tvScale
}{
	{"Top Shelf Image.imageset", []tvScale{{"1x", 1920, 720}, {"2x", 3840, 1440}}},
	{"Top Shelf Image Wide.imageset", []tvScale{{"1x", 2320, 720}, {"2x", 4640, 1440}}},
}

var tvLayerNames = []string{"Front", "Middle", "Back"}

func tvBrandassetsContents() map[string]any {
	return map[string]any{
		"assets": []any{
			map[string]any{"size": "1280x768", "idiom": "tv", "filename": "App Icon - App Store.imagestack", "role": "primary-app-icon"},
			map[string]any{"size": "400x240", "idiom": "tv", "filename": "App Icon.imagestack", "role": "primary-app-icon"},
			map[string]any{"size": "1920x720", "idiom": "tv", "filename": "Top Shelf Image.imageset", "role": "top-shelf-image"},
			map[string]any{"size": "2320x720", "idiom": "tv", "filename": "Top Shelf Image Wide.imageset", "role": "top-shelf-image-wide"},
		},
		"info": map[string]any{"version": 1, "author": "xcode"},
	}
}

// makeDevBannerRect is the rectangular analog of RenderDevBanner for tvOS's
// non-square layers: renders the square banner at the larger of the two
// dimensions, then crops to the requested rectangle from the bottom-right —
// where the stripe lives — instead of squashing it to fit.
func makeDevBannerRect(w, h int) *image.NRGBA {
	size := w
	if h > size {
		size = h
	}
	banner := RenderDevBanner(size)
	if w == size && h == size {
		return banner
	}
	dstX0, srcX0 := w-size, 0
	if dstX0 < 0 {
		srcX0 = -dstX0
		dstX0 = 0
	}
	dstY0, srcY0 := h-size, 0
	if dstY0 < 0 {
		srcY0 = -dstY0
		dstY0 = 0
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(canvas, image.Rect(dstX0, dstY0, w, h), banner, image.Point{X: srcX0, Y: srcY0}, draw.Over)
	return canvas
}

// GenerateTVIcons generates a complete tvOS .brandassets catalog from the
// same bg_color/glyph_* inputs GenerateIcons takes for the flat mac/iOS
// icons. Always overwrites whatever is at brandassetsDir. Returns false
// (after warning via h) if generation fails partway; a partially-written
// brandassets tree on disk after a false return is possible — this does not
// roll back a partial write on failure.
func GenerateTVIcons(h *Host, brandassetsDir string, opts IconOptions) bool {
	bg, fg, scale, err := opts.resolved()
	if err != nil {
		h.Err(fmt.Sprintf("apple.generate_tv_icons: %v", err))
		return false
	}
	transparent := color.NRGBA{}

	fail := func(err error) bool {
		h.Err(fmt.Sprintf("tvOS icon generation failed: %v", err))
		h.Err("Build will continue without custom icons.")
		return false
	}

	for _, stack := range tvIconStacks {
		stackDir := filepath.Join(brandassetsDir, stack.Name)
		for _, layerName := range tvLayerNames {
			layerDir := filepath.Join(stackDir, layerName+".imagestacklayer", "Content.imageset")
			var images []any
			for _, sc := range stack.Scales {
				var canvas *image.NRGBA
				switch layerName {
				case "Back":
					// Bottom layer: opaque bg plate, no glyph — tvOS errors
					// if this layer carries any transparency.
					canvas = flattenOpaqueRGB(image.NewNRGBA(image.Rect(0, 0, sc.W, sc.H)), bg)
				case "Middle":
					// Transparent spacer — gives the parallax effect room
					// between the opaque back and the glyph front.
					canvas = image.NewNRGBA(image.Rect(0, 0, sc.W, sc.H))
				default: // Front — the glyph, floating on transparency
					img, err := renderGlyph(sc.W, sc.H, transparent, opts.GlyphSVG, opts.GlyphText, fg, scale)
					if err != nil {
						return fail(err)
					}
					if opts.Dev {
						draw.Draw(img, img.Bounds(), makeDevBannerRect(sc.W, sc.H), image.Point{}, draw.Over)
					}
					canvas = img
				}
				fname := layerName + scaleSuffix(sc.Label) + ".png"
				if err := savePNG(filepath.Join(layerDir, fname), canvas); err != nil {
					return fail(err)
				}
				images = append(images, map[string]any{"idiom": "tv", "scale": sc.Label, "filename": fname})
			}
			if err := writeJSONDoc(filepath.Join(layerDir, "Contents.json"),
				map[string]any{"images": images, "info": map[string]any{"version": 1, "author": "xcode"}}); err != nil {
				return fail(err)
			}
			if err := writeJSONDoc(filepath.Join(stackDir, layerName+".imagestacklayer", "Contents.json"),
				map[string]any{"info": map[string]any{"version": 1, "author": "xcode"}}); err != nil {
				return fail(err)
			}
		}
		layers := make([]any, len(tvLayerNames))
		for i, n := range tvLayerNames {
			layers[i] = map[string]any{"filename": n + ".imagestacklayer"}
		}
		if err := writeJSONDoc(filepath.Join(stackDir, "Contents.json"),
			map[string]any{"layers": layers, "info": map[string]any{"version": 1, "author": "xcode"}}); err != nil {
			return fail(err)
		}
	}

	for _, set := range tvTopShelfSets {
		imagesetDir := filepath.Join(brandassetsDir, set.Name)
		var images []any
		for _, sc := range set.Scales {
			img, err := renderGlyph(sc.W, sc.H, bg, opts.GlyphSVG, opts.GlyphText, fg, scale)
			if err != nil {
				return fail(err)
			}
			if opts.Dev {
				draw.Draw(img, img.Bounds(), makeDevBannerRect(sc.W, sc.H), image.Point{}, draw.Over)
			}
			flat := flattenOpaqueRGB(img, bg)
			for _, idiom := range []string{"tv", "tv-marketing"} {
				fname := idiom + scaleSuffix(sc.Label) + ".png"
				if err := savePNG(filepath.Join(imagesetDir, fname), flat); err != nil {
					return fail(err)
				}
				images = append(images, map[string]any{"idiom": idiom, "scale": sc.Label, "filename": fname})
			}
		}
		if err := writeJSONDoc(filepath.Join(imagesetDir, "Contents.json"),
			map[string]any{"images": images, "info": map[string]any{"version": 1, "author": "xcode"}}); err != nil {
			return fail(err)
		}
	}

	if err := os.MkdirAll(brandassetsDir, 0o755); err != nil {
		return fail(err)
	}
	if err := writeJSONDoc(filepath.Join(brandassetsDir, "Contents.json"), tvBrandassetsContents()); err != nil {
		return fail(err)
	}
	return true
}

func scaleSuffix(label string) string {
	if label == "1x" {
		return ""
	}
	return "@" + label
}
