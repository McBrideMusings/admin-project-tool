# `apple` archetype — the `[apple]` schema

The `apple` archetype (`archetypes/apple.py`) exposes `build` / `dev` / `deploy` / `test` / `clean` / `icons` / `reload` as `kind = "python"` actions that read one config dict — the `[apple]` table of a project's `admin.toml`, delivered to the bodies as the `_APPLE_CONFIG` global. Every key below is read by a high-level wrapper in `admin_lib/apple.py`; the "read by" column names the wrapper (or `admin_lib/apple.py` line) so the source stays the authority.

All keys are **optional** unless noted — a wrapper that needs a missing key errors with a clear message at call time rather than at parse. Signing **team** is never a key: it comes from the `IOS_DEVELOPMENT_TEAM` environment variable (global convention), never `admin.toml`.

## Project

| Key | Default | Meaning |
|---|---|---|
| `project_path` | auto-detect | Path to the `.xcworkspace`/`.xcodeproj`. When omitted, the first `*.xcworkspace` then `*.xcodeproj` in the project root is used; errors if none found (`_detect_project_path`). |
| `prebuild_cmd` | — | Shell command run before every `xcodebuild` invocation (build/deploy/test, both platforms). |
| `derived_data_name` | — | DerivedData folder name; sets `-derivedDataPath`. |

## Schemes and build configurations

| Key | Default | Meaning |
|---|---|---|
| `mac_scheme` | — | Xcode scheme for the macOS target. Required for any `mac` action. |
| `ios_scheme` | — | Xcode scheme for the iOS target. Required for any `ios` action. |
| `mac_build_config` | `Debug` | Configuration for macOS `build`/`dev`. |
| `ios_build_config` | `Debug` | Configuration for iOS `build`/`dev`. |
| `mac_deploy_config` | `Release` | Configuration for macOS `deploy`. |
| `ios_deploy_config` | `Release` | Configuration for iOS `deploy`. |
| `dev_build_settings` | — | Extra `KEY=value` `xcodebuild` build settings appended in **dev** builds only. |

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

## Simulator / device

| Key | Meaning |
|---|---|
| `ios_device` | Simulator (or device) name for iOS `dev`/`deploy`. The `IOS_DEVICE` environment variable overrides it. When neither is set, the reusable simulator helpers pick the best available sim (see below). |

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
