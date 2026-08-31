// Package electron builds, deploys, and runs an Electron desktop shell from
// a project's own config — the Go port of admin_lib/electron.py. Any
// project shipping an Electron client configures the equivalent of Python's
// [electron] table and gets three operations, all driven by config:
//
//	BuildElectron   stage web/server resources, then `electron-builder
//	                --mac dir` — an artifact only, nothing installed.
//	DeployElectron  BuildElectron, then atomically install into
//	                Options.InstallDir (default /Applications).
//	DevElectron     run Electron from source, unpackaged, restarting the
//	                main process when a watched file changes.
//
// macOS-only: `ditto`, `.app` bundles, `/Applications`, and
// `electron-builder --mac` are all Apple-specific, matching the Python
// original's own scope (it hardcodes `--mac dir`).
//
// Not yet wired into a kind or a Lua host binding — see this ticket's (Go
// port of the "metro, react-native and electron helpers") own note in
// internal/metro's package doc comment: no kind name for "electron" exists
// in internal/kinds.Kinds yet, and Lua archetype bodies that would call
// this (admin-3rgp.37) haven't been written. This package is the real,
// callable, ready-to-wire port.
package electron

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Config is the Go port of admin_lib/electron.py's `[electron]` cfg dict.
type Config struct {
	DesktopDir        string // required — the Electron project dir.
	AppName           string // required — release bundle name.
	InstallDir        string // default "/Applications".
	OutDir            string // electron-builder output dir, relative to DesktopDir. Default "dist".
	WebSourceDir      string // optional — built via `npm run build`.
	WebDistDirname    string // default "web-dist".
	ServerDistDir     string // optional — repo-root-relative dir copied into the desktop project.
	ServerDistDirname string // default "server-dist".
	WatchFiles        []string
	DevEnv            map[string]string
	DevDistDirVar     string
}

func (c Config) installDir() string {
	if c.InstallDir != "" {
		return c.InstallDir
	}
	return "/Applications"
}

func (c Config) outDir() string {
	if c.OutDir != "" {
		return c.OutDir
	}
	return "dist"
}

func (c Config) webDistDirname() string {
	if c.WebDistDirname != "" {
		return c.WebDistDirname
	}
	return "web-dist"
}

func (c Config) serverDistDirname() string {
	if c.ServerDistDirname != "" {
		return c.ServerDistDirname
	}
	return "server-dist"
}

func electronBin(desktopDir string) (string, error) {
	bin := filepath.Join(desktopDir, "node_modules/.bin/electron")
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("electron binary not found at %s — run `npm install` in %s", bin, desktopDir)
	}
	return bin, nil
}

// builtAppPath finds electron-builder's `dir` target output — named `mac`
// on x64 but `mac-<arch>` on everything else (e.g. `mac-arm64`) — the Go
// port of admin_lib/electron.py::_built_app_path.
func builtAppPath(desktopDir, outDir, appName string) string {
	base := filepath.Join(desktopDir, outDir)
	macDir := "mac"
	if entries, err := os.ReadDir(base); err == nil {
		for _, e := range entries {
			if e.Name() == "mac" || strings.HasPrefix(e.Name(), "mac-") {
				macDir = e.Name()
				break
			}
		}
	}
	return filepath.Join(base, macDir, appName+".app")
}

