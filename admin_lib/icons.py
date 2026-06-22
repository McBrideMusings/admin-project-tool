"""General-purpose Apple (macOS + iOS) app-icon generation with an optional
DEV banner overlay. This is the ONE shared icon generator — every project's
icons go through here (via ``[apple.icons]`` + the apple archetype's ``icons``
command, or the dev-loop banner swap). Do NOT write per-project icon scripts.

═══════════════════════════════════════════════════════════════════════════════
 THE ALPHA-CHANNEL GOTCHA (read this before touching anything) — ADR-0007
═══════════════════════════════════════════════════════════════════════════════

macOS renders an app icon in the Dock based on whether the PNG is fully opaque:

  • FULLY OPAQUE (no alpha channel, i.e. RGB — or RGBA with every pixel α=255)
    → macOS renders it FULL-BLEED: the artwork fills the whole Dock tile,
      edge-to-edge, like every first-party app.

  • CONTAINS ANY TRANSPARENCY (an alpha channel with *any* pixel α<255 —
    a soft shadow, an anti-aliased edge, a deliberate transparent margin)
    → macOS treats it as a "legacy / bordered" icon and INSETS it into a
      smaller padded tile. It looks shrunken and wrong next to other apps.

This bites constantly because it's invisible in a PNG viewer — two icons that
look identical render completely differently in the Dock. The ONLY reliable
diagnostic is the pixel data:

    from PIL import Image
    im = Image.open("icon_1024.png")
    im.mode                                  # 'RGB'  → no alpha → full-bleed
                                             # 'RGBA' → has alpha → suspect
    im.convert("RGBA").split()[-1].getextrema()[0]   # min alpha; <255 → inset

A DEV banner overlay is the classic trap: its rotated, anti-aliased stripe (and
a sub-255 fill) drops the min alpha below 255, so a bannered icon that *looks*
fine renders inset. ``generate_icons`` therefore ALWAYS flattens its output to
opaque RGB (drops the alpha channel) — that is the fix, and it is non-optional.
If you add any new icon path, flatten it to RGB too.

iOS vs macOS:
  • iOS masks icons into the rounded-rect shape itself and forbids alpha — icons
    are full-bleed opaque squares. The flatten-to-RGB rule is correct there too.
  • macOS does NOT mask the artwork into a shape; the PNG is shown as-is. So a
    full-bleed Dock look REQUIRES a fully-opaque (no-alpha) square. The classic
    macOS "icon grid" with a ~10% transparent margin (≈824px art in a 1024px
    canvas) is the *alternative*, non-full-bleed convention — but that margin is
    alpha<255, so it triggers the inset treatment and is incompatible with
    full-bleed. Pick one; you cannot have a transparent margin AND full-bleed.

(For the *name* of a dev-variant app in the Dock — a separate gotcha about the
.app filename vs CFBundleName — see ``dev_macos_postprocess`` in apple.py and
ADR-0007.)

Dependencies (graceful degradation):
  - Pillow: required for all icon generation
  - cairosvg: required for SVG glyph mode (falls back to text without it)

Install: pip3 install Pillow cairosvg
"""

import os
import sys
import tempfile
import traceback

try:
    from .core import warn, err
except ImportError:
    def warn(msg): print(msg, file=sys.stderr)
    def err(msg): print(msg, file=sys.stderr)

_HAS_PIL = False
_HAS_CAIROSVG = False

try:
    from PIL import Image, ImageDraw, ImageFont
    _HAS_PIL = True
except ImportError:
    pass

try:
    import cairosvg
    _HAS_CAIROSVG = True
except ImportError:
    pass


def _hex_to_rgba(h):
    """Convert hex color string to RGBA tuple."""
    h = h.lstrip("#")
    r, g, b = int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16)
    return (r, g, b, 255)


_FONT_PATHS = [
    "/System/Library/Fonts/SFCompact.ttf",
    "/System/Library/Fonts/Helvetica.ttc",
    "/System/Library/Fonts/HelveticaNeue.ttc",
    "/Library/Fonts/Arial.ttf",
]


def _find_font(size):
    """Find a usable TrueType font at the given size. Returns ImageFont or None."""
    for path in _FONT_PATHS:
        try:
            return ImageFont.truetype(path, size)
        except (OSError, IOError):
            continue
    return None


_GLYPH_SCALE = 0.55


def _render_solid(size, bg_color):
    """Render a solid color square."""
    return Image.new("RGBA", (size, size), bg_color)


