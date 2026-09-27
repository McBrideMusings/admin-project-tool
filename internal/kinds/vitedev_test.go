package kinds

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseDevMountsSplitsNameEqualsPath proves DEV_MOUNTS's "name=path,
// name2=path2" shape parses into individual mounts.
func TestParseDevMountsSplitsNameEqualsPath(t *testing.T) {
	h := testHost(t)
	mounts, ok := parseDevMounts(h, "media=/mnt/media, books=/mnt/books")
	if !ok {
		t.Fatal("parseDevMounts ok = false, want true")
	}
	if len(mounts) != 2 || mounts[0] != (devMount{"media", "/mnt/media"}) || mounts[1] != (devMount{"books", "/mnt/books"}) {
		t.Fatalf("mounts = %+v, want [{media /mnt/media} {books /mnt/books}]", mounts)
	}
}

// TestParseDevMountsRejectsMissingEquals proves a malformed entry (no "=")
// is a hard failure, not a silently dropped entry.
//
// MUTATION EVIDENCE: change the `idx < 0` check to `continue` (skip the bad
// entry) instead of returning false, and this test fails — ok would come
// back true.
func TestParseDevMountsRejectsMissingEquals(t *testing.T) {
	h := testHost(t)
	_, ok := parseDevMounts(h, "not-a-valid-entry")
	if ok {
		t.Fatal("parseDevMounts ok = true, want false for an entry missing '='")
	}
}

// TestParseDevMountsRejectsDotName proves "." and ".." are rejected as
// mount names.
func TestParseDevMountsRejectsDotName(t *testing.T) {
	h := testHost(t)
	if _, ok := parseDevMounts(h, "..=/somewhere"); ok {
		t.Fatal("parseDevMounts should reject a '..' mount name")
	}
}

// TestParseDevMountsEmptyStringIsNoMounts proves an empty DEV_MOUNTS value
// parses to zero mounts (not an error).
func TestParseDevMountsEmptyStringIsNoMounts(t *testing.T) {
	h := testHost(t)
	mounts, ok := parseDevMounts(h, "")
	if !ok || len(mounts) != 0 {
		t.Fatalf("parseDevMounts(\"\") = (%v, %v), want ([], true)", mounts, ok)
	}
}

// TestSetupDevMountsRefusesMissingTarget proves a mount whose target
// directory doesn't exist refuses to start — setupDevMounts' "targets
// unavailable" guard.
func TestSetupDevMountsRefusesMissingTarget(t *testing.T) {
	h := testHost(t)
	dataDir := t.TempDir()
	mounts := []devMount{{Name: "media", Path: filepath.Join(t.TempDir(), "does-not-exist")}}
	if setupDevMounts(h, dataDir, mounts, true) {
		t.Fatal("setupDevMounts = true, want false when the mount target is missing")
	}
}

// TestSetupDevMountsCreatesSymlink proves a valid mount becomes a symlink
// under dataDir pointing at the target.
//
// MUTATION EVIDENCE: change `os.Symlink(m.Path, link)` to
// `os.Symlink(dataDir, link)` (point at the wrong thing) and this test
// fails — the resolved link target would be dataDir, not the mount's real
// target directory.
func TestSetupDevMountsCreatesSymlink(t *testing.T) {
	h := testHost(t)
	dataDir := t.TempDir()
	target := t.TempDir()
	mounts := []devMount{{Name: "media", Path: target}}
	if !setupDevMounts(h, dataDir, mounts, true) {
		t.Fatal("setupDevMounts = false, want true")
	}
	link := filepath.Join(dataDir, "media")
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if got != target {
		t.Errorf("symlink target = %q, want %q", got, target)
	}
}

