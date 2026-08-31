package dockerrun

import (
	"reflect"
	"testing"
)

func TestExpandEnvRefs(t *testing.T) {
	env := map[string]string{"FOO": "bar"}
	cases := []struct {
		in       string
		wantOut  string
		wantOk   bool
	}{
		{"plain", "plain", true},
		{"${FOO}", "bar", true},
		{"${MISSING}", "${MISSING}", false},
		{"${MISSING:-fallback}", "fallback", true},
		{"${MISSING:-${FOO}}", "bar", true},
		{"prefix-${FOO}-suffix", "prefix-bar-suffix", true},
	}
	for _, c := range cases {
		got, ok := ExpandEnvRefs(c.in, env)
		if got != c.wantOut || ok != c.wantOk {
			t.Errorf("ExpandEnvRefs(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.wantOut, c.wantOk)
		}
	}
}

func TestBuildArgsFromCfgBindMounts(t *testing.T) {
	cfg := map[string]any{
		"bind_mounts": []any{
			map[string]any{"host": "/mnt/data", "container": "/data"},
			map[string]any{"host": "/mnt/ro", "container": "/ro", "mode": "ro"},
		},
		"ports": []any{
			map[string]any{"host": float64(8080), "container": float64(80)},
		},
	}
	args := BuildArgsFromCfg(cfg, map[string]string{}, nil, nil)
	want := []string{"-v", "/mnt/data:/data", "-v", "/mnt/ro:/ro:ro", "-p", "8080:80/tcp"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("got %v, want %v", args, want)
	}
}

func TestBuildArgsFromCfgUserExpand(t *testing.T) {
	cfg := map[string]any{"user": "${APP_UID}"}
	envVars := map[string]string{"APP_UID": "99:100"}
	args := BuildArgsFromCfg(cfg, envVars, nil, nil)
	want := []string{"--user", "99:100"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("got %v, want %v", args, want)
	}

	// Unresolved var: the field is dropped, not rendered as a literal
	// "${APP_UID}" that would poison the drift comparison.
	args = BuildArgsFromCfg(cfg, map[string]string{}, nil, nil)
	if len(args) != 0 {
		t.Errorf("expected no args for unresolved ${APP_UID}, got %v", args)
	}
}

func TestBuildArgsFromInspectRoundTrip(t *testing.T) {
	inspect := map[string]any{
		"HostConfig": map[string]any{
			"Binds":        []any{"/mnt/data:/data"},
			"PortBindings": map[string]any{"80/tcp": []any{map[string]any{"HostIp": "", "HostPort": "8080"}}},
			"RestartPolicy": map[string]any{"Name": "unless-stopped"},
			"Privileged":   true,
			"Memory":       float64(25769803776),
		},
		"Config": map[string]any{
			"Env":  []any{"FOO=bar"},
			"User": "99:100",
		},
	}
	args := BuildArgsFromInspect(inspect)
	joined := ArgvToPairSet(args)
	for _, want := range []string{"-v\t/mnt/data:/data", "-p\t8080:80/tcp", "--restart\tunless-stopped",
		"--privileged\t", "--memory\t25769803776", "-e\tFOO=bar", "--user\t99:100"} {
		if !joined[want] {
			t.Errorf("missing expected pair %q in %v", want, args)
		}
	}
}

func TestComputeDriftMemoryUnitsDoNotFalsePositive(t *testing.T) {
	target := []string{"--memory", "24g"}
	live := []string{"--memory", "25769803776"}
	inTargetOnly, inLiveOnly := ComputeDrift(target, live)
	if len(inTargetOnly) != 0 || len(inLiveOnly) != 0 {
		t.Errorf("expected no drift for equal memory in different units, got target=%v live=%v", inTargetOnly, inLiveOnly)
	}
}

func TestComputeDriftRealDivergence(t *testing.T) {
	target := []string{"--memory", "24g", "-e", "FOO=bar"}
	live := []string{"--memory", "24g", "-e", "FOO=baz"}
	inTargetOnly, inLiveOnly := ComputeDrift(target, live)
	if !reflect.DeepEqual(inTargetOnly, []string{"-e\tFOO=bar"}) {
		t.Errorf("inTargetOnly = %v", inTargetOnly)
	}
	if !reflect.DeepEqual(inLiveOnly, []string{"-e\tFOO=baz"}) {
		t.Errorf("inLiveOnly = %v", inLiveOnly)
	}
}

func TestArgvPairAtBooleanAndNegativeValue(t *testing.T) {
	argv := []string{"--privileged", "--ulimit", "core=-1"}
	flag, val, hasVal, next := ArgvPairAt(argv, 0)
	if flag != "--privileged" || hasVal || next != 1 {
		t.Fatalf("boolean flag parsed wrong: %q %q %v %d", flag, val, hasVal, next)
	}
	flag, val, hasVal, next = ArgvPairAt(argv, 1)
	if flag != "--ulimit" || val != "core=-1" || !hasVal || next != 3 {
		t.Fatalf("value starting with '-' must still be paired, got %q %q %v %d", flag, val, hasVal, next)
	}
}

func TestLiveArgvForDriftStripsImageDefaults(t *testing.T) {
	inspect := map[string]any{
		"HostConfig": map[string]any{"NetworkMode": "bridge"},
		"Config":     map[string]any{"WorkingDir": "/app", "User": "appuser", "Env": []any{"BAKED=1"}},
	}
	args := LiveArgvForDrift(inspect, []string{"BAKED=1"}, nil, "/app", "appuser", map[string]any{})
	// workdir/user match the image defaults and cfg doesn't declare them ->
	// stripped. BAKED env var is image-baked -> stripped. --network bridge
	// is the implicit default -> stripped. Net result: nothing left.
	if len(args) != 0 {
		t.Errorf("expected fully-stripped live argv, got %v", args)
	}
}

func TestLiveArgvForDriftKeepsDeclaredMismatch(t *testing.T) {
	inspect := map[string]any{
		"HostConfig": map[string]any{},
		"Config":     map[string]any{"WorkingDir": "/other"},
	}
	// cfg declares workdir, so the image_field auto-strip does not apply —
	// a real mismatch between cfg's workdir and live's workdir must survive
	// into the argv for _compute_drift to catch.
	args := LiveArgvForDrift(inspect, nil, nil, "/app", "", map[string]any{"workdir": "/app"})
	want := []string{"--workdir", "/other"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("got %v, want %v", args, want)
	}
}

func TestIsSecretEnvName(t *testing.T) {
	for name, want := range map[string]bool{
		"TAUTULLI_API_KEY": true,
		"DB_PASSWORD":       true,
		"AUTH_TOKEN":        true,
		"APP_SECRET":        true,
		"PORT":              false,
		"APP_UID":           false,
	} {
		if got := IsSecretEnvName(name); got != want {
			t.Errorf("IsSecretEnvName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestMaskArgvForDisplay(t *testing.T) {
	argv := []string{"-e", "TAUTULLI_API_KEY=secretvalue", "-e", "PORT=8080"}
	masked := MaskArgvForDisplay(argv)
	want := []string{"-e", "TAUTULLI_API_KEY=" + MaskValue, "-e", "PORT=8080"}
	if !reflect.DeepEqual(masked, want) {
		t.Errorf("got %v, want %v", masked, want)
	}
	// Original must be untouched — display-only.
	if argv[1] != "TAUTULLI_API_KEY=secretvalue" {
		t.Errorf("MaskArgvForDisplay mutated its input: %v", argv)
	}
}

func TestFormatDriftReportMasksSecrets(t *testing.T) {
	inTargetOnly := []string{"-e\tTAUTULLI_API_KEY=newkey"}
	inLiveOnly := []string{"-e\tTAUTULLI_API_KEY=oldkey"}
	report := FormatDriftReport(inTargetOnly, inLiveOnly, true)
	if containsSubstr(report, "newkey") || containsSubstr(report, "oldkey") {
		t.Errorf("drift report leaked a secret value:\n%s", report)
	}
	if !containsSubstr(report, MaskValue) {
		t.Errorf("drift report did not mask the secret:\n%s", report)
	}
}

func containsSubstr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestNormalizeMemBytes(t *testing.T) {
	cases := map[string]string{
		"24g":  "25769803776",
		"1.5g": "1610612736",
		"512m": "536870912",
		"1024": "1024",
		"bogus": "bogus",
	}
	for in, want := range cases {
		if got := NormalizeMemBytes(in); got != want {
			t.Errorf("NormalizeMemBytes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultContainerName(t *testing.T) {
	cases := map[string]string{
		"myapp:latest":           "myapp",
		"registry.io/org/myapp":  "myapp",
		"myapp":                  "myapp",
	}
	for in, want := range cases {
		if got := DefaultContainerName(in); got != want {
			t.Errorf("DefaultContainerName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseEnvText(t *testing.T) {
	text := "# comment\nFOO=bar\n\nBAZ = quoted \"value\"\n"
	got := ParseEnvText(text)
	want := map[string]string{"FOO": "bar", "BAZ": `quoted "value"`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMountEnvDeps(t *testing.T) {
	cfg := map[string]any{
		"optional_mounts": []any{map[string]any{"env": "PLEX_DIR", "container": "/plex"}},
		"bind_mounts":     []any{map[string]any{"host": "${APPDATA}/x", "container": "/x"}},
	}
	got := MountEnvDeps(cfg)
	want := []string{"PLEX_DIR", "APPDATA"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
