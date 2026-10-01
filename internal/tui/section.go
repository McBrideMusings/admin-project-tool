package tui

import (
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
)

// Sections: the Branches pane groups by WHAT YOU DO NEXT, not by what a branch is.
//
// A branch moves between sections only when a state changes that somebody caused —
// a push, a review, a merge, a conflict appearing. No section is entered or left by
// a clock running out, which is the property the old 24-hour idle rule did not
// have: idleness is displayed so you can judge, never acted on.
//
// The ladder below is evaluated in order and the FIRST match wins, so a branch is
// in exactly one place. Two consequences worth stating, because both were design
// arguments:
//
//   - Clean Up is checked before ownership, so a colleague's merged branch and
//     yours sit together. "Deletable" is a fact about the branch, not about who
//     wrote it, and splitting it by owner would put the same one-keystroke sweep
//     behind two headings.
//   - Unknown ownership is checked before "not yours", so a branch nothing
//     identifies is never filed under somebody else by default.
type section int

const (
	secMain     section = iota // the default branch, pinned above everything
	secCleanup                 // proven landed — deleting loses nothing
	secUnsorted                // nothing identifies who started it
	secTheirs                  // not yours, fetched here, still live
	secFixes                   // yours, and something is broken
	secMerge                   // yours, approved, nothing in the way
	secWaiting                 // yours, out for review, nothing for you to do
	secContinue                // yours, no open PR — unfinished
	secCount
)

// sectionTitle is the heading text. The measurement that files a branch under
// each one used to be printed on the heading itself; it read as noise once you
// knew the sections by name, so it now lives only here and in classifySection's
// comments — one heading line instead of a heading plus a restated rule.
var sectionTitle = map[section]string{
	secContinue: "Continue Working",  // yours · no open PR, or a draft with nothing broken
	secFixes:    "Fixes Needed",      // yours · changes requested, a check failing, or conflicting
	secMerge:    "Ready to Merge",    // yours · approved, mergeable, checks green
	secWaiting:  "Waiting on Review", // yours · open, nothing for you to do
	secTheirs:   "Reviewing",         // not yours · fetched here and still live
	secCleanup:  "Clean Up",          // any owner · proven landed — deleting loses nothing
	secUnsorted: "Unsorted",          // nothing identifies who started it — claim or delete
}

// sectionOrder is the order the pane draws, which is NOT the order the ladder
// evaluates. The ladder is ordered by which test is most specific; the pane is
// ordered by how much the section wants your attention today.
var sectionOrder = []section{secContinue, secFixes, secMerge, secWaiting, secTheirs, secCleanup, secUnsorted}

// showsPurpose reports whether a section's rows draw their purpose subline. A
// purpose subline says what a branch is FOR, so you can resume it. A Clean Up
// row is about to be deleted, so the line is dead weight there — and because
// only some rows have one, it also makes row height irregular, which is the
// one property the fixed-column pane exists to provide.
func (s section) showsPurpose() bool { return s != secCleanup }

// columnPlan is what a section does with its own owner/state/hold/ready
// columns: which are dropped from every row in the section, and which of those
// dropped columns hoist a non-empty value onto the section's own rule as a
// chip. Computed once per section by planColumns so every row, subline and
// heading builder for that section agrees on it.
type columnPlan struct {
	drop  map[string]bool // "owner"|"state"|"hold"|"ready" — removed from this section's rows
	chips []string        // hoisted non-empty values, in row column order: owner, state, hold, ready
}

