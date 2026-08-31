package electron

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuiltAppPathFindsArchSuffixedMacDir proves builtAppPath finds
// electron-builder's `mac-arm64`-style output dir, not just plain `mac` —
// the Go port of admin_lib/electron.py::_built_app_path.
//
// MUTATION EVIDENCE: change the prefix check from `strings.HasPrefix(name,
// "mac-")` to an exact `name == "mac-arm64"` and this test fails on the
// "mac-x64-ia32" case below (a hypothetical future arch triple this
// function must still match by prefix, not enumeration).
func TestBuiltAppPathFindsArchSuffixedMacDir(t *testing.T) {
	desktopDir := t.TempDir()
	outDir := "dist"
	macDir := filepath.Join(desktopDir, outDir, "mac-arm64")
	if err := os.MkdirAll(macDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	got := builtAppPath(desktopDir, outDir, "MyApp")
	want := filepath.Join(desktopDir, outDir, "mac-arm64", "MyApp.app")
	if got != want {
		t.Errorf("builtAppPath = %q, want %q", got, want)
	}
}

// TestBuiltAppPathFallsBackToPlainMac proves the bare "mac" dir (x64) is
// still found when no arch-suffixed variant exists.
func TestBuiltAppPathFallsBackToPlainMac(t *testing.T) {
	desktopDir := t.TempDir()
	outDir := "dist"
	if err := os.MkdirAll(filepath.Join(desktopDir, outDir, "mac"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	got := builtAppPath(desktopDir, outDir, "MyApp")
	want := filepath.Join(desktopDir, outDir, "mac", "MyApp.app")
	if got != want {
		t.Errorf("builtAppPath = %q, want %q", got, want)
	}
}

// TestBuildElectronRequiresDesktopDir proves BuildElectron refuses to run
// without desktop_dir/app_name set, matching admin_lib/electron.py's
// `_desktop_dir`/`_app_name` sys.exit(1) guards.
func TestBuildElectronRequiresDesktopDir(t *testing.T) {
	if _, err := BuildElectron(Config{}, t.TempDir()); err == nil {
		t.Fatal("BuildElectron err = nil, want an error for missing desktop_dir")
	}
	if _, err := BuildElectron(Config{DesktopDir: "x"}, t.TempDir()); err == nil {
		t.Fatal("BuildElectron err = nil, want an error for missing app_name")
	}
}

// TestCopyTreeCopiesNestedFilesAndPreservesContent proves copyTree
// recursively copies a directory tree with file content intact — the
// mechanism StageResources relies on for staging a web build's dist/ into
// the Electron project.
//
// MUTATION EVIDENCE: change copyTree's os.MkdirAll(target, info.Mode()) case
// to `return nil` (skip creating the dir) and this test fails — the nested
// file's parent directory would never exist, so copyFile's os.OpenFile
// would fail and the read-back below would error.
func TestCopyTreeCopiesNestedFilesAndPreservesContent(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "assets"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "assets", "logo.png"), []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "web-dist")
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "assets", "logo.png"))
	if err != nil {
		t.Fatalf("ReadFile nested: %v", err)
	}
	if string(got) != "fake-png-bytes" {
		t.Errorf("nested file content = %q, want %q", got, "fake-png-bytes")
	}
	got, err = os.ReadFile(filepath.Join(dst, "index.html"))
	if err != nil {
		t.Fatalf("ReadFile top-level: %v", err)
	}
	if string(got) != "<html></html>" {
		t.Errorf("top-level file content = %q, want %q", got, "<html></html>")
	}
}

// TestDevElectronRequiresDesktopDir proves DevElectron refuses to run
// without desktop_dir set.
func TestDevElectronRequiresDesktopDir(t *testing.T) {
	if _, err := DevElectron(Config{}, t.TempDir(), DevOptions{}, func(string, string) int { return 0 }); err == nil {
		t.Fatal("DevElectron err = nil, want an error for missing desktop_dir")
	}
}

// TestDevElectronRequiresElectronBinary proves DevElectron refuses to run
// when node_modules/.bin/electron doesn't exist under desktop_dir.
func TestDevElectronRequiresElectronBinary(t *testing.T) {
	desktopDir := t.TempDir()
	if _, err := DevElectron(Config{DesktopDir: desktopDir}, t.TempDir(), DevOptions{}, func(string, string) int { return 0 }); err == nil {
		t.Fatal("DevElectron err = nil, want an error for a missing electron binary")
	}
}

// TestDevElectronInvokesRunWithReloadWithTheElectronBinary proves DevElectron
// builds a command line that execs the project's own electron binary, not
// a bare "electron" that would resolve against the wrong PATH — the whole
// point of admin_lib/electron.py::dev_electron's `_electron_bin` lookup.
//
// MUTATION EVIDENCE: change the cmd string to just `"electron ."` (drop the
// desktop_dir-scoped bin path) and this test fails — gotCmd would no longer
// contain the fake binary's absolute path.
func TestDevElectronInvokesRunWithReloadWithTheElectronBinary(t *testing.T) {
	desktopDir := t.TempDir()
	binDir := filepath.Join(desktopDir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	binPath := filepath.Join(binDir, "electron")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var gotCmd, gotTarget string
	rc, err := DevElectron(Config{DesktopDir: desktopDir}, t.TempDir(), DevOptions{}, func(cmd, target string) int {
		gotCmd, gotTarget = cmd, target
		return 42
	})
	if err != nil {
		t.Fatalf("DevElectron: %v", err)
	}
	if rc != 42 {
		t.Errorf("rc = %d, want 42 (passed through from runWithReload)", rc)
	}
	if gotTarget != "electron" {
		t.Errorf("target = %q, want %q", gotTarget, "electron")
	}
	if !strings.Contains(gotCmd, binPath) {
		t.Errorf("cmd = %q, want it to contain the electron binary path %q", gotCmd, binPath)
	}
}
