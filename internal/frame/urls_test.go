package frame

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func TestAnEntryNamingAProjectURLResolvesToIt(t *testing.T) {
	m := &manifest.Manifest{Status: map[string]manifest.StatusSpec{"local": {Kind: "http", URL: "http://localhost:8787"}}}
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
	m := &manifest.Manifest{Status: map[string]manifest.StatusSpec{"local": {Kind: "http", URL: "http://localhost:8787"}}}
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
	m := &manifest.Manifest{Status: map[string]manifest.StatusSpec{}}
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

// A custom label on an entry that ALSO names a [status] entry is
// discarded in favour of the row's own name — ResolveURLs reassigns both
// label and url together once a project match is found.
func TestAProjectURLMatchOverridesACustomLabel(t *testing.T) {
	m := &manifest.Manifest{Status: map[string]manifest.StatusSpec{"local": {Kind: "http", URL: "http://localhost:8787"}}}
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
	m := &manifest.Manifest{Status: map[string]manifest.StatusSpec{}}
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

func TestAnEntryNamingAStatusEntryWithAURLPasses(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"run": "true", "urls": []any{"dev-cf"}},
	}
	if err := ValidateURLRefs(actions, map[string]manifest.StatusSpec{"dev-cf": {Kind: "http", URL: "http://localhost:8787"}}); err != nil {
		t.Fatal(err)
	}
}

func TestALiteralURLPassesEvenWithNoStatusTable(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"run": "true", "urls": []any{"http://localhost:3000"}},
	}
	if err := ValidateURLRefs(actions, map[string]manifest.StatusSpec{}); err != nil {
		t.Fatal(err)
	}
}

func TestANameThatMatchesNothingIsAnErrorNotALiteral(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"run": "true", "urls": []any{"local"}},
	}
	err := ValidateURLRefs(actions, map[string]manifest.StatusSpec{"dev-cf": {Kind: "http", URL: "http://localhost:8787"}})
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
	err := ValidateURLRefs(actions, map[string]manifest.StatusSpec{"dev-cf": {Kind: "http", URL: "http://x"}})
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
	err := ValidateURLRefs(actions, map[string]manifest.StatusSpec{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, "'urls' must be a list") {
		t.Fatalf("got %q", got)
	}
}

// TestValidateURLRefsReportsEveryBadEntryNotJustTheFirst is admin-3rgp.8's
// acceptance criterion ("reports all of them") applied to url refs: two
// unrelated actions each with a bad entry must both show up in one error.
//
// MUTATION EVIDENCE: with ValidateURLRefs's loop changed to `return
// validate.Errorf(...)` on the first bad entry (restoring the old
// fail-fast shape) instead of accumulating into `problems`, this test
// failed with:
//
//	error "action \"a\": urls entry \"nowhere-a\" is not a URL and does not
//	name a [status.<label>] entry with a url — no [status] entries are
//	defined" is missing action "b"'s bad entry (nowhere-b)
//
// (reverted after confirming the failure).
func TestValidateURLRefsReportsEveryBadEntryNotJustTheFirst(t *testing.T) {
	actions := map[string]manifest.Action{
		"a": {"run": "true", "urls": []any{"nowhere-a"}},
		"b": {"run": "true", "urls": []any{"nowhere-b"}},
	}
	err := ValidateURLRefs(actions, map[string]manifest.StatusSpec{})
	if err == nil {
		t.Fatal("expected an error")
	}
	got := err.Error()
	if !strings.Contains(got, "nowhere-a") || !strings.Contains(got, "nowhere-b") {
		t.Fatalf("error %q is missing one action's bad entry", got)
	}
}

// -- ValidateFooterRefs ------------------------------------------------------

func TestValidateFooterRefsAcceptsABoolAndPassesWithNone(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev":  {"run": "true", "footer": true},
		"prod": {"run": "true"},
	}
	if err := ValidateFooterRefs(actions); err != nil {
		t.Fatalf("ValidateFooterRefs = %v, want nil", err)
	}
}

// TestValidateFooterRefsRejectsANonBool is the ticket's acceptance
// criterion applied to footer: `footer = "true"` is a typo TOML's lack of
// truthy strings makes worth reporting, not silently accepting.
func TestValidateFooterRefsRejectsANonBool(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"run": "true", "footer": "true"},
	}
	err := ValidateFooterRefs(actions)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, "'footer' must be true or false") || !strings.Contains(got, `action "dev"`) {
		t.Fatalf("got %q", got)
	}
}

// TestValidateFooterRefsNamesTheTargetTheBadEntryIsOn mirrors
// TestTheErrorNamesTheTargetTheBadEntryIsOn for footer instead of urls.
func TestValidateFooterRefsNamesTheTargetTheBadEntryIsOn(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {
			"kind": "multi-target",
			"cf":   map[string]any{"run": "true", "footer": "nope"},
		},
	}
	err := ValidateFooterRefs(actions)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, `target "cf"`) {
		t.Fatalf("got %q", got)
	}
}

// A command-kind status entry has no url, so a urls list cannot name it: the
// error says why instead of listing it as a known name.
func TestAnEntryNamingACommandStatusEntryIsRejected(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"run": "true", "urls": []any{"jobs"}},
	}
	err := ValidateURLRefs(actions, map[string]manifest.StatusSpec{"jobs": {Kind: "command", Run: "echo 3"}})
	if err == nil || !strings.Contains(err.Error(), "command entry with no url") {
		t.Fatalf("got %v", err)
	}
}
