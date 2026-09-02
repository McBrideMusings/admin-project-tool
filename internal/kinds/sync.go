package kinds

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// makeSync is kind="sync"'s factory — the Go port of
// admin_lib/interp.py::_make_sync plus admin_lib/core.py::sync's local
// branch. `run` is the destination path (resolved with manifest.ResolveEnv,
// erroring on an unset var with no default — a sync destination is never
// something to guess at), `src` defaults to "dist/", and `excludes` is a
// list of glob patterns matched against each entry's own basename, mirroring
// Python's shutil.ignore_patterns.
//
// Only the local-copy branch of Python's sync() is ported (delete=True, the
// historical default, always on): a remote user@host:/path destination
// shells out to rsync exactly as Python did, since that machinery already
// exists as a single command line. dry_run, post_sync and chown are not
// exposed — none of the 14 archetypes' current sync actions use them
// (measured the same way ADR-0010 measured Lua's host-call surface), and
// adding an unused knob is exactly the anti-pattern ADR-0009 warns against.
func makeSync(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	destTmpl, ok := actionString(action, "run")
	if !ok || destTmpl == "" {
		return nil, fmt.Errorf(`action %q: kind="sync" requires 'run' (a destination path)`, name)
	}
	src, _ := actionString(action, "src")
	if src == "" {
		src = "dist/"
	}
	excludes, ok := actionStringList(action, "excludes")
	if !ok {
		return nil, fmt.Errorf("action %q: 'excludes' must be a list of strings", name)
	}

	return func(args []string) error {
		h := newHost(m)
		dest, err := manifest.ResolveEnv(destTmpl)
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if err := runSync(h, src, dest, excludes); err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		return nil
	}, nil
}

// isRemoteDest mirrors admin_lib/core.py::sync's is_remote check:
// "user@host:/path" — a colon present, with an "@" somewhere before it.
func isRemoteDest(dest string) bool {
	idx := strings.Index(dest, ":")
	if idx < 0 {
		return false
	}
	return strings.Contains(dest[:idx], "@")
}

func runSync(h *luahost.Host, srcDir, dest string, excludes []string) error {
	if isRemoteDest(dest) {
		var exFlags []string
		for _, e := range excludes {
			exFlags = append(exFlags, "--exclude "+shellQuote(e))
		}
		cmd := "rsync -avz --delete " + strings.Join(exFlags, " ") + " " + srcDir + "/ " + dest + "/"
		h.Info("Syncing to " + dest)
		res, err := h.RunCmd(cmd, luahost.RunCmdOptions{CaptureLog: true})
		if err != nil {
			return err
		}
		if !res.Ok {
			h.Err("rsync failed")
			return fmt.Errorf("rsync failed")
		}
		h.Ok("Synced: " + dest)
		return nil
	}

	h.Info("Copying to " + dest)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	// Mirror the source exactly (Python's delete=True default): remove
	// anything at dest with no counterpart in src before copying, so a
	// renamed or deleted source file doesn't linger forever in dest.
	if err := pruneExtraneous(srcDir, dest, excludes); err != nil {
		return err
	}
	if err := copyTree(srcDir, dest, excludes); err != nil {
		return err
	}
	h.Ok("Synced: " + dest)
	return nil
}

// matchesExclude reports whether basename matches any of excludes — glob
// patterns matched against the entry's own name, mirroring Python's
// shutil.ignore_patterns (which matches basenames, not full paths).
func matchesExclude(basename string, excludes []string) bool {
	for _, pat := range excludes {
		if ok, _ := filepath.Match(pat, basename); ok {
			return true
		}
	}
	return false
}

// copyTree copies every file and directory under src into dest, skipping
// anything matching excludes.
func copyTree(src, dest string, excludes []string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if matchesExclude(d.Name(), excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(src); err == nil {
		mode = info.Mode()
	}
	return os.WriteFile(dest, data, mode)
}

// pruneExtraneous removes anything under dest that has no counterpart under
// src (and isn't itself excluded) — the local-copy branch's --delete
// equivalent.
func pruneExtraneous(src, dest string, excludes []string) error {
	return filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(dest, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if matchesExclude(d.Name(), excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		srcPath := filepath.Join(src, rel)
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			if d.IsDir() {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				return filepath.SkipDir
			}
			return os.Remove(path)
		}
		return nil
	})
}
