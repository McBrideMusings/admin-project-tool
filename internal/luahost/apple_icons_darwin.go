//go:build darwin

package luahost

// Ports the "icon-composer / asset-swap subsystem" admin-3rgp.38's NOTES
// named as remaining — admin_lib/apple.py's backup_icons / swap_dev_icons /
// restore_icons / backup_tv_icons / restore_tv_icons / swap_dev_icon_composer
// / restore_icon_composer, plus the piece of admin_lib/icons.py they call
// into for rendering (make_dev_banner / _apply_dev_banner).
//
// # The imaging decision this file makes
//
// admin_lib/icons.py's rendering is Pillow (+ optionally cairosvg) end to
// end: solid/text/SVG glyph backgrounds, a rotated "DEV" ribbon with a
// system font, bicubic resampling. ADR-0001's one stdlib-only carve-out was
// exactly this: try/except-guard Pillow/cairosvg and degrade to "skip icon
// generation, warn" when they're absent. Go has no equivalent optional-
// import — an unresolved package is a build failure, not a runtime nil — so
// the choice is binary: vendor a real imaging + font stack as a hard Go
// dependency (the epic's amendment already retires ADR-0001's dependency
// ban, so this is allowed), or find a smaller capability that needs none.
//
// This file takes the second path for the banner overlay specifically:
//
//   - The dev banner is rendered with the standard library alone
//     (image/draw/color/png) as a solid diagonal ribbon — no text, no font
//     dependency, no SVG glyph support, no bicubic resample. This covers
//     swap_dev_icons' has_glyph=False branch (overlay the shipped icon art
//     with a banner) and swap_dev_icon_composer's banner asset — the two
//     "mark this build as dev" paths that touch every Apple archetype's
//     default config. A viewer sees an unlabeled red diagonal stripe rather
//     than one reading "DEV"; the stripe alone already answers "is this the
//     dev build" for the workflow's purpose (a glance at the Home Screen).
//   - swap_dev_icons' has_glyph=True branch — generate_icons() compositing a
//     configured glyph onto a fresh background at every declared size —
//     needed font rendering as a real dependency. golang.org/x/image's font
//     stack was taken for that; generate_icons/generate_tv_icons are bound
//     in full — see apple_icons_generate_darwin.go's file doc comment for
//     the dependency comparison and decision (including admin-3rgp.48's
//     later removal of SVG glyph support and its oksvg/rasterx dependency).
//     This file's own dev-banner rendering deliberately stays stdlib-only
//     even so: it needs no font capability at all (a plain ribbon, no
//     text), so taking on that dependency here would buy nothing.
//
// Backup/restore (backup_icons, restore_icons, backup_tv_icons,
// restore_tv_icons) are plain file-copy operations with no imaging
// involved at all, and are bound in full below.

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// --- appiconset backup / swap / restore -------------------------------

// BackupIcons copies every *.png in each iconDirs[label] into
// backupDir/label, creating the destination if needed. A missing source
// directory is skipped, not an error — mirrors the Python original, which
// tolerates an archetype declaring a platform's icon dir that doesn't exist
// in a given checkout.
func BackupIcons(iconDirs map[string]string, backupDir string) error {
	for label, srcDir := range iconDirs {
		dest := filepath.Join(backupDir, label)
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		if !isDir(srcDir) {
			continue
		}
		if err := copyPNGs(srcDir, dest); err != nil {
			return err
		}
	}
	return nil
}

// RestoreIcons copies every *.png from backupDir/label back into
// iconDirs[label], then removes backupDir entirely.
func RestoreIcons(iconDirs map[string]string, backupDir string) error {
	for label, destDir := range iconDirs {
		src := filepath.Join(backupDir, label)
		if !isDir(src) {
			continue
		}
		if err := copyPNGs(src, destDir); err != nil {
			return err
		}
	}
	if isDir(backupDir) {
		return os.RemoveAll(backupDir)
	}
	return nil
}