def _render_text(size, bg_color, text, glyph_color, scale):
    """Render a text character centered on a colored background."""
    img = Image.new("RGBA", (size, size), bg_color)
    draw = ImageDraw.Draw(img)

    font_size = int(size * scale)
    font = _find_font(font_size)
    if font is None:
        font = ImageFont.load_default()

    draw.text(
        (size // 2, size // 2), text,
        fill=glyph_color, font=font, anchor="mm",
    )
    return img


def _render_svg(size, bg_color, svg_path, scale):
    """Render an SVG glyph centered on a colored background.

    SVGs use their own fill colors; glyph_color is not applied here.
    """
    img = Image.new("RGBA", (size, size), bg_color)

    glyph_size = max(int(size * scale), 1)
    with tempfile.NamedTemporaryFile(suffix=".png", delete=False) as tmp:
        tmp_path = tmp.name

    try:
        cairosvg.svg2png(
            url=svg_path, write_to=tmp_path,
            output_width=glyph_size, output_height=glyph_size,
        )
        glyph = Image.open(tmp_path).convert("RGBA")
    finally:
        os.unlink(tmp_path)

    offset = (size - glyph_size) // 2
    img.paste(glyph, (offset, offset), glyph)
    return img


def make_dev_banner(size):
    """Return a transparent ``size``×``size`` RGBA overlay carrying a red
    diagonal 'DEV' stripe in the bottom-right corner.

    Composite it over any icon art to mark a development build. All
    dimensions are proportional to ``size`` so the banner looks identical at
    any resolution. Requires Pillow (callers gate on ``_HAS_PIL``).
    """
    # Oversized canvas so the stripe extends past edges after rotation
    canvas_size = size * 2
    banner_height = int(size * 0.22)

    banner_img = Image.new("RGBA", (canvas_size, canvas_size), (0, 0, 0, 0))
    banner_draw = ImageDraw.Draw(banner_img)

    # Horizontal stripe centered on oversized canvas
    by1 = (canvas_size - banner_height) // 2
    by2 = by1 + banner_height
    banner_draw.rectangle([0, by1, canvas_size, by2], fill=(220, 40, 40, 240))

    # "DEV" text centered in the stripe
    font_size = int(size * 0.14)
    font = _find_font(font_size)
    if font is None:
        font = ImageFont.load_default()

    banner_draw.text(
        (canvas_size // 2, by1 + banner_height // 2),
        "DEV", fill=(255, 255, 255, 255), font=font, anchor="mm",
    )

    # Rotate 35 degrees around canvas center
    banner_img = banner_img.rotate(
        35, center=(canvas_size // 2, canvas_size // 2),
        resample=Image.BICUBIC,
    )

    # Crop so the stripe center lands in the bottom-right of the icon
    crop_x = (canvas_size // 2) - int(size * 0.72)
    crop_y = (canvas_size // 2) - int(size * 0.72)
    return banner_img.crop((crop_x, crop_y, crop_x + size, crop_y + size))


def _apply_dev_banner(img):
    """Overlay a red diagonal 'DEV' stripe in the bottom-right corner."""
    banner = make_dev_banner(img.width)
    img.paste(banner, (0, 0), banner)
    return img


def generate_icons(
    sizes,
    output_dir,
    bg_color="#1E1E2E",
    glyph_svg=None,
    glyph_text=None,
    glyph_color="#FFFFFF",
    glyph_scale=None,
    dev=False,
    filename_format="{size}.png",
):
    """Generate app icons at the requested sizes.

    Input precedence: glyph_svg (needs cairosvg) > glyph_text > solid bg only.
    glyph_scale: 0.0–1.0 of icon size (default 0.55).
    Icons are ALWAYS written as opaque RGB (no alpha channel): macOS renders a
    no-alpha icon edge-to-edge (full-bleed), while any alpha channel makes it
    inset the icon into a padded tile. Returns True on success, False if skipped.
    """
    if not _HAS_PIL:
        warn("Icon generation skipped: Pillow not installed.")
        warn("  Install with: pip3 install Pillow")
        return False

    bg = _hex_to_rgba(bg_color)
    fg = _hex_to_rgba(glyph_color)
    scale = glyph_scale if glyph_scale is not None else _GLYPH_SCALE

    # Resolve rendering mode
    use_svg = False
    use_text = None

    if glyph_svg:
        if _HAS_CAIROSVG:
            use_svg = True
        else:
            fallback_char = os.path.splitext(os.path.basename(glyph_svg))[0][0]
            warn(f"cairosvg not available, falling back to text mode ('{fallback_char}').")
            warn("  Install with: pip3 install cairosvg")
            use_text = fallback_char

    if not use_svg and use_text is None and glyph_text:
        use_text = glyph_text

    os.makedirs(output_dir, exist_ok=True)

    try:
        for size in sizes:
            if use_svg:
                img = _render_svg(size, bg, glyph_svg, scale)
            elif use_text:
                img = _render_text(size, bg, use_text, fg, scale)
            else:
                img = _render_solid(size, bg)

            if dev:
                img = _apply_dev_banner(img)

            # Always flatten onto the bg → opaque RGB, no alpha channel, so
            # macOS renders the icon full-bleed instead of insetting it.
            if img.mode == "RGBA":
                flat = Image.new("RGBA", img.size, bg)
                flat.paste(img, (0, 0), img)
                img = flat.convert("RGB")
            else:
                img = img.convert("RGB")

            fname = filename_format.format(size=size)
            img.save(os.path.join(output_dir, fname), "PNG")
    except Exception:
        traceback.print_exc()
        err("Icon generation failed. Build will continue without custom icons.")
        return False

    return True
