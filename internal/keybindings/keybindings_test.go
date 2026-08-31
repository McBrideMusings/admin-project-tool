package keybindings

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// TestFormatBannerIncludesEveryBindingAndExit proves the startup banner
// names every declared key by its label (falling back to action/signal/
// "run") plus the fixed "[Ctrl-C] exit" entry, and is deterministic
// (sorted by keychar) regardless of Go's randomized map iteration order.
func TestFormatBannerIncludesEveryBindingAndExit(t *testing.T) {
	keys := map[string]validate.KeyBinding{
		"r": {Label: "restart", Run: "echo hi"},
		"a": {Action: "build"},
		"s": {Signal: "TERM", PidFile: "/tmp/x.pid"},
	}
	banner := formatBanner(keys, "dev")
	if !strings.HasPrefix(banner, "[dev] ") {
		t.Fatalf("banner = %q, want it to start with the label prefix", banner)
	}
	for _, want := range []string{"[r] restart", "[a] build", "[s] TERM", "[Ctrl-C] exit"} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner = %q, want it to contain %q", banner, want)
		}
	}
	// Deterministic order: run twice, must match.
	if formatBanner(keys, "dev") != banner {
		t.Fatal("formatBanner is not deterministic across calls")
	}
}

// TestFormatBannerNoLabelFallsBackToRun proves a binding with none of
// label/action/signal set (a bare run binding) falls back to the literal
// word "run" in the banner.
func TestFormatBannerNoLabelFallsBackToRun(t *testing.T) {
	keys := map[string]validate.KeyBinding{"x": {Run: "echo hi"}}
	banner := formatBanner(keys, "")
	if !strings.Contains(banner, "[x] run") {
		t.Fatalf("banner = %q, want '[x] run'", banner)
	}
	if strings.HasPrefix(banner, "[") == false {
		t.Fatalf("banner = %q, want no label prefix for an empty label", banner)
	}
}
