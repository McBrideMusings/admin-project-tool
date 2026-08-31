package agentskill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recorder struct{ lines []string }

func (r *recorder) Info(m string) { r.lines = append(r.lines, m) }
func (r *recorder) Ok(m string)   { r.lines = append(r.lines, m) }

const skillBody = `---
name: my-tool
description: does a thing
---

# My Tool

Body text.
`

func srcDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(dir, "reference")
	if err := os.MkdirAll(ref, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ref, "ops.md"), []byte("# Ops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The marker goes AFTER the closing `---`: frontmatter must stay first or the
// skill is not recognised at all.
func TestSkillMDKeepsFrontmatterFirst(t *testing.T) {
	dest := t.TempDir()
	installed, skipped, err := Install(srcDir(t), []string{dest}, &recorder{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed != 2 || skipped != 0 {
		t.Fatalf("installed=%d skipped=%d, want 2/0", installed, skipped)
	}

	out, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(out), "\n")
	if lines[0] != "---" {
		t.Fatalf("first line is %q, want the frontmatter fence", lines[0])
	}
	markerAt := strings.Index(string(out), Marker)
	closeAt := strings.Index(string(out[4:]), "---") + 4
	if markerAt < closeAt {
		t.Error("marker was written inside the frontmatter")
	}
	if !strings.Contains(string(out), "# My Tool") {
		t.Error("body was lost")
	}
}

func TestReferenceFilesCarryTheMarker(t *testing.T) {
	dest := t.TempDir()
	if _, _, err := Install(srcDir(t), []string{dest}, &recorder{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dest, "reference", "ops.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), Marker) {
		t.Error("reference file is missing the leading marker")
	}
}

// The whole point of the marker: a skill the user wrote themselves is never
// clobbered.
func TestHandWrittenSkillIsLeftAlone(t *testing.T) {
	dest := t.TempDir()
	mine := "---\nname: mine\n---\n\nHands off.\n"
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	installed, skipped, err := Install(srcDir(t), []string{dest}, rec)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if skipped != 1 {
		t.Errorf("skipped=%d, want 1", skipped)
	}
	if installed != 1 { // the reference file still installs
		t.Errorf("installed=%d, want 1", installed)
	}
	out, _ := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if string(out) != mine {
		t.Error("a hand-written SKILL.md was overwritten")
	}
}

// A second run must update in place, not skip its own output.
func TestReinstallOverwritesOurOwn(t *testing.T) {
	dest := t.TempDir()
	src := srcDir(t)
	if _, _, err := Install(src, []string{dest}, &recorder{}); err != nil {
		t.Fatal(err)
	}
	installed, skipped, err := Install(src, []string{dest}, &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	if installed != 2 || skipped != 0 {
		t.Errorf("installed=%d skipped=%d on reinstall, want 2/0", installed, skipped)
	}
}

func TestNoSkillMDIsAnError(t *testing.T) {
	if _, _, err := Install(t.TempDir(), []string{t.TempDir()}, &recorder{}); err == nil {
		t.Fatal("expected an error when the source has no SKILL.md")
	}
}

func TestSkillWithoutFrontmatterGetsALeadingMarker(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("# Plain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, _, err := Install(src, []string{dest}, &recorder{}); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if !strings.HasPrefix(string(out), Marker) {
		t.Errorf("want a leading marker, got:\n%s", out)
	}
}

func TestDefaultTargetsNameBothAgents(t *testing.T) {
	got := DefaultTargets("my-tool")
	if len(got) != 2 {
		t.Fatalf("DefaultTargets = %v, want two", got)
	}
	if !strings.HasSuffix(got[0], filepath.Join(".claude", "skills", "my-tool")) {
		t.Errorf("got[0] = %q", got[0])
	}
	if !strings.HasSuffix(got[1], filepath.Join(".codex", "skills", "my-tool")) {
		t.Errorf("got[1] = %q", got[1])
	}
}
