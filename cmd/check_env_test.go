package cmd

import (
	"reflect"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func TestUnresolvedConfigEnvRefs(t *testing.T) {
	t.Setenv("CHECK_ENV_SET", "value")
	t.Setenv("CHECK_ENV_EMPTY", "")
	m := &manifest.Manifest{Settings: map[string]map[string]any{
		"apple": {
			"project_path":     "${CHECK_ENV_UNSET}/App.xcodeproj",
			"development_team": "${CHECK_ENV_EMPTY}",
			"scheme":           "${CHECK_ENV_SET}",
			"bundle":           "${CHECK_ENV_UNSET_WITH_DEFAULT:-com.example}",
			"sign":             "${CHECK_ENV_EXPLICIT_EMPTY:-}",
		},
		"docker_run": {
			"env": []any{map[string]any{"name": "TOKEN", "value": "${CHECK_ENV_UNSET}"}},
		},
		// Not a config table ResolveEnv reads: a shell-local here is legal.
		"terminal": {"spawn": "${CHECK_ENV_UNSET}"},
	}}

	got := unresolvedConfigEnvRefs(m)
	want := []unresolvedEnvRef{
		{Var: "CHECK_ENV_EMPTY", Origin: "apple.development_team", State: "set but empty"},
		{Var: "CHECK_ENV_UNSET", Origin: "apple.project_path", State: "not set"},
		{Var: "CHECK_ENV_UNSET", Origin: "docker_run.env[0].value", State: "not set"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unresolvedConfigEnvRefs =\n  %+v\nwant\n  %+v", got, want)
	}
}

func TestDockerBuildArgsComputedRefsAreNotUnresolved(t *testing.T) {
	t.Setenv("CHECK_ENV_UNSET", "")
	m := &manifest.Manifest{Settings: map[string]map[string]any{
		"docker_build": {"args": map[string]any{
			"REV": "${GIT_REVISION}", "AT": "${BUILD_TIME}", "BAD": "${CHECK_ENV_UNSET}",
		}},
	}}
	got := unresolvedConfigEnvRefs(m)
	want := []unresolvedEnvRef{{Var: "CHECK_ENV_UNSET", Origin: "docker_build.args.BAD", State: "set but empty"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unresolvedConfigEnvRefs = %+v, want %+v", got, want)
	}
}
