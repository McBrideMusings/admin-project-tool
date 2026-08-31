package kinds

import (
	"reflect"
	"testing"
)

func TestParseAppleFlagsDefaults(t *testing.T) {
	f := parseAppleFlags(nil)
	if f.logLevel != "app" {
		t.Errorf("default logLevel = %q, want %q", f.logLevel, "app")
	}
	if f.clean || f.install {
		t.Error("clean/install should default false")
	}
	if len(f.launchArgs) != 0 {
		t.Error("launchArgs should default empty")
	}
}

func TestParseAppleFlagsLogClean(t *testing.T) {
	f := parseAppleFlags([]string{"--log", "debug", "--clean"})
	if f.logLevel != "debug" {
		t.Errorf("logLevel = %q, want %q", f.logLevel, "debug")
	}
	if !f.clean {
		t.Error("--clean should set clean=true")
	}
	if f.install {
		t.Error("install should stay false")
	}
}

func TestParseAppleFlagsInstallAndArgs(t *testing.T) {
	f := parseAppleFlags([]string{"--install", "--args", "--foo", "bar"})
	if !f.install {
		t.Error("--install should set install=true")
	}
	want := []string{"--foo", "bar"}
	if !reflect.DeepEqual(f.launchArgs, want) {
		t.Errorf("launchArgs = %v, want %v", f.launchArgs, want)
	}
}

func TestMakeAppleRequiresRun(t *testing.T) {
	_, err := makeApple("build-ios", map[string]any{}, nil)
	if err == nil {
		t.Fatal("expected an error when 'run' is absent")
	}
}
