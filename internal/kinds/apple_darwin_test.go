//go:build darwin

package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
)

func TestAppleProcessFallsBackToScheme(t *testing.T) {
	// No project (empty string skips the pbxproj lookup) and no
	// mac_process override — should fall back to the scheme name itself.
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

// plantDerivedApp creates DerivedData/<dd>/Build/Products/Release/Bar.app
// under root and returns its Build/Products/Release directory.
func plantDerivedApp(t *testing.T, root, dd string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, dd, "Build", "Products", "Release")
	app := filepath.Join(dir, "Bar.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(app, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return dir
}

func stubTargetBuildDir(t *testing.T, dir string) {
	t.Helper()
	prev := appleTargetBuildDir
	appleTargetBuildDir = func(luahost.XcodebuildOptions) (string, error) { return dir, nil }
	t.Cleanup(func() { appleTargetBuildDir = prev })
}

// Two worktrees of one project each leave a Foo-* DerivedData directory. The
// sibling's app is newer; the build's own TARGET_BUILD_DIR must still win.
func TestAppleBuiltAppReadsTargetBuildDirNotNewestDerivedData(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	ours := plantDerivedApp(t, root, "Foo-ours", now.Add(-time.Hour))
	plantDerivedApp(t, root, "Foo-sibling", now)
	stubTargetBuildDir(t, ours)

	got, err := appleBuiltApp(luahost.XcodebuildOptions{Scheme: "Foo", Config: "Release"}, "Bar")
	if err != nil {
		t.Fatalf("appleBuiltApp: %v", err)
	}
	if want := filepath.Join(ours, "Bar.app"); got != want {
		t.Errorf("appleBuiltApp = %q, want %q", got, want)
	}
}

// The product name reaches the xcodebuild query, so a scheme with several
// targets resolves the app target's TARGET_BUILD_DIR, not the first target's.
func TestAppleBuiltAppPassesProductToTargetBuildDir(t *testing.T) {
	ours := plantDerivedApp(t, t.TempDir(), "Foo-ours", time.Now())
	var seen string
	prev := appleTargetBuildDir
	appleTargetBuildDir = func(o luahost.XcodebuildOptions) (string, error) { seen = o.Product; return ours, nil }
	t.Cleanup(func() { appleTargetBuildDir = prev })

	if _, err := appleBuiltApp(luahost.XcodebuildOptions{Scheme: "Foo", Config: "Release"}, "Bar"); err != nil {
		t.Fatalf("appleBuiltApp: %v", err)
	}
	if seen != "Bar" {
		t.Errorf("TargetBuildDir got Product %q, want %q", seen, "Bar")
	}
}

// No fallback: an app missing from TARGET_BUILD_DIR is an error even when a
// sibling DerivedData directory holds one.
func TestAppleBuiltAppRefusesWhenAppMissingFromTargetBuildDir(t *testing.T) {
	root := t.TempDir()
	plantDerivedApp(t, root, "Foo-sibling", time.Now())
	ours := filepath.Join(root, "Foo-ours", "Build", "Products", "Release")
	if err := os.MkdirAll(ours, 0o755); err != nil {
		t.Fatal(err)
	}
	stubTargetBuildDir(t, ours)

	got, err := appleBuiltApp(luahost.XcodebuildOptions{Scheme: "Foo", Config: "Release"}, "Bar")
	if err == nil {
		t.Fatalf("appleBuiltApp = %q, want an error", got)
	}
}

func TestAppleBuiltAppRefusesWithoutTargetBuildDir(t *testing.T) {
	stubTargetBuildDir(t, "")
	if got, err := appleBuiltApp(luahost.XcodebuildOptions{Scheme: "Foo", Config: "Release"}, "Bar"); err == nil {
		t.Fatalf("appleBuiltApp = %q, want an error", got)
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

func TestAppleDevEnvResolvesVars(t *testing.T) {
	t.Setenv("APPLE_DEV_ENV_TEST_VAR", "resolved-value")
	cfg := map[string]any{
		"dev_env": map[string]any{
			"PLAIN": "literal",
			"VAR":   "${APPLE_DEV_ENV_TEST_VAR}",
		},
	}
	got, err := appleDevEnv(cfg)
	if err != nil {
		t.Fatalf("appleDevEnv: %v", err)
	}
	if got["PLAIN"] != "literal" {
		t.Errorf("PLAIN = %q, want %q", got["PLAIN"], "literal")
	}
	if got["VAR"] != "resolved-value" {
		t.Errorf("VAR = %q, want %q", got["VAR"], "resolved-value")
	}
}

func TestAppleDevEnvPropagatesResolveError(t *testing.T) {
	cfg := map[string]any{
		"dev_env": map[string]any{"BROKEN": "${APPLE_DEV_ENV_UNSET_VAR}"},
	}
	if _, err := appleDevEnv(cfg); err == nil {
		t.Fatal("appleDevEnv: want error for an unresolvable ${VAR}, got nil")
	}
}

func TestAppleDevEnvNilWhenAbsent(t *testing.T) {
	got, err := appleDevEnv(map[string]any{})
	if err != nil {
		t.Fatalf("appleDevEnv: %v", err)
	}
	if got != nil {
		t.Errorf("appleDevEnv with no dev_env = %v, want nil", got)
	}
}

func TestRunApplePrebuildRunsOnceAndErrorsOnFailure(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	h := luahost.New(dir)

	cfg := map[string]any{"prebuild_cmd": "echo ran >> " + marker}
	if err := runApplePrebuild(cfg, h); err != nil {
		t.Fatalf("first runApplePrebuild: %v", err)
	}
	if err := runApplePrebuild(cfg, h); err != nil {
		t.Fatalf("second runApplePrebuild: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading marker: %v", err)
	}
	if got := len(strings.Fields(string(data))); got != 1 {
		t.Errorf("prebuild_cmd ran %d times, want 1 (memoized per cfg value)", got)
	}

	failing := map[string]any{"prebuild_cmd": "exit 1"}
	if err := runApplePrebuild(failing, h); err == nil {
		t.Fatal("runApplePrebuild: want error when prebuild_cmd exits non-zero, got nil")
	}
}

// --- mac_deploy_verify -------------------------------------------------
//
// appleDeployMac only calls appleRunMacDeployVerify and, on a nil error,
// proceeds to macapp.Install(appPath, installPath) — so a non-nil error
// from this function is exactly what keeps installPath untouched. These
// tests exercise the extracted function directly rather than the full
// appleDeployMac flow, which needs a real xcodebuild.

func writeAppleVerifyScript(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "verify.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("writing verify script: %v", err)
	}
	return path
}

func TestAppleRunMacDeployVerifyUnsetIsNoop(t *testing.T) {
	dir := t.TempDir()
	h := luahost.New(dir)

	if err := appleRunMacDeployVerify(map[string]any{}, h, filepath.Join(dir, "Built.app")); err != nil {
		t.Fatalf("appleRunMacDeployVerify with no mac_deploy_verify: %v", err)
	}
	if err := appleRunMacDeployVerify(map[string]any{"mac_deploy_verify": ""}, h, filepath.Join(dir, "Built.app")); err != nil {
		t.Fatalf("appleRunMacDeployVerify with empty mac_deploy_verify: %v", err)
	}
}

func TestAppleRunMacDeployVerifyFailureReturnsError(t *testing.T) {
	dir := t.TempDir()
	h := luahost.New(dir)
	script := writeAppleVerifyScript(t, dir, "exit 1")
	cfg := map[string]any{"mac_deploy_verify": script}

	err := appleRunMacDeployVerify(cfg, h, filepath.Join(dir, "Built.app"))
	if err == nil {
		t.Fatal("appleRunMacDeployVerify: want error when the verify command exits non-zero, got nil")
	}
	if !strings.Contains(err.Error(), script) {
		t.Errorf("error %q does not name the failing command %q", err.Error(), script)
	}
	if !strings.Contains(err.Error(), "exited 1") {
		t.Errorf("error %q does not name the exit code", err.Error())
	}
}

func TestAppleRunMacDeployVerifySuccessReceivesAppPath(t *testing.T) {
	dir := t.TempDir()
	h := luahost.New(dir)
	marker := filepath.Join(dir, "marker")
	t.Setenv("APPLE_MAC_DEPLOY_VERIFY_TEST_MARKER", marker)
	script := writeAppleVerifyScript(t, dir, `echo "$1" > "$APPLE_MAC_DEPLOY_VERIFY_TEST_MARKER"`)
	cfg := map[string]any{"mac_deploy_verify": script}
	appPath := filepath.Join(dir, "Built.app")

	if err := appleRunMacDeployVerify(cfg, h, appPath); err != nil {
		t.Fatalf("appleRunMacDeployVerify: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading marker: %v", err)
	}
	if strings.TrimSpace(string(got)) != appPath {
		t.Errorf("verify command received %q, want the app path %q", strings.TrimSpace(string(got)), appPath)
	}
}
