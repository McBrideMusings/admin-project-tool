package kinds

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func stubFactory(name string, action manifest.Action, m *manifest.Manifest) (Runner, error) {
	return func(args []string) error { return nil }, nil
}

// TestRegisterUndeclaredKindFails proves invariant 1: registering a factory
// under a name absent from Kinds is an error, and MustRegister panics on it.
func TestRegisterUndeclaredKindFails(t *testing.T) {
	r := NewRegistry()
	err := r.Register("not-a-kind", stubFactory)
	if err == nil {
		t.Fatal("Register with an undeclared kind name should error")
	}
	if !strings.Contains(err.Error(), "not-a-kind") {
		t.Errorf("error %q should name the offending kind", err.Error())
	}

	defer func() {
		if recover() == nil {
			t.Error("MustRegister with an undeclared kind name should panic")
		}
	}()
	r.MustRegister("also-not-a-kind", stubFactory)
}

// TestDoubleRegistrationFails proves a second Register call for a kind that
// already has a bound factory is rejected, matching register_factory's
// copy-paste double-registration guard.
func TestDoubleRegistrationFails(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("shell", stubFactory); err != nil {
		t.Fatalf("first registration of shell should succeed, got %v", err)
	}
	err := r.Register("shell", stubFactory)
	if err == nil {
		t.Fatal("second registration of shell should error")
	}
	if !strings.Contains(err.Error(), "shell") {
		t.Errorf("error %q should name the kind", err.Error())
	}
}

// TestMissingFactoryFailsValidation proves invariant 2: a registry missing
// one declared kind's factory fails Validate with an error naming that
// kind — the exact call factories.go's init() panics on, exercised here
// without killing the test binary.
func TestMissingFactoryFailsValidation(t *testing.T) {
	r := NewRegistry()
	for name := range Kinds {
		if name == "shell" {
			continue // deliberately left unbound
		}
		r.MustRegister(name, stubFactory)
	}

	err := r.Validate()
	if err == nil {
		t.Fatal("Validate should error when a declared kind has no factory")
	}
	if !strings.Contains(err.Error(), "shell") {
		t.Errorf("error %q should name the unbound kind %q", err.Error(), "shell")
	}
}

// TestDefaultRegistryIsComplete proves the shipped registry (populated by
// factories.go's init()) has no gaps — if it did, the package would already
// have panicked on import and this test would never run, but this also
// checks it programmatically via the same Validate() path.
func TestDefaultRegistryIsComplete(t *testing.T) {
	if err := Default.Validate(); err != nil {
		t.Fatalf("Default.Validate() = %v, want nil", err)
	}
}

// TestStubFactoryErrorsAtDispatch proves the placeholder factories fail only
// when their Runner is actually invoked (dispatch time), never when the
// factory itself is built or registered (startup time). "apple-dev-loop" is
// used here rather than "shell" — admin-3rgp.35 replaced shell's stub with a
// real implementation, and admin-3rgp.15 replaced raylib's — so
// apple-dev-loop (still out of scope, ported by admin-3rgp.12 later) is now
// the representative still-a-stub kind.
func TestStubFactoryErrorsAtDispatch(t *testing.T) {
	f, ok := Default.Lookup("apple-dev-loop")
	if !ok {
		t.Fatal("apple-dev-loop should have a registered factory")
	}
	run, err := f("apple-dev-loop", manifest.Action{}, nil)
	if err != nil {
		t.Fatalf("building the runner should not itself error, got %v", err)
	}
	if err := run(nil); err == nil {
		t.Fatal("the stub runner should error when actually invoked")
	} else if !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("error %q should say not-yet-implemented", err.Error())
	}
}
