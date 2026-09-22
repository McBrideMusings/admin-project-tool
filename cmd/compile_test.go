package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/compiledinfo"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// repoRootForTest returns this checkout's module root — the same directory
// admin compile needs as sourceDir. Go testing runs a package's tests with
// the package directory as the working directory, so ".." from cmd/ is the
// repo root regardless of how `go test` itself was invoked.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("expected %s to be the module root (go.mod missing): %v", dir, err)
	}
	return dir
}

// withSourceDir points the package-level sourceDir var (normally baked in
// by install.sh's -ldflags -X) at dir for the duration of one test.
func withSourceDir(t *testing.T, dir string) {
	t.Helper()
	orig := sourceDir
	sourceDir = dir
	t.Cleanup(func() { sourceDir = orig })
}

// TestResolveProjectManifestUsesEmbeddedManifestWhenPresent is the
// standalone half of the acceptance criteria: once compiledinfo carries a
// manifest, resolveProjectManifest returns it regardless of dir or whether
// an admin.toml exists on disk there at all — the same short-circuit a
// compiled artifact relies on to run with nothing beside it.
func TestResolveProjectManifestUsesEmbeddedManifestWhenPresent(t *testing.T) {
	m := &manifest.Manifest{
		Root:        "/wherever/it/was/compiled",
		ProjectName: "embedded-project",
		Overrides: map[string]manifest.Command{
			"greet": {Desc: "say hi", Steps: []string{"greet"}},
		},
		Actions: map[string]manifest.Action{
			"greet": {"kind": "shell", "run": "echo hi"},
		},
	}
	encoded, err := compiledinfo.Encode(m)
	if err != nil {
		t.Fatalf("compiledinfo.Encode = %v, want nil", err)
	}

	origB64, origSrc, origBuilt := compiledinfo.ManifestB64, compiledinfo.SourcePath, compiledinfo.BuiltAt
	compiledinfo.ManifestB64 = encoded
	compiledinfo.SourcePath = "/original/admin.toml"
	compiledinfo.BuiltAt = "2026-01-01T00:00:00Z"
	t.Cleanup(func() {
		compiledinfo.ManifestB64, compiledinfo.SourcePath, compiledinfo.BuiltAt = origB64, origSrc, origBuilt
	})

	// An empty temp dir with no admin.toml at all — resolveProjectManifest
	// must not even look.
	dir := t.TempDir()
	got, ok, err := resolveProjectManifest(context.Background(), dir)
	if err != nil {
		t.Fatalf("resolveProjectManifest = %v, want nil", err)
	}
	if !ok {
		t.Fatal("resolveProjectManifest ok=false with an embedded manifest present, want true")
	}
	if got.ProjectName != "embedded-project" {
		t.Fatalf("ProjectName = %q, want %q", got.ProjectName, "embedded-project")
	}
	if _, ok := got.Overrides["greet"]; !ok {
		t.Fatal("embedded manifest's greet command did not survive resolveProjectManifest")
	}
	// Root must be rewritten to the running test binary's own directory
	// (os.Args[0]'s), never the path the manifest was compiled with.
	if got.Root == "/wherever/it/was/compiled" {
		t.Fatalf("Root = %q, want it rewritten away from the compile-time path", got.Root)
	}
}

