// palette_test.go covers the Work tab's command palette (admin-3rgp.72.2):
// the resolved-entry model, fuzzy search, and the box's idle/typing content
// — sized like the neighbouring work_test.go, one focused test per stated
// behaviour.
package tui

import (
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// paletteTestModel is a multi-target-plus-flat fixture: "dev" has two
// targets (never a bare-parent entry), "lint" is a flat shell command.
func paletteTestModel() Model {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"dev":  {Desc: "dev server", Steps: []string{"dev"}},
			"lint": {Desc: "lint everything", Steps: []string{"lint"}},
		},
		Actions: map[string]manifest.Action{
			"dev": {
				"kind": "multi-target",
				"targets": map[string]any{
					"cf":    "cd apps && bunx wrangler dev --target cf",
					"local": "cd apps && bunx wrangler dev --target local",
				},
			},
			"lint": {"kind": "shell", "run": "bun run lint"},
		},
	}
	return workTestModel(m)
}

// TestBuildPaletteEntriesNeverEmitsABareMultiTargetParent is the ticket's own
// core claim: a multi-target command yields one entry per target and NO
// bare-parent entry (ambiguous, can't be dispatched); a flat command yields
// exactly one entry.
func TestBuildPaletteEntriesNeverEmitsABareMultiTargetParent(t *testing.T) {
	mm := paletteTestModel()
	entries := mm.buildPaletteEntries()

	var resolved []string
	for _, e := range entries {
		resolved = append(resolved, e.resolved())
	}
	for _, want := range []string{"dev cf", "dev local", "lint"} {
		found := false
		for _, r := range resolved {
			if r == want {
				found = true
			}
		}
		if !found {
			t.Errorf("entries = %v, missing %q", resolved, want)
		}
	}
	for _, r := range resolved {
		if r == "dev" {
			t.Errorf("entries = %v, contains a bare \"dev\" parent — a multi-target command must never resolve to one", resolved)
		}
	}
	lintRuns := 0
	for _, e := range entries {
		if e.resolved() == "lint" {
			lintRuns++
			if e.run != "bun run lint" {
				t.Errorf("lint entry run = %q, want %q", e.run, "bun run lint")
			}
		}
	}
	if lintRuns != 1 {
		t.Errorf("lint resolved to %d entries, want exactly 1", lintRuns)
	}
}

// TestPaletteCandidatesRanksTargetMatchAheadOfUnrelated is the ticket's own
// "cf finds dev cf directly, never just dev" — proven the way the ported
// word-by-word algorithm actually resolves it: "dev" locks word 0 (the
// parent), and the in-progress word "c" then ranks by a literal-prefix match
// ("cf") ahead of a merely-contained one ("local" also contains "c", scored
// lower with no prefix bonus — fuzzyScore's own rule) and never surfaces a
// bare "dev" parent (buildPaletteEntries never emits one to begin with).
func TestPaletteCandidatesRanksTargetMatchAheadOfUnrelated(t *testing.T) {
	mm := paletteTestModel()
	entries := mm.buildPaletteEntries()

	cands, _, _ := paletteCandidates(entries, "dev c")
	if len(cands) == 0 {
		t.Fatalf("paletteCandidates(%q) returned nothing", "dev c")
	}
	if cands[0].resolved() != "dev cf" {
		t.Errorf("top candidate for %q = %q, want %q", "dev c", cands[0].resolved(), "dev cf")
	}
	for _, c := range cands {
		if c.resolved() == "dev" {
			t.Errorf("candidates for %q contain a bare \"dev\" parent: %+v", "dev c", cands)
		}
		if c.resolved() == "lint" {
			t.Errorf("candidates for %q contain the unrelated \"lint\" entry: %+v", "dev c", cands)
		}
	}
}

// TestPaletteIdleContentPopulatedMRU is the idle screen with a populated
// strip — the hint row names the count and there are exactly as many rows as
// entries. Compare against commands-palette.txt's own "3 recent" case.
func TestPaletteIdleContentPopulatedMRU(t *testing.T) {
	mru := []paletteEntry{
		{parent: "grafana", target: "deploy", run: "cd apps && bunx wrangler grafana --target deploy"},
		{parent: "dev", target: "cf", run: "cd apps && bunx wrangler dev --target cf"},
		{parent: "test", target: "gate", run: "cd apps && bunx wrangler test --target gate"},
	}
	body := paletteIdleContent(90, mru, false, 0)
	if len(body) != 1+len(mru) {
		t.Fatalf("paletteIdleContent produced %d rows, want %d (1 hint + %d strip rows)", len(body), 1+len(mru), len(mru))
	}
	if !strings.Contains(body[0], "/ search") || !strings.Contains(body[0], "b browse all") || !strings.Contains(body[0], "3 recent") {
		t.Errorf("hint row = %q, want it to read \"/ search   b browse all   3 recent\"", stripANSI(body[0]))
	}
	for i, e := range mru {
		if !strings.Contains(body[i+1], e.resolved()) {
			t.Errorf("strip row %d = %q, missing resolved name %q", i, stripANSI(body[i+1]), e.resolved())
		}
	}
}

