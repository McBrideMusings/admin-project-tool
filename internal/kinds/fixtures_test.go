package kinds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestParseFixtureSizeParsesUnits proves parseFixtureSize matches
// admin_lib/fixtures.py::parse_size's 1024-based unit parsing.
//
// MUTATION EVIDENCE: change the GB multiplier from 1024^3 to 1000^3 and
// this test fails — "1GB" would parse to 1000000000 instead of 1073741824.
func TestParseFixtureSizeParsesUnits(t *testing.T) {
	cases := map[string]int64{
		"0":     0,
		"2048":  2048,
		"500KB": 500 * 1024,
		"1.5GB": int64(1.5 * 1024 * 1024 * 1024),
		"1GB":   1024 * 1024 * 1024,
	}
	for spec, want := range cases {
		got, err := parseFixtureSize(spec)
		if err != nil {
			t.Errorf("parseFixtureSize(%q) error: %v", spec, err)
			continue
		}
		if got != want {
			t.Errorf("parseFixtureSize(%q) = %d, want %d", spec, got, want)
		}
	}
}

// TestIterFixtureFilesExpandsCount proves the count expansion — the Go port
// of admin_lib/fixtures.py::_iter_files' numbered-copy behaviour, zero-padded
// to at least 2 digits.
//
// MUTATION EVIDENCE: change the zero-pad width formula from `len(strconv.Itoa(count))`
// (with the `if width < 2 { width = 2 }` floor) to a fixed width of 1, and
// this test fails — "track1.flac".."track3.flac" would come back instead of
// the zero-padded "track01.flac".."track03.flac".
func TestIterFixtureFilesExpandsCount(t *testing.T) {
	entry := map[string]any{
		"files": []any{
			map[string]any{"name": "track.flac", "size": "10B", "count": int64(3)},
		},
	}
	files, err := iterFixtureFiles(entry)
	if err != nil {
		t.Fatalf("iterFixtureFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("iterFixtureFiles returned %d files, want 3", len(files))
	}
	want := []string{"track01.flac", "track02.flac", "track03.flac"}
	for i, f := range files {
		if f.Name != want[i] {
			t.Errorf("files[%d].Name = %q, want %q", i, f.Name, want[i])
		}
		if f.Size != 10 {
			t.Errorf("files[%d].Size = %d, want 10", i, f.Size)
		}
	}
}

// TestMakeFixturesGenerateThenResetRoundTrips is the end-to-end acceptance
// test for both kinds together: generate writes real files with the
// declared sizes under baseline, and reset restores a working copy that
// matches — the Go port of admin_lib/fixtures.py's generate_tree +
// restore_tree pairing.
func TestMakeFixturesGenerateThenResetRoundTrips(t *testing.T) {
	root := t.TempDir()
	baseline := filepath.Join(root, "data", "baseline")
	working := filepath.Join(root, "data", "working")

	m := &manifest.Manifest{Fixtures: map[string]any{
		"baseline": baseline,
		"working":  working,
		"tree": []any{
			map[string]any{
				"dir": "music",
				"files": []any{
					map[string]any{"name": "track.flac", "size": "100B"},
				},
			},
		},
	}}

	genRunner, err := makeFixturesGenerate("fixtures-generate", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makeFixturesGenerate: %v", err)
	}
	if err := genRunner(nil); err != nil {
		t.Fatalf("generate runner() = %v, want nil", err)
	}
	trackPath := filepath.Join(baseline, "music", "track.flac")
	info, err := os.Stat(trackPath)
	if err != nil {
		t.Fatalf("generated file missing: %v", err)
	}
	if info.Size() != 100 {
		t.Errorf("generated file size = %d, want 100", info.Size())
	}

	// Mutate the working tree the way a test session would (delete a file),
	// then reset must restore it from baseline.
	if err := os.MkdirAll(working, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(working, "stray.txt"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	resetRunner, err := makeFixturesReset("fixtures-reset", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makeFixturesReset: %v", err)
	}
	if err := resetRunner(nil); err != nil {
		t.Fatalf("reset runner() = %v, want nil", err)
	}

	if _, err := os.Stat(filepath.Join(working, "music", "track.flac")); err != nil {
		t.Errorf("reset did not restore music/track.flac: %v", err)
	}
	if _, err := os.Stat(filepath.Join(working, "stray.txt")); !os.IsNotExist(err) {
		t.Error("reset should have deleted stray.txt (not present in baseline)")
	}
}

// TestMakeFixturesGenerateRefusesNonEmptyBaselineWithoutForce proves the
// safety guard: re-running generate against a populated baseline without
// --force must fail, matching admin_lib/fixtures.py::generate_tree.
//
// MUTATION EVIDENCE: change the `len(entries) > 0 && !force` guard to
// `false` (never refuse) and this test fails — the second generate call
// would silently wipe and rewrite the curated baseline.
func TestMakeFixturesGenerateRefusesNonEmptyBaselineWithoutForce(t *testing.T) {
	root := t.TempDir()
	baseline := filepath.Join(root, "baseline")
	m := &manifest.Manifest{Fixtures: map[string]any{
		"baseline": baseline,
		"tree": []any{
			map[string]any{"files": []any{map[string]any{"name": "a.bin", "size": "1B"}}},
		},
	}}
	runner, err := makeFixturesGenerate("fixtures-generate", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makeFixturesGenerate: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Error("second generate (no --force) = nil, want an error for a non-empty baseline")
	}
	if err := runner([]string{"--force"}); err != nil {
		t.Errorf("generate with --force = %v, want nil", err)
	}
}
