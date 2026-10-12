package cmd

import (
	"bytes"
	"runtime/debug"
	"testing"
)

// TestVersionFlag drives `admin --version` through cobra: the line is the
// build revision, then the source checkout, which is the shape
// ~/.claude/hooks/tests-worktree-link-locals.sh reads as "<rev> <src>".
func TestVersionFlag(t *testing.T) {
	var out bytes.Buffer
	t.Cleanup(func() {
		_ = rootCmd.Flags().Set("version", "false")
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
	})
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"--version"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("admin --version: %v", err)
	}
	if want := versionLine(buildRevision(), sourceDir) + "\n"; out.String() != want {
		t.Errorf("admin --version printed %q, want %q", out.String(), want)
	}
	if got, want := versionLine("abc", "/src/admin"), "abc /src/admin"; got != want {
		t.Errorf("versionLine with a source dir = %q, want %q", got, want)
	}
	if got, want := versionLine("abc", ""), "abc"; got != want {
		t.Errorf("versionLine without a source dir = %q, want %q", got, want)
	}
}

// TestRevisionFromSettings pins what `admin --version` prints, since
// ~/.claude's hook suites compare it against the commit that carries a fix.
func TestRevisionFromSettings(t *testing.T) {
	const sha = "bdce50c306005de8f4a6dc24842a827d6c6a20e2"
	cases := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{"no vcs stamp", nil, "unknown"},
		{"clean tree", []debug.BuildSetting{{Key: "vcs.revision", Value: sha}, {Key: "vcs.modified", Value: "false"}}, sha},
		{"dirty tree", []debug.BuildSetting{{Key: "vcs.revision", Value: sha}, {Key: "vcs.modified", Value: "true"}}, sha + "+dirty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := revisionFromSettings(tc.settings); got != tc.want {
				t.Errorf("revisionFromSettings = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestColorfgbgDarkBackground exercises colorfgbgDarkBackground directly —
// no live terminal, no OSC 11 query, no pty — proving the heuristic that
// stands in for termenv's live background-color probe (see admin-sz7w.1).
func TestColorfgbgDarkBackground(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"unset", "", true},
		{"malformed, no semicolon", "notacolorfgbg", true},
		{"malformed last field", "15;notanumber", true},
		{"dark background, index 0", "const;0", true},
		{"light background, index 15", "const;15", false},
		{"three-field form, dark", "15;default;0", true},
		{"three-field form, light", "0;default;15", false},
		{"dark background, index 6 (cyan, boundary)", "const;6", true},
		{"light background, index 7 (white, boundary)", "const;7", false},
		{"light background, index 8 (bright black, boundary)", "const;8", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := colorfgbgDarkBackground(tc.raw)
			if got != tc.want {
				t.Errorf("colorfgbgDarkBackground(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
