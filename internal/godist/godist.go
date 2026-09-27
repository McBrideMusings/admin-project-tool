// Package godist cross-compiles a Go binary for an os/arch matrix, with an
// optional git-revision/dirty ldflags stamp and a VERSION file derived from
// running the binary's own version flag.
//
// Generic: any Go project configures source_dir/out_dir/binary/targets via a
// [go_dist] table. No project-specific paths live here.
package godist

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultTargets is the os/arch matrix used when [go_dist] declares none.
var DefaultTargets = []string{"darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64"}

// Config is the Go port of the `[go_dist]` cfg dict.
type Config struct {
	SourceDir    string   // Go module dir, default ".".
	OutDir       string   // default "dist".
	Binary       string   // required — output filename prefix.
	Targets      []string // "os/arch" pairs, default DefaultTargets.
	Stamp        bool     // -s -w -X main.buildRevision/buildDirty.
	VersionFlag  string   // e.g. "-version" — probed to write out_dir/VERSION.
	ExtraLdflags string
	Trimpath     bool
	CgoEnabled   string // default "0".
}

// Reporter is the subset of the host's output surface this package needs, so
// it stays free of an internal/luahost import (internal/kinds depends on
// luahost; luahost would have to depend back to bind this).
type Reporter interface {
	Info(msg string)
	Ok(msg string)
	Err(msg string)
}

func (c Config) sourceDir() string {
	if c.SourceDir == "" {
		return "."
	}
	return c.SourceDir
}

func (c Config) outDir() string {
	if c.OutDir == "" {
		return "dist"
	}
	return c.OutDir
}

func (c Config) cgoEnabled() string {
	if c.CgoEnabled == "" {
		return "0"
	}
	return c.CgoEnabled
}

func (c Config) targets() []string {
	if len(c.Targets) == 0 {
		return DefaultTargets
	}
	return c.Targets
}

func gitShortRev(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitDirty(dir string) bool {
	cmd := exec.Command("git", "diff", "--quiet", "HEAD")
	cmd.Dir = dir
	return cmd.Run() != nil
}

// Build cross-compiles cfg.Binary for every target, writing
// <out_dir>/<binary>-<os>-<arch>, plus <out_dir>/VERSION when VersionFlag is
// set. Returns the number of binaries built.
func Build(cfg Config, r Reporter) (int, error) {
	if cfg.Binary == "" {
		return 0, fmt.Errorf("go_dist.binary not set")
	}
	if err := os.MkdirAll(cfg.outDir(), 0o755); err != nil {
		return 0, err
	}
	absOut, err := filepath.Abs(cfg.outDir())
	if err != nil {
		return 0, err
	}

	var parts []string
	if cfg.Stamp {
		dirty := ""
		if gitDirty(cfg.sourceDir()) {
			dirty = "true"
		}
		parts = append(parts, "-s", "-w",
			"-X", "main.buildRevision="+gitShortRev(cfg.sourceDir()),
			"-X", "main.buildDirty="+dirty)
	}
	if cfg.ExtraLdflags != "" {
		parts = append(parts, cfg.ExtraLdflags)
	}
	ldflagsArg := ""
	if len(parts) > 0 {
		ldflagsArg = "-ldflags " + shQuote(strings.Join(parts, " "))
	}

	if cfg.VersionFlag != "" {
		probe := exec.Command("/bin/sh", "-c", "go run "+ldflagsArg+" . "+cfg.VersionFlag)
		probe.Dir = cfg.sourceDir()
		var stderr strings.Builder
		probe.Stderr = &stderr
		out, err := probe.Output()
		if err != nil {
			return 0, fmt.Errorf("version probe failed: %s", strings.TrimSpace(stderr.String()))
		}
		version := strings.TrimSpace(string(out))
		if err := os.WriteFile(filepath.Join(absOut, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
			return 0, err
		}
		r.Info("building " + cfg.Binary + " " + version + " -> " + cfg.outDir())
	}

	trimArg := ""
	if cfg.Trimpath {
		trimArg = "-trimpath "
	}

	count := 0
	for _, target := range cfg.targets() {
		osName, arch, ok := strings.Cut(target, "/")
		if !ok {
			return count, fmt.Errorf("go_dist.targets: %q is not os/arch", target)
		}
		binPath := filepath.Join(absOut, cfg.Binary+"-"+osName+"-"+arch)
		r.Info("  " + osName + "/" + arch + " -> " + filepath.Base(binPath))

		build := exec.Command("/bin/sh", "-c",
			"GOOS="+osName+" GOARCH="+arch+" CGO_ENABLED="+cfg.cgoEnabled()+" "+
				"go build "+trimArg+ldflagsArg+" -o "+shQuote(binPath)+" .")
		build.Dir = cfg.sourceDir()
		build.Stdout = os.Stdout
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			return count, fmt.Errorf("build failed for %s/%s: %w", osName, arch, err)
		}
		count++
	}

	suffix := ""
	if cfg.VersionFlag != "" {
		suffix = " + VERSION"
	}
	r.Ok(fmt.Sprintf("done — %d binaries%s", count, suffix))
	return count, nil
}

// Stage copies a Build()-produced matrix (binary-* plus VERSION) into
// destDir, clearing stale copies first. For an app that bundles the
// binaries as a resource (an iOS folder reference, an Electron
// server-dist). Fails loudly when the dist is missing rather than shipping
// a bundle with nothing in it.
func Stage(cfg Config, destDir string, r Reporter) (int, error) {
	if cfg.Binary == "" {
		return 0, fmt.Errorf("go_dist.binary not set")
	}
	versionPath := filepath.Join(cfg.outDir(), "VERSION")
	if fi, err := os.Stat(versionPath); err != nil || fi.IsDir() {
		return 0, fmt.Errorf("no %s — run: admin build dist", versionPath)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return 0, err
	}

	stale, _ := filepath.Glob(filepath.Join(destDir, cfg.Binary+"-*"))
	for _, p := range stale {
		_ = os.Remove(p)
	}
	_ = os.Remove(filepath.Join(destDir, "VERSION"))

	srcs, _ := filepath.Glob(filepath.Join(cfg.outDir(), cfg.Binary+"-*"))
	staged := 0
	for _, src := range srcs {
		if err := copyPreservingMode(src, filepath.Join(destDir, filepath.Base(src))); err != nil {
			return staged, err
		}
		staged++
	}
	if err := copyPreservingMode(versionPath, filepath.Join(destDir, "VERSION")); err != nil {
		return staged, err
	}

	r.Ok(fmt.Sprintf("staged %d %s binaries + VERSION -> %s", staged, cfg.Binary, destDir))
	return staged, nil
}

func copyPreservingMode(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, fi.Mode())
}

func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, "\t\n '\"$&*()[]{}|;<>?!~`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