// TestSetupDevMountsPrunesStaleSymlink proves a symlink under dataDir whose
// name is no longer in the (non-empty) mount list gets removed —
// setupDevMounts' prune loop. The prune loop only runs at all when
// `mounts` is non-empty (an empty/nil mount list returns success
// immediately, before ever reaching the loop) — this test's `keep` mount is
// what gets past that early return.
//
// MUTATION EVIDENCE: remove the prune loop entirely and this test fails —
// the stale symlink would still be present after setupDevMounts returns.
func TestSetupDevMountsPrunesStaleSymlink(t *testing.T) {
	h := testHost(t)
	dataDir := t.TempDir()
	staleTarget := t.TempDir()
	stale := filepath.Join(dataDir, "stale")
	if err := os.Symlink(staleTarget, stale); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	keepTarget := t.TempDir()
	mounts := []devMount{{Name: "keep", Path: keepTarget}}
	if !setupDevMounts(h, dataDir, mounts, true) {
		t.Fatal("setupDevMounts = false, want true")
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("stale symlink still present: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dataDir, "keep")); err != nil {
		t.Errorf("kept mount should still exist: %v", err)
	}
}

// TestSetupDevMountsRefusesToClobberARealFile proves a non-symlink already
// occupying the mount point is never overwritten.
func TestSetupDevMountsRefusesToClobberARealFile(t *testing.T) {
	h := testHost(t)
	dataDir := t.TempDir()
	target := t.TempDir()
	realFile := filepath.Join(dataDir, "media")
	if err := os.WriteFile(realFile, []byte("not a symlink"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	mounts := []devMount{{Name: "media", Path: target}}
	if setupDevMounts(h, dataDir, mounts, true) {
		t.Fatal("setupDevMounts = true, want false — must refuse to clobber a real file")
	}
	data, err := os.ReadFile(realFile)
	if err != nil || string(data) != "not a symlink" {
		t.Errorf("real file was modified: data=%q err=%v", data, err)
	}
}

// TestBuildViteDevOptionsMapsCoreFields proves buildViteDevOptions maps the
// [vite_dev] table's scalar fields onto viteDevOptions as a straight
// passthrough.
func TestBuildViteDevOptionsMapsCoreFields(t *testing.T) {
	root := t.TempDir()
	cfg := map[string]any{
		"vite_port":    int64(5173),
		"backend_port": int64(3000),
		"frontend_dir": "web",
		"backend_dir":  "server",
		"backend_cmd":  "cargo run",
	}
	opts, err := buildViteDevOptions(root, cfg)
	if err != nil {
		t.Fatalf("buildViteDevOptions: %v", err)
	}
	if opts.VitePort != 5173 || opts.BackendPort != 3000 || opts.FrontendDir != "web" ||
		opts.BackendDir != "server" || opts.BackendCmd != "cargo run" {
		t.Errorf("opts = %+v, want vite_port=5173 backend_port=3000 frontend_dir=web backend_dir=server backend_cmd='cargo run'", opts)
	}
}

// TestBuildViteDevOptionsRejectsMultipleDataRoots proves two
// data_subdirs entries both claiming is_data_root=true is a config error.
//
// MUTATION EVIDENCE: change `dataRootCount > 1` to `dataRootCount > 2` and
// this test fails — buildViteDevOptions would return no error for two
// conflicting data roots.
func TestBuildViteDevOptionsRejectsMultipleDataRoots(t *testing.T) {
	root := t.TempDir()
	cfg := map[string]any{
		"data_subdirs": []any{
			map[string]any{"name": "a", "is_data_root": true},
			map[string]any{"name": "b", "is_data_root": true},
		},
	}
	if _, err := buildViteDevOptions(root, cfg); err == nil {
		t.Fatal("buildViteDevOptions err = nil, want an error for two is_data_root subdirs")
	}
}

// TestBuildViteDevOptionsExportsSubdirEnv proves a data_subdirs entry with
// `env`/`export_as` exposes its resolved path on BackendEnv, resolved
// against the project root rather than the process's cwd — hence the Chdir
// to an unrelated directory: a relative data_dir used to reach
// filepath.Abs, which anchored it to wherever `admin` was typed.
func TestBuildViteDevOptionsExportsSubdirEnv(t *testing.T) {
	root := t.TempDir()
	t.Chdir(t.TempDir())
	cfg := map[string]any{
		"data_dir": "data",
		"data_subdirs": []any{
			map[string]any{"name": "photos", "export_as": "PHOTOS_DIR"},
		},
	}
	opts, err := buildViteDevOptions(root, cfg)
	if err != nil {
		t.Fatalf("buildViteDevOptions: %v", err)
	}
	want := filepath.Join(root, "data", "photos")
	if got := opts.BackendEnv["PHOTOS_DIR"]; got != want {
		t.Errorf("BackendEnv[PHOTOS_DIR] = %q, want %q", got, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Errorf("subdir was not created on disk: %v", err)
	}
}

// TestBuildViteDevOptionsEmptyConfigIsAnError proves an empty [vite_dev]
// table is refused.
func TestBuildViteDevOptionsEmptyConfigIsAnError(t *testing.T) {
	if _, err := buildViteDevOptions(t.TempDir(), nil); err == nil {
		t.Fatal("buildViteDevOptions(nil) err = nil, want an error")
	}
}
