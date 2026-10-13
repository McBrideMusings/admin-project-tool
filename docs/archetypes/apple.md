# `apple` archetype — the `[apple]` schema

The `apple` archetype (`archetypes/apple/archetype.toml`) declares one `kind = "apple"` action per platform+verb (`build-mac`, `dev-ios`, `deploy-tv`, `clean-ios`, ...; see [ADR-0019](../adr/0019-the-tool-defines-mechanism-admin-toml-defines-structure.md) for why the archetype itself wires no `build`/`dev`/`deploy` command or picker — a project composes only the targets it has, typically via its own `kind = "multi-target"` action). Each action's `run` names a verb (`build_mac`, `dev_ios`, ...) that `internal/kinds/apple.go`'s `makeApple` dispatches through `runAppleVerb` in `internal/kinds/apple_darwin.go`, against one config map — the `[apple]` table of the project's `admin.toml`. Every key below is read by that dispatch or one of the verb bodies in `apple_darwin.go`.

All keys are **optional** unless noted — a wrapper that needs a missing key errors with a clear message at call time rather than at parse. Signing **team** is never a key: it comes from the `IOS_DEVELOPMENT_TEAM` environment variable (global convention), never `admin.toml`.

## Project

| Key | Default | Meaning |
|---|---|---|
| `project_path` | auto-detect | Path to the `.xcworkspace`/`.xcodeproj`. When omitted, the first `*.xcworkspace` then `*.xcodeproj` in the project root is used; errors if none found (`appleProject`, `internal/kinds/apple_darwin.go`). |
| `prebuild_cmd` | — | Shell command run once per invocation, before the project path resolves — so it covers `build`, `dev`, `deploy`, and `icons` on both platforms. Runs in `runApplePrebuild`, called from `appleProject`, which every apple action funnels through. A non-zero exit aborts the command. The motivating case is a generated `.xcodeproj` (XcodeGen, Tuist) kept out of version control: `prebuild_cmd = "xcodegen generate"` means a fresh clone or an edited spec can never be built against a missing or stale project. |
| `derived_data_name` | — | DerivedData folder name; sets `-derivedDataPath`. |

## Schemes and build configurations

| Key | Default | Meaning |
|---|---|---|
| `mac_scheme` | — | Xcode scheme for the macOS target. Required by `build-mac`/`dev-mac`/`deploy-mac`/`clean` (when clean falls back to it). |
| `ios_scheme` | — | Xcode scheme for the iOS target. Required by `build-ios`/`dev-ios`/`dev-device`/`deploy-ios`. |
| `tv_scheme` | — | Xcode scheme for the tvOS target. Required by `build-tv`/`dev-tv`/`dev-device-tv`/`deploy-tv`. `dev-tv` runs the tvOS simulator; `deploy-tv` builds Release and installs on a paired Apple TV. |
| `mac_build_config` | `Debug` | Configuration for macOS `build`/`dev`. |
| `ios_build_config` | `Debug` | Configuration for iOS `build`/`dev`. |
| `tv_build_config` | `Debug` | Configuration for tvOS `build`/`dev`. |
| `mac_deploy_config` | `Release` | Configuration for macOS `deploy`. |
| `ios_deploy_config` | `Release` | Configuration for iOS `deploy`. |
| `tv_deploy_config` | `Release` | Configuration for tvOS `deploy`. |
| `dev_build_settings` | — | Extra `KEY=value` `xcodebuild` build settings appended in **dev** builds only. |
| `dev_chip_dirs` | — | Source directories that receive the DEV-build chip. See below. |

## The DEV-build chip (`dev_chip_dirs`)

A dev build paints a small red `DEV` marker into the bottom-leading corner of
every window it opens. `DEV·<worker>` when the bundle ID carries a per-worker
suffix (`com.example.app.dev.lxhd31`), because per-worker builds are dev builds
too and the bare word cannot tell three simultaneously-running apps apart.

```toml
[apple]
dev_chip_dirs = ["Scratchpad", "ScratchpadIOS"]   # one per app target
```

Every apple verb — `build`, `dev`, `deploy`, `clean` — writes
`AdminDevChip.swift` and `AdminDevChipBootstrap.m` into each named directory,
rewriting them whenever they drift from the tool's copy. **The tool supplies
every byte; the manifest supplies only the destinations**, which is the
mechanism/structure split [ADR-0019](../adr/0019-the-tool-defines-mechanism-admin-toml-defines-structure.md)
draws — the tool cannot guess a project's target layout. Name one directory per
app target; skip extensions and widgets, which have no windows.

