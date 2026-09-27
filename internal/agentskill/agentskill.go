// Package agentskill installs an agent-facing SKILL.md (plus reference/*.md)
// into a coding agent's skill directories — ~/.claude/skills/<name> and
// ~/.codex/skills/<name> by default — so an agent working in the project can
// discover its CLI.
//
// Refuses to overwrite a skill it did not write: every file it installs
// carries a marker line, and a target that exists without that marker is
// treated as hand-written and left alone.
package agentskill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Marker is the line every installed file carries, so a previously installed
// skill is still recognised as ours and gets updated rather than skipped.
const Marker = "<!-- installed by admin: agent_skill.install_agent_skill() -->"

// Reporter is the subset of the host's output surface this package needs.
type Reporter interface {
	Info(msg string)
	Ok(msg string)
}

// DefaultTargets returns the skill directories a skill named `name` installs
// into when a project declares no explicit list.
func DefaultTargets(name string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "~"
	}
	return []string{
		filepath.Join(home, ".claude", "skills", name),
		filepath.Join(home, ".codex", "skills", name),
	}
}

func hasMarker(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), Marker)
}

// Install copies srcDir/SKILL.md and srcDir/reference/*.md into every dest,
// returning (installed, skipped).
func Install(srcDir string, dests []string, r Reporter) (int, int, error) {
	abs, err := filepath.Abs(srcDir)
	if err != nil {
		return 0, 0, err
	}
	skillMD := filepath.Join(abs, "SKILL.md")
	if fi, err := os.Stat(skillMD); err != nil || fi.IsDir() {
		return 0, 0, fmt.Errorf("no SKILL.md under %s", abs)
	}

	var refFiles []string
	refDir := filepath.Join(abs, "reference")
	if entries, err := os.ReadDir(refDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				refFiles = append(refFiles, filepath.Join(refDir, e.Name()))
			}
		}
		sort.Strings(refFiles)
	}

	installed, skipped := 0, 0
	for _, dest := range dests {
		r.Info(dest)
		i, s, err := writeSkillMD(skillMD, filepath.Join(dest, "SKILL.md"), r)
		if err != nil {
			return installed, skipped, err
		}
		installed, skipped = installed+i, skipped+s

		for _, f := range refFiles {
			i, s, err := writeReference(f, filepath.Join(dest, "reference", filepath.Base(f)), r)
			if err != nil {
				return installed, skipped, err
			}
			installed, skipped = installed+i, skipped+s
		}
	}

	r.Ok(fmt.Sprintf("installed %d file(s), skipped %d", installed, skipped))
	return installed, skipped, nil
}

// writeSkillMD puts the marker AFTER the closing `---` of the frontmatter:
// frontmatter has to stay first for the skill to be recognised at all.
func writeSkillMD(src, dest string, r Reporter) (int, int, error) {
	if skip(dest, r) {
		return 0, 1, nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")

	var out []string
	inFront, markerWritten := false, false
	for i, line := range lines {
		if i == 0 && line == "---" {
			inFront = true
			out = append(out, line)
			continue
		}
		if inFront && line == "---" {
			out = append(out, line, "", Marker)
			inFront = false
			markerWritten = true
			continue
		}
		out = append(out, line)
	}
	if !markerWritten {
		out = append([]string{Marker, ""}, out...)
	}
	return write(dest, strings.Join(out, "\n")+"\n", r)
}

func writeReference(src, dest string, r Reporter) (int, int, error) {
	if skip(dest, r) {
		return 0, 1, nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return 0, 0, err
	}
	return write(dest, Marker+"\n"+string(data), r)
}

func skip(dest string, r Reporter) bool {
	if _, err := os.Stat(dest); err == nil && !hasMarker(dest) {
		r.Info("  skip  " + dest + " (not ours — leaving your version alone)")
		return true
	}
	return false
}

func write(dest, body string, r Reporter) (int, int, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, 0, err
	}
	if err := os.WriteFile(dest, []byte(body), 0o644); err != nil {
		return 0, 0, err
	}
	r.Info("  write " + dest)
	return 1, 0, nil
}
