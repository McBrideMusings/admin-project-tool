package guards

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestRunRequiresFailFastOrder is this ticket's acceptance criterion
// verbatim: "Guards run fail-fast in listed order." Three guards are
// registered; the second fails, so the third must never run and the
// returned error must carry the second guard's message.
//
// MUTATION EVIDENCE: change RunRequires's `if !passed` to `if passed`
// (inverting the fail check) and re-run — the real failure was:
//
//	guards_test.go:46: error = "requires: guard \"a\" failed", want it to carry guard b's message
//	guards_test.go:49: called = [a], want [a b] (fail-fast: c never runs)
//
// i.e. the mutant treats a *passing* guard ("a") as the failure signal, so
// it stops after "a" and reports a synthetic failure for "a" itself — "b"
// (the actually-failing guard) and "c" never run at all — proving the test
// discriminates the fail-fast branch rather than merely checking a name.
func TestRunRequiresFailFastOrder(t *testing.T) {
	t.Cleanup(Reset)
	Reset()

	var called []string
	RegisterGuard("a", func() (bool, string) {
		called = append(called, "a")
		return true, ""
	})
	RegisterGuard("b", func() (bool, string) {
		called = append(called, "b")
		return false, "b: deliberately failing"
	})
	RegisterGuard("c", func() (bool, string) {
		called = append(called, "c")
		return true, ""
	})

	err := RunRequires([]string{"a", "b", "c"})
	if err == nil {
		t.Fatalf(`RunRequires([a b c]) = <nil>, want an error from guard "b"`)
	}
	if !strings.Contains(err.Error(), "b: deliberately failing") {
		t.Errorf("error = %q, want it to carry guard b's message", err.Error())
	}
	if want := []string{"a", "b"}; !equalStrings(called, want) {
		t.Errorf("called = %v, want %v (fail-fast: c never runs)", called, want)
	}
}

// TestRunRequiresAllPass proves an all-pass list returns nil and every
// guard in the list runs, in order.
func TestRunRequiresAllPass(t *testing.T) {
	t.Cleanup(Reset)
	Reset()

	var called []string
	RegisterGuard("a", func() (bool, string) { called = append(called, "a"); return true, "" })
	RegisterGuard("b", func() (bool, string) { called = append(called, "b"); return true, "" })

	if err := RunRequires([]string{"a", "b"}); err != nil {
		t.Fatalf("RunRequires([a b]) = %v, want nil", err)
	}
	if want := []string{"a", "b"}; !equalStrings(called, want) {
		t.Errorf("called = %v, want %v", called, want)
	}
}

// TestRunRequiresUnknownGuardIsError proves a name with no registered
// implementation is itself a failure — the runtime backstop for a name
// ValidateRequires should have already caught at resolve time.
func TestRunRequiresUnknownGuardIsError(t *testing.T) {
	t.Cleanup(Reset)
	Reset()
	if err := RunRequires([]string{"never-registered"}); err == nil {
		t.Fatal("RunRequires with an unknown guard name should error")
	}
}

// TestRunRequiresEmptyListPasses proves requires = [] runs no guards and
// passes — the acceptance criterion "requires = [] on a child runs no
// guards" at the primitive level (the multi-target-specific inherit/override
// behaviour is covered in internal/kinds).
func TestRunRequiresEmptyListPasses(t *testing.T) {
	if err := RunRequires(nil); err != nil {
		t.Fatalf("RunRequires(nil) = %v, want nil", err)
	}
	if err := RunRequires([]string{}); err != nil {
		t.Fatalf("RunRequires([]) = %v, want nil", err)
	}
}

// TestValidateRequiresCatchesUnknownActionGuard is the acceptance
// criterion "a guard name that resolves to neither a built-in nor a user
// table is a validation error."
func TestValidateRequiresCatchesUnknownActionGuard(t *testing.T) {
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"deploy": {"kind": "shell", "run": "true", "requires": []any{"no-such-guard"}},
		},
	}
	err := ValidateRequires(m)
	if err == nil {
		t.Fatal("ValidateRequires should reject an unknown guard reference")
	}
	if !strings.Contains(err.Error(), "no-such-guard") || !strings.Contains(err.Error(), "deploy") {
		t.Errorf("error %q should name the action and the unknown guard", err.Error())
	}
}

