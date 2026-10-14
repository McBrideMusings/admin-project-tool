package tui

import (
	"strconv"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/keptnote"
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
	secContinue: "Continue Working",  // yours · no open PR, or a draft
	secFixes:    "Fixes Needed",      // yours · changes requested, a check failing, or conflicting
	secMerge:    "Ready to Merge",    // yours · approved, mergeable, checks green
	secWaiting:  "Waiting on Review", // yours · open, nothing broken — unpushed work shows in the git cell
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

// columnPlan is the pane's column geometry, shared by every section: which of
// owner/pr/git no row has anything to say in, and how wide the name, PR and git
// columns are. One plan for the whole pane, so a column starts and ends at the
// same cell in Continue Working as in Waiting on Review — sections that sized
// their own columns put the same column at a different offset under each
// heading, and the eye stops being able to read straight down it.
type columnPlan struct {
	drop map[string]bool // "owner"|"pr"|"git" — empty on every row, so not drawn
	// nameW is the longest branch name in the pane, in cells. The name column is
	// sized to it before the purpose column gets anything: a branch name you
	// cannot read in full is a row you cannot act on, while a purpose cut short
	// still says what the branch is for. 0 means unplanned — size to the row's own
	// name.
	nameW int
	// prW and gitW are the pane's longest PR and git cells, capped at brPRW and
	// brGitW. A column sized for its worst case ("#1234 checks failing ✓") when
	// the longest value is "#289 draft" takes a dozen cells from the name for
	// nothing. 0 means unplanned — use the cap.
	prW, gitW int
}

// planColumns measures every row the pane draws. Each column's value MUST be
// computed the same way brPanelLine renders it, or the plan and the row disagree.
func planColumns(rows []paneRow) columnPlan {
	plan := columnPlan{drop: map[string]bool{"owner": true, "pr": true, "git": true}}
	for _, r := range rows {
		plan.nameW = max(plan.nameW, vlen(r.name))
		plan.prW = min(brPRW, max(plan.prW, vlen(prText(r))))
		plan.gitW = min(brGitW, max(plan.gitW, vlen(gitText(r))))
		if ownerCell(r) != "" {
			plan.drop["owner"] = false
		}
		if prText(r) != "" {
			plan.drop["pr"] = false
		}
		if gitText(r) != "" {
			plan.drop["git"] = false
		}
	}
	return plan
}

// ownerCell is who a row's branch belongs to, printed only in Reviewing: every
// other section is your own work (or, in Unsorted, nobody's), where the column
// would read "you" on every row and say nothing.
func ownerCell(r paneRow) string {
	if r.sec != secTheirs {
		return ""
	}
	return r.owner.who
}

// paneRow is one branch fully resolved: its section, who owns it, its PR or
// landing word, its standing against origin, and what is holding it. The pane
// renders this and nothing else, so the list and the delete path cannot reach
// different verdicts.
type paneRow struct {
	name  string
	sec   section
	owner ownership
	state string   // the PR's review word or the landing proof; "" when neither applies
	holds []string // uncommitted / unpushed / active — what a delete would cost; never drawn
	ready string   // "ready" | "keep" | "held" — the sweep's verdict; never drawn
	// ahead, behind, changed and ignored are the row's git cell, in `git status`
	// notation: ↑ commits on no origin ref, ↓ commits the default branch has that
	// this one does not, ~ files changed in its worktree, !! ignored paths in its
	// worktree the primary checkout lacks. On the default branch itself ↑/↓ are
	// measured against its own upstream instead.
	ahead, behind, changed, ignored int
	// kept is the worker reaper's recorded reason for leaving this branch's
	// worktree on disk (internal/keptnote), nil when there is none.
	kept    *keptnote.Note
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
// holds is what THIS row currently holds — see
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
//
// A non-draft open PR outranks held work: the PR's state is the next action
// (fix it, merge it, or wait), and work not yet pushed to it shows in the row's
// own git cell rather than pulling the branch out of its PR section. A draft
// says the author is still working, so it keeps the old order — held work files
// it under Continue Working, and only a draft holding nothing reaches Fixes Needed.
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
	case pr != nil && !pr.IsDraft && len(broken) > 0:
		return secFixes
	case pr != nil && !pr.IsDraft && pr.Review == gh.ReviewApproved:
		return secMerge
	case pr != nil && !pr.IsDraft:
		return secWaiting
	case unfinished:
		return secContinue
	case own.unknown:
		return secUnsorted
	case pr != nil && len(broken) > 0:
		return secFixes
	default:
		return secContinue
	}
}

// stateWord is the word in the row's PR cell for what is NOT a PR state: the
// proof the branch landed, or a worktree stopped mid-rebase or on conflicted
// files. An open PR's own state is prMark's, never a word. "" for a branch with
// neither — whether its work is on origin is the git cell's job, not this one's.
// "empty" (a landedProof) is a null state, blanked at render by prText in
// local.go.
func stateWord(r paneRow, landed string) string {
	if landed != "" {
		return landed
	}
	for _, b := range r.broken {
		switch b {
		case "changes", "checks failing", "conflicts":
			// Read off the PR, so prMark already shows it.
		default:
			return b
		}
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
//
// An open PR vetoes, so a PR list that never loaded cannot be read as "no open PR":
// while prsUnknown holds there is no proof, and the sweep takes nothing. Deleting
// one branch by hand (`x`) never asks this question and works either way.
func (m repoView) landedProof(branch string, hasOpenPR bool) string {
	if hasOpenPR || m.prsUnknown() {
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
// files and ignored files only the worktree has come first because they are the
// unrecoverable half — removing the worktree ends them, while local-only commits
// survive in the reflog.
func holdsOf(changed, ignored int, unpushed bool, ahead int, landed string) []string {
	var out []string
	if changed > 0 {
		out = append(out, strconv.Itoa(changed)+" uncommitted")
	}
	if ignored > 0 {
		out = append(out, strconv.Itoa(ignored)+" ignored")
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

// prsUnknown reports that GitHub's open-PR list is not known, as opposed to known
// to be empty: the last fetch failed and no earlier one ever succeeded, so there
// are no cached rows either. A failed fetch over a cached list is not unknown —
// ApplyTo keeps the old rows, which are stale but real.
func (m repoView) prsUnknown() bool {
	return m.remote.PRsFetchError != "" && m.remote.PRsFetchedAt.IsZero()
}
