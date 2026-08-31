# `apple` archetype — the `[apple]` schema

The `apple` archetype (`archetypes/apple.py`) exposes `build` / `dev` / `deploy` / `test` / `clean` / `icons` / `reload` as `kind = "python"` actions that read one config dict — the `[apple]` table of a project's `admin.toml`, delivered to the bodies as the `_APPLE_CONFIG` global. Every key below is read by a high-level wrapper in `admin_lib/apple.py`; the "read by" column names the wrapper (or `admin_lib/apple.py` line) so the source stays the authority.

All keys are **optional** unless noted — a wrapper that needs a missing key errors with a clear message at call time rather than at parse. Signing **team** is never a key: it comes from the `IOS_DEVELOPMENT_TEAM` environment variable (global convention), never `admin.toml`.

## Project

| Key | Default | Meaning |
|---|---|---|
| `project_path` | auto-detect | Path to the `.xcworkspace`/`.xcodeproj`. When omitted, the first `*.xcworkspace` then `*.xcodeproj` in the project root is used; errors if none found (`_detect_project_path`). |
| `prebuild_cmd` | — | Shell command run once per invocation, before the project path resolves — so it covers `build`, `dev`, `deploy`, and `icons` on both platforms. Runs in `_run_prebuild_cmd`, called from `_detect_project_path`, which every apple action funnels through. A non-zero exit aborts the command. The motivating case is a generated `.xcodeproj` (XcodeGen, Tuist) kept out of version control: `prebuild_cmd = "xcodegen generate"` means a fresh clone or an edited spec can never be built against a missing or stale project. |
| `derived_data_name` | — | DerivedData folder name; sets `-derivedDataPath`. |

## Schemes and build configurations

| Key | Default | Meaning |
|---|---|---|
| `mac_scheme` | — | Xcode scheme for the macOS target. Its presence is what puts `mac` in the `build` / `dev` / `deploy` target pickers at all. |
| `ios_scheme` | — | Xcode scheme for the iOS target. Its presence is what puts `ios` — and `dev`'s `device` target — in the pickers at all. |
| `tv_scheme` | — | Xcode scheme for the tvOS target. Its presence is what puts `tv` in the `build` / `dev` / `deploy` pickers at all. `dev tv` runs the tvOS simulator; `deploy tv` builds Release and installs on a paired Apple TV. |
| `mac_build_config` | `Debug` | Configuration for macOS `build`/`dev`. |
| `ios_build_config` | `Debug` | Configuration for iOS `build`/`dev`. |
| `tv_build_config` | `Debug` | Configuration for tvOS `build`/`dev`. |
| `mac_deploy_config` | `Release` | Configuration for macOS `deploy`. |
| `ios_deploy_config` | `Release` | Configuration for iOS `deploy`. |
| `tv_deploy_config` | `Release` | Configuration for tvOS `deploy`. |
| `dev_build_settings` | — | Extra `KEY=value` `xcodebuild` build settings appended in **dev** builds only. |

**The archetype assumes no platform.** A project gets exactly the targets its scheme keys declare. An iOS-only manifest is never shown `mac`, and `admin deploy mac` there stops immediately, naming the targets the project does have, instead of getting as far as `deploy_mac` and reporting a missing `mac_scheme`. One source decides this for all three commands: `APPLE_PLATFORMS` / `apple_platforms()` / `resolve_apple_platform()` in `admin_lib/apple.py`. Adding a platform is one entry there plus a dispatch branch in the bodies. Cover: `tests/test_apple_platform_gating.py`.

## Process names (for kill / running-detection)

Process name resolution falls back through: explicit key → the scheme's `PRODUCT_NAME` Xcode setting → the scheme name itself.

| Key | Meaning |
|---|---|
| `mac_process` | Running-process name for the macOS app. |
| `ios_process` | Running-process name for the iOS app. |

## macOS install paths

| Key | Default | Meaning |
|---|---|---|
| `mac_install_path_prod` | `/Applications` | Destination for a deployed (prod) macOS build. |
| `mac_install_path_dev` | — | Destination for a `dev` macOS build install, when dev-install is requested. |

## macOS dev-variant renaming

