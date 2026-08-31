package manifest

import (
	"strings"
	"testing"
)

func TestResolveEnvSubstitutesFromEnvironment(t *testing.T) {
	t.Setenv("ADMIN_TEST_VAR", "hello")
	got, err := ResolveEnv("prefix-${ADMIN_TEST_VAR}-suffix")
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if got != "prefix-hello-suffix" {
		t.Errorf("got %q, want %q", got, "prefix-hello-suffix")
	}
}

func TestResolveEnvUsesDefaultWhenUnset(t *testing.T) {
	got, err := ResolveEnv("${ADMIN_TEST_TOTALLY_UNSET:-fallback}")
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if got != "fallback" {
		t.Errorf("got %q, want %q", got, "fallback")
	}
}

func TestResolveEnvErrorsOnUnresolvedRequired(t *testing.T) {
	_, err := ResolveEnv("${ADMIN_TEST_TOTALLY_UNSET}")
	if err == nil || !strings.Contains(err.Error(), "ADMIN_TEST_TOTALLY_UNSET") {
		t.Fatalf("ResolveEnv error = %v, want an error naming the unset var", err)
	}
}

// TestResolveEnvExplicitValueWinsOverComputed proves an explicit environment
// value beats a ComputedVars entry of the same name — "an explicit
// environment value still wins, so a project can pin one when the automatic
// answer is wrong" (docs/architecture reference, ported from
// admin_lib/core.py::resolve_env's own comment).
//
// MUTATION EVIDENCE: swap the os.LookupEnv branch and the ComputedVars
// branch's priority (check ComputedVars first), and this test fails — it
// would see "computed" instead of the environment override "pinned".
func TestResolveEnvExplicitValueWinsOverComputed(t *testing.T) {
	ComputedVars["ADMIN_TEST_COMPUTED"] = func() string { return "computed" }
	defer delete(ComputedVars, "ADMIN_TEST_COMPUTED")
	t.Setenv("ADMIN_TEST_COMPUTED", "pinned")

	got, err := ResolveEnv("${ADMIN_TEST_COMPUTED}")
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if got != "pinned" {
		t.Errorf("got %q, want the explicit environment value %q", got, "pinned")
	}
}

func TestResolveEnvNoPlaceholderIsUnchanged(t *testing.T) {
	got, err := ResolveEnv("plain string, no placeholders")
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if got != "plain string, no placeholders" {
		t.Errorf("got %q, want the input unchanged", got)
	}
}

// TestResolveComputedLeavesPlainVarsAlone is the whole reason
// ResolveComputed exists (per its own doc comment): a shell-local `${x}`
// must reach the shell verbatim, not be resolved or rejected here.
//
// MUTATION EVIDENCE: replace ResolveComputed's body with a call to
// ResolveEnv (ignoring the error), and this test fails — ResolveEnv has no
// default and no ComputedVars entry for PLAIN_SHELL_VAR, so it would panic
// on the ignored error's zero value or (if the error is surfaced) turn a
// pass-through case into a hard failure.
func TestResolveComputedLeavesPlainVarsAlone(t *testing.T) {
	got := ResolveComputed("echo ${PLAIN_SHELL_VAR}")
	if got != "echo ${PLAIN_SHELL_VAR}" {
		t.Errorf("got %q, want the placeholder passed through unchanged", got)
	}
}

func TestResolveComputedSubstitutesComputedVar(t *testing.T) {
	ComputedVars["ADMIN_TEST_COMPUTED2"] = func() string { return "computed-value" }
	defer delete(ComputedVars, "ADMIN_TEST_COMPUTED2")

	got := ResolveComputed("host=${ADMIN_TEST_COMPUTED2}")
	if got != "host=computed-value" {
		t.Errorf("got %q, want the computed value substituted", got)
	}
}

func TestResolveComputedNoPlaceholderIsUnchanged(t *testing.T) {
	got := ResolveComputed("no placeholders here")
	if got != "no placeholders here" {
		t.Errorf("got %q, want the input unchanged", got)
	}
}