func copyPNGs(srcDir, destDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		if err := copyFile(filepath.Join(srcDir, e.Name()), filepath.Join(destDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// --- tvOS .brandassets backup / restore --------------------------------

const tvBackupLabel = "tv-brandassets"

// BackupTVIcons copies brandassetsDir's whole tree into
// backupDir/tv-brandassets, replacing any prior backup there.
func BackupTVIcons(brandassetsDir, backupDir string) error {
	dest := filepath.Join(backupDir, tvBackupLabel)
	if isDir(dest) {
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
	}
	if !isDir(brandassetsDir) {
		return nil
	}
	return copyTree(brandassetsDir, dest)
}

// RestoreTVIcons replaces brandassetsDir with the tree backed up under
// backupDir/tv-brandassets, then removes backupDir.
func RestoreTVIcons(brandassetsDir, backupDir string) error {
	src := filepath.Join(backupDir, tvBackupLabel)
	if !isDir(src) {
		return nil
	}
	if isDir(brandassetsDir) {
		if err := os.RemoveAll(brandassetsDir); err != nil {
			return err
		}
	}
	if err := copyTree(src, brandassetsDir); err != nil {
		return err
	}
	if isDir(backupDir) {
		return os.RemoveAll(backupDir)
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// --- Dev banner rendering (stdlib-only; see file doc comment) -----------

// devBannerColor is the Python original's fill(220, 40, 40, 240) — an opaque
// red stripe.
var devBannerColor = color.NRGBA{R: 220, G: 40, B: 40, A: 240}

// RenderDevBanner returns a transparent size×size RGBA image carrying a
// diagonal red stripe through the bottom-right corner, at roughly the same
// geometry as admin_lib/icons.py's make_dev_banner (a 0.22×size-tall band
// rotated 35°) — minus the "DEV" text (see file doc comment for why).
//
// Implemented by inverse rotation: for each output pixel, rotate its
// position by -35° around the image center and test whether that point
// falls inside the un-rotated horizontal band. This needs no affine-
// transform or resampling library because nothing is ever forward-rotated —
// only the band membership test is evaluated in rotated space.
func RenderDevBanner(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	bandHeight := float64(size) * 0.22
	cx, cy := float64(size)/2, float64(size)/2
	const angle = 35 * math.Pi / 180
	sin, cos := math.Sin(angle), math.Cos(angle)

	// The band, before rotation, is centered vertically on the canvas and
	// spans the full width — matching make_dev_banner's oversized-canvas
	// rectangle before crop. Shifting the center down-and-right by 0.22*size
	// (Python's crop offset, expressed directly instead of via an oversized
	// canvas + crop) lands the stripe in the bottom-right corner.
	shift := float64(size) * 0.22
	bcx, bcy := cx+shift, cy+shift

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-bcx, float64(y)-bcy
			// Rotate (dx, dy) by -angle to test in the band's own frame.
			ry := -dx*sin + dy*cos
			if ry >= -bandHeight/2 && ry <= bandHeight/2 {
				img.SetNRGBA(x, y, devBannerColor)
			}
		}
	}
	return img
}

// SaveDevBannerPNG renders a size×size dev banner and writes it to outPath
// as a PNG, creating parent directories as needed.
func SaveDevBannerPNG(size int, outPath string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, RenderDevBanner(size))
}

// ApplyDevBannerToPNGFile overlays a dev banner (sized to the image's own
// width) onto the PNG at path and re-saves it as opaque RGB — mirrors
// _apply_dev_banner + the "drop the alpha channel" step swap_dev_icons'
// no-glyph branch performs so macOS renders the icon full-bleed instead of
// insetting it into a padded tile.
func ApplyDevBannerToPNGFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}

	bounds := src.Bounds()
	banner := RenderDevBanner(bounds.Dx())

	// Composite banner over a copy of src, then flatten onto opaque white
	// (matching Python's Image.convert("RGB"), which drops any residual
	// alpha by compositing over black in Pillow's default — the icons this
	// runs against are always fully opaque already, so the flatten backing
	// color never actually shows through in practice).
	out := image.NewNRGBA(bounds)
	draw.Draw(out, bounds, src, bounds.Min, draw.Src)
	draw.Draw(out, banner.Bounds().Add(bounds.Min), banner, image.Point{}, draw.Over)

	rgb := image.NewNRGBA(bounds)
	draw.Draw(rgb, bounds, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(rgb, bounds, out, bounds.Min, draw.Over)
	for i := 3; i < len(rgb.Pix); i += 4 {
		rgb.Pix[i] = 255
	}

	out2, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out2.Close()
	return png.Encode(out2, rgb)
}

// SwapDevIconsBanner overlays the dev banner onto every *.png in each
// iconDirs[label], in place. Binds swap_dev_icons' has_glyph=False branch
// only (see file doc comment); a caller wanting glyph-based fresh generation
// gets false back so it can warn and skip, exactly as the Python original
// does when Pillow is missing.
func SwapDevIconsBanner(iconDirs map[string]string) (bool, error) {
	ok := true
	for _, srcDir := range iconDirs {
		if !isDir(srcDir) {
			continue
		}
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			return false, err
		}
		for _, e := range entries {
			name := strings.ToLower(e.Name())
			if e.IsDir() || !strings.HasSuffix(name, ".png") {
				continue
			}
			if err := ApplyDevBannerToPNGFile(filepath.Join(srcDir, e.Name())); err != nil {
				ok = false
			}
		}
	}
	return ok, nil
}