Nothing else is required of the project. There is no call site: the Objective-C
file runs the chip from `+load`, before `main`, so no line of app code
references it. Both halves of the failure this replaces are removals rather than
guards — see the header of `internal/kinds/appledevchip.go` and
[ADR-0020](../adr/0020-the-archetype-ships-the-dev-build-chip.md).

A directory that is not already inside an app target's sources needs adding to
that target once. xcodegen and Xcode's file-system-synchronized groups pick up a
whole folder, so with either there is nothing to do.

The gate is the runtime bundle identifier — a trailing `.dev` or an infix
`.dev.`, matching the convention `ADMIN_DEV_BUNDLE_SUFFIX` already establishes.
A production build compiles both files and draws nothing. Naming a directory
that does not exist is a hard error, not a skip: a typo there would leave the
chip silently absent, which is exactly the failure it exists to prevent.

**The archetype assumes no platform.** Because `build-mac`/`build-ios`/`build-tv` (and their `dev`/`deploy`/`clean` counterparts) are separate actions rather than targets behind one auto-populated picker, a project wires up only the ones matching the schemes it declares — usually as targets of its own `kind = "multi-target"` command. Calling a verb whose scheme key is missing fails immediately naming the missing key (e.g. `apple.mac_scheme required for clean`, from `runAppleVerb` in `internal/kinds/apple_darwin.go`), rather than getting partway through a build.

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
| `mac_install_path_dev` | — | Where the macOS dev build is installed: by `deploy_mac_dev`, and by `dev_mac --install`. Both build with the DEV-banner icons and the dev rename. |
| `mac_deploy_verify` | — | Shell command run by `deploy_mac`, from the project root, after build and after notarize/staple, before `install_path` is touched. The built `.app` path is shell-quoted and appended as the final argument. Output streams to the terminal; a non-zero exit aborts the deploy and leaves `install_path` untouched. Unset means no verify step, identical to today's behavior. |

## Distribution signing for deploy

A plain `deploy` builds Release under the project's development signing, so the
installed app carries `get-task-allow` and a debugger can attach. A distribution
identity is reachable only by archiving and then running `-exportArchive`, which
re-signs the app; `appleArchiveAndExport` (`internal/kinds/apple_darwin.go`) does
both, with manual signing against a profile installed under
`~/Library/MobileDevice/Provisioning Profiles`.

| Key | Default | Meaning |
|---|---|---|
| `mac_sign_identity` | — | A value containing `Developer ID` routes `deploy mac` through a `developer-id` export with this certificate. |
| `mac_provisioning_profile` | — | Name or UUID of the Developer ID profile for the Mac bundle id. Required by the Developer ID export. |
| `notarize` | `false` | Implies the Developer ID export, then submits the app to the notary service and staples it before install. |
| `ios_provisioning_profile` | — | Name or UUID of an ad-hoc profile for the iOS bundle id. Setting it routes `deploy ios` through a `release-testing` (ad-hoc) export; the `.ipa`'s `Payload/*.app` is what devicectl installs. The profile must list the device's UDID. |
| `ios_export_identity` | `Apple Distribution` | Certificate the ad-hoc export signs with. Separate from `ios_sign_identity`, which every iOS build uses, the dev loop included. |

macOS can drop `get-task-allow` without distribution signing: set
`CODE_SIGN_INJECT_BASE_ENTITLEMENTS = NO` on the Release configuration. iOS cannot,
because a development profile requires `get-task-allow = true` and codesign rejects
an entitlements file that sets it false.

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
| `launch_args` | List of strings appended to the macOS `dev` launch as `open -n <app> --args ...`, so the app receives them as its own process arguments. Read by the macOS and simulator dev loops. Each `${VAR}` is expanded (including `${WORKTREE_SLUG}`), and an unset reference is an error; `--args` on the command line replaces the list and is passed verbatim. |

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

Both go through `appleDeployDevice` (`internal/kinds/apple_darwin.go`), which builds for the real SDK
(`generic/platform=iOS` → `-iphoneos`, `generic/platform=tvOS` → `-appletvos`),
installs with `xcrun devicectl device install app`, and launches the production
bundle ID. `devicePlatforms` in `internal/luahost/apple_darwin.go` holds
everything that differs between the two.

`xcrun devicectl list devices` returns every paired CoreDevice in one array — a
phone and an Apple TV side by side — so `h.FindConnectedDevice(platform, deviceType)`
(`internal/luahost/apple_devices_darwin.go`) filters on `hardwareProperties.platform` before
taking the first row. Without that filter, a paired Apple TV listed first became the target
of `deploy-ios` and the iPhone build was pushed at the TV.

