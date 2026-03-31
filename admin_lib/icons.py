"""General-purpose icon generation with optional DEV banner overlay.

Dependencies (graceful degradation):
  - Pillow: required for all icon generation
  - cairosvg: required for SVG glyph mode (falls back to text without it)

Install: pip3 install Pillow cairosvg
"""

import os
import sys
import tempfile
import traceback

# admin_lib.core may not be on the path when icons is used standalone,
# so fall back to plain stderr output.
try:
    from .core import warn as _warn, err as _err
except ImportError:
    def _warn(msg): print(msg, file=sys.stderr)
    def _err(msg): print(msg, file=sys.stderr)

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


def _render_text(size, bg_color, text, glyph_color):
    """Render a text character centered on a colored background."""
    img = Image.new("RGBA", (size, size), bg_color)
    draw = ImageDraw.Draw(img)

    font_size = int(size * _GLYPH_SCALE)
    font = _find_font(font_size)
    if font is None:
        font = ImageFont.load_default()

    draw.text(
        (size // 2, size // 2), text,
        fill=glyph_color, font=font, anchor="mm",
    )
    return img


def _render_svg(size, bg_color, svg_path):
    """Render an SVG glyph centered on a colored background.

    SVGs use their own fill colors; glyph_color is not applied here.
    """
    img = Image.new("RGBA", (size, size), bg_color)

    glyph_size = max(int(size * _GLYPH_SCALE), 1)
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


def _apply_dev_banner(img):
    """Overlay a red diagonal 'DEV' stripe in the bottom-right corner.

    All dimensions are proportional to image size so the banner looks
    identical at any resolution.
    """
    size = img.width

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
    cropped = banner_img.crop((crop_x, crop_y, crop_x + size, crop_y + size))

    img.paste(cropped, (0, 0), cropped)
    return img


def generate_icons(
    sizes,
    output_dir,
    bg_color="#1E1E2E",
    glyph_svg=None,
    glyph_text=None,
    glyph_color="#FFFFFF",
    dev=False,
    filename_format="{size}.png",
):
    """Generate app icons at the requested sizes.

    Input modes (in priority order):
      1. glyph_svg  — SVG file rendered centered on background (needs cairosvg)
      2. glyph_text — text/character rendered centered on background
      3. neither    — solid background color only

    Returns True on success, False if skipped (missing deps or error).
    """
    if not _HAS_PIL:
        _warn("Icon generation skipped: Pillow not installed.")
        _warn("  Install with: pip3 install Pillow")
        return False

    bg = _hex_to_rgba(bg_color)
    fg = _hex_to_rgba(glyph_color)

    # Resolve rendering mode
    use_svg = False
    use_text = None

    if glyph_svg:
        if _HAS_CAIROSVG:
            use_svg = True
        else:
            fallback_char = os.path.splitext(os.path.basename(glyph_svg))[0][0]
            _warn(f"cairosvg not available, falling back to text mode ('{fallback_char}').")
            _warn("  Install with: pip3 install cairosvg")
            use_text = fallback_char

    if not use_svg and use_text is None and glyph_text:
        use_text = glyph_text

    os.makedirs(output_dir, exist_ok=True)

    try:
        for size in sizes:
            if use_svg:
                img = _render_svg(size, bg, glyph_svg)
            elif use_text:
                img = _render_text(size, bg, use_text, fg)
            else:
                img = _render_solid(size, bg)

            if dev:
                img = _apply_dev_banner(img)

            fname = filename_format.format(size=size)
            img.save(os.path.join(output_dir, fname), "PNG")
    except Exception:
        traceback.print_exc()
        _err("Icon generation failed. Build will continue without custom icons.")
        return False

    return True
