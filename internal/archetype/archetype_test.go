package archetype

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadArchetypeDirectory(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "hugo", `
name = "hugo"

[commands.build]
desc  = "Build site"
steps = ["hugo-build"]

[actions.hugo-build]
kind = "shell"
run  = "hugo --minify"
`, false)

	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}
	arch, err := Load(sp, "hugo")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if arch.Name != "hugo" {
		t.Errorf("Name = %q, want %q (directory name is canonical)", arch.Name, "hugo")
	}
	if arch.Source != root {
		t.Errorf("Source = %q, want %q", arch.Source, root)
	}
	if arch.Scripts != "" {
		t.Errorf("Scripts = %q, want empty (no scripts/ dir present)", arch.Scripts)
	}
	if arch.Partial.ProjectName != "" {
		t.Errorf("Partial.ProjectName = %q, want empty for a partial manifest", arch.Partial.ProjectName)
	}
	cmd, ok := arch.Partial.Overrides["build"]
	if !ok || cmd.Desc != "Build site" {
		t.Errorf("Partial.Overrides[build] = %+v, ok=%v", cmd, ok)
	}
	if _, ok := arch.Partial.Actions["hugo-build"]; !ok {
		t.Errorf("Partial.Actions = %v, want hugo-build", arch.Partial.Actions)
	}
	for _, u := range arch.Partial.Unknown {
		if u == "name" {
			t.Error("Unknown contains 'name' — archetype self-naming must not land in Unknown")
		}
	}
}

func TestScriptsDirectoryIsLocatedNotExecuted(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "docker", "[commands.build]\ndesc = \"x\"\nsteps = [\"y\"]\n", true)
	// A non-executable placeholder in scripts/ — LoadFrom must never attempt
	// to run it, only report its containing directory's path.
	scriptPath := filepath.Join(root, "docker", ScriptsDir, "dev-run.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 1\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}
	arch, err := Load(sp, "docker")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join("docker", ScriptsDir)
	if arch.Scripts != want {
		t.Errorf("Scripts = %q, want %q", arch.Scripts, want)
	}
}

func TestMissingArchetypeTomlErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}
	_, err := Load(sp, "empty")
	if err == nil {
		t.Fatal("Load: want error for a directory with no archetype.toml")
	}
	if !strings.Contains(err.Error(), Filename) {
		t.Errorf("error = %q, want it to name %q", err.Error(), Filename)
	}
	if !strings.Contains(err.Error(), filepath.Join("empty", Filename)) {
		t.Errorf("error = %q, want it to name the missing path", err.Error())
	}
}

func TestLoadUnknownArchetypeNamesSearchedSlots(t *testing.T) {
	root := t.TempDir()
	sp := SearchPath{{Label: "slot-a", FS: os.DirFS(root)}, {Label: "slot-b", FS: os.DirFS(root)}}
	_, err := Load(sp, "nope")
	if err == nil {
		t.Fatal("Load: want error for unknown archetype")
	}
	if !strings.Contains(err.Error(), "slot-a") || !strings.Contains(err.Error(), "slot-b") {
		t.Errorf("error = %q, want it to list every slot searched", err.Error())
	}
}
