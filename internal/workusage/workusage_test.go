package workusage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempCacheDir points os.UserCacheDir() at a fresh temp directory for the
// life of the test, by overriding $HOME (darwin resolves UserCacheDir to
// $HOME/Library/Caches) — same seam internal/data's tests would use for the
// same underlying os.UserCacheDir() call.
func withTempCacheDir(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
}

func TestFreshRepoEmpty(t *testing.T) {
	withTempCacheDir(t)

	got := Top("example/fresh-repo", 5)
	if got == nil {
		t.Fatalf("Top on a fresh repo returned nil, want an empty (non-nil-checked) slice")
	}
	if len(got) != 0 {
		t.Fatalf("Top on a fresh repo = %v, want empty", got)
	}
}

func TestRecordDedupMovesToFront(t *testing.T) {
	withTempCacheDir(t)
	nwo := "example/dedup-repo"

	Record(nwo, "deploy grafana")
	Record(nwo, "deploy prometheus")
	Record(nwo, "deploy grafana") // repeat — should move to front, not duplicate

	got := Top(nwo, 10)
	want := []string{"deploy grafana", "deploy prometheus"}
	if len(got) != len(want) {
		t.Fatalf("Top = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Top = %v, want %v", got, want)
		}
	}
}

func TestRecordCapsEntryCount(t *testing.T) {
	withTempCacheDir(t)
	nwo := "example/cap-repo"

	for i := 0; i < maxEntries+10; i++ {
		Record(nwo, fmt.Sprintf("cmd-%d", i))
	}

	got := Top(nwo, maxEntries+10)
	if len(got) != maxEntries {
		t.Fatalf("stored list len = %d, want cap of %d", len(got), maxEntries)
	}
}

func TestTopRespectsN(t *testing.T) {
	withTempCacheDir(t)
	nwo := "example/top-n-repo"

	Record(nwo, "a")
	Record(nwo, "b")
	Record(nwo, "c")

	got := Top(nwo, 2)
	want := []string{"c", "b"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Top(nwo, 2) = %v, want %v", got, want)
	}
}

func TestStoredJSONIsStableAndReadable(t *testing.T) {
	withTempCacheDir(t)
	nwo := "example/format-repo"

	Record(nwo, "deploy grafana")
	Record(nwo, "test")

	dir, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir: %v", err)
	}
	path := filepath.Join(dir, "work-usage", filepathSafe(nwo)+".json")

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading cache file at %s: %v", path, err)
	}

	// Human-readable: indented, one string per line.
	text := string(b)
	if !strings.Contains(text, "\n  \"") {
		t.Fatalf("cache file is not indented JSON:\n%s", text)
	}

	// Round-trips to exactly what Record wrote.
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatalf("cache file is not valid JSON: %v\n%s", err, text)
	}
	want := []string{"test", "deploy grafana"}
	if len(list) != len(want) || list[0] != want[0] || list[1] != want[1] {
		t.Fatalf("round-tripped list = %v, want %v", list, want)
	}
}

func TestRecordIgnoresEmptyInputs(t *testing.T) {
	withTempCacheDir(t)

	Record("", "deploy grafana")
	Record("example/empty-input-repo", "")

	if got := Top("example/empty-input-repo", 5); len(got) != 0 {
		t.Fatalf("Top after empty-input Records = %v, want empty", got)
	}
}
