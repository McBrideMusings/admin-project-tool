package kinds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestDocsMissingDirIsAnError proves makeDocs refuses to run when
// [docs].dir doesn't exist — the Go port of admin_lib/interp.py::_make_docs'
// `if not os.path.isdir(dirpath)` guard.
func TestDocsMissingDirIsAnError(t *testing.T) {
	m := &manifest.Manifest{Settings: map[string]map[string]any{"docs": {"dir": "does-not-exist"}}}
	runner, err := makeDocs("docs", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makeDocs: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() = nil, want an error for a missing dir")
	}
}

// TestDocsRunsConfiguredScript proves the Runner invokes `<pkg> run
// <script>` inside [docs].dir — the Go port of _make_docs' `pkg + " run " +
// script` command line.
//
// MUTATION EVIDENCE: change the default script from "docs:dev" to
// "docs:build" (i.e. drop the cfg["script"] override) and this test fails —
// the fake npm records "run docs:build", not the "run custom-docs" this
// test asserts on.
func TestDocsRunsConfiguredScript(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	docsDir := filepath.Join(root, "site")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	binDir := t.TempDir()
	fakeNpmScript(t, binDir, `echo "$@" > `+filepath.Join(docsDir, "argv.txt"))

	m := &manifest.Manifest{Settings: map[string]map[string]any{"docs": {"dir": "site", "script": "custom-docs"}}}
	runner, err := makeDocs("docs", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makeDocs: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	data, err := os.ReadFile(filepath.Join(docsDir, "argv.txt"))
	if err != nil {
		t.Fatalf("expected argv.txt from the fake npm: %v", err)
	}
	if got := string(data); got != "run custom-docs\n" {
		t.Errorf("fake npm saw argv %q, want %q", got, "run custom-docs\n")
	}
}

// TestDocsDefaultsToDocsDevScript proves an unset [docs].script defaults to
// "docs:dev" and an unset [docs].dir defaults to ".".
func TestDocsDefaultsToDocsDevScript(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	binDir := t.TempDir()
	fakeNpmScript(t, binDir, `echo "$@" > argv.txt`)

	runner, err := makeDocs("docs", manifest.Action{}, nil, nil)
	if err != nil {
		t.Fatalf("makeDocs: %v", err)
	}
	if err := runner(nil); err != nil {
		t.Fatalf("runner() = %v, want nil", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "argv.txt"))
	if err != nil {
		t.Fatalf("expected argv.txt from the fake npm: %v", err)
	}
	if got := string(data); got != "run docs:dev\n" {
		t.Errorf("fake npm saw argv %q, want %q", got, "run docs:dev\n")
	}
}
