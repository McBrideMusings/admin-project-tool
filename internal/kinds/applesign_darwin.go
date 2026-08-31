//go:build darwin

package kinds

// The dev/prod bundle-suffix convention layer — the Go port of
// admin_lib/apple.py's _compose_sign_settings, _entitlements_paths,
// _target_needs_provisioning_profile and _compose_dev_settings. This was the
// one piece of admin-3rgp.12 apple_darwin.go's package doc comment named as
// deliberately not ported; its absence was observed live as an unexpanded
// "$(...)" xcconfig substitution reaching a real `log stream` predicate (see
// internal/luahost/apple_pbxproj_darwin.go's expandXcodeValue, which is the
// other half of that fix — resolving a substitution when the pbxproj itself
// can answer it).
//
// Built on the existing primitives rather than a second bundle-composition
// path: appleBundle (apple_darwin.go) is still the one place that decides a
// target's bundle id, dev or prod. This file only composes the xcodebuild
// -setting overrides layered on top: code signing (_compose_sign_settings)
// and, for a dev build, ADMIN_DEV_BUNDLE_SUFFIX (_compose_dev_settings) so
// each target's own pbxproj-declared
// "com.foo.app$(ADMIN_DEV_BUNDLE_SUFFIX)" convention actually resolves at
// build time instead of building on the prod identity unnoticed.
import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// appleEntitlementsPaths returns every entitlements file this project might
// sign with, as absolute paths — sourced from the Xcode project (via
// CODE_SIGN_ENTITLEMENTS on each configured scheme's target) rather than the
// manifest, matching admin_lib/apple.py's _entitlements_paths: admin.toml has
// no entitlements key, and adding one would be a second place to keep the
// answer correct.
//
// CODE_SIGN_ENTITLEMENTS is relative to SRCROOT — the directory holding the
// .xcodeproj/.xcworkspace — NOT the project root. h.ProjectRoot is used only
// when project is empty (no .xcodeproj/.xcworkspace could be detected at
// all); resolving against the project root instead of SRCROOT silently
// produces a nonexistent path for any project nested a level down, exactly
// the incident the Python docstring for this function names.
func appleEntitlementsPaths(cfg map[string]any, h *luahost.Host, project string) []string {
	srcroot := h.ProjectRoot
	if project != "" {
		if d := filepath.Dir(project); d != "" && d != "." {
			srcroot = d
		}
	}
	var seen []string
	for _, schemeKey := range []string{"mac_scheme", "ios_scheme", "tv_scheme"} {
		scheme := cfgString(cfg, schemeKey, "")
		if scheme == "" {
			continue
		}
		value, ok := luahost.XcodeSetting(project, scheme, "CODE_SIGN_ENTITLEMENTS")
		if !ok || value == "" {
			continue
		}
		path := value
		if !filepath.IsAbs(path) {
			path = filepath.Join(srcroot, value)
		}
		found := false
		for _, p := range seen {
			if p == path {
				found = true
				break
			}
		}
		if !found {
			seen = append(seen, path)
		}
	}
	return seen
}

// appleTargetNeedsProvisioningProfile reports whether this target cannot be
// signed and installed without a real, creatable-or-refreshable provisioning
// profile. Ports admin_lib/apple.py's _target_needs_provisioning_profile.
//
// Every iOS/tvOS device install needs one unconditionally, so platform in
// ("ios", "tv") short-circuits to true. For mac, the entitlements files the
// manifest's schemes point at are read for the keys that actually require
// one (iCloud, App Groups, push, HealthKit, …) — the plain sandbox and its
// network client are granted by the entitlement alone and don't count, so a
// plain sandboxed Mac app keeps the stable manual-signing identity it had
// before this existed.
func appleTargetNeedsProvisioningProfile(cfg map[string]any, h *luahost.Host, project, platform string) bool {
	if platform == "ios" || platform == "tv" {
		return true
	}
	keysNeedingAProfile := []string{"com.apple.developer.", "aps-environment"}
	for _, path := range appleEntitlementsPaths(cfg, h, project) {
		data, err := os.ReadFile(path)
		if err != nil {
			// An unreadable entitlements file makes this answer false, which
			// forces manual signing and fails the build with a message
			// naming a capability — pointing at the developer portal instead
			// of at the path that could not be read. Say so.
			h.Warn(fmt.Sprintf("entitlements file not readable, assuming no profile needed: %s", path))
			continue
		}
		text := string(data)
		for _, k := range keysNeedingAProfile {
			if strings.Contains(text, k) {
				return true
			}
		}
	}
	return false
}

