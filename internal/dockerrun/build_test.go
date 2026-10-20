package dockerrun

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func initRepo(t *testing.T) (dir, sha string) {
	t.Helper()
	dir = t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(out))
}

func TestBuildImageCommandWithoutArgsIsUnchanged(t *testing.T) {
	cases := []struct {
		name string
		p    BuildParams
		want string
	}{
		{"local", BuildParams{Image: "app:latest"}, `docker build --platform linux/amd64 -t 'app:latest' .`},
		{"local with dockerfile and target", BuildParams{
			BuildCfg: map[string]any{"dockerfile": "deploy/Dockerfile"}, Target: "prod", Image: "app:latest",
		}, `docker build --platform linux/amd64 -f 'deploy/Dockerfile' --target 'prod' -t 'app:latest' .`},
		{"on_host local", BuildParams{OnHost: true, Context: "/src", Image: "app", IsLocal: true},
			`cd '/src' && docker build -t 'app' .`},
		{"on_host remote", BuildParams{OnHost: true, Context: "/src", Image: "app", Host: "root@h"},
			`ssh 'root@h' 'cd '\''/src'\'' && docker build -t '\''app'\'' .'`},
	}
	for _, c := range cases {
		got, err := BuildImageCommand(c.p)
		if err != nil || got != c.want {
			t.Errorf("%s: got (%q, %v), want %q", c.name, got, err, c.want)
		}
	}
}

func TestBuildImageCommandCarriesBuildArgs(t *testing.T) {
	dir, sha := initRepo(t)
	t.Setenv("BUILD_ARG_TEST_TAG", "v1")
	cfg := map[string]any{"args": map[string]any{
		"ZED":     "${GIT_REVISION}",
		"CREATED": "${BUILD_TIME}",
		"TAG":     "${BUILD_ARG_TEST_TAG}",
		"DEFAULT": "${BUILD_ARG_TEST_UNSET:-fallback}",
		"QUOTED":  "it's",
	}}
	now := time.Date(2026, 10, 20, 1, 23, 45, 0, time.UTC)
	want := "--build-arg 'CREATED=2026-10-20T01:23:45Z' --build-arg 'DEFAULT=fallback' " +
		"--build-arg 'QUOTED=it'\\''s' --build-arg 'TAG=v1' --build-arg 'ZED=" + sha + "' "

	local, err := BuildImageCommand(BuildParams{BuildCfg: cfg, Image: "app", ProjectRoot: dir, Now: now})
	if err != nil || !strings.Contains(local, want+"-t 'app' .") {
		t.Fatalf("local build: got (%q, %v), want it to contain %q", local, err, want)
	}
	onHost, err := BuildImageCommand(BuildParams{BuildCfg: cfg, Image: "app", ProjectRoot: dir, Now: now,
		OnHost: true, Context: "/src", IsLocal: true})
	if err != nil || !strings.Contains(onHost, want+"-t 'app' .") {
		t.Fatalf("on_host build: got (%q, %v), want it to contain %q", onHost, err, want)
	}
}

func TestBuildImageCommandRejectsUnresolvedReference(t *testing.T) {
	cfg := map[string]any{"args": map[string]any{"REV": "${BUILD_ARG_TEST_UNSET}"}}
	got, err := BuildImageCommand(BuildParams{BuildCfg: cfg, Image: "app", ProjectRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "args.REV") {
		t.Fatalf("got (%q, %v), want an error naming args.REV", got, err)
	}
}

func TestBuildArgGitRevisionUnresolvedWhenGitFails(t *testing.T) {
	cfg := map[string]any{"args": map[string]any{"REV": "${GIT_REVISION}", "SAFE": "${GIT_REVISION:-unknown}"}}
	// t.TempDir() is not a git repository.
	if _, err := BuildImageCommand(BuildParams{BuildCfg: cfg, Image: "app", ProjectRoot: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "args.REV") {
		t.Fatalf("want an error naming args.REV, got %v", err)
	}
	cfg = map[string]any{"args": map[string]any{"SAFE": "${GIT_REVISION:-unknown}"}}
	got, err := BuildImageCommand(BuildParams{BuildCfg: cfg, Image: "app", ProjectRoot: t.TempDir()})
	if err != nil || !strings.Contains(got, "--build-arg 'SAFE=unknown'") {
		t.Fatalf("got (%q, %v), want SAFE=unknown", got, err)
	}
}
