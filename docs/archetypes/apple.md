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
| `tv_scheme` | — | Xcode scheme for the tvOS target. Its presence is what puts `tv` in the `build` / `dev` pickers at all (`deploy` has no tvOS path). |
| `mac_build_config` | `Debug` | Configuration for macOS `build`/`dev`. |
| `ios_build_config` | `Debug` | Configuration for iOS `build`/`dev`. |
| `tv_build_config` | `Debug` | Configuration for tvOS `build`/`dev`. |
| `mac_deploy_config` | `Release` | Configuration for macOS `deploy`. |
| `ios_deploy_config` | `Release` | Configuration for iOS `deploy`. |
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
| `sign_identity` | Selector for the signing identity (matched against available identities). |
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

## Icons — the nested `[apple.icons]` table

Read by the icon-rendering wrappers (see [ADR-0007](../adr/0007-apple-icon-rendering.md)).

| Key | Meaning |
|---|---|
| `mac_asset_dir` | Path to the macOS `.appiconset` (or asset) directory. |
| `ios_asset_dir` | Path to the iOS asset directory. |
| `mac_icon` | Path to an Icon Composer `.icon` bundle; its presence routes the dev-icon swap through the `.icon` path instead of the appiconset PNG path. |
| `bg_color`, `glyph_text`, `glyph_svg`, `glyph_color`, `glyph_scale` | Forwarded to `generate_icons()` for procedural icon generation. |

## Reusable simulator helpers

The `admin_lib.apple` iOS-simulator helpers (`resolve_simulator`, `ensure_booted_simulator`, `install_and_launch_sim`, …) are callable by bare name from any `kind = "python"` action once the `apple` module is in scope — the calling contract, signatures, and a minimal example are tabulated under **"Reusable library helpers in `kind = "python"` bodies"** in [architecture/design.md](../architecture/design.md).
