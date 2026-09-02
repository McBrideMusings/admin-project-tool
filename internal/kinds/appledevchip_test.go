package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chdir puts the test in a temp dir, the way the interpreter cd's to the
// project root before dispatch — dev_chip_dirs entries are project-relative.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return dir
}

func TestMaterializeDevChipNoDirsIsNoop(t *testing.T) {
	dir := chdirTemp(t)
	if err := materializeDevChip(map[string]any{}, newHost()); err != nil {
		t.Fatalf("no dev_chip_dirs should be a no-op, got %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("wrote %d entries without dev_chip_dirs", len(entries))
	}
}

func TestMaterializeDevChipWritesBothFiles(t *testing.T) {
	dir := chdirTemp(t)
	if err := os.Mkdir(filepath.Join(dir, "App"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"dev_chip_dirs": []any{"App"}}
	if err := materializeDevChip(cfg, newHost()); err != nil {
		t.Fatal(err)
	}
	for name, want := range devChipFiles {
		got, err := os.ReadFile(filepath.Join(dir, "App", name))
		if err != nil {
			t.Fatalf("%s not written: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s content differs from the embedded template", name)
		}
	}
}

// The whole point: deleting or editing the chip is undone by the next build.
func TestMaterializeDevChipRepairsDrift(t *testing.T) {
	dir := chdirTemp(t)
	if err := os.Mkdir(filepath.Join(dir, "App"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"dev_chip_dirs": []any{"App"}}
	if err := materializeDevChip(cfg, newHost()); err != nil {
		t.Fatal(err)
	}
	swift := filepath.Join(dir, "App", "AdminDevChip.swift")
	boot := filepath.Join(dir, "App", "AdminDevChipBootstrap.m")
	if err := os.Remove(swift); err != nil { // a refactor deletes it
		t.Fatal(err)
	}
	if err := os.WriteFile(boot, []byte("// gutted\n"), 0o644); err != nil { // or edits it
		t.Fatal(err)
	}
	if err := materializeDevChip(cfg, newHost()); err != nil {
		t.Fatal(err)
	}
	for name, want := range devChipFiles {
		got, err := os.ReadFile(filepath.Join(dir, "App", name))
		if err != nil || string(got) != want {
			t.Fatalf("%s not repaired (err=%v)", name, err)
		}
	}
}

// A named directory that isn't there means the chip would be silently absent
// from the build — the exact failure the chip exists to prevent.
func TestMaterializeDevChipMissingDirIsAnError(t *testing.T) {
	chdirTemp(t)
	cfg := map[string]any{"dev_chip_dirs": []any{"Nope"}}
	err := materializeDevChip(cfg, newHost())
	if err == nil {
		t.Fatal("a missing dev_chip_dirs entry must be an error, not a skip")
	}
	if !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("error should name the bad directory, got %q", err)
	}
}

// The templates are what the app actually compiles, so the properties that
// make the chip trustworthy are asserted on the bytes that ship.
func TestDevChipTemplateProperties(t *testing.T) {
	swift := devChipFiles["AdminDevChip.swift"]
	for _, want := range []string{
		`hasSuffix(".dev")`,     // dev/prod gate is the runtime bundle ID
		`contains(".dev.")`,     // per-worker builds are dev builds too
		`"DEV·\(tail)"`,         // the suffix, not just the bare word
		`guard isDevBuild else`, // production draws nothing
	} {
		if !strings.Contains(swift, want) {
			t.Errorf("AdminDevChip.swift no longer contains %q", want)
		}
	}
	// Never intercepts input, on either platform.
	if strings.Count(swift, "func hitTest") != 2 {
		t.Error("AdminDevChip.swift must decline hit-testing on both platforms")
	}
	boot := devChipFiles["AdminDevChipBootstrap.m"]
	if !strings.Contains(boot, "+ (void)load") {
		t.Error("the bootstrap must run from +load — that is what makes the call site unnecessary")
	}
	if !strings.Contains(boot, `NSClassFromString(@"AdminDevChip")`) {
		t.Error("the bootstrap must reach the chip by name, so it needs no header")
	}
}