// ApplyTVDevBanner overlays the dev banner onto an EXISTING tvOS
// .brandassets tree in place, without regenerating anything — the overlay
// counterpart to GenerateTVIcons for a project that ships its own tvOS art
// rather than a glyph config. Mirrors admin_lib/icons.py's
// apply_tv_dev_banner: only the topmost Front layer of each imagestack
// (tvIconStacks, from apple_icons_generate_darwin.go) and the Top Shelf
// images get the banner, matching Apple's own guidance to keep markup on the
// uppermost layer — Back and Middle are never touched, so ADR-0007's
// "Back must be fully opaque" rule is never at stake here.
//
// Unlike ApplyDevBannerToPNGFile (the flat appiconset overlay), this does
// NOT flatten to opaque RGB: it preserves whatever alpha channel the source
// PNG already carries, matching the Python original's
// Image.open(...).convert("RGBA") + paste round-trip — the Front layer is
// meant to float transparently over Middle/Back, so flattening it here
// would reintroduce exactly the inset-icon bug ADR-0007 exists to avoid on
// the *other* layers.
func ApplyTVDevBanner(h *Host, brandassetsDir string) bool {
	for _, stack := range tvIconStacks {
		frontDir := filepath.Join(brandassetsDir, stack.Name, "Front.imagestacklayer", "Content.imageset")
		if err := overlayDevBannerDir(frontDir); err != nil {
			h.Err(fmt.Sprintf("apple.apply_tv_dev_banner: %v", err))
			return false
		}
	}
	for _, set := range tvTopShelfSets {
		if err := overlayDevBannerDir(filepath.Join(brandassetsDir, set.Name)); err != nil {
			h.Err(fmt.Sprintf("apple.apply_tv_dev_banner: %v", err))
			return false
		}
	}
	return true
}

