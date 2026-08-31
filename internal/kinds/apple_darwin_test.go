//go:build darwin

package kinds

import "testing"

func TestAppleProcessFallsBackToScheme(t *testing.T) {
	// No project (empty string skips the pbxproj lookup) and no
	// mac_process override — should fall back to the scheme name itself,
	// matching admin_lib/apple.py's _resolve_mac_process fallback chain.
	got := appleProcess("", map[string]any{}, "mac", "MyScheme")
	if got != "MyScheme" {
		t.Errorf("appleProcess fallback = %q, want %q", got, "MyScheme")
	}
}

func TestAppleProcessPrefersExplicitConfig(t *testing.T) {
	cfg := map[string]any{"mac_process": "ExplicitName"}
	got := appleProcess("", cfg, "mac", "MyScheme")
	if got != "ExplicitName" {
		t.Errorf("appleProcess = %q, want %q", got, "ExplicitName")
	}
}

func TestAppleBundleDevSuffix(t *testing.T) {
	cfg := map[string]any{"ios_bundle": "com.example.app"}
	got := appleBundle("", cfg, "ios", true)
	if got != "com.example.app.dev" {
		t.Errorf("dev bundle = %q, want %q", got, "com.example.app.dev")
	}
	got = appleBundle("", cfg, "ios", false)
	if got != "com.example.app" {
		t.Errorf("prod bundle = %q, want %q", got, "com.example.app")
	}
}

func TestAppleBundleExplicitDevOverride(t *testing.T) {
	cfg := map[string]any{"ios_bundle": "com.example.app", "ios_dev_bundle": "com.example.app.custom"}
	got := appleBundle("", cfg, "ios", true)
	if got != "com.example.app.custom" {
		t.Errorf("dev bundle override = %q, want %q", got, "com.example.app.custom")
	}
}

func TestAppleSubsystemPrefersExplicit(t *testing.T) {
	cfg := map[string]any{"subsystem": "com.explicit.subsystem", "mac_bundle": "com.example.mac"}
	if got := appleSubsystem("", cfg, "mac", true); got != "com.explicit.subsystem" {
		t.Errorf("subsystem = %q, want explicit override", got)
	}
}

func TestAppleDerivedDataNameFromExplicitConfig(t *testing.T) {
	cfg := map[string]any{"derived_data_name": "MyDerivedName"}
	if got := appleDerivedDataName(cfg, "/some/Project.xcodeproj"); got != "MyDerivedName" {
		t.Errorf("derived data name = %q, want %q", got, "MyDerivedName")
	}
}

func TestAppleDerivedDataNameFallsBackToProjectBasename(t *testing.T) {
	if got := appleDerivedDataName(map[string]any{}, "/some/Project.xcodeproj"); got != "Project" {
		t.Errorf("derived data name = %q, want %q", got, "Project")
	}
	if got := appleDerivedDataName(map[string]any{}, "/some/Project.xcworkspace"); got != "Project" {
		t.Errorf("derived data name = %q, want %q", got, "Project")
	}
}

func TestAppleDerivedGlobShape(t *testing.T) {
	got := appleDerivedGlob("Foo", "Debug", "-iphonesimulator", "Bar")
	want := "~/Library/Developer/Xcode/DerivedData/Foo-*/Build/Products/Debug-iphonesimulator/Bar.app"
	if got != want {
		t.Errorf("appleDerivedGlob = %q, want %q", got, want)
	}
}

func TestAppleBuildActionCleanFlag(t *testing.T) {
	if got := appleBuildAction(false); got != "build" {
		t.Errorf("appleBuildAction(false) = %q, want %q", got, "build")
	}
	if got := appleBuildAction(true); got != "clean build" {
		t.Errorf("appleBuildAction(true) = %q, want %q", got, "clean build")
	}
}