// TestValidateRequiresCatchesUnknownTargetGuard proves the same audit
// reaches into a kind="multi-target" action's inline per-target requires,
// not just the action-level default.
func TestValidateRequiresCatchesUnknownTargetGuard(t *testing.T) {
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"deploy": {
				"kind": "multi-target",
				"cf":   map[string]any{"run": "true", "requires": []any{"bogus"}},
			},
		},
	}
	err := ValidateRequires(m)
	if err == nil {
		t.Fatal("ValidateRequires should reject an unknown guard reference on a target")
	}
	if !strings.Contains(err.Error(), "bogus") || !strings.Contains(err.Error(), "cf") {
		t.Errorf("error %q should name the target and the unknown guard", err.Error())
	}
}

// TestValidateRequiresPassesForBuiltinsAndUserGuards proves both authoring
// paths resolve: a built-in name and a manifest-declared [guards.<name>].
func TestValidateRequiresPassesForBuiltinsAndUserGuards(t *testing.T) {
	m := &manifest.Manifest{
		Guards: map[string]manifest.Guard{
			"no-tag": {Run: "true", Fail: "tagged"},
		},
		Actions: map[string]manifest.Action{
			"deploy": {"kind": "shell", "run": "true", "requires": []any{"git-clean", "no-tag"}},
		},
	}
	if err := ValidateRequires(m); err != nil {
		t.Fatalf("ValidateRequires = %v, want nil", err)
	}
}

// TestValidateRequiresIgnoresInheritingTarget proves a target with no
// `requires` key at all is not flagged — absent means "inherit the parent",
// which is valid by construction (the parent's own requires, if any, was
// already validated).
func TestValidateRequiresIgnoresInheritingTarget(t *testing.T) {
	m := &manifest.Manifest{
		Actions: map[string]manifest.Action{
			"deploy": {
				"kind":     "multi-target",
				"requires": []any{"git-clean"},
				"cf":       map[string]any{"run": "true"}, // no requires: inherits
			},
		},
	}
	if err := ValidateRequires(m); err != nil {
		t.Fatalf("ValidateRequires = %v, want nil", err)
	}
}

// TestRequiresOfAbsentIsNil proves no `requires` key is "no requires", not
// an error.
func TestRequiresOfAbsentIsNil(t *testing.T) {
	reqs, err := RequiresOf(manifest.Action{"kind": "shell", "run": "true"})
	if err != nil {
		t.Fatalf("RequiresOf = %v, want nil error", err)
	}
	if len(reqs) != 0 {
		t.Errorf("RequiresOf = %v, want empty", reqs)
	}
}

// TestRequiresOfMalformedIsError proves a `requires` that isn't a list of
// strings is reported rather than silently ignored or panicking.
func TestRequiresOfMalformedIsError(t *testing.T) {
	if _, err := RequiresOf(manifest.Action{"requires": "not-a-list"}); err == nil {
		t.Fatal("RequiresOf with a non-list requires should error")
	}
	if _, err := RequiresOf(manifest.Action{"requires": []any{"ok", 5}}); err == nil {
		t.Fatal("RequiresOf with a non-string element should error")
	}
}

// TestRegisterWiresUserDefinedShellGuards proves Register makes a manifest's
// [guards.<name>] table runnable through RunRequires — the interpreter
// registration phase.
func TestRegisterWiresUserDefinedShellGuards(t *testing.T) {
	t.Cleanup(Reset)
	Reset()

	Register(map[string]manifest.Guard{
		"no-tag": {Run: "exit 1", Fail: "HEAD is already tagged — bump the version first."},
	})

	err := RunRequires([]string{"no-tag"})
	if err == nil {
		t.Fatal("RunRequires([no-tag]) should fail: the guard's run exits 1")
	}
	if !strings.Contains(err.Error(), "bump the version first") {
		t.Errorf("error %q should carry the static fail message", err.Error())
	}
}

// TestResetRestoresBuiltins proves Reset doesn't just clear the registry —
// it re-registers the three built-ins, so a test suite that calls Reset
// between cases never loses git-clean/git-pushed/deps-fresh.
func TestResetRestoresBuiltins(t *testing.T) {
	Reset()
	for name := range Builtin {
		if err := RunRequires([]string{name}); err == nil {
			// deps-fresh commonly no-ops true outside a node project; either
			// outcome (pass or a real diagnostic failure) proves the guard
			// ran rather than "unknown guard" — only the latter would prove
			// Reset failed to restore it.
			continue
		} else if strings.Contains(err.Error(), "unknown guard") {
			t.Errorf("built-in guard %q missing after Reset: %v", name, err)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
