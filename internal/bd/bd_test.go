package bd

import (
	"encoding/json"
	"strings"
	"testing"
)

// listPayload is a trimmed copy of a real `bd list --json` response: two beads,
// one of them held up by the other, plus the structural link that must not be
// mistaken for one.
const listPayload = `[
  {
    "id": "bdtest-qt0",
    "title": "Add password reset",
    "status": "open",
    "priority": 1,
    "issue_type": "feature",
    "owner": "someone@example.com",
    "created_at": "2026-08-06T21:32:44Z",
    "updated_at": "2026-08-06T21:32:44Z",
    "labels": ["backend"],
    "dependency_count": 0,
    "dependent_count": 2
  },
  {
    "id": "bdtest-isd",
    "title": "Wire reset email",
    "status": "open",
    "priority": 0,
    "issue_type": "task",
    "owner": "pierce@example.com",
    "description": "## Summary\n\nSend the reset email once the token lands.",
    "acceptance_criteria": "- [ ] The email goes out",
    "created_at": "2026-08-06T21:32:45Z",
    "updated_at": "2026-08-06T21:32:45Z",
    "labels": ["backend"],
    "dependencies": [
      {"issue_id": "bdtest-isd", "depends_on_id": "bdtest-qt0", "type": "blocks"},
      {"issue_id": "bdtest-isd", "depends_on_id": "bdtest-epc", "type": "parent-child"}
    ],
    "dependency_count": 2,
    "dependent_count": 0
  }
]`

// TestOnlyBlocksDependenciesHoldWorkUp pins which links stop a bead being ready.
// beads records four kinds and only one of them is about order: a bead whose
// single link is a parent epic is work you can start today, and filing it under
// "Blocked" would hide it behind an epic that will never close first.
func TestOnlyBlocksDependenciesHoldWorkUp(t *testing.T) {
	var raw []rawBead
	if err := json.Unmarshal([]byte(listPayload), &raw); err != nil {
		t.Fatalf("bd list payload did not parse: %v", err)
	}
	beads := make([]Bead, 0, len(raw))
	for _, r := range raw {
		beads = append(beads, r.bead())
	}

	var isd Bead
	for _, b := range beads {
		if b.ID == "bdtest-isd" {
			isd = b
		}
	}
	if got := strings.Join(isd.Blockers, ","); got != "bdtest-qt0" {
		t.Errorf("bdtest-isd waits on %q, want only the blocks link (bdtest-qt0)", got)
	}
	if isd.Assignee != "pierce" || isd.Type != "task" {
		t.Errorf("fields dropped in parsing: assignee=%q type=%q", isd.Assignee, isd.Type)
	}
	// The overlay renders both of these, and it renders them from the list
	// response — a second `bd show` for a bead already in memory buys nothing.
	if !strings.Contains(isd.Description, "Send the reset email") {
		t.Errorf("description dropped in parsing: %q", isd.Description)
	}
	if !strings.Contains(isd.AcceptanceCriteria, "The email goes out") {
		t.Errorf("acceptance criteria dropped in parsing: %q", isd.AcceptanceCriteria)
	}

	open := OpenSet(beads)
	if !IsBlocked(isd, open) {
		t.Error("a bead whose blocker is still open reported as ready")
	}

	// Closing the blocker takes it out of the list entirely, which is exactly how
	// the blocked bead learns it can start.
	withoutBlocker := OpenSet([]Bead{isd})
	if IsBlocked(isd, withoutBlocker) {
		t.Error("a bead whose blocker has been closed still reported as blocked")
	}
}

// TestBeadsSortHighestPriorityFirst pins the order rows are read in: P0 above
// P1, and a stable tiebreak so two reads of the same workspace never disagree
// about which row comes first.
func TestBeadsSortHighestPriorityFirst(t *testing.T) {
	beads := []Bead{
		{ID: "c", Priority: 2},
		{ID: "a", Priority: 0},
		{ID: "b", Priority: 2},
	}
	sortBeads(beads)
	var got []string
	for _, b := range beads {
		got = append(got, b.ID)
	}
	if strings.Join(got, "") != "abc" {
		t.Errorf("sorted to %v, want [a b c] — P0 first, then id", got)
	}
}

// TestOwnerIsReadFromTheOwnerField pins the field name and the trim. bd sends the
// assignee as `owner` and as a full email address; reading it as `assignee` left
// the column empty in every workspace, and rendering the address whole would
// spend the column on a domain every row repeats.
func TestOwnerIsReadFromTheOwnerField(t *testing.T) {
	for _, c := range []struct{ owner, want string }{
		{"user@example.com", "user"},
		{"pierce", "pierce"},
		{"", ""},
		{"@handle", "@handle"}, // no local part to keep
	} {
		if got := (rawBead{Assignee: c.owner}).bead().Assignee; got != c.want {
			t.Errorf("owner %q rendered as %q, want %q", c.owner, got, c.want)
		}
	}
}

// TestUnblocksCountsEverythingDownstream is the column's whole claim: the number
// beside a bead is how much open work finishing it would eventually free, not
// how many things touch it directly. A bead at the head of a chain and one
// holding up a single leaf must not read the same.
func TestUnblocksCountsEverythingDownstream(t *testing.T) {
	// a ← b ← c ← d is a chain; a also gates e directly.
	beads := []Bead{
		{ID: "a"},
		{ID: "b", Blockers: []string{"a"}},
		{ID: "c", Blockers: []string{"b"}},
		{ID: "d", Blockers: []string{"c"}},
		{ID: "e", Blockers: []string{"a"}},
		{ID: "lonely"},
	}
	got := Unblocks(beads)
	for id, want := range map[string]int{"a": 4, "b": 2, "c": 1, "d": 0, "e": 0, "lonely": 0} {
		if got[id] != want {
			t.Errorf("%s unblocks %d, want %d", id, got[id], want)
		}
	}
	// A bead that frees nothing is absent from the map, which is what lets the
	// column print a blank cell instead of a "0" on most of the pane.
	if _, ok := got["d"]; ok {
		t.Errorf("d frees nothing but has an entry: the column would print 0")
	}
}

// TestUnblocksIgnoresClosedBlockers keeps the count over work that still exists.
// A closed bead is not in the list at all, so naming it as a blocker must not
// credit it with freeing anything.
func TestUnblocksIgnoresClosedBlockers(t *testing.T) {
	beads := []Bead{
		{ID: "open-one", Blockers: []string{"already-closed"}},
		{ID: "other"},
	}
	if got := Unblocks(beads); len(got) != 0 {
		t.Errorf("Unblocks credited a closed bead: %v", got)
	}
}

// TestUnblocksSurvivesACycle guards the render path. bd does not promise an
// acyclic graph, and an unbounded walk here would hang the dashboard rather than
// draw a wrong number.
func TestUnblocksSurvivesACycle(t *testing.T) {
	beads := []Bead{
		{ID: "x", Blockers: []string{"z"}},
		{ID: "y", Blockers: []string{"x"}},
		{ID: "z", Blockers: []string{"y"}},
	}
	got := Unblocks(beads)
	for _, id := range []string{"x", "y", "z"} {
		if got[id] == 0 || got[id] > 3 {
			t.Errorf("%s unblocks %d in a 3-cycle, want 1..3", id, got[id])
		}
	}
}