// overlayDevBannerDir overlays a rectangular dev banner (sized to each
// image's own bounds) onto every *.png directly inside dir, in place. A
// missing directory is a no-op, not an error — mirrors
// admin_lib/icons.py's _overlay_dev_banner_dir. A file that fails to decode
// as a PNG is skipped, also matching the Python original's per-file
// try/except; any other failure (a banner that fails to encode/write)
// aborts and is returned to the caller, matching apply_tv_dev_banner's
// outer try/except aborting the whole operation on a non-decode error.
func overlayDevBannerDir(dir string) error {
	if !isDir(dir) {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if e.IsDir() || !strings.HasSuffix(name, ".png") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		src, err := png.Decode(f)
		f.Close()
		if err != nil {
			continue
		}

		bounds := src.Bounds()
		banner := makeDevBannerRect(bounds.Dx(), bounds.Dy())
		out := image.NewNRGBA(bounds)
		draw.Draw(out, bounds, src, bounds.Min, draw.Src)
		draw.Draw(out, banner.Bounds().Add(bounds.Min), banner, image.Point{}, draw.Over)

		outFile, err := os.Create(path)
		if err != nil {
			return err
		}
		err = png.Encode(outFile, out)
		outFile.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// --- Icon Composer (.icon) banner inject / strip -----------------------

const (
	devBannerAsset = "dev-banner.png"
	devBannerLayer = "dev-banner"
)

func iconComposerJSONPath(iconPath string) string  { return filepath.Join(iconPath, "icon.json") }
func iconComposerAssetsDir(iconPath string) string { return filepath.Join(iconPath, "Assets") }

// isBannerGroup reports whether a parsed icon.json group is the injected
// DEV-banner group.
func isBannerGroup(group map[string]any) bool {
	layers, _ := group["layers"].([]any)
	for _, l := range layers {
		layer, ok := l.(map[string]any)
		if ok && layer["name"] == devBannerLayer {
			return true
		}
	}
	return false
}

// IconComposerInjectBanner appends the opaque DEV-banner group to a parsed
// icon.json document. Idempotent (a stale banner group from a re-swap is
// dropped first) and a pure transform — no filesystem access — mirroring
// admin_lib/apple.py's _inject_banner_layer exactly, including its return
// value (the same doc, mutated).
func IconComposerInjectBanner(doc map[string]any) map[string]any {
	groups := stripBannerGroups(doc)
	groups = append(groups, map[string]any{
		"layers": []any{
			map[string]any{
				"glass":      false,
				"image-name": devBannerAsset,
				"name":       devBannerLayer,
				"position": map[string]any{
					"scale":                 1,
					"translation-in-points": []any{0, 0},
				},
			},
		},
	})
	doc["groups"] = groups
	return doc
}

// IconComposerStripBanner removes the injected DEV-banner group from a
// parsed icon.json document. Pure transform, mirrors _strip_banner_layer.
func IconComposerStripBanner(doc map[string]any) map[string]any {
	doc["groups"] = stripBannerGroups(doc)
	return doc
}

func stripBannerGroups(doc map[string]any) []any {
	groupsAny, _ := doc["groups"].([]any)
	out := make([]any, 0, len(groupsAny))
	for _, g := range groupsAny {
		group, ok := g.(map[string]any)
		if ok && isBannerGroup(group) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// SwapDevIconComposer injects an opaque DEV banner into the Icon Composer
// bundle at iconPath, backing up the pristine icon.json to backupDir first
// and rendering the banner asset sized to the base art (Assets/icon-raw.png)
// when present, else 1024. Returns false (with no error) when icon.json is
// missing, matching the Python original's "skip, don't fail" behaviour for
// a bundle that isn't actually Icon-Composer-shaped.
func SwapDevIconComposer(iconPath, backupDir string) (bool, error) {
	jsonPath := iconComposerJSONPath(iconPath)
	if !isFile(jsonPath) {
		return false, nil
	}

	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return false, err
	}
	if err := copyFile(jsonPath, filepath.Join(backupDir, "icon.json")); err != nil {
		return false, err
	}

	assetsDir := iconComposerAssetsDir(iconPath)
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		return false, err
	}
	baseSize := 1024
	raw := filepath.Join(assetsDir, "icon-raw.png")
	if isFile(raw) {
		if f, err := os.Open(raw); err == nil {
			if cfg, err := png.DecodeConfig(f); err == nil {
				if cfg.Width > cfg.Height {
					baseSize = cfg.Width
				} else {
					baseSize = cfg.Height
				}
			}
			f.Close()
		}
	}

	if err := SaveDevBannerPNG(baseSize, filepath.Join(assetsDir, devBannerAsset)); err != nil {
		return false, err
	}

	doc, err := readJSONDoc(jsonPath)
	if err != nil {
		return false, err
	}
	IconComposerInjectBanner(doc)
	if err := writeJSONDoc(jsonPath, doc); err != nil {
		return false, err
	}
	return true, nil
}

// RestoreIconComposer restores iconPath's icon.json to its pristine state:
// prefers the byte-exact backup in backupDir (keeps the user's own
// formatting), else strips the banner group from whatever's currently on
// disk. Removes the generated banner asset and backupDir either way.
func RestoreIconComposer(iconPath, backupDir string) error {
	jsonPath := iconComposerJSONPath(iconPath)
	backupJSON := filepath.Join(backupDir, "icon.json")

	if isFile(backupJSON) {
		if err := copyFile(backupJSON, jsonPath); err != nil {
			return err
		}
	} else if isFile(jsonPath) {
		doc, err := readJSONDoc(jsonPath)
		if err != nil {
			return err
		}
		IconComposerStripBanner(doc)
		if err := writeJSONDoc(jsonPath, doc); err != nil {
			return err
		}
	}

	banner := filepath.Join(iconComposerAssetsDir(iconPath), devBannerAsset)
	if isFile(banner) {
		if err := os.Remove(banner); err != nil {
			return err
		}
	}
	if isDir(backupDir) {
		return os.RemoveAll(backupDir)
	}
	return nil
}

func readJSONDoc(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}

func writeJSONDoc(path string, doc map[string]any) error {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
