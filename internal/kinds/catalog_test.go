package kinds

import "testing"

// TestADR0010KindSet pins ADR-0010's kind set: no python or python-script
// kind, and kind="lua" as the embedded-VM authoring language.
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

	// A few representative facts about the catalog.
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
