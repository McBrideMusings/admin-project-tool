// fixtures.go is the Go port of admin_lib/fixtures.py: synthesize a dev
// fixture tree of real random-byte files (generate), and restore a mutable
// working copy from that baseline (reset) — pairing with cleandev.go's
// cleanDevFromConfig for the optional cache/DB wipe reset can fold in.
package kinds

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// parseFixtureSize parses a human size ("1.5GB", "500KB", "2048") to bytes —
// the Go port of admin_lib/fixtures.py::parse_size. nil (an absent `size`
// key) is 0 bytes, matching Python's `f.get("size", 0)` default.
func parseFixtureSize(spec any) (int64, error) {
	switch v := spec.(type) {
	case nil:
		return 0, nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case string:
		s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(v), " ", ""))
		if s == "" {
			return 0, fmt.Errorf("fixtures: empty size")
		}
		units := []struct {
			suffix string
			mult   float64
		}{
			{"TB", 1024 * 1024 * 1024 * 1024},
			{"GB", 1024 * 1024 * 1024},
			{"MB", 1024 * 1024},
			{"KB", 1024},
			{"B", 1},
		}
		for _, u := range units {
			if strings.HasSuffix(s, u.suffix) {
				num, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
				if err != nil {
					return 0, fmt.Errorf("fixtures: invalid size %q", v)
				}
				return int64(num * u.mult), nil
			}
		}
		num, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("fixtures: invalid size %q", v)
		}
		return int64(num), nil
	default:
		return 0, fmt.Errorf("fixtures: invalid size %v", v)
	}
}

// fixtureFile is one file generateFixtureTree writes.
type fixtureFile struct {
	Name string
	Size int64
}

// iterFixtureFiles expands one tree entry's `files` list into concrete
// (name, size) pairs — the Go port of admin_lib/fixtures.py::_iter_files. A
// file with count = N expands to N numbered copies, zero-padded to the
// count's own width (minimum 2).
func iterFixtureFiles(entry map[string]any) ([]fixtureFile, error) {
	var out []fixtureFile
	for _, raw := range cfgList(entry, "files") {
		f, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := cfgString(f, "name", "")
		if name == "" {
			return nil, fmt.Errorf("fixtures: file entry missing 'name': %v", f)
		}
		size, err := parseFixtureSize(f["size"])
		if err != nil {
			return nil, err
		}
		count := cfgInt(f, "count", 1)
		if count <= 1 {
			out = append(out, fixtureFile{Name: name, Size: size})
			continue
		}
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		width := len(strconv.Itoa(count))
		if width < 2 {
			width = 2
		}
		for i := 1; i <= count; i++ {
			out = append(out, fixtureFile{
				Name: fmt.Sprintf("%s%0*d%s", stem, width, i, ext),
				Size: size,
			})
		}
	}
	return out, nil
}

// writeRandomFixtureFile writes size bytes of crypto/rand output to path in
// 8MB chunks — the Go port of admin_lib/fixtures.py::_write_random.
// crypto/rand, not math/rand, to match Python's os.urandom: this data feeds
// a media scanner's fixture tree, not a simulation, so there is no reason
// to accept a weaker source.
func writeRandomFixtureFile(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	const chunkSize = 8 * 1024 * 1024
	buf := make([]byte, chunkSize)
	remaining := size
	for remaining > 0 {
		n := int64(chunkSize)
		if remaining < n {
			n = remaining
		}
		if _, err := io.ReadFull(rand.Reader, buf[:n]); err != nil {
			return err
		}
		if _, err := f.Write(buf[:n]); err != nil {
			return err
		}
		remaining -= n
	}
	return nil
}

// generateFixtureTree synthesizes tree into baseline as real files of
// random bytes — the Go port of admin_lib/fixtures.py::generate_tree.
// Refuses to populate a non-empty baseline unless force.
func generateFixtureTree(h *luahost.Host, baseline string, tree []any, force bool) error {
	if baseline == "" {
		return fmt.Errorf("fixtures: no baseline path configured")
	}
	if info, err := os.Stat(baseline); err == nil && info.IsDir() {
		entries, _ := os.ReadDir(baseline)
		if len(entries) > 0 && !force {
			return fmt.Errorf("fixtures: baseline %s is not empty — pass --force to regenerate", baseline)
		}
	}
	if force {
		_ = os.RemoveAll(baseline)
	}
	if err := os.MkdirAll(baseline, 0o755); err != nil {
		return err
	}

	var total int64
	count := 0
	for _, rawEntry := range tree {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			continue
		}
		subdir := cfgString(entry, "dir", "")
		targetDir := baseline
		if subdir != "" {
			targetDir = filepath.Join(baseline, subdir)
		}
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return err
		}
		files, err := iterFixtureFiles(entry)
		if err != nil {
			return err
		}
		for _, file := range files {
			path := filepath.Join(targetDir, file.Name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := writeRandomFixtureFile(path, file.Size); err != nil {
				return err
			}
			total += file.Size
			count++
			rel, err := filepath.Rel(baseline, path)
			if err != nil {
				rel = path
			}
			h.Info("  " + rel + " (" + formatBytes(file.Size) + ")")
		}
	}
	h.Ok(fmt.Sprintf("fixtures: generated %d files (%s) under %s", count, formatBytes(total), baseline))
	return nil
}

