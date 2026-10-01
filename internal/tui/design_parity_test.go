package tui

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m|\x1b\]8;;[^\x1b]*\x1b\\`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// protoModel reproduces a slice of the Claude Design prototype's demo dataset so
// the rendered output can be checked against the prototype's layout.
func protoModel() Model {
	m := New(context.Background(), "/repo", "Forevr-Games/social-poker", "main", "main")
	// admin-70hb: New() now opens on the tab bar; this fixture asserts the
	// body's own layout (panels, list rows, the footer's row-scoped keys), so
	// pin focus there. TestFullPaletteRendered's tab-bar-pill assertion is the
	// one place that needs the opposite state, and it sets focus back itself.
	m.focus = focusBody
	m.width, m.height = 96, 30
	br := func(name string, a, b int, up, wt bool, age string, cur bool) git.BranchInfo {
		bi := git.BranchInfo{Name: name, Ahead: a, Behind: b, HasRemote: up, LastCommit: age, Current: cur}
		if wt {
			bi.WorktreePath = "/repo/.worktrees/" + name
		}
		return bi
	}
	m.local = localData{
		branches: []git.BranchInfo{
			br("pierce/791-spike-mobile-app-wrapper", 11, 56, true, true, "19m", false),
			br("pierce/1145-centralize-chip-balance-mutations", 0, 0, false, true, "64m", false),
			br("main", 0, 0, true, false, "15h", true),
			br("bounty-durable-payouts", 0, 36, true, true, "18h", false),
			br("austin/818-cf-only-cutover", 0, 24, true, true, "18h", false),
		},
		commits: []git.Commit{
			{SHA: "df09f4cd", Subject: "Merge pull request #1016 from Forevr-Games/austin/818-cf-only-cutover", When: "15h"},
		},
		stashes: []git.Stash{
			{Ref: "stash@{0}", Subject: "WIP on issue-699 · fix(cloudflare): ensure sign-out clears session"},
		},
		signals: map[string]gitSignals{}, wtState: map[string]wtState{}, branchUnsafe: map[string]bool{},
		firstAuthor: map[string]string{
			"pierce/791-spike-mobile-app-wrapper":           "pierce@example.com",
			"pierce/1145-centralize-chip-balance-mutations": "pierce@example.com",
			"bounty-durable-payouts":                        "mate@example.com",
			"austin/818-cf-only-cutover":                    "austin@example.com",
		},
		branchNote: map[string]string{},
		lastSubject: map[string]string{
			"bounty-durable-payouts": "refactor(bounty): split the payout table",
		},
	}
	m.identity = data.Identity{Login: "pierce", Email: "pierce@example.com"}
	m.manifest = &manifest.Manifest{
		Trackers: []string{"github"}, HasTracker: true,
		UsesPullRequests: true, HasUsesPullRequests: true,
	}
	m.haveLocal = true
	m.remote = data.Remote{
		PRs: []gh.OpenPR{
			{Number: 1092, Title: "Spike: Capacitor iOS app wrapper (#791)", Checks: gh.ChecksPassing, Bucket: gh.BucketMine},
			{Number: 1017, Title: "feat(leagues): monthly division ladder", Author: "mate", Bucket: gh.BucketAssigned, MyReview: gh.NotReviewed},
		},
		ViewerLogin: "pierce",
		Issues: []gh.IssueItem{
			{Number: 1148, Title: "Standardize chip_balance reads behind a wallet read API", Labels: []string{"cloudflare"}, Assignees: []string{"pierce"}, PR: &gh.IssuePR{Number: 1149, Checks: gh.ChecksPassing}},
			{Number: 1150, Title: "Flaky reconnect on backgrounded socket", Labels: []string{"bug"}},
			{Number: 1151, Title: "League ladder polish pass", Labels: []string{"leagues"}, Assignees: []string{"mate"}},
		},
	}
	m.haveData = true
	return m
}

func mustContainAll(t *testing.T, label, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("%s: missing %q\n%s", label, w, out)
		}
	}
}

// TestRepoTabLayout asserts the panels, compact status, and tab bar match the design.
func TestRepoTabLayout(t *testing.T) {
	m := protoModel()
	out := stripANSI(m.View())
	mustContainAll(t, "Repo tab", out,
		"┌─ Branches ", "┌─ [Commits ", "Stashes ", "└─",
		// Ahead and behind each sit in their own sub-column, so they are checked
		// separately rather than as one adjacent pair.
		// The pane groups by what to do next, so the headings and the ownership
		// glyphs are the layout — not a flat list with ahead/behind counts.
		// Every non-main branch in this fixture is worktree-backed, so the single
		// state glyph renders ◇ for all of them — ownership is carried by color
		// alone (invisible to stripANSI), not by a distinct shape.
		"⇅ next action", "◆", "main", "Continue Working", "Reviewing", "◇ ", "19m",
		"  Repo ", "PRs 2",
		// The footer no longer advertises `space select` — the key still works,
		// it just isn't a hint the Branches pane spends footer width on.
		"↑↓ move", "↵ checkout", "⇥ pane", "←→ tab",
	)
	if strings.Contains(out, "1 Local") {
		t.Errorf("tab bar still shows numbered '1 Local' label\n%s", out)
	}
}

// TestListTabsDesign checks the PRs/Issues rows and section rules.
func TestListTabsDesign(t *testing.T) {
	m := protoModel()
	m.active = tabPRs
	mustContainAll(t, "PRs", stripANSI(m.View()),
		"Mine", "✓ #1092", "in review", "Needs me", "needs review", "@mate", "Waiting on others")
	m.active = tabIssues
	mustContainAll(t, "Issues", stripANSI(m.View()),
		"Assigned to me", "✓ #1148", "cloudflare", "PR open", "Unassigned", "Assigned to someone else")
}

// TestFullPaletteRendered proves the multi-color palette is emitted (not a
// near-monochrome render). The colors are ANSI 16-color indices that resolve
// through the user's terminal theme, so the test asserts that several distinct
// ANSI color codes appear — never fixed hex values.
func TestFullPaletteRendered(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	m := protoModel()
	// lipgloss renders ANSI 16-color indices as basic SGR codes: foreground
	// 30–37 / 90–97, background 40–47 / 100–107. These are palette slots the
	// terminal theme colors — collect the distinct foreground slots used.
	sgr := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	isFG := func(n int) bool { return (n >= 30 && n <= 37) || (n >= 90 && n <= 97) }
	seen := map[string]bool{}
	for _, tb := range []tab{tabLocal, tabPRs, tabIssues} {
		m.active = tb
		for _, mt := range sgr.FindAllStringSubmatch(m.View(), -1) {
			for _, p := range strings.Split(mt[1], ";") {
				if n, err := strconv.Atoi(p); err == nil && isFG(n) {
					seen[p] = true
				}
			}
		}
	}
	if len(seen) < 6 {
		t.Errorf("expected a multi-color palette (≥6 distinct ANSI colors), got %d: %v", len(seen), seen)
	}
	// The active tab pill is the only background-painted element on the tab-bar
	// line; assert it carries a themed background (40–47 / 100–107) — which
	// only holds while focus is on the tab bar (admin-70hb): in the body, the
	// pill renders outlined instead so the cursor row is the one solid thing.
	m.active = tabLocal
	m.focus = focusTabs
	tabLine := strings.Split(m.View(), "\n")[1]
	hasBG := false
	for _, mt := range sgr.FindAllStringSubmatch(tabLine, -1) {
		for _, p := range strings.Split(mt[1], ";") {
			if n, err := strconv.Atoi(p); err == nil && ((n >= 40 && n <= 47) || (n >= 100 && n <= 107)) {
				hasBG = true
			}
		}
	}
	if !hasBG {
		t.Errorf("active tab pill is not painted with a themed background: %q", tabLine)
	}
}

// TestEveryLineWithinWidth guards against panel borders overflowing the frame.
func TestEveryLineWithinWidth(t *testing.T) {
	m := protoModel()
	for _, tb := range []tab{tabLocal, tabPRs, tabIssues} {
		m.active = tb
		for i, l := range strings.Split(m.View(), "\n") {
			if w := lipgloss.Width(l); w > m.width {
				t.Errorf("tab %s line %d width %d > %d: %q", tabDisplay(tb), i, w, m.width, l)
			}
		}
	}
}

// TestEmptyStashesSaysSo pins what the strip draws when Stashes is the list on
// screen and there are none: the toggle still names it with a zero, and one line
// says it is empty rather than leaving a box of blank rows.
func TestEmptyStashesSaysSo(t *testing.T) {
	m := protoModel()
	m.local.stashes = nil
	m.historyStashes = true
	out := stripANSI(m.View())
	mustContainAll(t, "empty stashes", out, "[Stashes 0]", "no stashes", "Commits 1")
}