When `mac_dev_rename` is on and a dev product/display name resolves, the dev build is renamed so it can sit alongside the prod app.

The rename also decides how `dev` finds its own running app between rebuilds. With the rename on, that's the `<name>Dev.app` bundle path; with it off, no such bundle exists, so `dev` matches the build-products path (`/Build/Products/<config>/<name>.app/…`) instead — still distinct from a prod copy in `/Applications`. Set `mac_dev_process` to override either.

| Key | Default | Meaning |
|---|---|---|
| `mac_dev_rename` | `true` | Whether to rename the macOS dev build to the dev product/display name. Also selects how the dev loop identifies its running app (above). |
| `mac_dev_product_name` | `<base>Dev` | Dev build's product (bundle) name. |
| `mac_dev_display_name` | `<base> Dev` | Dev build's display name. |
| `ios_dev_display_name` | `<base> Dev` | iOS dev build's display name. |
| `mac_dev_process` | `<DevProduct>.app/Contents/MacOS/<name>`, or the build-products path when `mac_dev_rename = false` | `pgrep -f` pattern identifying the running dev build. |

## Dev bundle-ID suffix (pbxproj setup required)

`dev` never passes a literal `PRODUCT_BUNDLE_IDENTIFIER` as a build setting — `xcodebuild`'s `-scheme` build applies a setting passed that way to **every target in the project**, so naming one bundle ID there forces an app-extension target (a widget, a share extension, a notification service, ...) to claim its parent app's identity, and the simulator/device refuses to install the result ("Mismatched bundle IDs").

Instead `dev` computes `ADMIN_DEV_BUNDLE_SUFFIX` — the difference between the resolved dev bundle ID and the resolved prod one (`mac_bundle`/`ios_bundle` vs. `mac_dev_bundle`/`ios_dev_bundle`, default `<prod>.dev`) — and passes only that as a build setting. **Each target's own `PRODUCT_BUNDLE_IDENTIFIER` in the Xcode project must reference it** for dev/prod side-by-side installs to work:

```
// app target
PRODUCT_BUNDLE_IDENTIFIER = com.example.app$(ADMIN_DEV_BUNDLE_SUFFIX)

// widget extension target
PRODUCT_BUNDLE_IDENTIFIER = com.example.app$(ADMIN_DEV_BUNDLE_SUFFIX).widgets
```

A prod build passes no suffix, so `$(ADMIN_DEV_BUNDLE_SUFFIX)` expands to nothing and prod IDs are unaffected. **A project whose pbxproj hasn't been updated to reference the variable doesn't fail loudly** — it silently builds dev on top of prod (same bundle ID as the installed prod copy, so dev overwrites it instead of installing side-by-side).

An explicit `mac_dev_bundle`/`ios_dev_bundle` that isn't the prod bundle ID plus a suffix can't be expressed as `ADMIN_DEV_BUNDLE_SUFFIX` and is a hard error naming both values — set it to `<prod bundle ID>` plus a suffix, or drop the key to get the `<prod>.dev` default. Putting `PRODUCT_BUNDLE_IDENTIFIER` into `dev_build_settings` is also a hard error for the same reason: that string is exactly what breaks extensions.

## Signing

| Key | Meaning |
|---|---|
| `sign_identity` | Selector for the signing identity (matched against available identities). Pins a stable certificate so macOS TCC grants survive a rebuild. **It no longer forces `CODE_SIGN_STYLE=Manual` on apps whose entitlements need a provisioning profile** — anything under `com.apple.developer.*` or `aps-environment` keeps automatic signing, and the identity is passed by name rather than by hash, because automatic signing rejects a hash. Manual style with an empty profile specifier tells `xcodebuild` never to create a profile, so a capability-bearing app failed with "requires a provisioning profile with the &lt;X&gt; feature" — an error that reads as a missing capability on a correctly-configured App ID. |
| `code_sign_identity` | Explicit `CODE_SIGN_IDENTITY` value; only consulted when `sign_identity` is set and this is unset. |

Team is `IOS_DEVELOPMENT_TEAM` from the environment — not a key here.

## Log streaming

| Key | Meaning |
|---|---|
| `subsystem` | `os_log` subsystem used to build the `log stream` predicate for `dev` console tailing. |

