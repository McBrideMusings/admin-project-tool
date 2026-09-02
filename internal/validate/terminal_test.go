package validate

import (
	"strings"
	"testing"
)

func TestTerminalWellFormed(t *testing.T) {
	raw := map[string]any{
		"spawn":  "spawn cmd",
		"attach": "attach cmd",
		"kill":   "kill cmd",
		"status": "status cmd",
	}
	if err := Terminal(raw); err != nil {
		t.Fatalf("Terminal(%#v) = %v, want nil", raw, err)
	}
}

func TestTerminalThreeKeysNoStatusPasses(t *testing.T) {
	raw := map[string]any{
		"spawn":  "spawn cmd",
		"attach": "attach cmd",
		"kill":   "kill cmd",
	}
	if err := Terminal(raw); err != nil {
		t.Fatalf("Terminal(%#v) = %v, want nil (status is optional)", raw, err)
	}
}

func TestTerminalMissingAttach(t *testing.T) {
	raw := map[string]any{
		"spawn": "spawn cmd",
		"kill":  "kill cmd",
	}
	err := Terminal(raw)
	if err == nil {
		t.Fatal("Terminal = nil, want an error naming the missing 'attach' key")
	}
	if !strings.Contains(err.Error(), "attach") {
		t.Fatalf("error %q does not name the missing key 'attach'", err.Error())
	}
}

func TestTerminalUnknownKey(t *testing.T) {
	raw := map[string]any{
		"spawn":  "spawn cmd",
		"attach": "attach cmd",
		"kill":   "kill cmd",
		"bogus":  "nope",
	}
	err := Terminal(raw)
	if err == nil {
		t.Fatal("Terminal = nil, want an error naming the unknown key")
	}
	msg := err.Error()
	for _, want := range []string{"bogus", "spawn", "attach", "kill", "status"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not name %q", msg, want)
		}
	}
}

func TestTerminalEmptyStringSpawn(t *testing.T) {
	raw := map[string]any{
		"spawn":  "   ",
		"attach": "attach cmd",
		"kill":   "kill cmd",
	}
	err := Terminal(raw)
	if err == nil {
		t.Fatal("Terminal = nil, want an error for an empty/whitespace 'spawn'")
	}
	if !strings.Contains(err.Error(), "spawn") {
		t.Fatalf("error %q does not name 'spawn'", err.Error())
	}
}

func TestTerminalAbsentTablePasses(t *testing.T) {
	if err := Terminal(nil); err != nil {
		t.Fatalf("Terminal(nil) = %v, want nil", err)
	}
	if err := Terminal(map[string]any{}); err != nil {
		t.Fatalf("Terminal(empty map) = %v, want nil", err)
	}
}
