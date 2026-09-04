// Package workusage records and reads back a per-repo, most-recent-first list
// of fully-resolved dispatch invocations from the Work tab — "deploy grafana",
// never the bare command name "deploy" a multi-target dispatch is a no-op
// without its target. It backs the future command palette's MRU strip
// (admin-3rgp.72); this package owns only the on-disk list, not the palette
// UI.
package workusage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// maxEntries caps the stored list. The palette wants the top 5 (Top's n
// parameter), but the cap here is deliberately larger so a recently-bumped
// invocation isn't lost to the cap before it has a chance to resurface.
const maxEntries = 20

// cachePath mirrors internal/data.cachePath's convention exactly (same
// os.UserCacheDir()-with-TempDir-fallback base, same filepathSafe(nwo)+".json"
// naming), but under its own "work-usage" subdirectory rather than data.go's
// "repo" — one JSON file per repo, in its own namespace so the two caches
// never collide or get pruned together.
func cachePath(nwo string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	base := filepath.Join(dir, "work-usage")
	name := filepathSafe(nwo) + ".json"
	return filepath.Join(base, name)
}

// filepathSafe matches internal/data.filepathSafe's convention (that helper is
// unexported there, and one line isn't worth a cross-package dependency —
// rowkeys.go already duplicates commandNamed for the same reason). Keep the
// two in sync by hand if data.go's convention ever changes.
var filepathSafeReplacer = strings.NewReplacer("/", "-", "\\", "-")

func filepathSafe(nwo string) string {
	return filepathSafeReplacer.Replace(nwo)
}

// load reads the stored list for nwo. Any absence or error — no file yet, a
// corrupt file, a permissions problem — reads back as an empty list rather
// than an error: this is a best-effort convenience cache, never a source of
// truth a caller needs to distinguish failure from "nothing recorded yet".
func load(nwo string) []string {
	b, err := os.ReadFile(cachePath(nwo))
	if err != nil {
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return nil
	}
	return list
}

// save writes list via a temp-file-then-rename, matching internal/data.save's
// pattern so a reader never observes a partially-written file. Errors are
// ignored — same best-effort rationale as data.go's save.
func save(nwo string, list []string) {
	path := cachePath(nwo)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), path)
}

// Record adds invocation to nwo's MRU list, moving it to the front if it's
// already present (never a duplicate entry) and capping the list at
// maxEntries (dropping the oldest). Call this at the moment a dispatch
// actually succeeds — never speculatively, and never for a bare command name
// when a target was involved.
func Record(nwo, invocation string) {
	if nwo == "" || invocation == "" {
		return
	}
	list := load(nwo)
	out := make([]string, 0, len(list)+1)
	out = append(out, invocation)
	for _, v := range list {
		if v == invocation {
			continue
		}
		out = append(out, v)
	}
	if len(out) > maxEntries {
		out = out[:maxEntries]
	}
	save(nwo, out)
}

// Top returns the n most recently used invocations for nwo, most-recent-first.
// A fresh repo (no cache file yet) or any read error returns an empty slice,
// never nil-vs-error ambiguity and never a guessed or seeded entry.
func Top(nwo string, n int) []string {
	list := load(nwo)
	if n <= 0 || len(list) == 0 {
		return []string{}
	}
	if len(list) > n {
		list = list[:n]
	}
	return list
}