// planColumns decides, for one section's rows, which of owner/state/hold/ready
// are constant across every row. A column constant and non-empty is dropped
// from the rows and printed once as a chip on the section's rule; a column
// constant and empty (every row has nothing to say) is dropped and named
// nowhere; a column that varies is left on the rows untouched.
//
// A section of fewer than two rows hoists nothing — there is no "constant
// across the section" to observe with only one data point, and the freed
// width would buy that one row a wider name for no reader-visible reason.
// secMain never hoists either: it draws no heading, so a chip there would be
// printed nowhere and just vanish.
//
// Each column's value MUST be computed the same way brPanelLine renders it,
// or the plan and the row disagree — a column would be dropped from the row
// while the plan believes it varies, or printed on the rule while the row
// still shows a different value.
func planColumns(sec section, rows []paneRow) columnPlan {
	plan := columnPlan{drop: map[string]bool{}}
	if len(rows) < 2 || sec == secMain {
		return plan
	}
	cols := []struct {
		name string
		val  func(paneRow) string
	}{
		{"owner", func(r paneRow) string {
			if r.sec == secMain {
				return ""
			}
			return r.owner.who
		}},
		{"state", func(r paneRow) string { return stateCell(r) }},
		{"hold", func(r paneRow) string { return strings.Join(r.holds, " · ") }},
		{"ready", func(r paneRow) string {
			if r.sec == secMain {
				return ""
			}
			return r.ready
		}},
	}
	for _, c := range cols {
		first := c.val(rows[0])
		constant := true
		for _, r := range rows[1:] {
			if c.val(r) != first {
				constant = false
				break
			}
		}
		if !constant {
			continue
		}
		plan.drop[c.name] = true
		if first != "" {
			plan.chips = append(plan.chips, first)
		}
	}
	return plan
}

// paneRow is one branch fully resolved: its section, who owns it, the single
// word describing its state, and what is holding it. The pane renders this and
// nothing else, so the list and the delete path cannot reach different verdicts.
type paneRow struct {
	name    string
	sec     section
	owner   ownership
	state   string   // one or two tokens, never longer than brStateW
	holds   []string // uncommitted / unpushed — what a delete would cost
	ready   string   // "ready" | "keep" | "held"
	broken  []string // why it is in Fixes Needed
	issue   int      // issue number parsed from the branch name, 0 when none
	purpose purpose  // the subline: what this branch is for
	pr      *gh.OpenPR
	prNum   int
}

// blockers lists everything wrong with an open PR, measured independently. One
// list rather than a precedence chain because they co-occur and the row should
// say so: a branch can be conflicting AND have a check failing, and hiding the
// second behind the first sends you back for a second look.
func blockers(pr *gh.OpenPR, op string, conflicted int) []string {
	var out []string
	if pr != nil {
		if pr.Review == gh.ReviewChangesRequested {
			out = append(out, "changes")
		}
		if pr.Checks == gh.ChecksFailing {
			out = append(out, "checks failing")
		}
		if pr.Mergeable == "CONFLICTING" {
			out = append(out, "conflicts")
		}
	}
	// A paused rebase/merge in the worktree, or an index git records as unmerged,
	// is the same class of fact as a conflicting PR — work stopped on something
	// that needs a person — and belongs in the same section.
	if op != "" {
		out = append(out, opLabel(op))
	} else if conflicted > 0 {
		out = append(out, conflictNote(conflicted))
	}
	return out
}

// classifySection is the ladder. Every argument is a measurement, so the rule for
// each section is readable in one place rather than spread over the renderer.
// classifySection is the ladder. holds is what THIS row currently holds — see
// holdsOf — and it is what keeps a landed branch someone is still sitting in out
// of Clean Up: "empty" or "merged" is a proof about the committed history, not a
// statement that the WORKTREE has nothing left in it.
//
// claimedByOther is the only thing that can override real held work: a PR or a
// first commit that names someone ELSE. Ownership resolves to `unknown` exactly
// when neither rung fired — no PR, no commit of its own — and a branch that never
// got attributed anything still carries the strongest local evidence there is: it
// is checked out on THIS machine with real uncommitted files sitting in it. That
// counts as yours to finish unless a PR or a commit history says it is somebody
// else's, which is why `unfinished` treats `own.mine` and `own.unknown` the same
// and only `own.theirs` (mine==false, unknown==false — someone else's PR or first
// commit) blocks it.
func classifySection(protected bool, own ownership, landed string, pr *gh.OpenPR, broken []string, holds []string) section {
	claimedByOther := !own.mine && !own.unknown
	unfinished := len(holds) > 0 && !claimedByOther

	switch {
	case protected:
		return secMain
	case landed != "" && !unfinished:
		return secCleanup
	case claimedByOther:
		return secTheirs
	case unfinished:
		return secContinue
	case own.unknown:
		return secUnsorted
	case pr != nil && len(broken) > 0:
		return secFixes
	case pr != nil && !pr.IsDraft && pr.Review == gh.ReviewApproved:
		return secMerge
	case pr != nil && !pr.IsDraft:
		return secWaiting
	default:
		return secContinue
	}
}