## Launch arguments

| Key | Meaning |
|---|---|
| `launch_args` | List of strings appended to the macOS `dev` launch as `open -n <app> --args ...`, so the app receives them as its own process arguments. Read by `dev_mac`. Mac only today — `dev_ios`/`dev_tv` take a `launch_args` parameter but nothing reads this key into it. |

## Simulator / device

| Key | Meaning |
|---|---|
| `ios_device` | Simulator (or device) name for iOS `dev`/`deploy`. The `IOS_DEVICE` environment variable overrides it. When neither is set, the reusable simulator helpers pick the best available sim (see below). |
| `tv_device` | Simulator name for tvOS `dev`/`build`, e.g. `Apple TV 4K (3rd generation)`. The `TV_DEVICE` environment variable overrides it. When neither is set, the newest available Apple TV simulator is picked. |

A booted simulator only answers for its own platform — a booted iPhone is never
returned to a `tv` action, and vice versa. If no simulator of the platform
exists at all, the tvOS path says how to install the runtime
(`xcodebuild -downloadPlatform tvOS`), which is the usual reason a fresh machine
has none.

### Physical devices (`deploy ios` / `deploy tv`)

Both go through `deploy_device()`, which builds for the real SDK
(`generic/platform=iOS` → `-iphoneos`, `generic/platform=tvOS` → `-appletvos`),
installs with `xcrun devicectl device install app`, and launches the production
bundle ID. `DEVICE_PLATFORMS` in `admin_lib/apple.py` holds everything that
differs between the two.

`xcrun devicectl list devices` returns every paired CoreDevice in one array — a
phone and an Apple TV side by side — so `find_connected_device(platform)`
filters on `hardwareProperties.platform` before taking the first row. Without
that filter, a paired Apple TV listed first became the target of `deploy ios`
and the iPhone build was pushed at the TV.

**The device is registered with the team automatically.** Before the build, both
paths call `ensure_device_registered()`, which looks the hardware UDID up in App
Store Connect and POSTs it if Apple has never seen it (and re-enables it if it
was disabled). Without that, a new device fails the build with *"Your team has
no devices from which to generate a provisioning profile"* — a message about the
team, so it reads as a broken account rather than as an unregistered device, and
the old fix was a trip to the developer portal. `-allowProvisioningUpdates`
creates profiles but never registers hardware, which is why the API key has to
do it explicitly. No key configured → a warning, and the build runs anyway.

An Apple TV has no cable to plug in: pairing is network-only and one-time — on
the Apple TV, Settings → Remotes and Devices → Remote App and Devices, then
Xcode → Window → Devices and Simulators on the Mac and type the PIN. Until that
is done devicectl does not list it, and `deploy tv` says so with those steps
rather than failing on a missing device.

## Icons — the nested `[apple.icons]` table

Read by the icon-rendering wrappers (see [ADR-0007](../adr/0007-apple-icon-rendering.md)).

| Key | Meaning |
|---|---|
| `mac_asset_dir` | Path to the macOS `.appiconset` (or asset) directory. |
| `ios_asset_dir` | Path to the iOS asset directory. |
| `tv_asset_dir` | Path to the tvOS `.brandassets` directory (the whole bundle — `App Icon.imagestack`, `App Icon - App Store.imagestack`, `Top Shelf Image.imageset`, `Top Shelf Image Wide.imageset` — not a single appiconset). Wholly regenerated by `regenerate_icons()`/`generate_tv_icons()`; unlike `mac_asset_dir`/`ios_asset_dir`, the sizes and layer names inside are fixed by Apple's schema, not read off existing PNGs. |
| `mac_icon` | Path to an Icon Composer `.icon` bundle; its presence routes the mac/iOS dev-icon swap through the `.icon` path instead of the appiconset PNG path. Independent of `tv_asset_dir` — a project can use Icon Composer for mac/iOS and a `.brandassets` catalog for tv at the same time. |
| `bg_color`, `glyph_text`, `glyph_color`, `glyph_scale` | Forwarded to `generate_icons()` (mac/iOS) and `generate_tv_icons()` (tv) for procedural icon generation — one glyph config drives all three platforms. `glyph_text` is the only supported glyph source. |
| `glyph_svg` | **No longer supported** (admin-3rgp.48 dropped SVG glyph rendering, along with its unmaintained `oksvg`/`rasterx` dependency). Setting it fails `generate_icons()`/`generate_tv_icons()` with an error naming the dropped capability, rather than rendering a blank icon — use `glyph_text` instead. |