// TestCompileEmitsStandaloneArtifactThatRunsWithNoManifestBeside proves the
// first two acceptance criteria end to end: `admin compile [DIR]` (via
// runCompile, sourceDir pointed at this checkout) produces a working ./admin
// binary, and running that binary from an unrelated cwd — with no
// admin.toml, no ~/.admin, nothing beside it but the artifact itself —
// executes the command it was compiled with. It also drives the third
// criterion: the artifact's --compiled-info output names the source
// manifest and a build timestamp.
func TestCompileEmitsStandaloneArtifactThatRunsWithNoManifestBeside(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a full admin binary via `go build`; skipped with -short")
	}
	withSourceDir(t, repoRootForTest(t))

	projectDir := t.TempDir()
	toml := `project_name = "compiletest"

[commands.custom.greet]
desc = "say hi"
steps = ["greet"]

[actions.greet]
kind = "shell"
run = "echo hello-from-compiled-admin"
`
	manifestPath := filepath.Join(projectDir, "admin.toml")
	if err := os.WriteFile(manifestPath, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	m, ok, err := resolveProjectManifest(context.Background(), projectDir)
	if err != nil || !ok {
		t.Fatalf("resolveProjectManifest = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	manifestB64, err := compiledinfo.Encode(m)
	if err != nil {
		t.Fatalf("compiledinfo.Encode = %v", err)
	}

	// Place the artifact somewhere entirely unrelated to projectDir, so
	// running it proves the manifest travels inside the binary rather than
	// being re-read off projectDir/admin.toml.
	runDir := t.TempDir()
	target := filepath.Join(runDir, "admin")
	builtAt, err := time.Parse(time.RFC3339, "2026-09-22T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if err := buildStandalone(context.Background(), sourceDir, target, manifestB64, manifestPath, builtAt); err != nil {
		t.Fatalf("buildStandalone = %v", err)
	}

	if info, err := os.Stat(target); err != nil {
		t.Fatalf("compiled artifact missing at %s: %v", target, err)
	} else if info.Mode()&0o111 == 0 {
		t.Fatalf("compiled artifact at %s is not executable: mode %v", target, info.Mode())
	}

	// Run it from runDir (no admin.toml here) with cwd somewhere else
	// entirely, to prove it doesn't matter where the process happens to
	// start — the manifest is inside the binary.
	elsewhere := t.TempDir()
	cmd := exec.Command(target, "greet")
	cmd.Dir = elsewhere
	cmd.Env = []string{"PATH=/usr/bin:/bin"} // no ADMIN_* env, no HOME dependence on ~/.admin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("running the compiled artifact failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("hello-from-compiled-admin")) {
		t.Fatalf("compiled artifact's greet command did not run the embedded action; stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}

	// --compiled-info: the frozen-manifest hazard made visible in the
	// artifact itself.
	infoCmd := exec.Command(target, "--compiled-info")
	infoCmd.Dir = elsewhere
	var infoOut bytes.Buffer
	infoCmd.Stdout = &infoOut
	if err := infoCmd.Run(); err != nil {
		t.Fatalf("--compiled-info failed: %v", err)
	}
	got := infoOut.String()
	if !bytes.Contains([]byte(got), []byte(manifestPath)) {
		t.Fatalf("--compiled-info output %q does not name the source manifest %q", got, manifestPath)
	}
	if !bytes.Contains([]byte(got), []byte("2026-09-22T12:00:00Z")) {
		t.Fatalf("--compiled-info output %q does not name the build time", got)
	}
}

// TestRunCompileEndToEnd drives runCompile itself (the actual `admin
// compile [DIR]` entry point cmd/root.go dispatches to), rather than the
// buildStandalone helper directly, and checks the artifact it writes at
// <dir>/admin runs standalone the same way.
func TestRunCompileEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a full admin binary via `go build`; skipped with -short")
	}
	withSourceDir(t, repoRootForTest(t))

	projectDir := t.TempDir()
	toml := `project_name = "runcompiletest"

[commands.custom.greet]
desc = "say hi"
steps = ["greet"]

[actions.greet]
kind = "shell"
run = "echo hello-from-run-compile"
`
	if err := os.WriteFile(filepath.Join(projectDir, "admin.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	testCmd := &cobra.Command{}
	testCmd.SetContext(context.Background())
	if err := runCompile(testCmd, []string{projectDir}); err != nil {
		t.Fatalf("runCompile = %v, want nil", err)
	}

	target := filepath.Join(projectDir, "admin")
	elsewhere := t.TempDir()
	cmd := exec.Command(target, "greet")
	cmd.Dir = elsewhere
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("running the artifact runCompile wrote failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("hello-from-run-compile")) {
		t.Fatalf("artifact did not run the embedded action; stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

// TestRunCompileWithoutSourceDirFailsClearly proves admin compile reports a
// clear, actionable error — not a `go build` failure two layers down — when
// the binary running it wasn't built with sourceDir embedded (a plain
// `go build .`/`go test`/`go run .`, never install.sh's build_binary).
func TestRunCompileWithoutSourceDirFailsClearly(t *testing.T) {
	withSourceDir(t, "")

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "admin.toml"), []byte(`project_name = "x"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	testCmd := &cobra.Command{}
	testCmd.SetContext(context.Background())
	err := runCompile(testCmd, []string{projectDir})
	if err == nil {
		t.Fatal("runCompile with no sourceDir = nil, want an error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("source")) {
		t.Fatalf("error %q does not explain the missing source directory", err.Error())
	}
}
