package dockerrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildDeploySyncPlanChownAndPostSync is admin-9bex's regression test:
// a [deploy] table declaring both chown and post_sync must render BOTH into
// the plan every time — the rsync command must carry --chown, and
// PostSyncCmd must be set — with no code path that reads one but not the
// other. The bug report's suspected root cause was a second, generic sync
// implementation that never read [deploy]'s chown/post_sync/host keys at
// all; this package is the only implementation of a [deploy] sync, so that
// class of bug has no second path to hide in.
func TestBuildDeploySyncPlanChownAndPostSync(t *testing.T) {
	cfg := map[string]any{
		"src":       "deploy/appdata",
		"dest":      "/mnt/user/appdata/etv-station",
		"host":      "root@${UNRAID_HOST}",
		"chown":     "${APP_UID:-99}:${APP_GID:-100}",
		"post_sync": "chown -R ${APP_UID:-99}:${APP_GID:-100} ${APPDATA_PATH}",
	}
	envVars := map[string]string{
		"UNRAID_HOST":  "unraid.example.com",
		"APPDATA_PATH": "/mnt/user/appdata/etv-station",
	}
	plan, errs := BuildDeploySyncPlan(cfg, envVars, false, false)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !plan.IsRemote {
		t.Fatalf("expected a remote plan for host=root@unraid.example.com, got local")
	}
	if plan.Chown != "99:100" {
		t.Errorf("chown = %q, want 99:100", plan.Chown)
	}
	if !containsSubstr(plan.RsyncCmd, "--chown=99:100") {
		t.Errorf("rsync command missing --chown=99:100: %q", plan.RsyncCmd)
	}
	wantPostSync := "chown -R 99:100 /mnt/user/appdata/etv-station"
	if plan.PostSyncCmd != wantPostSync {
		t.Errorf("PostSyncCmd = %q, want %q", plan.PostSyncCmd, wantPostSync)
	}
}

func TestBuildDeploySyncPlanLocalHostSkipsChownFlag(t *testing.T) {
	// A local deploy target renders no rsync command at all (the Lua caller
	// takes the local-copy branch instead) — chown has no effect on a local
	// copy.
	cfg := map[string]any{"src": "deploy/appdata", "dest": "/tmp/x", "host": "localhost", "chown": "99:100"}
	plan, errs := BuildDeploySyncPlan(cfg, nil, true, false)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if plan.IsRemote {
		t.Fatalf("expected a local plan, got remote: %+v", plan)
	}
	if plan.RsyncCmd != "" {
		t.Errorf("expected no rsync command for a local target, got %q", plan.RsyncCmd)
	}
}

func TestBuildDeploySyncPlanMissingSrcDest(t *testing.T) {
	_, errs := BuildDeploySyncPlan(map[string]any{"src": "x"}, nil, false, false)
	if len(errs) == 0 {
		t.Fatal("expected an error for a missing 'dest'")
	}
}

func TestBuildDeploySyncPlanUnknownKey(t *testing.T) {
	cfg := map[string]any{"src": "x", "dest": "y", "bogus": true}
	_, errs := BuildDeploySyncPlan(cfg, nil, false, false)
	if len(errs) == 0 || !containsSubstr(errs[0], "bogus") {
		t.Fatalf("expected an unknown-key error naming 'bogus', got %v", errs)
	}
}

func TestBuildDeploySyncPlanDeleteAndExcludeFlags(t *testing.T) {
	cfg := map[string]any{
		"src": "x", "dest": "y", "host": "user@remote",
		"delete":  true,
		"exclude": []any{"*.log", "*.tmp"},
	}
	plan, errs := BuildDeploySyncPlan(cfg, nil, false, false)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !containsSubstr(plan.RsyncCmd, "--delete") {
		t.Errorf("expected --delete in %q", plan.RsyncCmd)
	}
	if !containsSubstr(plan.RsyncCmd, "--exclude '*.log'") || !containsSubstr(plan.RsyncCmd, "--exclude '*.tmp'") {
		t.Errorf("expected both excludes in %q", plan.RsyncCmd)
	}
}