**The device is registered with the team automatically.** Before the build, both
paths call `appleEnsureDeviceRegistered` (`internal/kinds/apple_appid_darwin.go`), which looks
the hardware UDID up in App Store Connect and POSTs it if Apple has never seen it (and
re-enables it if it was disabled). Without that, a new device fails the build with *"Your team has
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

Read by the `icons` action's Lua body (`archetypes/apple/archetype.toml`, `kind = "lua"`), which
calls the `apple.generate_icons`/`apple.generate_tv_icons` host functions (see
[ADR-0007](../adr/0007-apple-icon-rendering.md) and
[architecture/lua-host-api.md](../architecture/lua-host-api.md)).

| Key | Meaning |
|---|---|
| `mac_asset_dir` | Path to the macOS `.appiconset` (or asset) directory. |
| `ios_asset_dir` | Path to the iOS asset directory. |
| `tv_asset_dir` | Path to the tvOS `.brandassets` directory (the whole bundle — `App Icon.imagestack`, `App Icon - App Store.imagestack`, `Top Shelf Image.imageset`, `Top Shelf Image Wide.imageset` — not a single appiconset). Wholly regenerated by `apple.generate_tv_icons`; the sizes and layer names inside are fixed by Apple's schema. |
| `sizes` | **Required** for `icons regenerate` on `mac_asset_dir`/`ios_asset_dir` — a list of pixel sizes (e.g. `[16, 32, 128, 256, 512, 1024]`) passed to `apple.generate_icons`. `admin icons regenerate` errors naming the missing key if unset; there is no auto-detection of sizes from existing committed PNGs or a fresh catalog's `Contents.json`. |
| `mac_icon` | **Not implemented.** The Icon Composer `.icon` bundle dev-icon-swap path is not wired into `admin icons` today (`archetypes/apple/archetype.toml`'s `icons` action notes this as a follow-up); only the appiconset PNG path works. |
| `bg_color`, `glyph_text`, `glyph_color`, `glyph_scale` | Forwarded to `apple.generate_icons` (mac/iOS) and `apple.generate_tv_icons` (tv) for procedural icon generation — one glyph config drives all three platforms. `glyph_text` is the only supported glyph source. |
| `glyph_svg` | **No longer supported** (admin-3rgp.48 dropped SVG glyph rendering, along with its unmaintained `oksvg`/`rasterx` dependency). Setting it fails `apple.generate_icons`/`apple.generate_tv_icons` with an error naming the dropped capability, rather than rendering a blank icon — use `glyph_text` instead. |

Also not implemented in the `icons` action today: a separate always-committed `*-dev` asset
catalog (`mac_dev_asset_dir`/`ios_dev_asset_dir`), and self-healing a stranded dev banner left by
an interrupted process. Both are noted as follow-ups in the action's own source comment.

### tvOS: a layered stack, not a flat PNG

A tvOS app icon isn't a size ladder like mac/iOS — it's a **parallax image
stack** (Front/Middle/Back layers Xcode slides apart as the icon takes
focus) plus a separate wide **Top Shelf image**. `apple.generate_tv_icons`
(`GenerateTVIcons` in `internal/luahost/apple_icons_generate_darwin.go`) builds the real Xcode
asset-catalog schema under `tv_asset_dir`:

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
Front layer and the Top Shelf images (the same `dev` path as mac/iOS);
without one it overlays the banner onto the *existing* Front-layer and
Top-Shelf PNGs in place via `apple.apply_tv_dev_banner`, leaving the committed
Back/Middle art untouched — mirroring `apple.swap_dev_icons_banner`'s no-glyph path for
appiconsets. Backup/restore copies the whole `.brandassets` tree
(`BackupTVIcons`/`RestoreTVIcons` in `internal/luahost/apple_icons_darwin.go`) rather than
individual files, since the catalog is a directory tree, not a flat set of
PNGs.

## Reusable simulator helpers

The `apple.*` iOS-simulator functions (`apple.resolve_simulator`, `apple.ensure_booted_simulator`, `apple.install_and_launch_sim`, …) registered by `internal/luahost` are callable from any `kind = "lua"` action — the full function catalog and signatures are tabulated in [architecture/lua-host-api.md](../architecture/lua-host-api.md).

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

Capability names accept Apple's enum (`ICLOUD`, `PUSH_NOTIFICATIONS`, …) or the friendlier aliases in `internal/ascapi/ascapi.go` (`icloud`, `push`, `app-groups`, …).

All three variables or none: `xcodebuild` rejects a partial set, and a path pointing at a missing file falls back to whatever account Xcode has rather than failing every build on that machine.
