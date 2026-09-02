package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestTrackSortLabelsAndCycleWraps pins the six order names and the cycle
// order the ticket calls out: last modified, priority, oldest first, newest
// first, most dependencies first, by type — wrapping back to the first after
// the sixth.
func TestTrackSortLabelsAndCycleWraps(t *testing.T) {
	want := []string{"last modified", "priority", "oldest first", "newest first", "most dependencies", "by type"}
	s := sortLastModified
	for i, label := range want {
		if got := s.label(); got != label {
			t.Errorf("order %d: label() = %q, want %q", i, got, label)
		}
		s = s.next()
	}
	if s != sortLastModified {
		t.Errorf("cycling past the sixth order landed on %v, want it to wrap back to sortLastModified", s)
	}
}

// beadSortFixture builds four beads with known dates, a known dependency
// shape, known types, and known priorities, in an input order ("d", "b", "c",
// "a") chosen so every order that ties on its primary key ("b" and "d" both
// updated at t2; "b", "c", "d" all unblocking nothing; "b" and "c" both P0)
// has to fall back to the beads' original relative order to pass — proving
// the sort is stable, not just correct on the fields that do differ.
func beadSortFixture() []bd.Bead {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	a := bd.Bead{ID: "a", Type: "bug", Updated: t1, Priority: 2}
	b := bd.Bead{ID: "b", Type: "chore", Updated: t2, Blockers: []string{"a"}, Priority: 0}
	c := bd.Bead{ID: "c", Type: "feature", Updated: t3, Blockers: []string{"a"}, Priority: 0}
	d := bd.Bead{ID: "d", Type: "task", Updated: t2, Priority: 1}
	return []bd.Bead{d, b, c, a}
}

func beadIDs(beads []bd.Bead) []string {
	ids := make([]string, len(beads))
	for i, b := range beads {
		ids[i] = b.ID
	}
	return ids
}

func assertOrder(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s: got %v, want %v", label, got, want)
			return
		}
	}
}

// TestSortBeadsByOrders asserts each of the five orders against the fixture's
// known dates, dependency counts (via bd.Unblocks — "a" unblocks both "b" and
// "c") and types, and that ties are broken stably by input order rather than
// by id or any other implicit key.
func TestSortBeadsByOrders(t *testing.T) {
	unblocks := bd.Unblocks(beadSortFixture())
	if unblocks["a"] != 2 {
		t.Fatalf("fixture sanity: a should unblock 2 (b and c), got %d", unblocks["a"])
	}

	cases := []struct {
		order trackSort
		want  []string
	}{
		{sortLastModified, []string{"c", "d", "b", "a"}},
		{sortPriority, []string{"b", "c", "d", "a"}}, // b, c tie at P0 — input order (d,b,c,a) keeps b before c
		{sortOldestFirst, []string{"a", "d", "b", "c"}},
		{sortNewestFirst, []string{"c", "d", "b", "a"}},
		{sortMostDeps, []string{"a", "d", "b", "c"}},
		{sortByType, []string{"a", "b", "c", "d"}}, // bug < chore < feature < task
	}
	for _, tc := range cases {
		beads := beadSortFixture()
		sortBeadsBy(beads, tc.order, unblocks)
		assertOrder(t, tc.order.label(), beadIDs(beads), tc.want)
	}
}

// TestSortBeadsByEmptyAndSingle proves every order handles a nil slice and a
// one-item slice without panicking or reordering the lone item.
func TestSortBeadsByEmptyAndSingle(t *testing.T) {
	for order := sortLastModified; order < numTrackSorts; order++ {
		var empty []bd.Bead
		sortBeadsBy(empty, order, nil)
		if len(empty) != 0 {
			t.Errorf("%s: empty slice grew to %d", order.label(), len(empty))
		}

		single := []bd.Bead{{ID: "solo", Type: "task", Updated: time.Now()}}
		sortBeadsBy(single, order, map[string]int{"solo": 3})
		if len(single) != 1 || single[0].ID != "solo" {
			t.Errorf("%s: single-item slice changed: %v", order.label(), single)
		}
	}
}