func TestBuildDeploySyncPlanDryRunFlag(t *testing.T) {
	cfg := map[string]any{"src": "x", "dest": "y", "host": "user@remote"}
	plan, errs := BuildDeploySyncPlan(cfg, nil, false, true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !containsSubstr(plan.RsyncCmd, "--dry-run") {
		t.Errorf("expected --dry-run in %q", plan.RsyncCmd)
	}
}

// --- owns: the allow-list that lets `delete` prune orphans safely ---------

func ownsFixture(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "channels", "chan-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "channels", "chan-a", "channel.yaml"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "station.yaml"), []byte("cfg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestOwnsFilterArgsIsAnAllowListEndingInCatchAllExclude(t *testing.T) {
	got := strings.Join(OwnsFilterArgs("", []string{"channels", "a/b"}), " ")

	// shq quotes only what needs it, so build the expectations through it
	// rather than assuming every argument comes back in single quotes.
	for _, want := range []string{"--include " + shq("/channels"), "--include " + shq("/channels/**"),
		"--include " + shq("/a/"), "--include " + shq("/a/b")} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if !strings.HasSuffix(got, "--exclude "+shq("*")) {
		t.Errorf("allow-list must end in the catch-all exclude, got %s", got)
	}
}

func TestOwnsFilterArgsSkipsContentsRuleForAFile(t *testing.T) {
	got := strings.Join(OwnsFilterArgs(ownsFixture(t), []string{"station.yaml"}), " ")

	if !strings.Contains(got, "--include "+shq("/station.yaml")) {
		t.Errorf("file entry must still be included: %s", got)
	}
	if strings.Contains(got, "/station.yaml/**") {
		t.Errorf("a file has no contents rule: %s", got)
	}
}

func TestBuildDeploySyncPlanPutsExcludesBeforeOwnsIncludes(t *testing.T) {
	src := ownsFixture(t)
	cfg := map[string]any{
		"src": src, "dest": "/mnt/appdata", "host": "root@box",
		"delete": true, "exclude": []any{"README.md"}, "owns": []any{"channels"},
	}

	plan, errs := BuildDeploySyncPlan(cfg, nil, false, false)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// rsync takes the first matching rule, so a carve-out inside an owned path
	// only wins if it is emitted ahead of the include that would claim it.
	if strings.Index(plan.RsyncCmd, "--exclude "+shq("README.md")) > strings.Index(plan.RsyncCmd, "--include "+shq("/channels")) {
		t.Errorf("excludes must precede owns includes: %s", plan.RsyncCmd)
	}
	if !strings.Contains(plan.RsyncCmd, "--delete") || !strings.HasSuffix(plan.RsyncCmd, shq("root@box:/mnt/appdata")+"/") {
		t.Errorf("plan lost its delete flag or target: %s", plan.RsyncCmd)
	}
	if plan.FilterArgs == "" {
		t.Error("FilterArgs must be set so the local branch can apply the same rules")
	}
}

func TestBuildDeploySyncPlanRefusesAnEmptyOwnedDirectory(t *testing.T) {
	// The git-worktree case: the mirror is gitignored, so `channels/` exists
	// but is empty, and mirroring it with --delete wipes the destination.
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "channels"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"src": src, "dest": "/mnt/appdata", "host": "root@box",
		"delete": true, "owns": []any{"channels"}}

	plan, errs := BuildDeploySyncPlan(cfg, nil, false, false)

	if plan != nil {
		t.Fatal("an empty owned directory must not produce a plan")
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "empty directory") {
		t.Errorf("expected an empty-directory refusal, got %v", errs)
	}
}

func TestBuildDeploySyncPlanRefusesOwnsOutsideSrc(t *testing.T) {
	src := ownsFixture(t)
	for _, entry := range []string{"../../etc", "nope"} {
		cfg := map[string]any{"src": src, "dest": "/mnt/appdata", "host": "root@box",
			"owns": []any{entry}}
		if plan, errs := BuildDeploySyncPlan(cfg, nil, false, false); plan != nil || len(errs) == 0 {
			t.Errorf("owns=%q must be refused, got plan=%v errs=%v", entry, plan, errs)
		}
	}
}

func TestOwnsIsAnAcceptedDeployKey(t *testing.T) {
	if unknown := UnknownDeployKeys(map[string]any{"owns": []any{}}); len(unknown) != 0 {
		t.Errorf("owns must be accepted, got %v", unknown)
	}
	if unknown := UnknownDeployKeys(map[string]any{"ownz": []any{}}); len(unknown) != 1 {
		t.Errorf("a typo must still be rejected, got %v", unknown)
	}
}
