//go:build darwin

package kinds

// Dev-icon-swap hooks and the iOS display-name patch — the Go port of
// admin_lib/apple.py's build_icon_hooks + patch_ios_display_name, wired
// into the dev-loop build sequence in appledevloop_darwin.go. Closes the
// gap apple_darwin.go's package doc comment used to name explicitly.
//
// Scope matches build_icon_hooks(cfg)'s default call exactly: icon dirs are
// always resolved from [apple.icons] mac_asset_dir/ios_asset_dir regardless
// of which platform (mac/ios/tv) is actually building — tv brandassets and
// the Icon Composer .icon path are out of scope here, since only
// backup_icons/swap_dev_icons_banner/restore_icons are bound in
// internal/luahost today.
//
// appleIconDirs deliberately does NOT resolve its configured paths against
// h.ProjectRoot: archetypes/apple/archetype.toml's own "icons swap-dev"
// action passes the raw configured relative path straight into
// apple.backup_icons/swap_dev_icons_banner, and BackupIcons/RestoreIcons
// resolve relative to the process's own cwd — matching that existing,
// live-verified behaviour keeps a dev-loop backup interoperable with the
// standalone `admin icons restore` verb.
import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
)

// appleIconDirs mirrors _resolve_icon_dirs(cfg, ("mac", "ios")): only the
// configured entries come back, keyed by label, as the raw relative path
// from admin.toml.
func appleIconDirs(cfg map[string]any) map[string]string {
	icons := cfgMap(cfg, "icons")
	dirs := map[string]string{}
	if v := cfgString(icons, "mac_asset_dir", ""); v != "" {
		dirs["mac"] = v
	}
	if v := cfgString(icons, "ios_asset_dir", ""); v != "" {
		dirs["ios"] = v
	}
	return dirs
}

// appleIconBackupDir is the literal relative path every icon-swap caller
// shares — archetypes/apple/archetype.toml's "icons" action and
// admin_lib/apple.py's build_icon_hooks default alike — so a dev-loop
// backup and the standalone `admin icons restore` verb stay interoperable.
const appleIconBackupDir = "tmp/icon-backup"

// appleBuildIconHooks returns (pre, post) for the dev-icon swap, mirroring
// build_icon_hooks: pre backs up the committed appiconset PNGs then swaps
// in the DEV banner; post restores the originals. Both are safe to call
// unconditionally — they no-op when [apple.icons] has no mac/ios asset dir
// configured, so call sites don't need their own "if hooks configured"
// branch (matching Python's `if pre_build: pre_build()` callers).
//
// pre also self-heals a stranded backup left by an interrupted prior dev
// run (Ctrl-C between BackupIcons and SwapDevIconsBanner, or between a
// build and RestoreIcons) — the Go equivalent of
// restore_stranded_dev_icons/_restore_stranded_appiconset_dev_icons: restore
// from the backup dir first, so a crash never lets a DEV-banner icon reach
// a fresh build undetected.
func appleBuildIconHooks(cfg map[string]any, h *luahost.Host) (pre func() error, post func() error) {
	dirs := appleIconDirs(cfg)
	if len(dirs) == 0 {
		return func() error { return nil }, func() error { return nil }
	}
	pre = func() error {
		if isAppleBackupDirPopulated(appleIconBackupDir) {
			h.Warn("Stranded dev icons found in " + appleIconBackupDir + " (interrupted dev run?) — restoring originals")
			if err := luahost.RestoreIcons(dirs, appleIconBackupDir); err != nil {
				return err
			}
		}
		if err := luahost.BackupIcons(dirs, appleIconBackupDir); err != nil {
			return err
		}
		ok, err := luahost.SwapDevIconsBanner(dirs)
		if err != nil {
			return err
		}
		if !ok {
			h.Warn("swapping dev icons failed for one or more icon dirs")
		}
		return nil
	}
	post = func() error {
		return luahost.RestoreIcons(dirs, appleIconBackupDir)
	}
	return pre, post
}

// isAppleBackupDirPopulated reports whether backupDir already holds
// anything — evidence of a stranded backup from an interrupted dev run.
func isAppleBackupDirPopulated(backupDir string) bool {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// appleResolveDevDisplayName mirrors _resolve_sim_dev_display_name: an
// explicit `{platform}_dev_display_name` override, else "<process> Dev".
// Returns "" (no-op for applePatchDisplayName) when process is also empty.
func appleResolveDevDisplayName(cfg map[string]any, platform, process string) string {
	if v := cfgString(cfg, platform+"_dev_display_name", ""); v != "" {
		return v
	}
	if process == "" {
		return ""
	}
	return process + " Dev"
}

// applePatchDisplayName mirrors patch_ios_display_name exactly: patch
// CFBundleDisplayName into the built .app's Info.plist, then re-sign with
// the app's original identity (scraped from `codesign -dvvv`'s first
// "Authority=" line, falling back to ad-hoc "-"). No-op when displayName is
// empty. The re-sign step is mandatory — skipping it breaks device
// installs.
func applePatchDisplayName(appPath, displayName string) error {
	if displayName == "" {
		return nil
	}
	infoPlist := appPath + "/Info.plist"
	if out, err := exec.Command("plutil", "-replace", "CFBundleDisplayName", "-string", displayName, infoPlist).CombinedOutput(); err != nil {
		return fmt.Errorf("plutil patch failed: %s", strings.TrimSpace(string(out)))
	}

	// codesign -dvvv writes to stderr; "Authority=" lines are listed
	// leaf → root, so the first one is the leaf identity to re-sign with.
	identity := "-"
	sigCmd := exec.Command("codesign", "-dvvv", appPath)
	sigOut, _ := sigCmd.CombinedOutput()
	for _, line := range strings.Split(string(sigOut), "\n") {
		if strings.HasPrefix(line, "Authority=") {
			identity = strings.TrimSpace(strings.TrimPrefix(line, "Authority="))
			break
		}
	}

	if out, err := exec.Command("codesign", "--force", "--sign", identity,
		"--preserve-metadata=entitlements,requirements", appPath).CombinedOutput(); err != nil {
		return fmt.Errorf("resign after display-name patch failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
