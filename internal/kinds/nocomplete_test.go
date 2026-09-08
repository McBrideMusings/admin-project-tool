package kinds

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func TestValidateNoCompleteAcceptsBool(t *testing.T) {
	actions := map[string]manifest.Action{
		"logs": {"kind": "shell", "run": "tail -f app.log", "no_complete": true},
	}
	if err := ValidateNoComplete(actions); err != nil {
		t.Errorf("ValidateNoComplete() = %v, want nil", err)
	}
}

func TestValidateNoCompleteRejectsNonBoolOnAction(t *testing.T) {
	actions := map[string]manifest.Action{
		"logs": {"kind": "shell", "run": "tail -f app.log", "no_complete": "true"},
	}
	err := ValidateNoComplete(actions)
	if err == nil || !strings.Contains(err.Error(), "no_complete must be a boolean") {
		t.Errorf("ValidateNoComplete() = %v, want a 'no_complete must be a boolean' error", err)
	}
	if err != nil && !strings.Contains(err.Error(), `"logs"`) {
		t.Errorf("error = %v, want it to name the action", err)
	}
}
