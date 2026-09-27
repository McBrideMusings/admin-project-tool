//go:build darwin

package kinds

import (
	"reflect"
	"testing"
)

func TestParseAppIDArgsCapabilityAlias(t *testing.T) {
	wanted, name, ids := parseAppIDArgs([]string{"--capability", "app-groups"})
	if !reflect.DeepEqual(wanted, []string{"APP_GROUPS"}) {
		t.Errorf("wanted = %v, want [APP_GROUPS]", wanted)
	}
	if name != "" {
		t.Errorf("displayName = %q, want empty", name)
	}
	if len(ids) != 0 {
		t.Errorf("identifiers = %v, want none", ids)
	}
}

func TestParseAppIDArgsUnknownCapabilityUppercased(t *testing.T) {
	wanted, _, _ := parseAppIDArgs([]string{"--capability", "some-new-thing"})
	if !reflect.DeepEqual(wanted, []string{"SOME-NEW-THING"}) {
		t.Errorf("wanted = %v, want an upper-cased passthrough", wanted)
	}
}

func TestParseAppIDArgsNameAndIdentifiers(t *testing.T) {
	wanted, name, ids := parseAppIDArgs([]string{
		"com.example.app", "--name", "My App", "--capability", "icloud",
	})
	if !reflect.DeepEqual(wanted, []string{"ICLOUD"}) {
		t.Errorf("wanted = %v, want [ICLOUD]", wanted)
	}
	if name != "My App" {
		t.Errorf("displayName = %q, want %q", name, "My App")
	}
	if !reflect.DeepEqual(ids, []string{"com.example.app"}) {
		t.Errorf("identifiers = %v, want [com.example.app]", ids)
	}
}

func TestParseAppIDArgsMultipleCapabilities(t *testing.T) {
	wanted, _, _ := parseAppIDArgs([]string{"--capability", "push", "--capability", "app-groups"})
	if !reflect.DeepEqual(wanted, []string{"PUSH_NOTIFICATIONS", "APP_GROUPS"}) {
		t.Errorf("wanted = %v, want [PUSH_NOTIFICATIONS APP_GROUPS]", wanted)
	}
}

func TestParseAppIDArgsDanglingFlagIsIgnored(t *testing.T) {
	// A trailing --capability/--name with no value following isn't a
	// recognized flag occurrence (i+1 < len(args) fails), so it falls
	// through to the default case and is treated as a positional token.
	wanted, name, ids := parseAppIDArgs([]string{"--capability"})
	if len(wanted) != 0 {
		t.Errorf("wanted = %v, want none for a dangling --capability", wanted)
	}
	if name != "" {
		t.Errorf("displayName = %q, want empty", name)
	}
	if !reflect.DeepEqual(ids, []string{"--capability"}) {
		t.Errorf("identifiers = %v, want the dangling flag treated as positional", ids)
	}
}

func TestAppleEnsureDeviceRegisteredNoopsWithoutUDID(t *testing.T) {
	// No udid, no Host method should ever be reached (nil Host would panic
	// on any h.Warn/h.Info/h.Ok call) — this is the guard that keeps a
	// simulator-only build (empty udid) from touching the network at all.
	appleEnsureDeviceRegistered(nil, "", "Some Device")
}
