# ADR-0007: Apple (macOS + iOS) app-icon generation — alpha, full-bleed, and dev naming

**Status:** Accepted

## Context

Generating macOS/iOS app icons through the admin tool repeatedly produced icons
that looked correct as PNGs but rendered *wrong in the Dock* — shrunken/inset
instead of full-bleed — and dev-variant apps showed the wrong name on hover.
Two non-obvious macOS behaviours are responsible. Both are invisible in an image
viewer and cost hours each time they're rediscovered, so they are documented
here as the single source of truth.

### Gotcha 1 — the alpha channel controls full-bleed vs inset

macOS decides how to draw an app icon in the Dock from whether the PNG is fully
opaque:

| Icon pixels | Dock rendering |
| --- | --- |
| **Fully opaque** — `RGB` (no alpha channel), or `RGBA` with *every* pixel α=255 | **Full-bleed** — artwork fills the whole tile, edge-to-edge, like first-party apps |
| **Any transparency** — an alpha channel with *any* pixel α<255 (soft shadow, anti-aliased edge, deliberate transparent margin) | **Inset** — macOS shrinks it into a smaller padded tile; looks wrong next to other apps |

The trigger is **any sub-255 alpha anywhere**, not the mere presence of an alpha
channel — but the only safe guarantee is to strip the alpha channel entirely.

This is invisible in Preview/Finder: two icons that look identical render
differently in the Dock. The only reliable diagnostic is the pixel data:

```python
from PIL import Image
im = Image.open("icon_1024.png")
im.mode                                            # 'RGB' → no alpha → full-bleed
                                                   # 'RGBA' → has alpha → suspect
im.convert("RGBA").split()[-1].getextrema()[0]     # min alpha; <255 → renders inset
```

The classic trap is a **DEV banner overlay**: its rotated, anti-aliased red
stripe (and a sub-255 fill) drops the min alpha below 255, so a bannered icon
that looks fine renders inset.

### Gotcha 2 — the Dock tooltip is the `.app` filename, not `CFBundleName`

A dev variant's *name* shows up in several independent places, each fed by a
different source:

| Surface | Source |
| --- | --- |
| Dock **hover tooltip** / Finder | the **`.app` filename** (e.g. `Macterm.app` → "Macterm") |
| Menu-bar app menu | `CFBundleName` |
| Some Finder/system surfaces | `CFBundleDisplayName` |
| In-app strings (window title, welcome screen, …) | hardcoded in the app, unless they read `Bundle.main`'s `CFBundleDisplayName` |

So setting `CFBundleName`/`CFBundleDisplayName` to "Macterm Dev" fixes the menu
bar but **not** the Dock tooltip — that needs the `.app` *file* renamed to
`Macterm Dev.app`.

### iOS vs macOS

- **iOS** masks icons into the rounded-rect shape itself and forbids alpha —
  icons are full-bleed opaque squares. The flatten-to-RGB rule is correct there.
- **macOS** does *not* mask the artwork into a shape; the PNG is shown as-is. A
  full-bleed Dock look therefore *requires* a fully-opaque (no-alpha) square.
  The classic macOS "icon grid" with a ~10% transparent margin (≈824px artwork
  centered in a 1024px canvas) is the *alternative*, non-full-bleed convention —
  but that margin is alpha<255, so it triggers the inset treatment. **You cannot
  have a transparent margin AND full-bleed; pick one.** We pick full-bleed →
  no alpha.

## Decision

1. **`admin_lib.icons.generate_icons` always emits opaque RGB** (it strips the
   alpha channel by flattening onto the bg colour). There is no `opaque` flag —
   it's the only behaviour. Every icon path (including the dev-banner swap's
   overlay) flattens to RGB. Any new icon path must do the same.

2. **One shared generator.** All projects generate icons through
   `generate_icons` — via `[apple.icons]` + the apple archetype's `icons`
   command (regenerate / swap-dev / restore) or the dev-loop banner swap.
   **No per-project icon scripts.**

3. **Dev-variant Dock name = rename the `.app`.** Enable
   `[apple] mac_dev_product_name = "<Name> Dev"`; the dev loop's
   `dev_macos_postprocess` renames the built `.app` → `<Name> Dev.app`, patches
   `CFBundleDisplayName`, and re-signs with the stable identity (so TCC grants
   survive). `PRODUCT_NAME` stays stable so the build's `.app` (and anything
   referencing it, e.g. a test target's `TEST_HOST`) is unaffected.

4. **In-app strings read the bundle display name.** Window titles, welcome
   screens, etc. should read `CFBundleDisplayName` from `Bundle.main` (not a
   hardcoded app name) so the dev variant labels itself everywhere.

## How to configure a project

```toml
[apple]
# … scheme/bundle/etc …
mac_dev_product_name = "Macterm Dev"   # → dev app renamed "Macterm Dev.app" (Dock)

[apple.icons]
mac_asset_dir = "Path/To/Assets.xcassets/AppIcon.appiconset"
# glyph_svg / glyph_text / bg_color → generate from a glyph (regenerate path);
# omit the glyph → the dev-loop swap overlays the banner on the committed art.
```

- Generate/refresh production icons: `admin icons regenerate`
- The dev build automatically gets the DEV banner (overlaid + flattened to RGB,
  restored after the build) and the renamed `.app`.

## Consequences

- Icons render full-bleed in the Dock; dev variants are visually (banner) and
  nominally ("… Dev") distinct everywhere.
- A transparent macOS "margin" look is not available alongside full-bleed (by
  design — see Gotcha 1 / iOS-vs-macOS).
- Code references: `admin_lib/icons.py` (alpha flatten + module docstring),
  `admin_lib/apple.py` `dev_macos_postprocess` (rename) and `swap_dev_icons`
  (banner overlay → RGB).
