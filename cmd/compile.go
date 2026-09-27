package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/compiledinfo"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// sourceDir is this admin binary's own Go module checkout on this machine —
// baked in at install time by install.sh's build_binary via
// -ldflags -X github.com/McBrideMusings/admin-project-tool/cmd.sourceDir=....
// It is empty for
// any binary built without that flag — `go build .`, `go test ./...`,
// `go run .` — which is what makes admin compile fail with a clear message
// instead of silently trying to build from nothing. Tests that need a
// non-empty sourceDir set it directly (same package, no exported setter
// needed).
var sourceDir string

// compileSourceEntries is every top-level path admin compile copies out of
// sourceDir before running `go build` against the copy — exactly what a Go
// build of this module needs (module files, the main package, cmd/,
// internal/) and nothing else: archetypes/, detectors/, docs/, tests/ etc.
// are not needed for the build — archetypes/detectors are already
// merged into the manifest before compile ever runs (internal/archetype's
// SearchPathFor resolves them once, at admin compile time — the compiled
// artifact dispatches off the already-resolved manifest and never looks them
// up again).
var compileSourceEntries = []string{"go.mod", "go.sum", "main.go", "cmd", "internal"}

// compileCmd is admin-tlhw.6. Per the recommendation in
// docs/spikes/thin-binary-compile.md ("Recommendation to admin-tlhw.6"), it
// emits a full copy of this binary — every kind, the dashboard, every tool
// verb — with the target project's resolved manifest embedded
// (internal/compiledinfo), not a kind-trimmed build: the measured savings
// from trimming don't justify running `go build` fresh per manifest, and
// ADR-0006's reason to compile at all (a machine with no admin installed)
// has never been about binary size.
var compileCmd = &cobra.Command{
	Use:   "compile [DIR]",
	Short: "Build a standalone ./admin binary embedding this project's resolved manifest",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCompile(cmd, args)
	},
}

func init() {
	rootCmd.AddCommand(compileCmd)
}

func runCompile(cmd *cobra.Command, args []string) error {
	dir, err := checkTargetDir(args)
	if err != nil {
		return err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}

	// resolveProjectManifestFromDisk, never resolveProjectManifest: admin
	// compile [DIR] always means "resolve DIR's own admin.toml off disk",
	// even in the hypothetical case this binary itself carries an embedded
	// manifest (cmd/manifest.go's resolveProjectManifestFromDisk doc
	// comment) — the general short-circuit every other dispatch uses would
	// silently ignore dir and re-embed this binary's own frozen manifest
	// instead.
	m, ok, err := resolveProjectManifestFromDisk(cmd.Context(), dir)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no %s in %s or any directory above it — nothing to compile", manifest.Filename, dir)
	}

	if sourceDir == "" {
		return fmt.Errorf("this admin binary was not built with its own source directory embedded (it has no sourceDir) — reinstall with install.sh, then retry admin compile")
	}
	if _, statErr := os.Stat(filepath.Join(sourceDir, "go.mod")); statErr != nil {
		return fmt.Errorf("admin compile needs the tool's own Go source at %s, which is missing or unreadable: %w", sourceDir, statErr)
	}

	sourcePath := filepath.Join(m.Root, manifest.Filename)
	builtAt := time.Now().UTC()

	manifestB64, err := compiledinfo.Encode(m)
	if err != nil {
		return fmt.Errorf("encoding %s for embedding: %w", sourcePath, err)
	}

	target := filepath.Join(dir, "admin")
	if err := buildStandalone(cmd.Context(), sourceDir, target, manifestB64, sourcePath, builtAt); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s (standalone, built from %s at %s)\n", target, sourcePath, builtAt.Format(time.RFC3339))
	return nil
}

// buildStandalone copies sourceDir's Go module into a temp directory,
// overwrites that copy's internal/compiledinfo/generated.go with the
// resolved manifest plus its provenance, `go build`s the copy, and moves the
// resulting binary to target — never touching sourceDir itself, and never
// leaving a half-written target.
func buildStandalone(ctx context.Context, srcDir, target, manifestB64, sourcePath string, builtAt time.Time) error {
	buildDir, err := os.MkdirTemp("", "admin-compile-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(buildDir)

	if err := copyModuleSource(srcDir, buildDir); err != nil {
		return fmt.Errorf("copying %s into a build directory: %w", srcDir, err)
	}

	generated := compiledinfo.GeneratedSource(manifestB64, sourcePath, builtAt.Format(time.RFC3339))
	genPath := filepath.Join(buildDir, "internal", "compiledinfo", "generated.go")
	if err := os.WriteFile(genPath, []byte(generated), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", genPath, err)
	}

	tmpBinary := filepath.Join(buildDir, ".admin-compiled")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", tmpBinary, ".")
	buildCmd.Dir = buildDir
	buildCmd.Env = os.Environ()
	out, buildErr := buildCmd.CombinedOutput()
	if buildErr != nil {
		return fmt.Errorf("go build failed:\n%s", out)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	// Write to a temp file in the target's own directory first, then rename
	// into place, so a failed compile never leaves a half-written ./admin —
	// os.Rename is atomic within one filesystem, which a same-directory temp
	// file guarantees (a cross-device rename would not be).
	tmpTarget := target + ".compiling"
	if err := copyFile(tmpBinary, tmpTarget, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpTarget, target); err != nil {
		os.Remove(tmpTarget)
		return err
	}
	return nil
}

// copyModuleSource copies each of compileSourceEntries from srcDir to
// dstDir, preserving relative structure.
func copyModuleSource(srcDir, dstDir string) error {
	for _, entry := range compileSourceEntries {
		src := filepath.Join(srcDir, entry)
		dst := filepath.Join(dstDir, entry)
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}
		if info.IsDir() {
			if err := copyDir(src, dst); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(src, dst, info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// runCompiledInfo backs the --compiled-info flag (root.go): a running admin
// binary's own answer to ADR-0006's frozen-manifest hazard — "a compiled
// artifact silently runs an old manifest" — by reporting, in the artifact
// itself, which admin.toml it was built from and when, rather than leaving
// that as a rule a human has to remember.
func runCompiledInfo(cmd *cobra.Command) error {
	if !compiledinfo.Embedded() {
		fmt.Fprintln(cmd.OutOrStdout(), "not a compiled artifact — this is the installed admin binary, resolving admin.toml from disk")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "compiled from %s at %s\n", compiledinfo.SourcePath, compiledinfo.BuiltAt)
	return nil
}
