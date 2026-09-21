// workband_test.go covers admin-3rgp.72.6's own acceptance criteria: the
// live band's title rule (bandTitle), the fixed-height field block
// (bandFieldsCommand/bandFieldsWorker via slotRows), the idle row's
// title-only band, and fitPath's segment elision. Follows
// workurls_test.go's pattern of calling the band's own functions directly
// rather than standing up a full Frame.
package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// TestBandTitleExactlyBodyWidth is AC1: at 80x24, 96x30 and 178x46 (the
// three geometries the ticket names — the band's own body width, i.e. the
// row list's own lw = width-2, matching what renderWorkBody actually hands
// renderWorkBand), the title rule is exactly that width and ends with
// " ──".
func TestBandTitleExactlyBodyWidth(t *testing.T) {
	freshLivework(t)
	live := &livework.Entry{
		ID: "band-title-live", Name: "dev", Handle: "pty-band-title-live", State: livework.StateRunning,
		StartedAt: time.Now().Add(-4 * time.Minute),
	}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add: %v", err)
	}

	for _, geo := range []struct{ w, h int }{{80, 24}, {96, 30}, {178, 46}} {
		lw := maxi(geo.w-2, 20)
		title := bandTitle(lw, live, "dev")
		if got := vlen(title); got != lw {
			t.Errorf("%dx%d: bandTitle width = %d, want exactly %d (body width): %q", geo.w, geo.h, got, lw, title)
		}
		if !strings.HasSuffix(title, " ──") {
			t.Errorf("%dx%d: bandTitle = %q, want it to end with \" ──\"", geo.w, geo.h, title)
		}
	}
}

// TestBandTitleDropsCountBeforeSqueezingName is AC1's "identity wins the
// space": on a narrow terminal the right-side "also N running" count drops
// entirely rather than truncating the previewed row's own name.
func TestBandTitleDropsCountBeforeSqueezingName(t *testing.T) {
	freshLivework(t)
	live := &livework.Entry{
		ID: "band-title-narrow-live", Name: "a-fairly-long-command-name", Handle: "pty-band-title-narrow-live",
		State: livework.StateRunning, StartedAt: time.Now(),
	}
	other := &livework.Entry{ID: "band-title-other", Name: "other", Handle: "pty-band-title-other", State: livework.StateRunning, StartedAt: time.Now()}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := livework.Default.Add(other); err != nil {
		t.Fatalf("Add: %v", err)
	}

	const w = 32
	title := bandTitle(w, live, "a-fairly-long-command-name")
	if got := vlen(title); got != w {
		t.Fatalf("bandTitle width = %d, want exactly %d: %q", got, w, title)
	}
	if strings.Contains(title, "also") {
		t.Errorf("bandTitle at w=%d kept the count instead of dropping it, want the name to win the space: %q", w, title)
	}
	if !strings.Contains(stripANSI(title), "a-fairly-lo") {
		t.Errorf("bandTitle at w=%d dropped the name it should have preserved: %q", w, title)
	}
}

