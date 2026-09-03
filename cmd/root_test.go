package cmd

import "testing"

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
