package luahost

import (
	"strings"
	"testing"
)

func TestDockerRunBuildArgsFromCfgReachesLua(t *testing.T) {
	h, out, _ := newTestHost(t)
	cfg := map[string]any{
		"docker_run": map[string]any{
			"ports": []map[string]any{
				{"host": int64(8080), "container": int64(80)},
			},
			"privileged": true,
		},
	}
	err := h.Exec(`
local args = dockerrun.build_args_from_cfg(config.docker_run, {})
info(table.concat(args, " "))
`, cfg)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "-p 8080:80/tcp") || !strings.Contains(got, "--privileged") {
		t.Fatalf("build_args_from_cfg output missing expected pieces: %q", got)
	}
}

func TestDockerRunContainerNamePrefersDeclared(t *testing.T) {
	h, out, _ := newTestHost(t)
	cfg := map[string]any{
		"docker_run": map[string]any{"container_name": "rpg-toolkit-selfhost"},
	}
	err := h.Exec(`
info("declared=" .. dockerrun.container_name(config.docker_run, "ghcr.io/me/rpg-toolkit:latest"))
info("derived=" .. dockerrun.container_name({}, "ghcr.io/me/rpg-toolkit:latest"))
`, cfg)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "declared=rpg-toolkit-selfhost") || !strings.Contains(got, "derived=rpg-toolkit") || strings.Contains(got, "derived=rpg-toolkit-selfhost") {
		t.Fatalf("container_name output = %q, want declared name then derived name", got)
	}
}

// TestDockerRunComputeDriftRoundTrip proves the deploy/diff actions' actual
// call shape — build args from an inspect blob, then diff them against a
// declared/target argv list — works through the Lua binding, not just the
// pure Go function directly.
func TestDockerRunComputeDriftRoundTrip(t *testing.T) {
	h, out, _ := newTestHost(t)
	err := h.Exec(`
local inspect = {
  HostConfig = {Memory = 25769803776},
  Config = {},
}
local live_args = dockerrun.build_args_from_inspect(inspect)
local target_args = {"--memory", "24g"}
local target_only, live_only = dockerrun.compute_drift(target_args, live_args)
info("target_only=" .. #target_only .. " live_only=" .. #live_only)
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out.String(), "target_only=0 live_only=0") {
		t.Fatalf("expected no drift for equal memory in different units, got %q", out.String())
	}
}

// TestDockerRunFormatDriftReportMasksSecrets proves the Lua-facing path
// (not just the pure Go function) never lets a secret env value reach the
// text a deploy prints or logs — admin-wxz0.
func TestDockerRunFormatDriftReportMasksSecrets(t *testing.T) {
	h, out, _ := newTestHost(t)
	err := h.Exec(`
local target_only = {"-e\tTAUTULLI_API_KEY=newkey"}
local live_only = {"-e\tTAUTULLI_API_KEY=oldkey"}
info(dockerrun.format_drift_report(target_only, live_only, {}, true))
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "newkey") || strings.Contains(got, "oldkey") {
		t.Fatalf("drift report reaching Lua leaked a secret: %q", got)
	}
	if !strings.Contains(got, "TAUTULLI_API_KEY") {
		t.Fatalf("drift report dropped the variable name entirely: %q", got)
	}
}

func TestDockerRunMaskEnvAssignment(t *testing.T) {
	h, out, _ := newTestHost(t)
	err := h.Exec(`
info(dockerrun.mask_env_assignment("DB_PASSWORD=hunter2"))
info(dockerrun.mask_env_assignment("PORT=8080"))
`, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "hunter2") {
		t.Fatalf("secret leaked through mask_env_assignment: %q", got)
	}
	if !strings.Contains(got, "PORT=8080") {
		t.Fatalf("non-secret value should pass through unchanged: %q", got)
	}
}
