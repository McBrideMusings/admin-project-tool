"""Unit tests for admin_lib/icons.py's tvOS layered-icon-stack generator.

icons.py has no problematic imports (unlike apple.py's .core dependency), so
it's imported directly rather than through test_apple.py's exec+regex loader.
Skips outright if Pillow isn't installed — same optional-dependency carve-out
icons.py itself documents.
"""
from __future__ import annotations

import os
import tempfile

import pytest

pytest.importorskip("PIL")

from admin_lib.icons import (  # noqa: E402
    apply_tv_dev_banner,
    generate_tv_icons,
    make_dev_banner_rect,
)


def _stack_layer_png(brandassets_dir, stack, layer, fname):
    return os.path.join(
        brandassets_dir, stack, f"{layer}.imagestacklayer", "Content.imageset", fname
    )


def test_generate_tv_icons_writes_the_full_brandassets_schema():
    with tempfile.TemporaryDirectory() as root:
        brand = os.path.join(root, "App Icon & Top Shelf Image.brandassets")
        ok = generate_tv_icons(brand, bg_color="#123456", glyph_text="X")
        assert ok is True

        # Top-level Contents.json lists all four elements.
        import json
        with open(os.path.join(brand, "Contents.json")) as f:
            top = json.load(f)
        filenames = {a["filename"] for a in top["assets"]}
        assert filenames == {
            "App Icon.imagestack",
            "App Icon - App Store.imagestack",
            "Top Shelf Image.imageset",
            "Top Shelf Image Wide.imageset",
        }

        # Home-screen icon: 1x + 2x, three layers each.
        for layer in ("Front", "Middle", "Back"):
            assert os.path.isfile(_stack_layer_png(brand, "App Icon.imagestack", layer, f"{layer}.png"))
            assert os.path.isfile(_stack_layer_png(brand, "App Icon.imagestack", layer, f"{layer}@2x.png"))

        # App Store icon: 1x only.
        assert os.path.isfile(
            _stack_layer_png(brand, "App Icon - App Store.imagestack", "Back", "Back.png")
        )
        assert not os.path.isfile(
            _stack_layer_png(brand, "App Icon - App Store.imagestack", "Back", "Back@2x.png")
        )

        # Top Shelf images at both scales, tv + tv-marketing idioms.
        for fname in ("tv.png", "tv@2x.png", "tv-marketing.png", "tv-marketing@2x.png"):
            assert os.path.isfile(os.path.join(brand, "Top Shelf Image.imageset", fname))
    print("OK: generate_tv_icons writes the full .brandassets schema")


def test_generate_tv_icons_layer_sizes_and_opacity():
    from PIL import Image

    with tempfile.TemporaryDirectory() as root:
        brand = os.path.join(root, "brand.brandassets")
        generate_tv_icons(brand, bg_color="#123456", glyph_text="X")

        back = Image.open(_stack_layer_png(brand, "App Icon.imagestack", "Back", "Back.png"))
        front = Image.open(_stack_layer_png(brand, "App Icon.imagestack", "Front", "Front.png"))

        assert back.size == (400, 240)
        assert back.mode == "RGB", "the bottom layer must be opaque — tvOS errors otherwise"

        assert front.size == (400, 240)
        alpha_min, alpha_max = front.convert("RGBA").split()[-1].getextrema()
        assert alpha_min < 255, "the front layer must carry transparency for the parallax effect"

        store_back = Image.open(
            _stack_layer_png(brand, "App Icon - App Store.imagestack", "Back", "Back.png")
        )
        assert store_back.size == (1280, 768)

        top_1x = Image.open(os.path.join(brand, "Top Shelf Image.imageset", "tv.png"))
        top_2x = Image.open(os.path.join(brand, "Top Shelf Image.imageset", "tv@2x.png"))
        assert top_1x.size == (1920, 720)
        assert top_2x.size == (3840, 1440)

        wide_1x = Image.open(os.path.join(brand, "Top Shelf Image Wide.imageset", "tv.png"))
        assert wide_1x.size == (2320, 720)
    print("OK: tvOS layer/top-shelf sizes and back-layer opacity match Apple's schema")


def test_generate_tv_icons_dev_banner_only_touches_front_layer():
    from PIL import Image

    with tempfile.TemporaryDirectory() as root:
        prod = os.path.join(root, "prod.brandassets")
        dev = os.path.join(root, "dev.brandassets")
        generate_tv_icons(prod, bg_color="#123456", glyph_text="X", dev=False)
        generate_tv_icons(dev, bg_color="#123456", glyph_text="X", dev=True)

        back_prod = open(_stack_layer_png(prod, "App Icon.imagestack", "Back", "Back.png"), "rb").read()
        back_dev = open(_stack_layer_png(dev, "App Icon.imagestack", "Back", "Back.png"), "rb").read()
        assert back_prod == back_dev, "the DEV banner must not touch the opaque Back layer"

        front_prod = open(_stack_layer_png(prod, "App Icon.imagestack", "Front", "Front.png"), "rb").read()
        front_dev = open(_stack_layer_png(dev, "App Icon.imagestack", "Front", "Front.png"), "rb").read()
        assert front_prod != front_dev, "the DEV banner must be baked into the Front layer"
    print("OK: dev=True banners the Front layer only, leaving Back untouched")


def test_apply_tv_dev_banner_overlays_existing_art_in_place():
    with tempfile.TemporaryDirectory() as root:
        brand = os.path.join(root, "brand.brandassets")
        # Real committed art (no glyph regeneration involved).
        generate_tv_icons(brand, bg_color="#223344", glyph_svg=None, glyph_text=None, dev=False)

        front_path = _stack_layer_png(brand, "App Icon.imagestack", "Front", "Front.png")
        back_path = _stack_layer_png(brand, "App Icon.imagestack", "Back", "Back.png")
        before_front = open(front_path, "rb").read()
        before_back = open(back_path, "rb").read()

        ok = apply_tv_dev_banner(brand)
        assert ok is True

        after_front = open(front_path, "rb").read()
        after_back = open(back_path, "rb").read()
        assert after_front != before_front, "overlay must modify the committed Front layer in place"
        assert after_back == before_back, "overlay must leave the Back layer untouched"
    print("OK: apply_tv_dev_banner overlays committed art without regenerating from a glyph")


def test_make_dev_banner_rect_matches_requested_rectangle():
    banner = make_dev_banner_rect(400, 240)
    assert banner.size == (400, 240)
    banner_square = make_dev_banner_rect(512, 512)
    assert banner_square.size == (512, 512)
    print("OK: make_dev_banner_rect renders at the requested non-square size")
