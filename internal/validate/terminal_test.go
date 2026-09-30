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

func TestTerminalReadIsALegalStringKey(t *testing.T) {
	base := map[string]any{"spawn": "s", "attach": "a", "kill": "k", "read": "herdr pane read x"}
	if err := Terminal(base); err != nil {
		t.Fatalf("Terminal with read = %v, want nil", err)
	}
	base["read"] = 3
	err := Terminal(base)
	if err == nil || !strings.Contains(err.Error(), "terminal.read") {
		t.Fatalf("Terminal with non-string read = %v, want an error naming terminal.read", err)
	}
}

func TestTerminalDetachAloneStillRequiresLauncherKeys(t *testing.T) {
	err := Terminal(map[string]any{"detach": true})
	if err == nil || !strings.Contains(err.Error(), "spawn, attach and kill are required together") {
		t.Fatalf("Terminal with only detach = %v, want the required-together error", err)
	}
}

func TestTerminalDetachMustBeBool(t *testing.T) {
	ok := map[string]any{"spawn": "s", "attach": "a", "kill": "k", "detach": true}
	if err := Terminal(ok); err != nil {
		t.Fatalf("Terminal with detach = true = %v, want nil", err)
	}
	bad := map[string]any{"spawn": "s", "attach": "a", "kill": "k", "detach": "true"}
	err := Terminal(bad)
	if err == nil || !strings.Contains(err.Error(), "terminal.detach") {
		t.Fatalf("Terminal with detach = \"true\" = %v, want an error naming terminal.detach", err)
	}
}
