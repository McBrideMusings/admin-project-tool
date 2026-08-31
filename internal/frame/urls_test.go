package frame

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func TestAnEntryNamingAProjectURLResolvesToIt(t *testing.T) {
	m := &manifest.Manifest{Urls: map[string]string{"local": "http://localhost:8787"}}
	got, err := ResolveURLs([]any{map[string]any{"label": "local", "url": "local"}}, m)
	if err != nil {
		t.Fatal(err)
	}
	want := []URLEntry{{"local", "http://localhost:8787"}}
	if !equalEntries(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAnEntryThatNamesNothingIsUsedAsALiteralURL(t *testing.T) {
	m := &manifest.Manifest{Urls: map[string]string{"local": "http://localhost:8787"}}
	got, err := ResolveURLs([]any{"http://127.0.0.1:9999"}, m)
	if err != nil {
		t.Fatal(err)
	}
	want := []URLEntry{{"http://127.0.0.1:9999", "http://127.0.0.1:9999"}}
	if !equalEntries(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestEnvVarsExpandAtResolveTime(t *testing.T) {
	t.Setenv("FRAME_TEST_PORT", "4321")
	m := &manifest.Manifest{Urls: map[string]string{}}
	got, err := ResolveURLs([]any{
		map[string]any{"label": "Dev", "url": "http://localhost:${FRAME_TEST_PORT}"},
	}, m)
	if err != nil {
		t.Fatal(err)
	}
	want := []URLEntry{{"Dev", "http://localhost:4321"}}
	if !equalEntries(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A custom label on an entry that ALSO names a project [urls] row is
// discarded in favour of the row's own name — matches
// admin_lib/frame.py::resolve_urls exactly (it reassigns both label and url
// together once a project match is found).
func TestAProjectURLMatchOverridesACustomLabel(t *testing.T) {
	m := &manifest.Manifest{Urls: map[string]string{"local": "http://localhost:8787"}}
	got, err := ResolveURLs([]any{map[string]any{"label": "My Custom Label", "url": "local"}}, m)
	if err != nil {
		t.Fatal(err)
	}
	want := []URLEntry{{"local", "http://localhost:8787"}}
	if !equalEntries(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestResolveURLsPropagatesAnUnresolvedRequiredVarError(t *testing.T) {
	m := &manifest.Manifest{Urls: map[string]string{}}
	_, err := ResolveURLs([]any{"http://localhost:${FRAME_TEST_TOTALLY_UNSET_VAR}"}, m)
	if err == nil {
		t.Fatal("expected an error for an unresolved required var")
	}
}

func equalEntries(a, b []URLEntry) bool {
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

// -- ValidateURLRefs --------------------------------------------------------

func TestAnEntryNamingARowOfTheURLsTablePasses(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"run": "true", "urls": []any{"dev-cf"}},
	}
	if err := ValidateURLRefs(actions, map[string]string{"dev-cf": "http://localhost:8787"}); err != nil {
		t.Fatal(err)
	}
}

func TestALiteralURLPassesEvenWithNoURLsTable(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"run": "true", "urls": []any{"http://localhost:3000"}},
	}
	if err := ValidateURLRefs(actions, map[string]string{}); err != nil {
		t.Fatal(err)
	}
}

func TestANameThatMatchesNothingIsAnErrorNotALiteral(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"run": "true", "urls": []any{"local"}},
	}
	err := ValidateURLRefs(actions, map[string]string{"dev-cf": "http://localhost:8787"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, "is not a URL and does not name") {
		t.Fatalf("got %q", got)
	}
}

func TestTheErrorNamesTheTargetTheBadEntryIsOn(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {
			"kind": "multi-target",
			"cf":   map[string]any{"run": "true", "urls": []any{"lan"}},
		},
	}
	err := ValidateURLRefs(actions, map[string]string{"dev-cf": "http://x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, `target "cf"`) {
		t.Fatalf("got %q", got)
	}
}

func TestValidateURLRefsRejectsANonListURLsValue(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"run": "true", "urls": "http://localhost:8787"},
	}
	err := ValidateURLRefs(actions, map[string]string{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, "'urls' must be a list") {
		t.Fatalf("got %q", got)
	}
}