// issueSortFixture builds three issues whose numbers and updated times
// disagree with each other, in an order that differs from every possible
// sort of them ("30, 10, 20"), so the mostDeps/byType no-op case is
// distinguishable from an accidental sort by number or date.
func issueSortFixture() []gh.IssueItem {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	return []gh.IssueItem{
		{Number: 30, Updated: t2},
		{Number: 10, Updated: t3},
		{Number: 20, Updated: t1},
	}
}

func issueNums(items []gh.IssueItem) []int {
	nums := make([]int, len(items))
	for i, it := range items {
		nums[i] = it.Number
	}
	return nums
}

// TestSortIssuesByOrders asserts the three orders issues can actually express
// (oldest/newest by number, since GitHub assigns numbers in creation order
// and gh.IssueItem carries no CreatedAt; last modified by Updated) and that
// the two bead-only orders — no dependency graph, no type field on an issue —
// leave the incoming order untouched.
func TestSortIssuesByOrders(t *testing.T) {
	cases := []struct {
		order trackSort
		want  []int
	}{
		{sortLastModified, []int{10, 30, 20}},
		{sortPriority, []int{30, 10, 20}}, // none labeled — unmatched priority (5) keeps input order
		{sortOldestFirst, []int{10, 20, 30}},
		{sortNewestFirst, []int{30, 20, 10}},
		{sortMostDeps, []int{30, 10, 20}}, // no-op: issues carry no dependency graph
		{sortByType, []int{30, 10, 20}},   // no-op: issues carry no type field
	}
	for _, tc := range cases {
		items := issueSortFixture()
		sortIssuesBy(items, tc.order)
		got := issueNums(items)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.order.label(), got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got %v, want %v", tc.order.label(), got, tc.want)
				break
			}
		}
	}
}

// TestSortIssuesByPriorityLabels drives sortPriority against real labels —
// P0-style and priority:-style, mixed case — and proves an issue carrying
// neither sorts after every labeled one rather than first.
func TestSortIssuesByPriorityLabels(t *testing.T) {
	items := []gh.IssueItem{
		{Number: 1, Labels: []string{"bug", "p2"}},
		{Number: 2, Labels: []string{"Priority: Critical"}},
		{Number: 3, Labels: []string{}}, // unlabeled — must sort last
		{Number: 4, Labels: []string{"priority/high"}},
	}
	sortIssuesBy(items, sortPriority)
	want := []int{2, 4, 1, 3}
	got := issueNums(items)
	if len(got) != len(want) {
		t.Fatalf("sortPriority by label: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("sortPriority by label: got %v, want %v", got, want)
			break
		}
	}
}

// TestIssuePriority pins the label -> priority mapping directly: P0-P4,
// priority:/priority/ word forms, case-insensitivity, and the no-match
// fallback to 5 (sorts after every real priority).
func TestIssuePriority(t *testing.T) {
	cases := []struct {
		labels []string
		want   int
	}{
		{[]string{"P0"}, 0},
		{[]string{"p4"}, 4},
		{[]string{"priority: critical"}, 0},
		{[]string{"priority:high"}, 1},
		{[]string{"Priority/Medium"}, 2},
		{[]string{"priority/low"}, 3},
		{[]string{"priority: backlog"}, 4},
		{[]string{"bug", "enhancement"}, 5},
		{nil, 5},
	}
	for _, tc := range cases {
		if got := issuePriority(tc.labels); got != tc.want {
			t.Errorf("issuePriority(%v) = %d, want %d", tc.labels, got, tc.want)
		}
	}
}

// TestSortIssuesByEmptyAndSingle mirrors TestSortBeadsByEmptyAndSingle for
// the Issues comparator.
func TestSortIssuesByEmptyAndSingle(t *testing.T) {
	for order := sortLastModified; order < numTrackSorts; order++ {
		var empty []gh.IssueItem
		sortIssuesBy(empty, order)
		if len(empty) != 0 {
			t.Errorf("%s: empty slice grew to %d", order.label(), len(empty))
		}

		single := []gh.IssueItem{{Number: 7, Updated: time.Now()}}
		sortIssuesBy(single, order)
		if len(single) != 1 || single[0].Number != 7 {
			t.Errorf("%s: single-item slice changed: %v", order.label(), single)
		}
	}
}