// appleComposeSignSettings builds xcodebuild build-setting overrides for
// code signing from [apple] config: code_sign_identity, development_team,
// code_sign_style (each resolved through manifest.ResolveEnv), plus, when
// sign_identity is present with no explicit code_sign_identity, a dynamic
// identity resolved from the keychain (luahost.ResolveSigningIdentity) so a
// dev build gets a stable signature (TCC grants persist across rebuilds)
// without a hardcoded hash in the manifest. Ports
// admin_lib/apple.py's _compose_sign_settings.
//
// platform is "ios" | "tv" | "mac" — device platforms always need a
// provisioning profile (see appleTargetNeedsProvisioningProfile), which
// decides whether the resolved identity is passed by name (automatic
// signing, capable of iCloud/push/etc.) or by hash with manual signing and
// no profile at all (mac only, and only when nothing needs one).
func appleComposeSignSettings(cfg map[string]any, h *luahost.Host, project, platform string) string {
	var parts []string
	for _, pair := range [][2]string{
		{"code_sign_identity", "CODE_SIGN_IDENTITY"},
		{"development_team", "DEVELOPMENT_TEAM"},
		{"code_sign_style", "CODE_SIGN_STYLE"},
	} {
		manifestKey, xcodeKey := pair[0], pair[1]
		raw, present := cfg[manifestKey]
		if !present || raw == nil {
			continue
		}
		val, err := manifest.ResolveEnv(fmt.Sprintf("%v", raw))
		if err != nil {
			h.Warn(fmt.Sprintf("apple.%s: %v", manifestKey, err))
			continue
		}
		if val != "" {
			parts = append(parts, fmt.Sprintf(`%s="%s"`, xcodeKey, val))
		}
	}

	_, hasSignIdentity := cfg["sign_identity"]
	_, hasCodeSignIdentity := cfg["code_sign_identity"]
	if hasSignIdentity && !hasCodeSignIdentity {
		selector := cfgString(cfg, "sign_identity", "")
		ident, warnLines := luahost.ResolveSigningIdentity(selector)
		for _, w := range warnLines {
			h.Warn(w)
		}
		switch {
		case ident != "" && appleTargetNeedsProvisioningProfile(cfg, h, project, platform):
			// Automatic signing refuses a hash: "X is automatically signed,
			// but code signing identity <hash> has been manually specified".
			// It accepts the identity by *name*, which is what sign_identity
			// holds anyway ("Apple Development"), and which still pins the
			// certificate family the TCC grants are keyed to.
			resolvedSelector, err := manifest.ResolveEnv(fmt.Sprintf("%v", cfg["sign_identity"]))
			if err != nil {
				h.Warn(fmt.Sprintf("apple.sign_identity: %v", err))
			} else {
				parts = append(parts, fmt.Sprintf(`CODE_SIGN_IDENTITY="%s"`, resolvedSelector))
			}
		case ident != "":
			// Manual signing ONLY when the app needs no provisioning
			// profile. CODE_SIGN_STYLE=Manual with an empty
			// PROVISIONING_PROFILE_SPECIFIER tells xcodebuild to sign with
			// an existing profile and never make one — so
			// -allowProvisioningUpdates and an App Store Connect API key are
			// both ignored, and a target declaring any capability fails
			// with a profile error naming that capability, which reads
			// exactly like the capability is missing from the App ID.
			//
			// An app with entitlements beyond the sandbox — iCloud, push,
			// App Groups — must therefore keep automatic signing (the
			// branch above). The stable identity this exists for (so macOS
			// TCC grants survive a rebuild) is still pinned here; only the
			// style differs.
			parts = append(parts, fmt.Sprintf(`CODE_SIGN_IDENTITY="%s"`, ident))
			parts = append(parts, "CODE_SIGN_STYLE=Manual")
			parts = append(parts, "PROVISIONING_PROFILE_SPECIFIER=")
		}
	}

	return strings.Join(parts, " ")
}

