package validate

import (
	"errors"
	"strings"
	"testing"
)

// TestCommandRoutingCleanPasses proves a command with only known keys and
// steps validates clean.
func TestCommandRoutingCleanPasses(t *testing.T) {
	if err := CommandRouting(`command "deploy"`, nil, true); err != nil {
		t.Fatalf("CommandRouting = %v, want nil", err)
	}
}

// TestCommandRoutingRejectsKindRunTargets is this ticket's acceptance
// criterion: "A command containing kind, run or targets is rejected." The
// error must name the rule (ADR-0003) and every offending key.
//
// MUTATION EVIDENCE: with the ADR-0003 hint's key set narrowed to just
// {"kind"} (dropping "run" and "targets" from the trigger check), this test
// still passes (the message still says "invalid key(s) kind, run, targets"
// because extraKeys itself is unconditionally reported) — so the mutation
// that actually breaks this test is deleting the invalid-key report
// entirely:
//
//	err := CommandRouting(`command "deploy"`, []string{"kind", "run", "targets"}, true)
//	if len(problems) == 0 { return nil }  // <- mutated to always return nil
//
// Ran with `if len(problems) == 0 { return nil }` replaced by
// `return nil` unconditionally at the top of CommandRouting: the test
// failed with:
//
//	CommandRouting(kind,run,targets) = <nil>, want a non-nil error naming ADR-0003
//
// (reverted after confirming the failure).
func TestCommandRoutingRejectsKindRunTargets(t *testing.T) {
	err := CommandRouting(`command "deploy"`, []string{"kind", "run", "targets"}, true)
	if err == nil {
		t.Fatal("CommandRouting(kind,run,targets) = <nil>, want a non-nil error naming ADR-0003")
	}
	got := err.Error()
	for _, want := range []string{"kind", "run", "targets", "ADR-0003", `command "deploy"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q does not mention %q", got, want)
		}
	}
}

// TestCommandRoutingRequiresSteps proves a command with no steps is
// rejected even when it carries no other extra keys.
func TestCommandRoutingRequiresSteps(t *testing.T) {
	err := CommandRouting(`command "empty"`, nil, false)
	if err == nil {
		t.Fatal("CommandRouting with hasSteps=false = nil, want an error")
	}
	if !strings.Contains(err.Error(), "steps") {
		t.Fatalf("error %q does not mention steps", err.Error())
	}
}

// TestFooterAcceptsBoolRejectsString is the ticket's "footer is a real
// bool" acceptance criterion — TOML has no truthy strings, so
// footer = "true" must be rejected as a typo, not silently accepted.
func TestFooterAcceptsBoolRejectsString(t *testing.T) {
	if err := Footer(`action "dev"`, true); err != nil {
		t.Fatalf("Footer(true) = %v, want nil", err)
	}
	if err := Footer(`action "dev"`, false); err != nil {
		t.Fatalf("Footer(false) = %v, want nil", err)
	}
	err := Footer(`action "dev"`, "true")
	if err == nil {
		t.Fatal(`Footer("true") = nil, want an error`)
	}
	if !strings.Contains(err.Error(), "'footer' must be true or false") {
		t.Fatalf("error %q does not name the rule", err.Error())
	}
}

// TestErrorIsDistinguishableFromAGenericError is this ticket's acceptance
// criterion: "Every validation failure is distinguishable as a validation
// error rather than a generic one." errors.As must find the *Error inside
// whatever a caller wraps it with, and must NOT match an unrelated error.
func TestErrorIsDistinguishableFromAGenericError(t *testing.T) {
	var ve *Error
	err := CommandRouting(`command "x"`, []string{"run"}, true)
	if !errors.As(error(err), &ve) {
		t.Fatal("errors.As did not find *validate.Error in a CommandRouting result")
	}

	generic := errors.New("some unrelated failure")
	var ve2 *Error
	if errors.As(generic, &ve2) {
		t.Fatal("errors.As matched *validate.Error against an unrelated plain error")
	}
}

// TestWrapIsIdempotentAndPreservesMessage proves Wrap tags a plain error as
// a validation error without altering its text, and does not double-wrap
// one that already is one.
func TestWrapIsIdempotentAndPreservesMessage(t *testing.T) {
	plain := errors.New("boom")
	wrapped := Wrap(plain)
	if wrapped.Error() != "boom" {
		t.Fatalf("Wrap(plain).Error() = %q, want %q", wrapped.Error(), "boom")
	}
	again := Wrap(wrapped)
	if again != wrapped {
		t.Fatal("Wrap(*Error) should return the same *Error, not re-wrap it")
	}
	if Wrap(nil) != nil {
		t.Fatal("Wrap(nil) should be nil")
	}
}
