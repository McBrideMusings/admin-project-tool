package godist

import (
	"os"
	"path/filepath"
	"testing"
)

type recorder struct{ lines []string }

func (r *recorder) Info(m string) { r.lines = append(r.lines, m) }
func (r *recorder) Ok(m string)   { r.lines = append(r.lines, m) }
func (r *recorder) Err(m string)  { r.lines = append(r.lines, m) }

func TestBuildNeedsABinaryName(t *testing.T) {
	if _, err := Build(Config{}, &recorder{}); err == nil {
		t.Fatal("expected go_dist.binary to be required")
	}
}

func TestBuildRejectsAMalformedTarget(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Binary: "svc", OutDir: filepath.Join(dir, "dist"), Targets: []string{"darwin-arm64"}}
	if _, err := Build(cfg, &recorder{}); err == nil {
		t.Fatal("expected \"darwin-arm64\" (no slash) to be rejected as not os/arch")
	}
}

func TestDefaultsFillIn(t *testing.T) {
	c := Config{}
	if c.sourceDir() != "." {
		t.Errorf("sourceDir = %q, want .", c.sourceDir())
	}
	if c.outDir() != "dist" {
		t.Errorf("outDir = %q, want dist", c.outDir())
	}
	if c.cgoEnabled() != "0" {
		t.Errorf("cgoEnabled = %q, want 0", c.cgoEnabled())
	}
	if len(c.targets()) != len(DefaultTargets) {
		t.Errorf("targets = %v, want the default matrix", c.targets())
	}
}

// Stage must fail loudly on a missing dist rather than quietly shipping a
// bundle with nothing in it to connect through.
func TestStageFailsWithoutVersion(t *testing.T) {
	cfg := Config{Binary: "svc", OutDir: t.TempDir()}
	if _, err := Stage(cfg, t.TempDir(), &recorder{}); err == nil {
		t.Fatal("expected Stage to fail when out_dir has no VERSION")
	}
}

func TestStageClearsStaleCopies(t *testing.T) {
	out := t.TempDir()
	dest := t.TempDir()
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(out, "VERSION", "2.0\n")
	write(out, "svc-darwin-arm64", "new-arm")
	write(out, "svc-linux-amd64", "new-amd")

	// A previous stage left an arch that is no longer built, plus an old VERSION.
	write(dest, "svc-linux-arm64", "stale")
	write(dest, "VERSION", "1.0\n")

	staged, err := Stage(Config{Binary: "svc", OutDir: out}, dest, &recorder{})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if staged != 2 {
		t.Errorf("staged = %d, want 2", staged)
	}
	if _, err := os.Stat(filepath.Join(dest, "svc-linux-arm64")); err == nil {
		t.Error("a stale binary from a previous stage survived")
	}
	v, _ := os.ReadFile(filepath.Join(dest, "VERSION"))
	if string(v) != "2.0\n" {
		t.Errorf("VERSION = %q, want the fresh one", v)
	}
	b, _ := os.ReadFile(filepath.Join(dest, "svc-darwin-arm64"))
	if string(b) != "new-arm" {
		t.Errorf("binary content = %q", b)
	}
}

func TestStagedBinaryStaysExecutable(t *testing.T) {
	out, dest := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "VERSION"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "svc-darwin-arm64"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(Config{Binary: "svc", OutDir: out}, dest, &recorder{}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dest, "svc-darwin-arm64"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Error("staged binary lost its executable bit")
	}
}
