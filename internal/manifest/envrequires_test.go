package manifest

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseEnvRequires(t *testing.T) {
	m, err := Parse(map[string]any{
		"project_name": "p",
		"env":          map[string]any{"requires": []any{"STASH_TOKEN", "UNRAID_HOST"}},
	}, "admin.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if want := []string{"STASH_TOKEN", "UNRAID_HOST"}; !reflect.DeepEqual(m.EnvRequires, want) {
		t.Errorf("EnvRequires = %v, want %v", m.EnvRequires, want)
	}
	if _, ok := m.Settings["env"]; ok {
		t.Error("[env] landed in Settings; it is a structural table")
	}
}

func TestParseEnvRejectsBadShapes(t *testing.T) {
	for name, env := range map[string]any{
		"unknown key": map[string]any{"needs": []any{"X"}},
		"not a list":  map[string]any{"requires": "X"},
		"not a name":  map[string]any{"requires": []any{"export X"}},
		"not a table": "X",
	} {
		if _, err := Parse(map[string]any{"project_name": "p", "env": env}, "admin.toml"); err == nil {
			t.Errorf("%s: Parse = nil error, want a rejection", name)
		}
	}
}

func TestMissingEnvCountsEmptyAsMissing(t *testing.T) {
	t.Setenv("ADMIN_T_SET", "v")
	t.Setenv("ADMIN_T_EMPTY", "")
	got := MissingEnv([]string{"ADMIN_T_SET", "ADMIN_T_EMPTY", "ADMIN_T_NEVER_SET_X9"})
	if want := []string{"ADMIN_T_EMPTY", "ADMIN_T_NEVER_SET_X9"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MissingEnv = %v, want %v", got, want)
	}
	err := CheckEnvRequires([]string{"ADMIN_T_SET", "ADMIN_T_EMPTY", "ADMIN_T_NEVER_SET_X9"})
	if err == nil || !strings.Contains(err.Error(), "ADMIN_T_EMPTY, ADMIN_T_NEVER_SET_X9") {
		t.Errorf("CheckEnvRequires = %v, want both names listed", err)
	}
	if err := CheckEnvRequires([]string{"ADMIN_T_SET"}); err != nil {
		t.Errorf("CheckEnvRequires(set) = %v, want nil", err)
	}
}
