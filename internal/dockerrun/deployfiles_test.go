package dockerrun

import "testing"

// TestBuildDeploySyncPlanChownAndPostSync is admin-9bex's regression test:
// a [deploy] table declaring both chown and post_sync must render BOTH into
// the plan every time — the rsync command must carry --chown, and
// PostSyncCmd must be set — with no code path that reads one but not the
// other. The bug report's suspected root cause was a second, generic sync
// implementation that never read [deploy]'s chown/post_sync/host keys at
// all (interp.py's `_make_sync`); this package is the only Go
// implementation of a [deploy] sync, so that class of bug has no second
// path to hide in.
func TestBuildDeploySyncPlanChownAndPostSync(t *testing.T) {
	cfg := map[string]any{
		"src":       "deploy/appdata",
		"dest":      "/mnt/user/appdata/etv-station",
		"host":      "root@${UNRAID_HOST}",
		"chown":     "${APP_UID:-99}:${APP_GID:-100}",
		"post_sync": "chown -R ${APP_UID:-99}:${APP_GID:-100} ${APPDATA_PATH}",
	}
	envVars := map[string]string{
		"UNRAID_HOST": "unraid.example.com",
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
	// copy, matching admin_lib/core.py::sync's warning for that case.
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