// copyFixtureTree deep-copies every file under src into dst, preserving
// directory structure — the piece of a pure-Go `rsync -a` restoreFixtureTree
// needs.
func copyFixtureTree(src, dst string) error {
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
		return copyFixtureFile(path, target, info.Mode())
	})
}

func copyFixtureFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// restoreFixtureTree wipes working entirely then deep-copies baseline into
// it — the Go port of admin_lib/fixtures.py::restore_tree (a pure-Go
// `rsync -a --delete`). Refuses if baseline is missing or empty.
func restoreFixtureTree(h *luahost.Host, baseline, working string) error {
	info, statErr := os.Stat(baseline)
	entries, _ := os.ReadDir(baseline)
	if baseline == "" || statErr != nil || !info.IsDir() || len(entries) == 0 {
		return fmt.Errorf("fixtures: baseline is empty or missing (%s) — run the generate/seed step first", baseline)
	}
	if working == "" {
		return fmt.Errorf("fixtures: no working path configured")
	}
	if _, err := os.Stat(working); err == nil {
		if err := os.RemoveAll(working); err != nil {
			return err
		}
	}
	absWorking, err := filepath.Abs(working)
	if err != nil {
		absWorking = working
	}
	if err := os.MkdirAll(filepath.Dir(absWorking), 0o755); err != nil {
		return err
	}
	if err := copyFixtureTree(baseline, working); err != nil {
		return err
	}
	n := 0
	_ = filepath.Walk(working, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n++
		}
		return nil
	})
	h.Ok(fmt.Sprintf("fixtures: restored %s from baseline (%d files)", working, n))
	return nil
}

// resolveFixtureDir resolves a baseline/working path from the [fixtures]
// table — expand ${VAR}, make absolute — the Go port of
// admin_lib/fixtures.py::_resolve_dir.
func resolveFixtureDir(cfg map[string]any, key string) (string, error) {
	val, err := manifest.ResolveEnv(cfgString(cfg, key, ""))
	if err != nil {
		return "", err
	}
	if val != "" && !filepath.IsAbs(val) {
		if abs, err := filepath.Abs(val); err == nil {
			val = abs
		}
	}
	return val, nil
}

// makeFixturesGenerate is kind="fixtures-generate"'s factory — the Go port
// of admin_lib/interp.py's fixtures-generate wiring
// (fixtures_generate_from_config). `--force` on the command line permits
// regenerating a non-empty baseline.
func makeFixturesGenerate(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	cfg := archetypeConfigTable(m, "fixtures")

	return func(args []string) error {
		h := newHost()
		if len(cfg) == 0 {
			msg := "fixtures: no [fixtures] table configured"
			h.Warn(msg)
			return fmt.Errorf("action %q: %s", name, msg)
		}
		force := false
		for _, a := range args {
			if a == "--force" {
				force = true
			}
		}
		baseline, err := resolveFixtureDir(cfg, "baseline")
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if err := generateFixtureTree(h, baseline, cfgList(cfg, "tree"), force); err != nil {
			h.Err(err.Error())
			return fmt.Errorf("action %q: %w", name, err)
		}
		return nil
	}, nil
}

// makeFixturesReset is kind="fixtures-reset"'s factory — the Go port of
// admin_lib/interp.py's fixtures-reset wiring (fixtures_reset_from_config).
// Optional `clean`/`db` keys fold a project's cache/DB wipe into the same
// reset by handing them to cleanDevFromConfig (cleandev.go) with force=true,
// exactly as Python routes the same two keys into admin_lib.cleanup.
func makeFixturesReset(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	cfg := archetypeConfigTable(m, "fixtures")

	return func(args []string) error {
		h := newHost()
		if len(cfg) == 0 {
			msg := "fixtures: no [fixtures] table configured"
			h.Warn(msg)
			return fmt.Errorf("action %q: %s", name, msg)
		}
		baseline, err := resolveFixtureDir(cfg, "baseline")
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		working, err := resolveFixtureDir(cfg, "working")
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if err := restoreFixtureTree(h, baseline, working); err != nil {
			h.Err(err.Error())
			return fmt.Errorf("action %q: %w", name, err)
		}

		clean := cfgList(cfg, "clean")
		db := cfgMap(cfg, "db")
		if len(clean) > 0 || len(db) > 0 {
			sub := map[string]any{}
			if len(clean) > 0 {
				sub["paths"] = clean
			}
			if len(db) > 0 {
				sub["db"] = db
			}
			cleanDevFromConfig(h, sub, true)
		}
		return nil
	}, nil
}