// TestPaletteIdleContentEmptyMRU is the idle screen with nothing recorded
// yet — never a guessed seed. Compare against commands-palette-empty.txt.
func TestPaletteIdleContentEmptyMRU(t *testing.T) {
	body := paletteIdleContent(90, nil, false, 0)
	if len(body) != 2 {
		t.Fatalf("paletteIdleContent(empty) produced %d rows, want 2 (hint + the empty-state row)", len(body))
	}
	if strings.Contains(body[0], "recent") {
		t.Errorf("hint row = %q, want the \"N recent\" clause omitted when the strip is empty", stripANSI(body[0]))
	}
	if !strings.Contains(body[1], "no commands run yet") {
		t.Errorf("body[1] = %q, want the single \"no commands run yet\" row", stripANSI(body[1]))
	}
}

// TestPaletteTypingContentShowsCaretCandidatesAndFooter covers the typing
// screen's three parts at once: the caret row, the candidate rows, and the
// "N matches · tab complete word · ↵ dispatch" footer — compare against
// commands-palette-typing.txt.
func TestPaletteTypingContentShowsCaretCandidatesAndFooter(t *testing.T) {
	mm := paletteTestModel()
	entries := mm.buildPaletteEntries()

	body := paletteTypingContent(90, "dev c", entries, false, 0)
	if len(body) < 2 {
		t.Fatalf("paletteTypingContent produced %d rows, want at least the caret row and a blank separator", len(body))
	}
	if !strings.Contains(stripANSI(body[0]), "/ dev c") {
		t.Errorf("caret row = %q, want it to start with \"/ dev c\"", stripANSI(body[0]))
	}
	joined := strings.Join(body, "\n")
	if !strings.Contains(joined, "dev cf") {
		t.Errorf("typing content missing the \"dev cf\" candidate row:\n%s", stripANSI(joined))
	}
	last := stripANSI(body[len(body)-1])
	if !strings.Contains(last, "match") || !strings.Contains(last, "tab complete word") || !strings.Contains(last, "↵ dispatch") {
		t.Errorf("footer row = %q, want the \"N match(es) · tab complete word · ↵ dispatch\" line", last)
	}
}

// TestPaletteBoxGeometryMatchesReferenceFrame is the box-geometry test named
// in the ticket: every rendered row is exactly the requested width in
// display cells, and the border characters line up in the same columns
// docs/spikes/admin-tui-desktop/commands-palette.txt renders them in.
func TestPaletteBoxGeometryMatchesReferenceFrame(t *testing.T) {
	const w = 94 // the reference frame's own box width (fullW, DumpWidth-2)
	body := paletteIdleContent(maxi(w-4, 8), []paletteEntry{
		{parent: "grafana", target: "deploy", run: "cd apps && bunx wrangler grafana --target deploy"},
	}, false, 0)
	box := paletteBoxFit(w, "Command Palette", body)

	for i, l := range box {
		if got := vlen(l); got != w {
			t.Errorf("row %d width = %d, want exactly %d: %q", i, got, w, stripANSI(l))
		}
	}
	if !strings.HasPrefix(box[0], "┌─ ") {
		t.Errorf("top border = %q, want it to start \"┌─ \"", stripANSI(box[0]))
	}
	if !strings.HasSuffix(stripANSI(box[0]), "┐") {
		t.Errorf("top border = %q, want it to end \"┐\"", stripANSI(box[0]))
	}
	last := box[len(box)-1]
	if !strings.HasPrefix(last, "└") || !strings.HasSuffix(last, "┘") {
		t.Errorf("bottom border = %q, want \"└\"...\"┘\"", last)
	}
	for i := 1; i < len(box)-1; i++ {
		if !strings.HasPrefix(box[i], "│ ") || !strings.HasSuffix(stripANSI(box[i]), " │") {
			t.Errorf("content row %d = %q, want it bordered \"│ ...  │\"", i, stripANSI(box[i]))
		}
	}
}

// TestWorkTabPlainKeyBeforeSlashReachesNormalKeyPath is the ticket's own
// acceptance criterion: idle never captures keystrokes — only "/" starts
// typing mode. A plain letter before that must reach the built-in switch
// exactly as it did before this ticket, and the same key after "/" must be
// captured as query text instead.
func TestWorkTabPlainKeyBeforeSlashReachesNormalKeyPath(t *testing.T) {
	mm := paletteTestModel()
	mm.active = tabWork
	mm.focus = focusBody

	next, _ := mm.handleKey(keyMsg("x"))
	nm := next.(Model)
	if nm.paletteTyping {
		t.Fatalf("a plain letter before / started palette typing mode")
	}
	if nm.paletteQuery != "" {
		t.Fatalf("paletteQuery = %q after a plain letter with typing not started, want empty", nm.paletteQuery)
	}

	next2, _ := nm.handleKey(keyMsg("/"))
	nm2 := next2.(Model)
	if !nm2.paletteTyping {
		t.Fatalf("/ on the Work tab did not start palette typing mode")
	}

	next3, _ := nm2.handleKey(keyMsg("x"))
	nm3 := next3.(Model)
	if nm3.paletteQuery != "x" {
		t.Fatalf("paletteQuery = %q after typing \"x\" in typing mode, want %q", nm3.paletteQuery, "x")
	}
}