### tvOS: a layered stack, not a flat PNG

A tvOS app icon isn't a size ladder like mac/iOS — it's a **parallax image
stack** (Front/Middle/Back layers Xcode slides apart as the icon takes
focus) plus a separate wide **Top Shelf image**. `generate_tv_icons()` in
`admin_lib/icons.py` builds the real Xcode asset-catalog schema under
`tv_asset_dir`:

```
App Icon.imagestack/                 400x240 @1x, 800x480 @2x   (home screen)
App Icon - App Store.imagestack/     1280x768 @1x only          (App Store)
  Front.imagestacklayer/Content.imageset/   the glyph, transparent background
  Middle.imagestacklayer/Content.imageset/  transparent spacer
  Back.imagestacklayer/Content.imageset/    opaque bg_color plate (required)
Top Shelf Image.imageset/            1920x720 @1x, 3840x1440 @2x
Top Shelf Image Wide.imageset/       2320x720 @1x, 4640x1440 @2x
```

It's a mechanical decomposition of the same `bg_color` + `glyph_text`
inputs the mac/iOS generator uses — Back is the opaque
background plate (tvOS errors if the bottom layer has any transparency),
Middle is an empty transparent spacer that gives the parallax effect room,
Front is the glyph floating on a transparent background. One glyph config
produces consistent icons on all three platforms; there is no separate tv
art direction to configure.

The DEV-banner swap (`swap-dev`/`restore`) follows suit: with a glyph
configured it regenerates the whole stack with the banner baked into the
Front layer and the Top Shelf images (same `dev=True` path as mac/iOS);
without one it overlays the banner onto the *existing* Front-layer and
Top-Shelf PNGs in place via `apply_tv_dev_banner()`, leaving the committed
Back/Middle art untouched — mirroring `swap_dev_icons`'s no-glyph path for
appiconsets. Backup/restore copies the whole `.brandassets` tree
(`backup_tv_icons`/`restore_tv_icons` in `admin_lib/apple.py`) rather than
individual files, since the catalog is a directory tree, not a flat set of
PNGs.

## Reusable simulator helpers

The `admin_lib.apple` iOS-simulator helpers (`resolve_simulator`, `ensure_booted_simulator`, `install_and_launch_sim`, …) are callable by bare name from any `kind = "python"` action once the `apple` module is in scope — the calling contract, signatures, and a minimal example are tabulated under **"Reusable library helpers in `kind = "python"` bodies"** in [architecture/design.md](../architecture/design.md).

## App Store Connect API key (optional, and worth it)

Set these three in the machine's environment — never in `admin.toml`, since the path is a credential:

```
APP_STORE_CONNECT_KEY_PATH    path to AuthKey_<KEYID>.p8
APP_STORE_CONNECT_KEY_ID      the <KEYID> from that filename
APP_STORE_CONNECT_ISSUER_ID   UUID from App Store Connect > Users and Access > Integrations
```

With all three set:

- Every `xcodebuild` the archetype runs carries `-authenticationKeyPath/-authenticationKeyID/-authenticationKeyIssuerID`, so signing and profile creation stop depending on an Apple ID being signed into Xcode. That login does not reliably persist, and when it lapses the failure names a capability rather than an account.
- `admin appid` works:

```sh
admin appid                                       # report on this project's identifiers
admin appid com.example.app --capability icloud   # enable a capability
admin appid com.example.new --name "New" --capability push   # register, then enable
```

Capability names accept Apple's enum (`ICLOUD`, `PUSH_NOTIFICATIONS`, …) or the friendlier aliases in `admin_lib/appstoreconnect.py` (`icloud`, `push`, `app-groups`, …).

All three variables or none: `xcodebuild` rejects a partial set, and a path pointing at a missing file falls back to whatever account Xcode has rather than failing every build on that machine.