// stateWord is the row's one-or-two-token status. Deliberately short: the column
// is a fixed width and a phrase that needs truncating to fit ("conflicts with
// main" cut to "conflicts with…") reads worse than the short word does.
// Note: "empty" (from stateWord's caller, landedProof) and "no commits" (the
// fallback in rowFor) are null states rendered as blank padded cells at the
// render layer by stateCell in local.go.
func stateWord(r paneRow, landed string) string {
	switch {
	case landed != "":
		return landed
	case len(r.broken) > 0:
		return r.broken[0]
	case r.pr != nil && r.pr.IsDraft:
		return "draft"
	case r.pr != nil && r.pr.Review == gh.ReviewApproved:
		return "approved"
	case r.pr != nil:
		return "in review"
	}
	return ""
}

// finishedProof reports whether a proof means the work is DONE rather than merely
// recoverable. The distinction decides two things: whether a branch belongs in
// Clean Up at all, and whether being touched recently is allowed to hold it.
//
//   - merged / remote deleted / superseded / closed — somebody landed, replaced, or
//     explicitly declined this. The branch is leftovers, and touching it an hour
//     ago does not make it less so.
//   - on origin — the commits are backed up, which says the branch is *safe* to
//     delete, not that it is finished. This is the ordinary state of live work
//     you have pushed, so filing it as cleanup would offer to delete the branch you
//     are in the middle of.
//   - empty — no commits ever landed, which is what a branch cut ten minutes ago and
//     one abandoned in March both look like. Only the clock separates them, so this
//     is the one proof that keeps the activity hold.
func finishedProof(reason string) bool {
	switch reason {
	case "merged", "remote deleted", "superseded", "closed":
		return true
	}
	return false
}

// landedProof says why deleting this branch loses nothing, or "" when it would.
// An open PR vetoes every route: whatever the commit graph says, work under review
// is not leftovers, and a `git branch -d` on it costs you the branch the PR is
// pointing at.
//
// "on origin" is deliberately NOT a proof here even though the classifier offers
// it. See finishedProof: being pushed is the normal state of work in progress.
func (m Model) landedProof(branch string, hasOpenPR bool) string {
	if hasOpenPR {
		return ""
	}
	// Only a proof counts. The circumstantial tier says a branch looks abandoned,
	// which is a reason to show it, never a reason to call deleting it free.
	if st := m.branchState(branch); st.conf == confCertain {
		reason := st.reason
		if reason == "gone" {
			reason = "remote deleted"
		}
		if reason == "on origin" {
			return ""
		}
		return reason
	}
	return ""
}

// readyWord is the per-row delete verdict, and it is a ROW STATE rather than a
// section on purpose: "can I delete this" is a different question from "what do I
// do next", it applies to your branches and other people's identically, and giving
// it its own heading is what made an earlier draft file the same branch twice.
func readyWord(landed string, holds []string) string {
	switch {
	case len(holds) > 0:
		return "held"
	case landed != "":
		return "ready"
	}
	return "keep"
}

// holdsOf is what a delete would cost, in the order it would cost it. Uncommitted
// files come first because they are the unrecoverable half — removing the worktree
// ends them, while local-only commits survive in the reflog.
func holdsOf(changed int, unpushed bool, ahead int, landed string) []string {
	var out []string
	if changed > 0 {
		out = append(out, strconv.Itoa(changed)+" uncommitted")
	}
	// Commits that exist nowhere else are only a hold while nothing proves they
	// landed. A squash-merged branch reads as "unpushed" by the commit graph — its
	// commits are on no other ref — and saying so would put every merged branch
	// behind a warning that the merge itself already answered.
	if landed == "" && unpushed && ahead > 0 {
		out = append(out, strconv.Itoa(ahead)+" unpushed")
	} else if landed == "" && unpushed {
		out = append(out, "unpushed")
	}
	return out
}
