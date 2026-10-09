package dockerrun

import (
	"reflect"
	"testing"
)

func TestPreflightArgvSharesRunConfigButNotPortsOrWrites(t *testing.T) {
	cfg := map[string]any{
		"restart": "unless-stopped",
		"user":    "99:100",
		"bind_mounts": []any{
			map[string]any{"host": "/mnt/appdata", "container": "/config", "mode": "rw"},
			map[string]any{"host": "/mnt/media", "container": "/media", "mode": "ro"},
		},
		"ports": []any{map[string]any{"host": float64(8409), "container": float64(8409)}},
		"env":   []any{map[string]any{"name": "ETV_ACCEL", "value": "vaapi"}},
		"preflight": map[string]any{
			"entrypoint": "etv-station",
			"args":       []any{"--config", "/config/station.yaml", "--check-config"},
		},
	}
	argv := BuildArgsFromCfg(cfg, map[string]string{}, nil, nil)
	got, err := PreflightArgv(cfg, argv, "etv-station:latest")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docker", "run", "--rm", "--entrypoint", "etv-station",
		"-v", "/mnt/appdata:/config:ro", "-v", "/mnt/media:/media:ro",
		"-e", "ETV_ACCEL=vaapi", "--user", "99:100",
		"etv-station:latest", "--config", "/config/station.yaml", "--check-config"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PreflightArgv\n got  %v\n want %v", got, want)
	}
}

func TestPreflightArgvUndeclaredAndIncomplete(t *testing.T) {
	if got, err := PreflightArgv(map[string]any{}, nil, "app"); got != nil || err != nil {
		t.Errorf("no [docker_run.preflight]: got (%v, %v), want (nil, nil)", got, err)
	}
	cfg := map[string]any{"preflight": map[string]any{"args": []any{"--check"}}}
	if _, err := PreflightArgv(cfg, nil, "app"); err == nil {
		t.Error("preflight with no entrypoint: want an error, got nil")
	}
}

func TestReadOnlyMount(t *testing.T) {
	cases := map[string]string{
		"/a:/b":          "/a:/b:ro",
		"/a:/b:rw":       "/a:/b:ro",
		"/a:/b:z":        "/a:/b:ro,z",
		"/a:/b:rw,Z":     "/a:/b:ro,Z",
		"/anonymous/vol": "/anonymous/vol",
	}
	for in, want := range cases {
		if got := readOnlyMount(in); got != want {
			t.Errorf("readOnlyMount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRollbackTag(t *testing.T) {
	cases := map[string]string{
		"etv-station:latest":            "etv-station:previous",
		"etv-station":                   "etv-station:previous",
		"registry:5000/me/app:v2":       "registry:5000/me/app:previous",
		"ghcr.io/me/app@sha256:abc1234": "ghcr.io/me/app:previous",
	}
	for in, want := range cases {
		if got := RollbackTag(in); got != want {
			t.Errorf("RollbackTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHealthProbeShellIsOneAttempt(t *testing.T) {
	cfg := map[string]any{"health": map[string]any{"url": "http://127.0.0.1:${PORT}/x", "timeout": float64(240), "interval": float64(5)}}
	env := map[string]string{"PORT": "8419"}
	want := `code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8419/x 2>/dev/null || true); [ "$code" = "200" ]`
	if got := HealthProbeShell(cfg, env, nil); got != want {
		t.Errorf("HealthProbeShell\n got  %s\n want %s", got, want)
	}
	if got := HealthCheckShell(cfg, env, nil); got != "for _ in $(seq 1 48); do "+want+" && exit 0; sleep 5; done; exit 1" {
		t.Errorf("HealthCheckShell changed shape: %s", got)
	}
	if got := HealthProbeShell(map[string]any{}, env, nil); got != "" {
		t.Errorf("no [docker_run.health]: got %q, want \"\"", got)
	}
}
