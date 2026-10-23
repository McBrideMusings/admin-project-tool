package macapp

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type nopLog struct{}

func (nopLog) Info(string) {}
func (nopLog) Warn(string) {}

func TestParsePidsMatchesOnlyTheBundle(t *testing.T) {
	ps := "  101 /Applications/Foo.app/Contents/MacOS/Foo\n" +
		"  102 /Applications/Foo Bar.app/Contents/MacOS/Foo Bar\n" +
		"  103 /Applications/Foo.app.previous/Contents/MacOS/Foo\n" +
		"  104 /usr/bin/Foo\n"
	got := parsePids(ps, "/Applications/Foo.app")
	if !reflect.DeepEqual(got, []int{101}) {
		t.Errorf("parsePids = %v, want [101]", got)
	}
}

func TestResolveRejectsAmbiguousGlob(t *testing.T) {
	dir := t.TempDir()
	for _, arch := range []string{"arm64", "x64"} {
		if err := os.MkdirAll(filepath.Join(dir, "App-darwin-"+arch, "App.app"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Resolve(filepath.Join(dir, "App-darwin-*", "App.app"))
	if err == nil || !strings.Contains(err.Error(), "matched several bundles") {
		t.Fatalf("Resolve on two matches: want a 'matched several bundles' error, got %v", err)
	}
}

func TestResolveGlobToOneBundle(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "App-darwin-arm64", "App.app")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(filepath.Join(dir, "App-darwin-*", "App.app"))
	if err != nil || got != want {
		t.Fatalf("Resolve = %q, %v; want %q", got, err, want)
	}
}

// TestInstallReplacesExistingBundle drives the real ditto + rename swap.
func TestInstallReplacesExistingBundle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("ditto is macOS-only")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "build", "App.app")
	dest := filepath.Join(dir, "Applications", "App.app")
	for path, body := range map[string]string{
		filepath.Join(src, "Contents", "new.txt"):  "new",
		filepath.Join(dest, "Contents", "old.txt"): "old",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Install(src, dest, nopLog{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Contents", "new.txt")); err != nil {
		t.Errorf("new bundle not in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Contents", "old.txt")); !os.IsNotExist(err) {
		t.Errorf("old bundle's file survived the swap: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "App.app" {
		t.Errorf("the destination folder holds more than the installed app: %v", entries)
	}
}
