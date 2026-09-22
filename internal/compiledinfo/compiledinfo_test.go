package compiledinfo

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestEncodeLoadRoundTrips proves a resolved manifest — including the `any`
// -typed fields (Actions, Settings, Raw) that TOML decoding produces as
// map[string]any / []any — survives Encode then Load unchanged. This is the
// exact shape admin compile embeds and a compiled artifact reads back.
func TestEncodeLoadRoundTrips(t *testing.T) {
	m := &manifest.Manifest{
		Root:        "/tmp/project",
		ProjectName: "roundtrip",
		PythonMin:   "3.11",
		Actions: map[string]manifest.Action{
			"build": {
				"kind": "shell",
				"run":  "echo hi",
				"env":  map[string]any{"FOO": "bar"},
			},
		},
		Overrides: map[string]manifest.Command{
			"build": {Desc: "build it", Steps: []string{"build"}},
		},
		Settings: map[string]map[string]any{
			"apple": {"scheme": "App", "targets": []any{"ios", "mac"}},
		},
		Raw: map[string]any{
			"project_name": "roundtrip",
			"nested":       map[string]any{"a": int64(1), "b": true},
		},
	}

	origManifestB64 := ManifestB64
	origSourcePath := SourcePath
	origBuiltAt := BuiltAt
	t.Cleanup(func() {
		ManifestB64, SourcePath, BuiltAt = origManifestB64, origSourcePath, origBuiltAt
	})

	encoded, err := Encode(m)
	if err != nil {
		t.Fatalf("Encode = %v, want nil", err)
	}
	if encoded == "" {
		t.Fatal("Encode returned an empty string for a non-empty manifest")
	}

	ManifestB64 = encoded
	if !Embedded() {
		t.Fatal("Embedded() = false after setting ManifestB64, want true")
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load = %v, want nil", err)
	}
	if got.ProjectName != m.ProjectName {
		t.Errorf("ProjectName = %q, want %q", got.ProjectName, m.ProjectName)
	}
	if got.Actions["build"]["run"] != "echo hi" {
		t.Errorf("Actions[build][run] = %v, want %q", got.Actions["build"]["run"], "echo hi")
	}
	if got.Overrides["build"].Desc != "build it" {
		t.Errorf("Overrides[build].Desc = %q, want %q", got.Overrides["build"].Desc, "build it")
	}
	targets, ok := got.Settings["apple"]["targets"].([]any)
	if !ok || len(targets) != 2 || targets[0] != "ios" {
		t.Errorf("Settings[apple][targets] = %#v, want [ios mac]", got.Settings["apple"]["targets"])
	}
	nested, ok := got.Raw["nested"].(map[string]any)
	if !ok || nested["a"] != int64(1) || nested["b"] != true {
		t.Errorf("Raw[nested] = %#v, want map[a:1 b:true]", got.Raw["nested"])
	}
}

// TestLoadWithoutEmbeddedManifestErrors proves Load on an ordinary
// (non-compiled) binary — ManifestB64 at its zero value — fails clearly
// instead of decoding garbage.
func TestLoadWithoutEmbeddedManifestErrors(t *testing.T) {
	origManifestB64 := ManifestB64
	t.Cleanup(func() { ManifestB64 = origManifestB64 })
	ManifestB64 = ""

	if Embedded() {
		t.Fatal("Embedded() = true with ManifestB64 empty, want false")
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load() = nil error on an unembedded binary, want an error")
	}
}

// TestGeneratedSourceParses proves the string admin compile writes into
// internal/compiledinfo/generated.go is syntactically valid Go — parsed
// with go/parser rather than just string-matched, so a manifest path or
// build timestamp containing a quote or backslash (Encode's base64 output
// never does, but SourcePath is an arbitrary filesystem path) can't produce
// a generated file that fails only deep inside admin compile's `go build`.
func TestGeneratedSourceParses(t *testing.T) {
	src := GeneratedSource(`b64=="whatever"`, `/path/with "quote"/and\backslash/admin.toml`, "2026-09-22T12:00:00Z")
	if !strings.Contains(src, `package compiledinfo`) {
		t.Fatalf("generated source missing package clause:\n%s", src)
	}
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "generated.go", src, 0); err != nil {
		t.Fatalf("GeneratedSource produced invalid Go: %v\n%s", err, src)
	}
}
