package kinds

import (
	"strings"
	"testing"
)

// TestBuildDockerDevCmdRequiresImageTag proves image_tag is required.
func TestBuildDockerDevCmdRequiresImageTag(t *testing.T) {
	if _, err := buildDockerDevCmd(map[string]any{}); err == nil {
		t.Fatal("buildDockerDevCmd with no image_tag should error")
	}
}

// TestBuildDockerDevCmdRendersPortsMountsAndEnv proves the rendered script
// carries a -p flag mapping host to container port, in that order, plus a
// bind-mount -v flag and a literal -e flag — the core of
// buildDockerDevCmd's schema.
//
// MUTATION EVIDENCE: swap `hostPort+":"+contPort` for `contPort+":"+hostPort`
// in buildDockerDevCmd's ports loop and this test fails: it asserts on
// "-p 8080:80" (host:container) and would instead see "-p 80:8080" —
// verified by hand-editing the swap in, re-running (got a failure reporting
// the script did not contain "-p 8080:80"), then reverting.
func TestBuildDockerDevCmdRendersPortsMountsAndEnv(t *testing.T) {
	cfg := map[string]any{
		"image_tag": "myapp:dev",
		"ports": []any{
			map[string]any{"host": int64(8080), "container": int64(80)},
		},
		"bind_mounts": []any{
			map[string]any{"host": "src", "container": "/app/src"},
		},
		"env": []any{
			map[string]any{"name": "FOO", "value": "bar"},
		},
	}
	got, err := buildDockerDevCmd(cfg)
	if err != nil {
		t.Fatalf("buildDockerDevCmd: %v", err)
	}
	if !strings.Contains(got, "-p 8080:80") {
		t.Errorf("script missing '-p 8080:80':\n%s", got)
	}
	if !strings.Contains(got, ":/app/src") {
		t.Errorf("script missing bind mount to /app/src:\n%s", got)
	}
	if !strings.Contains(got, "-e FOO=bar") {
		t.Errorf("script missing '-e FOO=bar':\n%s", got)
	}
	if !strings.Contains(got, "docker build") || !strings.Contains(got, "myapp:dev") {
		t.Errorf("script missing docker build of the tagged image:\n%s", got)
	}
}

// TestBuildDockerDevCmdPortDefaultsToHostPort proves an omitted `container`
// port falls back to the host port.
func TestBuildDockerDevCmdPortDefaultsToHostPort(t *testing.T) {
	cfg := map[string]any{
		"image_tag": "myapp:dev",
		"ports":     []any{map[string]any{"host": int64(3000)}},
	}
	got, err := buildDockerDevCmd(cfg)
	if err != nil {
		t.Fatalf("buildDockerDevCmd: %v", err)
	}
	if !strings.Contains(got, "-p 3000:3000") {
		t.Errorf("script missing '-p 3000:3000':\n%s", got)
	}
}

// TestBuildDockerDevCmdGPUAll proves gpu=true renders "--gpus all".
func TestBuildDockerDevCmdGPUAll(t *testing.T) {
	got, err := buildDockerDevCmd(map[string]any{"image_tag": "x", "gpu": true})
	if err != nil {
		t.Fatalf("buildDockerDevCmd: %v", err)
	}
	if !strings.Contains(got, "--gpus all") {
		t.Errorf("script missing '--gpus all':\n%s", got)
	}
}

// TestBuildDockerDevCmdOptionalMountIsConditional proves an optional_mounts
// entry renders as a runtime shell conditional (checked at container-launch
// time, not baked in) rather than an unconditional -v flag — env expansion
// for this field is deferred to the shell.
func TestBuildDockerDevCmdOptionalMountIsConditional(t *testing.T) {
	cfg := map[string]any{
		"image_tag": "x",
		"optional_mounts": []any{
			map[string]any{"env": "DATA_DIR", "container": "/data"},
		},
	}
	got, err := buildDockerDevCmd(cfg)
	if err != nil {
		t.Fatalf("buildDockerDevCmd: %v", err)
	}
	if !strings.Contains(got, `${DATA_DIR:-}`) || !strings.Contains(got, "/data") {
		t.Errorf("script missing conditional optional-mount shell:\n%s", got)
	}
}