// runShell runs cmd via /bin/sh -c in dir, streaming to the process's own
// stdout/stderr — the caller (a kind factory, once one exists) is
// responsible for routing this through internal/luahost.Host.RunCmd or
// internal/streams.OutputPump instead when this package is wired into the
// dispatch path; this package itself stays free of that dependency to avoid
// an import cycle (internal/kinds already depends on internal/luahost,
// which would need to depend back on this package to bind it as a Lua
// host function).
func runShell(dir, cmd string) error {
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Dir = dir
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// StageResources is the Go port of
// admin_lib/electron.py::stage_electron_resources: build the web client (if
// configured) and copy the repo's server dist (if configured) into the
// desktop project, ready for electron-builder.
func StageResources(cfg Config, projectRoot string) error {
	if cfg.WebSourceDir != "" {
		webDir := filepath.Join(projectRoot, cfg.WebSourceDir)
		if err := runShell(webDir, "npm run build"); err != nil {
			return fmt.Errorf("web client build failed: %w", err)
		}
		src := filepath.Join(projectRoot, cfg.WebSourceDir, "dist")
		dst := filepath.Join(cfg.DesktopDir, cfg.webDistDirname())
		_ = os.RemoveAll(dst)
		if err := copyTree(src, dst); err != nil {
			return fmt.Errorf("could not stage web dist: %w", err)
		}
	}

	if cfg.ServerDistDir != "" {
		src := filepath.Join(projectRoot, cfg.ServerDistDir)
		if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
			return fmt.Errorf("%s not found — build the server dist matrix first", src)
		}
		dst := filepath.Join(cfg.DesktopDir, cfg.serverDistDirname())
		_ = os.RemoveAll(dst)
		if err := copyTree(src, dst); err != nil {
			return fmt.Errorf("could not stage server dist: %w", err)
		}
	}
	return nil
}

// BuildElectron is the Go port of admin_lib/electron.py::build_electron:
// StageResources, then `electron-builder --mac dir`. Returns the built
// .app's path (release identity, self-contained — nothing installed).
func BuildElectron(cfg Config, projectRoot string) (string, error) {
	if cfg.DesktopDir == "" {
		return "", fmt.Errorf("[electron].desktop_dir not set")
	}
	if cfg.AppName == "" {
		return "", fmt.Errorf("[electron].app_name not set")
	}

	if err := StageResources(cfg, projectRoot); err != nil {
		return "", err
	}

	if err := runShell(cfg.DesktopDir, "npx electron-builder --mac dir"); err != nil {
		return "", fmt.Errorf("electron-builder failed: %w", err)
	}

	appPath := builtAppPath(cfg.DesktopDir, cfg.outDir(), cfg.AppName)
	if _, err := os.Stat(appPath); err != nil {
		return "", fmt.Errorf("built app not found at %s", appPath)
	}
	return appPath, nil
}

func killRunning(installDir, appName string) {
	exePath := filepath.Join(installDir, appName+".app", "Contents/MacOS", appName)
	if err := exec.Command("pgrep", "-f", exePath).Run(); err == nil {
		_ = exec.Command("pkill", "-f", exePath).Run()
	}
}

// InstallElectron is the Go port of admin_lib/electron.py::install_electron:
// atomically install a built .app into cfg.InstallDir, killing any running
// copy first. Uses `ditto`, not a generic recursive copy — a signed .app
// bundle's validity depends on its exact symlink/resource-fork structure
// (e.g. *.framework/Versions/Current), which a generic copy doesn't reliably
// preserve; see admin_lib/electron.py's own comment for the codesign
// failure this fixes.
func InstallElectron(builtPath string, cfg Config) error {
	if cfg.AppName == "" {
		return fmt.Errorf("[electron].app_name not set")
	}
	installDir := cfg.installDir()
	target := filepath.Join(installDir, cfg.AppName+".app")
	staged := filepath.Join(installDir, "."+cfg.AppName+".app.staged")

	_ = os.RemoveAll(staged)
	killRunning(installDir, cfg.AppName)

	if err := exec.Command("ditto", builtPath, staged).Run(); err != nil {
		return fmt.Errorf("ditto failed: %w", err)
	}

	// rename(2) can only replace an EMPTY directory — a non-empty existing
	// target (every install after the first) fails with ENOTEMPTY. The
	// running instance is already dead, so clearing the old bundle
	// immediately before the rename is safe.
	_ = os.RemoveAll(target)
	if err := os.Rename(staged, target); err != nil {
		return fmt.Errorf("could not install %s: %w", target, err)
	}
	_ = exec.Command("open", target).Run()
	return nil
}

