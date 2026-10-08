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
	_, err := makeApple("build-ios", map[string]any{}, nil, nil)
	if err == nil {
		t.Fatal("expected an error when 'run' is absent")
	}
}

func TestAppleLaunchArgsExpandsConfigButNotCLIArgs(t *testing.T) {
	t.Setenv("ADMIN_TEST_SUITE", "iptv-x")
	cfg := map[string]any{"launch_args": []any{"--defaults-suite=${ADMIN_TEST_SUITE}", "-v"}}

	got, err := appleLaunchArgs(cfg, appleFlags{})
	if err != nil {
		t.Fatalf("appleLaunchArgs: %v", err)
	}
	if want := []string{"--defaults-suite=iptv-x", "-v"}; !reflect.DeepEqual(got, want) {
		t.Errorf("config launch_args = %v, want %v", got, want)
	}

	cli := []string{"--literal=${ADMIN_TEST_SUITE}"}
	got, err = appleLaunchArgs(cfg, appleFlags{launchArgs: cli})
	if err != nil || !reflect.DeepEqual(got, cli) {
		t.Errorf("--args launch args = %v (err %v), want %v verbatim", got, err, cli)
	}

	cfg = map[string]any{"launch_args": []any{"--suite=${ADMIN_TEST_TOTALLY_UNSET}"}}
	if _, err := appleLaunchArgs(cfg, appleFlags{}); err == nil {
		t.Error("an unset ${VAR} in launch_args should error, not reach the app literally")
	}
}