// TestBandTitleFourStates pins the title rule's verbatim copy across all
// four states the ticket names: a running row, an idle row, and the two
// "everything else" phrasings on the right — nothing running at all vs. the
// previewed thing being the only thing running.
func TestBandTitleFourStates(t *testing.T) {
	t.Run("idle, nothing running", func(t *testing.T) {
		freshLivework(t)
		title := stripANSI(bandTitle(80, nil, "build"))
		if !strings.Contains(title, "not running") {
			t.Errorf("idle title = %q, want \"not running\"", title)
		}
		if strings.Count(title, "running") != 1 {
			t.Errorf("idle title with an empty registry = %q, want \"running\" exactly once", title)
		}
	})

	t.Run("live, nothing else running", func(t *testing.T) {
		freshLivework(t)
		e := &livework.Entry{ID: "solo-live", Name: "dev", Handle: "pty-solo-live", State: livework.StateRunning, StartedAt: time.Now()}
		if err := livework.Default.Add(e); err != nil {
			t.Fatalf("Add: %v", err)
		}
		title := stripANSI(bandTitle(80, e, "dev"))
		if !strings.Contains(title, "running") {
			t.Errorf("live title = %q, want the running state word", title)
		}
		if !strings.Contains(title, "nothing else running") {
			t.Errorf("live title, only entry previewed = %q, want \"nothing else running\"", title)
		}
	})

	t.Run("live, others running, plural counts", func(t *testing.T) {
		freshLivework(t)
		e := &livework.Entry{ID: "main-live", Name: "dev", Handle: "pty-main-live", State: livework.StateRunning, StartedAt: time.Now()}
		c1 := &livework.Entry{ID: "c1", Name: "c1", Handle: "pty-c1", State: livework.StateRunning, StartedAt: time.Now()}
		c2 := &livework.Entry{ID: "c2", Name: "c2", Handle: "pty-c2", State: livework.StateRunning, StartedAt: time.Now()}
		w1 := &livework.Entry{ID: "w1", Name: "w1", Handle: "pty-w1", State: livework.StateRunning, StartedAt: time.Now(), Worker: &livework.WorkerInfo{}}
		w2 := &livework.Entry{ID: "w2", Name: "w2", Handle: "pty-w2", State: livework.StateRunning, StartedAt: time.Now(), Worker: &livework.WorkerInfo{}}
		w3 := &livework.Entry{ID: "w3", Name: "w3", Handle: "pty-w3", State: livework.StateRunning, StartedAt: time.Now(), Worker: &livework.WorkerInfo{}}
		for _, x := range []*livework.Entry{e, c1, c2, w1, w2, w3} {
			if err := livework.Default.Add(x); err != nil {
				t.Fatalf("Add(%s): %v", x.ID, err)
			}
		}
		title := stripANSI(bandTitle(96, e, "dev"))
		if !strings.Contains(title, "also 2 commands · 3 workers") {
			t.Errorf("title with 2 other commands + 3 workers = %q, want \"also 2 commands · 3 workers\"", title)
		}
	})

	t.Run("idle, others running singular", func(t *testing.T) {
		freshLivework(t)
		c := &livework.Entry{ID: "c1", Name: "c1", Handle: "pty-c1", State: livework.StateRunning, StartedAt: time.Now()}
		w := &livework.Entry{ID: "w1", Name: "w1", Handle: "pty-w1", State: livework.StateRunning, StartedAt: time.Now(), Worker: &livework.WorkerInfo{}}
		if err := livework.Default.Add(c); err != nil {
			t.Fatalf("Add: %v", err)
		}
		if err := livework.Default.Add(w); err != nil {
			t.Fatalf("Add: %v", err)
		}
		title := stripANSI(bandTitle(96, nil, "build"))
		if !strings.Contains(title, "also 1 command · 1 worker") {
			t.Errorf("title with 1 other command + 1 worker = %q, want singular \"also 1 command · 1 worker\"", title)
		}
	})
}

// TestFieldBlockAlwaysFieldRowsH is the epic's core invariant across both
// block kinds: live command and live worker each produce exactly fieldRowsH
// rows, whatever they have to say.
func TestFieldBlockAlwaysFieldRowsH(t *testing.T) {
	cmdNoURL := bandFieldsCommand(&workRowData{cmdName: "dev", logPath: "/x/dev.log"}, 60)
	if len(cmdNoURL) != fieldRowsH {
		t.Errorf("bandFieldsCommand (no urls) produced %d rows, want fieldRowsH (%d)", len(cmdNoURL), fieldRowsH)
	}

	worker := bandFieldsWorker(&livework.Entry{
		ID: "w", LogPath: "/x/w.log", Worker: &livework.WorkerInfo{Branch: "b", Worktree: "/wt"},
	}, 60)
	if len(worker) != fieldRowsH {
		t.Errorf("bandFieldsWorker produced %d rows, want fieldRowsH (%d)", len(worker), fieldRowsH)
	}
}

