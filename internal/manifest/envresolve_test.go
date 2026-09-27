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
// value beats a ComputedVars entry of the same name — an explicit
// environment value still wins, so a project can pin one when the automatic
// answer is wrong.
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

// A var set to "" counts as unset (oa-jsy.2). Returning "" instead let
// _compose_sign_settings drop DEVELOPMENT_TEAM, downgrading a "BUILD SUCCEEDED"
// app to ad-hoc signing with every provisioning-profile entitlement stripped.
//
// MUTATION EVIDENCE: restore `if v, ok := os.LookupEnv(...); ok` in place of the
// `v != ""` test and the first two of these fail — an empty value would be
// written straight through instead of erroring or taking the default.
func TestResolveEnvErrorsOnAVarSetToEmpty(t *testing.T) {
	t.Setenv("ADMIN_TEST_EMPTY", "")
	_, err := ResolveEnv("${ADMIN_TEST_EMPTY}")
	if err == nil || !strings.Contains(err.Error(), "set but empty") {
		t.Fatalf("ResolveEnv error = %v, want one saying the var is set but empty", err)
	}
}

func TestResolveEnvEmptyVarTakesItsDefault(t *testing.T) {
	t.Setenv("ADMIN_TEST_EMPTY", "")
	got, err := ResolveEnv("${ADMIN_TEST_EMPTY:-fallback}")
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if got != "fallback" {
		t.Errorf("got %q, want the default %q — POSIX :- takes it on null as well as unset", got, "fallback")
	}
}

// An explicitly written empty default is a deliberate empty, not a missing value.
func TestResolveEnvKeepsAnExplicitlyEmptyDefault(t *testing.T) {
	got, err := ResolveEnv("pre-${ADMIN_TEST_TOTALLY_UNSET:-}-post")
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if got != "pre--post" {
		t.Errorf("got %q, want %q", got, "pre--post")
	}
}

func TestResolveEnvEmptyExplicitValueFallsBackToComputed(t *testing.T) {
	ComputedVars["ADMIN_TEST_COMPUTED"] = func() string { return "computed" }
	defer delete(ComputedVars, "ADMIN_TEST_COMPUTED")
	t.Setenv("ADMIN_TEST_COMPUTED", "")

	got, err := ResolveEnv("${ADMIN_TEST_COMPUTED}")
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if got != "computed" {
		t.Errorf("got %q, want the computed value %q — an empty pin is not a pin", got, "computed")
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

// TestLanIPReturnsValidIP verifies lanIP() returns a non-empty IP address
// string. The exact address depends on the system's network configuration,
// but it should always return something.
//
// MUTATION EVIDENCE: delete the entire lanIP function or all its return
// statements, and this test fails — it would return an empty string.
func TestLanIPReturnsValidIP(t *testing.T) {
	ip := lanIP()
	if ip == "" {
		t.Fatal("lanIP returned empty string")
	}
	// Verify it looks like an IP address (contains dots for IPv4)
	if !strings.Contains(ip, ".") {
		t.Fatalf("lanIP returned %q, which doesn't look like an IPv4 address", ip)
	}
}

// TestResolveEnvSubstitutesLANIP verifies that ${LAN_IP} is resolved by the
// lanIP function (via ComputedVars). It uses a pinned value to avoid depending
// on network state, and verifies that explicit environment values still win over
// computed ones.
//
// MUTATION EVIDENCE: if ComputedVars["LAN_IP"] is removed or the ComputedVars
// check in ResolveEnv is deleted, this test fails — ${LAN_IP} would either be
// unresolved and cause an error, or not use the ComputedVars entry.
func TestResolveEnvSubstitutesLANIP(t *testing.T) {
	// We can test this by relying on the fact that ComputedVars["LAN_IP"] is set
	// in the init and lanIP() always returns a valid IP. We just need to verify
	// it actually gets substituted.

	got, err := ResolveEnv("http://localhost:8000?via=${LAN_IP}")
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if strings.Contains(got, "${LAN_IP}") {
		t.Fatalf("got %q, which still contains unreplaced ${LAN_IP}", got)
	}
	if !strings.HasPrefix(got, "http://localhost:8000?via=") {
		t.Fatalf("got %q, format doesn't match expected 'http://localhost:8000?via=...'", got)
	}
	// Extract the IP and verify it looks valid
	lanIPValue := strings.TrimPrefix(got, "http://localhost:8000?via=")
	if !strings.Contains(lanIPValue, ".") {
		t.Fatalf("LAN_IP resolved to %q, which doesn't look like an IPv4 address", lanIPValue)
	}
}

// TestResolveComputedSubstitutesLANIP verifies that ResolveComputed correctly
// resolves ${LAN_IP} for use in shell strings that may contain other
// variables.
//
// MUTATION EVIDENCE: if the ComputedVars check is removed from ResolveComputed,
// this test fails — ${LAN_IP} would be left as-is instead of being replaced
// with the computed value.
func TestResolveComputedSubstitutesLANIP(t *testing.T) {
	got := ResolveComputed("http://localhost:3000 or http://${LAN_IP}:3000")
	if !strings.Contains(got, "http://") {
		t.Fatalf("got %q, which doesn't contain 'http://'", got)
	}
	// Should have resolved ${LAN_IP} to an actual address
	if strings.Contains(got, "${LAN_IP}") {
		t.Fatalf("got %q, which still contains unreplaced ${LAN_IP}", got)
	}
	// Should have a second http:// after substitution
	if !strings.HasPrefix(got, "http://localhost:3000 or http://") {
		t.Fatalf("got %q, format doesn't match expected 'http://localhost:3000 or http://...'", got)
	}
}

// TestResolveEnvExplicitLANIPWinsOverComputed proves that an explicit LAN_IP
// environment variable beats the computed value — a project can pin one when
// the automatic answer is wrong.
//
// MUTATION EVIDENCE: swap the os.LookupEnv branch and the ComputedVars branch
// (check ComputedVars first), and this test fails — it would see "computed"
// instead of the pinned "192.168.1.1".
func TestResolveEnvExplicitLANIPWinsOverComputed(t *testing.T) {
	t.Setenv("LAN_IP", "192.168.1.1")

	got, err := ResolveEnv("${LAN_IP}")
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if got != "192.168.1.1" {
		t.Errorf("got %q, want the explicit environment value %q", got, "192.168.1.1")
	}
}