// appleComposeDevSettings combines appleComposeSignSettings with
// ADMIN_DEV_BUNDLE_SUFFIX (appended last so it wins), for a dev build only —
// build/deploy pass appleComposeSignSettings alone. Ports
// admin_lib/apple.py's _compose_dev_settings.
//
// Never emits PRODUCT_BUNDLE_IDENTIFIER itself: a setting passed through
// XcodebuildOptions.ExtraSettings applies to every target in the project
// (see that field's doc comment), so naming one bundle ID there forces an
// app-extension target to claim its parent app's identity and the simulator
// refuses to install it. Instead each target's own PRODUCT_BUNDLE_IDENTIFIER
// in the pbxproj must reference $(ADMIN_DEV_BUNDLE_SUFFIX) so each target
// derives its own suffixed identity. The suffix is derived by subtracting
// the resolved prod bundle id (appleBundle(dev=false)) from the resolved dev
// one (appleBundle(dev=true)) — normally ".dev" from the "<prod>.dev"
// default.
//
// Errors rather than silently degrading, because under this scheme a bad
// value doesn't just misconfigure a nice-to-have — it glues the old broken
// (uncomposed, same-identity) behaviour back on:
//   - dev_build_settings containing PRODUCT_BUNDLE_IDENTIFIER — exactly the
//     string that breaks extensions.
//   - an explicit {platform}_dev_bundle that isn't the prod bundle id plus a
//     suffix — unexpressible as a suffix, so silently falling back to the
//     old global override would reintroduce the bug.
func appleComposeDevSettings(cfg map[string]any, h *luahost.Host, project, platform string) (string, error) {
	extras := strings.TrimSpace(cfgString(cfg, "dev_build_settings", ""))
	if strings.Contains(extras, "PRODUCT_BUNDLE_IDENTIFIER") {
		return "", fmt.Errorf(
			"apple.dev_build_settings contains PRODUCT_BUNDLE_IDENTIFIER — a "+
				"setting passed to xcodebuild this way applies to every target "+
				"in the project, so it forces an app extension's bundle ID to "+
				"match its parent app's and the simulator refuses to install "+
				"it. Remove it — admin derives ADMIN_DEV_BUNDLE_SUFFIX from "+
				"%s_dev_bundle instead; see the apple archetype docs.", platform)
	}

	prod := appleBundle(project, cfg, platform, false)
	devBundleID := appleBundle(project, cfg, platform, true)
	suffix := ""
	if devBundleID != "" {
		if prod == "" || !strings.HasPrefix(devBundleID, prod) {
			return "", fmt.Errorf(
				"apple.%s_dev_bundle (%q) is not the prod %s_bundle (%q) plus a "+
					"suffix — ADMIN_DEV_BUNDLE_SUFFIX can only express <prod "+
					"bundle ID> + <suffix>. Drop the explicit %s_dev_bundle key "+
					"to default to <prod>.dev, or set it to the prod bundle ID "+
					"with a suffix appended.", platform, devBundleID, platform, prod, platform)
		}
		suffix = devBundleID[len(prod):]
	}

	override := ""
	if suffix != "" {
		override = "ADMIN_DEV_BUNDLE_SUFFIX=" + suffix
	}
	result := override
	if extras != "" {
		if override != "" {
			result = extras + " " + override
		} else {
			result = extras
		}
	}

	sign := appleComposeSignSettings(cfg, h, project, platform)
	var parts []string
	if result != "" {
		parts = append(parts, result)
	}
	if sign != "" {
		parts = append(parts, sign)
	}
	return strings.Join(parts, " "), nil
}