// TestBandIdleIsTitleOnly: an idle row's band is its title rule — the name
// and "not running" — and nothing else. Every row beneath it is blank: no
// kind, desc, last run, last log, url or query field, and no placeholder.
func TestBandIdleIsTitleOnly(t *testing.T) {
	freshLivework(t)
	mm := workTestModel(nil)
	row := rowItem{kind: rowCommand, work: &workRowData{
		cmdName: "build", cmdDesc: "build it", cmdKind: "workflow", targetKey: "fast", logPath: "/x/build.log",
		urls:     []frame.URLEntry{{Label: "local", URL: "http://localhost:9"}},
		workflow: &kinds.WorkflowTarget{Source: kinds.WorkflowSourceQuery, Query: "status:closed"},
	}}
	const h = 12
	out := mm.renderWorkBand(row, 60, h)
	if len(out) != h {
		t.Fatalf("renderWorkBand produced %d lines, want exactly %d", len(out), h)
	}
	title := stripANSI(out[0])
	if !strings.Contains(title, "build fast") || !strings.Contains(title, "not running") {
		t.Errorf("idle title = %q, want the name and \"not running\"", title)
	}
	for i, l := range out[1:] {
		if s := strings.TrimSpace(stripANSI(l)); s != "" {
			t.Errorf("idle band row %d = %q, want blank", i+1, s)
		}
	}
}

