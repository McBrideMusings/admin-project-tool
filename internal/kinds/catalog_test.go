package kinds

import (
	"bufio"
	"os"
	"regexp"
	"testing"
)

// pythonKindNames reads admin_lib/kinds.py from the repo root and extracts
// every name passed to _kind("..."). Returns (nil, false) if the file can't
// be found — the test that uses this skips cleanly rather than failing, so a
// worktree layout change doesn't produce a false catalog-drift failure.
func pythonKindNames(t *testing.T) ([]string, bool) {
	t.Helper()
	path := "../../admin_lib/kinds.py"
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	re := regexp.MustCompile(`^_kind\("([^"]+)"`)
	var names []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if m := re.FindStringSubmatch(scanner.Text()); m != nil {
			names = append(names, m[1])
		}
	}
	return names, true
}

// TestCatalogMatchesPythonKinds proves acceptance criterion 1 ("every kind
// the Python KINDS catalog declared is declared here") by reading
// admin_lib/kinds.py directly, rather than asserting a hand-copied list that
// could drift from the source it was ported from.
func TestCatalogMatchesPythonKinds(t *testing.T) {
	pyNames, ok := pythonKindNames(t)
	if !ok {
		t.Skip("admin_lib/kinds.py not found relative to this test — skipping cross-check")
	}
	if len(pyNames) == 0 {
		t.Fatal("found admin_lib/kinds.py but extracted zero _kind(...) names — regex likely stale")
	}

	for _, name := range pyNames {
		if _, ok := Kinds[name]; !ok {
			t.Errorf("Python declares kind %q but internal/kinds.Kinds does not", name)
		}
	}
	for name := range Kinds {
		found := false
		for _, py := range pyNames {
			if py == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("internal/kinds.Kinds declares kind %q but Python admin_lib/kinds.py does not", name)
		}
	}
}

// TestPythonKindsStayDeclared guards against a regression this ticket's
// context explicitly calls out: an earlier draft of the epic proposed
// deleting python and python-script kinds. That was reversed — they stay
// declared like any other kind.
func TestPythonKindsStayDeclared(t *testing.T) {
	for _, name := range []string{"python", "python-script"} {
		if _, ok := Kinds[name]; !ok {
			t.Errorf("kind %q must stay declared in Kinds", name)
		}
	}
}

// TestDerivedViews asserts KnownKinds/ConfiglessKinds/TargetConsumingKinds/
// KindModules are genuinely computed from Kinds rather than hand-maintained
// alongside it.
func TestDerivedViews(t *testing.T) {
	if len(KnownKinds) != len(Kinds) {
		t.Fatalf("KnownKinds has %d entries, Kinds has %d", len(KnownKinds), len(Kinds))
	}
	for _, name := range KnownKinds {
		if _, ok := Kinds[name]; !ok {
			t.Errorf("KnownKinds contains %q which is not in Kinds", name)
		}
	}

	// A few representative facts, matching admin_lib/kinds.py's _kind() calls.
	configless := map[string]bool{}
	for _, n := range ConfiglessKinds {
		configless[n] = true
	}
	if !configless["clean-dev"] {
		t.Error("clean-dev should be configless")
	}
	if configless["shell"] {
		t.Error("shell should not be configless")
	}

	targetConsuming := map[string]bool{}
	for _, n := range TargetConsumingKinds {
		targetConsuming[n] = true
	}
	if !targetConsuming["multi-target"] || !targetConsuming["apple-dev-loop"] {
		t.Error("multi-target and apple-dev-loop should consume targets")
	}
	if targetConsuming["shell"] {
		t.Error("shell should not consume targets")
	}

	if mods, ok := KindModules["docker-dev"]; !ok || len(mods) != 1 || mods[0] != "docker" {
		t.Errorf("docker-dev modules = %v, want [docker]", mods)
	}
	if _, ok := KindModules["shell"]; ok {
		t.Error("shell has no modules and should be absent from KindModules")
	}

	k, ok := Declared("bridge-listen")
	if !ok {
		t.Fatal("bridge-listen should be declared")
	}
	if len(k.Modules) != 5 {
		t.Errorf("bridge-listen modules = %v, want 5 entries", k.Modules)
	}

	if _, ok := Declared("not-a-real-kind"); ok {
		t.Error("Declared should report false for an undeclared name")
	}
}
