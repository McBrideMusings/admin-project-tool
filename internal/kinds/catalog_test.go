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

// adr0010Divergence is the deliberate, permanent differences between this
// catalog and admin_lib/kinds.py: ADR-0010 deletes kind="python" and
// kind="python-script" from the Go side and adds kind="lua" in their place.
// admin_lib/kinds.py is not migrated kind-by-kind — it is deleted wholesale
// by admin-3rgp.30 — so it still declares the two Python kinds and knows
// nothing of "lua". kind="workflow" (admin-3rgp.24) is a second, unrelated
// addition: a brand-new Go-only kind with no Python counterpart at all
// (there was never a workflow concept in admin_lib), not a port of anything
// pythonKindNames could ever find. TestCatalogMatchesPythonKinds accounts
// for both gaps rather than either ignoring the cross-check or asserting a
// false exact-match.
var adr0010Divergence = struct {
	removedFromGo []string // declared in Python, deliberately absent from Go
	addedInGo     []string // declared in Go, deliberately absent from Python
}{
	removedFromGo: []string{"python", "python-script"},
	addedInGo:     []string{"lua", "workflow"},
}

// TestCatalogMatchesPythonKinds proves every kind the Python KINDS catalog
// declares is declared here too, MODULO the ADR-0010 divergence recorded
// above — by reading admin_lib/kinds.py directly, rather than asserting a
// hand-copied list that could drift from the source it was ported from.
func TestCatalogMatchesPythonKinds(t *testing.T) {
	pyNames, ok := pythonKindNames(t)
	if !ok {
		t.Skip("admin_lib/kinds.py not found relative to this test — skipping cross-check")
	}
	if len(pyNames) == 0 {
		t.Fatal("found admin_lib/kinds.py but extracted zero _kind(...) names — regex likely stale")
	}

	removed := map[string]bool{}
	for _, n := range adr0010Divergence.removedFromGo {
		removed[n] = true
	}
	for _, name := range pyNames {
		if removed[name] {
			continue
		}
		if _, ok := Kinds[name]; !ok {
			t.Errorf("Python declares kind %q but internal/kinds.Kinds does not", name)
		}
	}

	added := map[string]bool{}
	for _, n := range adr0010Divergence.addedInGo {
		added[n] = true
	}
	for name := range Kinds {
		if added[name] {
			continue
		}
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

// TestADR0010KindSet guards the reversal this ticket's context explicitly
// calls out: an earlier draft of the epic kept python/python-script
// declared. ADR-0010 reverses that again — they are deleted, and kind="lua"
// takes their place as the embedded-VM authoring language.
func TestADR0010KindSet(t *testing.T) {
	for _, name := range []string{"python", "python-script"} {
		if _, ok := Kinds[name]; ok {
			t.Errorf("kind %q must NOT be declared (ADR-0010 deletes it)", name)
		}
	}
	if _, ok := Kinds["lua"]; !ok {
		t.Error(`kind "lua" must be declared (ADR-0010 adds it)`)
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
