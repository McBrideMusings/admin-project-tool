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

func TestTerminalWindowLocaleAloneStillRequiresLauncherKeys(t *testing.T) {
	err := Terminal(map[string]any{"locale": "window"})
	if err == nil || !strings.Contains(err.Error(), "spawn, attach and kill are required together") {
		t.Fatalf("Terminal with only locale = window = %v, want the required-together error", err)
	}
}

func TestTerminalHostedLocaleAloneNeedsNoLauncher(t *testing.T) {
	if err := Terminal(map[string]any{"locale": "hosted"}); err != nil {
		t.Fatalf("Terminal with only locale = hosted = %v, want nil", err)
	}
}

func TestTerminalLocaleMustBeWindowOrHosted(t *testing.T) {
	launcher := func(locale any) map[string]any {
		return map[string]any{"spawn": "s", "attach": "a", "kill": "k", "locale": locale}
	}
	for _, ok := range []string{"window", "hosted"} {
		if err := Terminal(launcher(ok)); err != nil {
			t.Fatalf("Terminal with locale = %q = %v, want nil", ok, err)
		}
	}
	for _, bad := range []any{"inline", "pane", true} {
		err := Terminal(launcher(bad))
		if err == nil || !strings.Contains(err.Error(), "terminal.locale") {
			t.Fatalf("Terminal with locale = %#v = %v, want an error naming terminal.locale", bad, err)
		}
	}
}

func TestTerminalHostedLocaleWithAnyLauncherKeyStillRequiresAllThree(t *testing.T) {
	err := Terminal(map[string]any{"locale": "hosted", "status": "s"})
	if err == nil || !strings.Contains(err.Error(), "spawn, attach and kill are required together") {
		t.Fatalf("Terminal with locale = hosted and status = %v, want the required-together error", err)
	}
}

func TestTerminalDetachIsAnUnknownKey(t *testing.T) {
	err := Terminal(map[string]any{"spawn": "s", "attach": "a", "kill": "k", "detach": true})
	if err == nil || !strings.Contains(err.Error(), `unknown key "detach"`) {
		t.Fatalf("Terminal with detach = %v, want an unknown-key error", err)
	}
}