// TestFitPathElidesWholeSegments is AC-adjacent (fitPath replaces
// shortenLogPath): a path too long for its width loses whole middle
// segments — "/Users/…/logs/docs.log" — never a mid-rune cut, which would
// read as corruption ("/Users/pierce/Proj…tool/").
func TestFitPathElidesWholeSegments(t *testing.T) {
	p := "/Users/pierce/Projects/admin-project-tool/tmp/logs/docs.log"
	got := fitPath(p, 24)
	if vlen(got) > 24 {
		t.Fatalf("fitPath(%q, 24) = %q (width %d), want <= 24", p, got, vlen(got))
	}
	if !strings.HasPrefix(got, "/Users/") {
		t.Errorf("fitPath = %q, want it to stay absolute (start with /Users/)", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("fitPath = %q, want a segment-elision ellipsis", got)
	}
	// A mid-rune cut would produce a truncated final segment like
	// "Proj…tool" glued mid-word; the elided form's suffix must be an
	// UNCUT trailing segment straight from the original path.
	if !strings.HasSuffix(got, "docs.log") && !strings.HasSuffix(got, "…") {
		t.Errorf("fitPath = %q, want it to end on a whole segment or the ellipsis itself, not a mid-word cut", got)
	}

	// A path that already fits is returned unchanged.
	short := "/tmp/x.log"
	if got := fitPath(short, 40); got != short {
		t.Errorf("fitPath(%q, 40) = %q, want unchanged (already fits)", short, got)
	}
}

// TestFitPathBasenameSurvivesTinyBudget is admin-3rgp.72.14's core fix: the
// basename is never elided. At a budget too small to hold root + basename
// (even with the whole middle gone), the two escape hatches used to fall
// through to a plain front-anchored truncation of the FULL path — which
// keeps the root and cuts the basename, exactly the corruption fitPath
// exists to prevent. The basename must survive instead.
func TestFitPathBasenameSurvivesTinyBudget(t *testing.T) {
	p := "/Users/pierce/Projects/admin-project-tool/tmp/logs/docs.log"

	// Budget can hold "…/docs.log" but not "/Users/…/docs.log".
	got := fitPath(p, 10)
	if !strings.HasSuffix(got, "docs.log") {
		t.Errorf("fitPath(%q, 10) = %q, want the basename kept intact", p, got)
	}
	if vlen(got) > 10 {
		t.Errorf("fitPath(%q, 10) = %q (width %d), want <= 10", p, got, vlen(got))
	}

	// Budget too small even for "…/docs.log" (10 cells): the bare basename
	// must still come back whole, not a front-anchored cut of the full path.
	got = fitPath(p, 8)
	if !strings.HasSuffix(got, "docs.log") {
		t.Errorf("fitPath(%q, 8) = %q, want the bare basename \"docs.log\" (8 cells)", p, got)
	}

	// Budget too small even for the bare basename (8 cells): something must
	// give, but it's the basename itself that gets truncated — the result
	// must still be anchored on the basename, not on the path's root.
	got = fitPath(p, 4)
	if vlen(got) > 4 {
		t.Fatalf("fitPath(%q, 4) = %q (width %d), want <= 4", p, got, vlen(got))
	}
	if strings.HasPrefix(got, "/Users") {
		t.Errorf("fitPath(%q, 4) = %q, want the basename truncated, not the root kept and basename dropped", p, got)
	}
}

// TestFitPathMeasuresDisplayWidthNotBytes: a multi-byte path's escapes must
// be counted in terminal cells, not bytes, so it is never cut shorter than
// its ASCII equivalent would be.
func TestFitPathMeasuresDisplayWidthNotBytes(t *testing.T) {
	p := "/Users/pierce/Projects/café-app/tmp/logs/docs.log"
	got := fitPath(p, 24)
	if vlen(got) > 24 {
		t.Fatalf("fitPath(%q, 24) = %q (display width %d), want <= 24", p, got, vlen(got))
	}
	if !strings.HasSuffix(got, "docs.log") {
		t.Errorf("fitPath(%q, 24) = %q, want the basename kept intact", p, got)
	}
}

// TestFitPathMeasuresDisplayWidthNotBytesOnBasenameFallback exercises the
// same display-width requirement one rung further down the ladder: the
// tiny-budget fallback that truncates the BASENAME ITSELF (fitPath's final
// return, workview.go), not just the whole-path elision above it. A wide
// CJK basename has more bytes per rune than display cells, so a byte-length
// truncation would cut mid-rune (producing invalid UTF-8) or land well
// under the true display budget; a display-width truncation lands exactly
// at it with valid UTF-8 throughout.
func TestFitPathMeasuresDisplayWidthNotBytesOnBasenameFallback(t *testing.T) {
	p := "/Users/pierce/Projects/admin-project-tool/tmp/logs/日志café.log"
	got := fitPath(p, 4)
	if vlen(got) > 4 {
		t.Fatalf("fitPath(%q, 4) = %q (display width %d), want <= 4", p, got, vlen(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("fitPath(%q, 4) = %q, want valid UTF-8 (a byte-length cut would split a multi-byte rune)", p, got)
	}
	if strings.HasPrefix(got, "/Users") {
		t.Errorf("fitPath(%q, 4) = %q, want the basename truncated, not the root kept and basename dropped", p, got)
	}
}

// TestFitPathSingleEllipsisMark: the elision mark is one "…" standing in
// for whole segments, never a trailing "..." glued onto a partial component.
func TestFitPathSingleEllipsisMark(t *testing.T) {
	p := "/Users/pierce/Projects/admin-project-tool/tmp/logs/docs.log"
	for _, w := range []int{24, 16, 10, 8, 4} {
		got := fitPath(p, w)
		if strings.Contains(got, "...") {
			t.Errorf("fitPath(%q, %d) = %q, want the single-rune \"…\" mark, not \"...\"", p, w, got)
		}
	}
}

// TestFitPathNarrowingNeverCutsMidSegment walks a real absolute path from
// its full width down to a 4-column budget and asserts, at EVERY width,
// that the result never contains a partial segment: every piece on either
// side of the elision mark (or standing alone) is either the ellipsis
// itself or a whole segment straight from the original path — never a
// fragment of one.
func TestFitPathNarrowingNeverCutsMidSegment(t *testing.T) {
	p := "/Users/pierce/Projects/admin-project-tool/tmp/logs/docs.log"
	full := strings.Split(strings.TrimPrefix(p, "/"), "/")
	base := full[len(full)-1]

	isWholeOrEllipsis := func(part string) bool {
		if part == "" || part == "…" {
			return true
		}
		for _, s := range full {
			if part == s {
				return true
			}
		}
		return false
	}

	for w := vlen(p); w >= 4; w-- {
		got := fitPath(p, w)
		if vlen(got) > w {
			t.Fatalf("fitPath(p, %d) = %q, width %d exceeds budget", w, got, vlen(got))
		}
		// Below the "…/basename" floor, the basename itself may need
		// truncating (something has to give at a 4-column budget) — that
		// truncation is character-level by necessity, not a segment cut,
		// so it's exempt from the whole-segment check below.
		if !strings.HasSuffix(got, base) && strings.HasSuffix(got, "…") {
			continue
		}
		for _, part := range strings.SplitAfter(got, "/") {
			part = strings.TrimSuffix(part, "/")
			if !isWholeOrEllipsis(part) {
				t.Errorf("fitPath(p, %d) = %q, segment %q is a partial cut, not a whole segment or the ellipsis", w, got, part)
			}
		}
	}
}