// DeployElectron is the Go port of admin_lib/electron.py::deploy_electron:
// BuildElectron, then InstallElectron.
func DeployElectron(cfg Config, projectRoot string) error {
	built, err := BuildElectron(cfg, projectRoot)
	if err != nil {
		return err
	}
	return InstallElectron(built, cfg)
}

// DevOptions configures DevElectron.
type DevOptions struct {
	// Info/Warn print status lines the way admin_lib's info()/warn() do.
	Info func(msg string)
}

// DevElectron is the Go port of admin_lib/electron.py::dev_electron: run
// Electron from source, unpackaged (no electron-builder step — main-process
// files are read live), restarting the child when a watched file changes.
// Reuses internal/reload.RunWithReload's spawn/kill/signal-forward
// machinery — the watcher goroutine below sends this process the same
// SIGUSR1 that machinery already treats as "reload requested", the same
// "one restart-a-child implementation, not two" reasoning
// admin_lib/electron.py's own doc comment gives for reusing
// admin_lib.reload.run_with_reload.
//
// runWithReload is injected (rather than importing internal/reload
// directly) to keep this package's own tests free of spawning a real
// Electron process; the real kind factory that eventually wires this wires
// it to reload.RunWithReload.
func DevElectron(cfg Config, projectRoot string, opts DevOptions, runWithReload func(cmd, target string) int) (int, error) {
	if cfg.DesktopDir == "" {
		return 1, fmt.Errorf("[electron].desktop_dir not set")
	}
	bin, err := electronBin(cfg.DesktopDir)
	if err != nil {
		return 1, err
	}

	for k, v := range cfg.DevEnv {
		_ = os.Setenv(k, v)
	}
	if cfg.DevDistDirVar != "" && cfg.ServerDistDir != "" {
		_ = os.Setenv(cfg.DevDistDirVar, filepath.Join(projectRoot, cfg.ServerDistDir))
	}

	var watchPaths []string
	for _, f := range cfg.WatchFiles {
		p := filepath.Join(cfg.DesktopDir, f)
		if _, err := os.Stat(p); err == nil {
			watchPaths = append(watchPaths, p)
		}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	if len(watchPaths) > 0 {
		wg.Add(1)
		go watchFiles(watchPaths, stop, &wg)
	}
	defer func() {
		close(stop)
		wg.Wait()
	}()

	if opts.Info != nil {
		opts.Info("launching from source (unpackaged — main-process hot reload on file change)...")
	}
	cmd := "cd " + shQuote(cfg.DesktopDir) + " && exec " + shQuote(bin) + " ."
	return runWithReload(cmd, "electron"), nil
}

// watchFiles polls mtimes (not an FS-event watch — an atomic-rename save,
// how most editors write, makes inode-based watchers fire phantom events on
// the dead inode) and sends this process SIGUSR1 on a real change, debounced
// to avoid a double-fire from two files changing in the same editor save.
func watchFiles(paths []string, stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	mtimes := map[string]time.Time{}
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			mtimes[p] = fi.ModTime()
		}
	}
	var lastChange time.Time
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			changed := false
			for _, p := range paths {
				fi, err := os.Stat(p)
				if err != nil {
					continue
				}
				if !fi.ModTime().Equal(mtimes[p]) {
					mtimes[p] = fi.ModTime()
					changed = true
				}
			}
			if changed {
				now := time.Now()
				if now.Sub(lastChange) < 150*time.Millisecond {
					continue
				}
				lastChange = now
				_ = syscall.Kill(os.Getpid(), syscall.SIGUSR1)
			}
		}
	}
}

func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, "\t\n '\"$&*()[]{}|;<>?!~`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// copyTree recursively copies src to dst, preserving each file's mode —
// the Go stdlib has no shutil.copytree equivalent. This is used only for
// staging plain build output (web dist, server dist) into the Electron
// project, never for the signed .app bundle itself — InstallElectron uses
// `ditto` for that, exactly as admin_lib/electron.py does, since a generic
// copy does not reliably preserve a signed bundle's structure.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if dir := filepath.Dir(dst); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