// trackSortHeaderModel is a minimal beads-tracker model wide enough that the
// header's right side isn't dropped, with a small dependency graph so the
// header/footer tests exercise real data rather than an empty pane.
func trackSortHeaderModel() Model {
	m := seededModel()
	m.width, m.height = 110, 30
	m.manifest = &manifest.Manifest{Trackers: []string{"beads"}, HasTracker: true}
	m.active = tabBeads
	m.hasBeads, m.beadsProbed, m.haveBeads = true, true, true
	m.beads = beadSortFixture()
	return m
}

// TestTrackerHeaderNamesActiveSort drives `s` through the full cycle on the
// Beads tab via the real key handler and asserts, through View() — the same
// headless frame renderer TestListTabsDesign uses — that the header names
// whichever order is active, wrapping back to the first after the fifth
// press.
func TestTrackerHeaderNamesActiveSort(t *testing.T) {
	m := trackSortHeaderModel()
	labels := []string{"last modified", "priority", "oldest first", "newest first", "most dependencies", "by type", "last modified"}
	for i, want := range labels {
		out := stripANSI(m.View())
		if !strings.Contains(out, "sort: "+want) {
			t.Fatalf("press %d: header missing %q\n%s", i, "sort: "+want, out)
		}
		next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
		m = next.(Model)
	}
}

// TestTrackerHeaderNamesActiveSortOnIssues covers the same cycle on the
// Issues tab, since `s` is bound on both.
func TestTrackerHeaderNamesActiveSortOnIssues(t *testing.T) {
	m := seededModel()
	m.width, m.height = 110, 30
	m.active = tabIssues
	m.haveData = true
	m.remote.Issues = []gh.IssueItem{{Number: 1, Title: "one"}, {Number: 2, Title: "two"}}

	out := stripANSI(m.View())
	if !strings.Contains(out, "sort: last modified") {
		t.Fatalf("Issues tab header missing default sort label\n%s", out)
	}
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = next.(Model)
	out = stripANSI(m.View())
	if !strings.Contains(out, "sort: priority") {
		t.Fatalf("Issues tab header did not advance to the next order\n%s", out)
	}
}

// TestSortHintInFooter pins that `s`/`sort` joins the Beads and Issues footer
// hint lists (see keymap in view.go), which design_parity_test.go's keybar
// tests assert survives width-driven dropping at the ends but not necessarily
// the middle — this just checks the pair is declared at all.
func TestSortHintInFooter(t *testing.T) {
	m := trackSortHeaderModel()
	m.width = 200 // wide enough that keybar drops nothing
	bar := stripANSI(m.keybar(m.width))
	if !strings.Contains(bar, "s sort") {
		t.Errorf("Beads footer missing the s/sort hint: %q", bar)
	}

	m.active = tabIssues
	m.manifest = &manifest.Manifest{Trackers: []string{"github"}, HasTracker: true, UsesPullRequests: true, HasUsesPullRequests: true}
	bar = stripANSI(m.keybar(m.width))
	if !strings.Contains(bar, "s sort") {
		t.Errorf("Issues footer missing the s/sort hint: %q", bar)
	}
}

// TestBeadRowsRenderUnderEveryOrderEmptyAndSingle checks beadRows itself (the
// real row-building path, not just the comparator) handles zero and one bead
// under every order without panicking, and that a single bead still renders
// exactly one row.
func TestBeadRowsRenderUnderEveryOrderEmptyAndSingle(t *testing.T) {
	for order := sortLastModified; order < numTrackSorts; order++ {
		empty := trackSortHeaderModel()
		empty.beads = nil
		empty.trackSort = order
		_ = empty.beadRows() // must not panic

		single := trackSortHeaderModel()
		single.beads = []bd.Bead{{ID: "solo", Type: "task", Updated: time.Now()}}
		single.trackSort = order
		rows := rowsOfKind(single.beadRows(), rowBead)
		if len(rows) != 1 {
			t.Errorf("%s: single-bead beadRows produced %d bead rows, want 1", order.label(), len(rows))
		}
	}
}
