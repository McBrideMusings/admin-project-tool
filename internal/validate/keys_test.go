package validate

import (
	"strings"
	"testing"
)

func TestRowKeys(t *testing.T) {
	commands := map[string]bool{"implement": true, "open-bead": true}

	t.Run("no keys table is unaffected", func(t *testing.T) {
		bindings, shadows, err := RowKeys(nil, commands)
		if err != nil || bindings != nil || shadows != nil {
			t.Fatalf("RowKeys(nil) = (%v, %v, %v), want (nil, nil, nil)", bindings, shadows, err)
		}
	})

	t.Run("valid binding passes", func(t *testing.T) {
		raw := map[string]any{
			"bead": map[string]any{"o": "open-bead"},
		}
		bindings, shadows, err := RowKeys(raw, commands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(bindings) != 1 || bindings[0].Command != "open-bead" || bindings[0].Key != "o" {
			t.Fatalf("bindings = %+v", bindings)
		}
		if shadows != nil {
			t.Fatalf("shadows = %v, want nil", shadows)
		}
	})

	t.Run("missing command fails and names both", func(t *testing.T) {
		raw := map[string]any{
			"bead": map[string]any{"o": "does-not-exist"},
		}
		_, _, err := RowKeys(raw, commands)
		if err == nil {
			t.Fatal("expected error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "keys.bead.o") || !strings.Contains(msg, "does-not-exist") {
			t.Errorf("message %q missing binding or command name", msg)
		}
	})

	t.Run("reserved key fails and says reserved", func(t *testing.T) {
		raw := map[string]any{
			"bead": map[string]any{"q": "implement"},
		}
		_, _, err := RowKeys(raw, commands)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("message %q doesn't say reserved", err.Error())
		}
	})

	t.Run("non-bindable work-tab kind fails and lists bindable kinds", func(t *testing.T) {
		for _, kind := range []string{"command", "commandTarget", "worker", "nonsense"} {
			raw := map[string]any{
				kind: map[string]any{"o": "implement"},
			}
			_, _, err := RowKeys(raw, commands)
			if err == nil {
				t.Fatalf("%s: expected error", kind)
			}
			msg := err.Error()
			for _, want := range []string{"bead", "issue", "branch", "pr", "commit", "stash"} {
				if !strings.Contains(msg, want) {
					t.Errorf("%s: message %q missing bindable kind %q", kind, msg, want)
				}
			}
		}
	})

	t.Run("shadow passes and is reported separately from err", func(t *testing.T) {
		raw := map[string]any{
			"bead": map[string]any{"d": "implement"},
		}
		bindings, shadows, err := RowKeys(raw, commands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(bindings) != 1 {
			t.Fatalf("bindings = %+v", bindings)
		}
		if len(shadows) != 1 || !strings.Contains(shadows[0], "keys.bead.d") || !strings.Contains(shadows[0], "shadows") {
			t.Errorf("shadows = %v", shadows)
		}
	})

	t.Run("two problems in one manifest are both reported", func(t *testing.T) {
		raw := map[string]any{
			"bead":  map[string]any{"q": "implement"},          // reserved
			"issue": map[string]any{"o": "does-not-exist-cmd"}, // missing command
		}
		_, _, err := RowKeys(raw, commands)
		if err == nil {
			t.Fatal("expected error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "keys.bead.q") {
			t.Errorf("message missing reserved-key problem: %q", msg)
		}
		if !strings.Contains(msg, "keys.issue.o") || !strings.Contains(msg, "does-not-exist-cmd") {
			t.Errorf("message missing missing-command problem: %q", msg)
		}
		if strings.Count(msg, "\n") < 1 {
			t.Errorf("expected both problems joined, got: %q", msg)
		}
	})

	t.Run("reserved key that is also a builtin produces only the reserved error, no shadow", func(t *testing.T) {
		raw := map[string]any{
			"branch": map[string]any{"tab": "implement"}, // "tab" is reserved AND a built-in (pane) on branch
		}
		bindings, shadows, err := RowKeys(raw, commands)
		if err == nil {
			t.Fatal("expected error")
		}
		if len(bindings) != 0 {
			t.Fatalf("bindings = %+v, want none (reserved key must not be recorded as a binding)", bindings)
		}
		if len(shadows) != 0 {
			t.Fatalf("shadows = %v, want none — a reserved key must never also produce a shadow line", shadows)
		}
	})
}
