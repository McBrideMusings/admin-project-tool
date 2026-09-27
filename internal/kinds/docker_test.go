package kinds

import (
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestMakeDockerReloadLoopMissingRunErrors proves kind="docker-reload-loop"
// requires 'run' at build time.
func TestMakeDockerReloadLoopMissingRunErrors(t *testing.T) {
	if _, err := makeDockerReloadLoop("bad", manifest.Action{}, nil, nil); err == nil {
		t.Fatal("makeDockerReloadLoop with no 'run' should error")
	}
}

// TestMakeDockerDevEmptyConfigErrors proves an empty (or absent)
// `[docker_dev]` table fails at RUN time rather than build time — a runtime
// check on the resolved config, not a manifest-shape check the factory
// constructor could make earlier.
func TestMakeDockerDevEmptyConfigErrors(t *testing.T) {
	runner, err := makeDockerDev("dev", manifest.Action{}, nil, nil)
	if err != nil {
		t.Fatalf("makeDockerDev: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() with no [docker_dev] config should error")
	}
}

// TestMakeDockerDevMissingImageTagErrors proves a non-empty config missing
// the required image_tag still fails, distinctly from the empty-config case.
func TestMakeDockerDevMissingImageTagErrors(t *testing.T) {
	m := &manifest.Manifest{Settings: map[string]map[string]any{"docker_dev": {"target": "dev"}}}
	runner, err := makeDockerDev("dev", manifest.Action{}, m, nil)
	if err != nil {
		t.Fatalf("makeDockerDev: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() with no image_tag should error")
	}
}

// TestMakeServerReloadNoTargets proves kind="server-reload" surfaces
// SignalAll's "no running targets" error as the action's own error, rather
// than silently succeeding.
func TestMakeServerReloadNoTargets(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // irrelevant to PIDPrefix, just isolating env
	runner, err := makeServerReload("reload", manifest.Action{}, nil, nil)
	if err != nil {
		t.Fatalf("makeServerReload: %v", err)
	}
	// reload.PIDPrefix is a fixed /tmp path shared with the real system;
	// this only asserts the "no PID files at all" branch reliably fires
	// when a highly specific, never-otherwise-used filter is passed, so a
	// stray real dev loop running on the machine can't make this flaky.
	if err := runner([]string{"admin-3rgp-13-test-filter-that-matches-nothing"}); err == nil {
		t.Fatal("runner() with a filter matching nothing should error")
	}
}

// TestMakeBindMountDirsMkdirFalseIsSkipped proves makeBindMountDirs honors
// an explicit mkdir=false on a bind mount, leaving that host dir uncreated
// while still creating one with no mkdir key (mkdir defaults to true).
//
// MUTATION EVIDENCE: drop the `if present && !mkdir { continue }` check
// entirely (ignore the mkdir key) and this test fails with "expected
// .../should-not-exist to NOT exist" — verified by hand-removing the check,
// re-running (that exact failure appeared), then reverting.
func TestMakeBindMountDirsMkdirFalseIsSkipped(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg := map[string]any{
		"bind_mounts": []any{
			map[string]any{"host": "should-exist", "container": "/x"},
			map[string]any{"host": "should-not-exist", "container": "/y", "mkdir": false},
		},
	}
	if err := makeBindMountDirs(cfg); err != nil {
		t.Fatalf("makeBindMountDirs: %v", err)
	}
	assertFileExists(t, dir, "should-exist")
	assertFileMissing(t, dir, "should-not-exist")
}
