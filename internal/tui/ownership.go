package tui

import (
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
)

// Ownership: whose work a branch is, which is the axis the Branches pane groups on
// and the one thing git does not record.
//
// Git tracks who authored each commit and GitHub tracks who opened each PR;
// neither answers "is this branch mine", because a branch you keep up to date with
// the default branch carries your address in its history whatever its origin, and
// a branch you created from an agent run carries an address you may not recognise.
// So the answer is assembled from two rungs over a CONFIGURED identity, and when
// neither rung fires the answer is "unknown" — never a guess.
//
//  1. The author of the branch's pull request, if it has one. GitHub already
//     decided this and it is the strongest statement available.
//  2. The author of the FIRST commit the branch added to the default branch —
//     who started the work. Not "any commit by you": merging main in writes your
//     address into a colleague's branch, and an any-commit test therefore claims
//     branches you only helped rebase. On the repo this was designed against, that
//     was five of the six teammate branches present.
//
// There is deliberately no third rung. A branch nothing identifies goes to the
// Unsorted section, where it asks to be claimed or deleted — a wrong guess puts it
// in a section you then have to un-learn, which is worse than a row that admits it
// does not know.
type ownership struct {
	// who is the display name — "you", or a person's name derived from the commit
	// address ("darren"), or the GitHub login when there is no commit to name them
	// by, or "" when nothing identified it.
	who string
	// mine is the answer the sections actually branch on.
	mine bool
	// unknown means neither rung fired. Distinct from `!mine`: somebody else's
	// branch is a fact, an unattributable branch is a question.
	unknown bool
	// via names the rung that answered, for the row detail and for debugging a
	// surprising verdict.
	via string
}

// ownerOf resolves one branch. prAuthor is the login of the branch's PR ("" when
// it has none) and firstAuthor is the email from git.FirstAuthor ("" when the
// branch adds no non-merge commit of its own).
func ownerOf(id config.Identity, prAuthor, firstAuthor string) ownership {
	// The display name prefers the commit address over the GitHub login: the column
	// exists to name the person the way you already refer to them, and "darren" is
	// that name where "agrippanux" is an account.
	named := personName(firstAuthor)

	if prAuthor != "" {
		if id.IsMe(prAuthor) {
			return ownership{who: "you", mine: true, via: "pr author"}
		}
		// The friendly name only stands in for the PR author when the two plausibly
		// name the same person. firstAuthor is whoever wrote the branch's FIRST
		// commit, which is not always who opened the PR — a rework, a handoff, an
		// agent's own commit identity under a human's PR — and showing that name
		// unconditionally means the row can name the wrong person outright rather
		// than merely being less pretty about the right one.
		who := prAuthor
		if named != "" && namesMatch(named, prAuthor) {
			who = named
		}
		return ownership{who: who, via: "pr author"}
	}
	if firstAuthor == "" {
		return ownership{unknown: true, via: "nothing"}
	}
	if id.Owns(firstAuthor) {
		return ownership{who: "you", mine: true, via: "first commit"}
	}
	return ownership{who: named, via: "first commit"}
}

// namesMatch is a loose check that a commit-derived name and a GitHub login could
// name the same person — one contains the other, case-insensitively. It is
// deliberately permissive (catches "chris" / "chris-foreVRGames") rather than
// exact, because the two are drawn from different systems that have no reason to
// agree on spelling; the point is only to rule out the case where they are
// obviously two different people ("darren" / "agrippanux").
func namesMatch(name, login string) bool {
	name, login = strings.ToLower(name), strings.ToLower(login)
	return strings.Contains(login, name) || strings.Contains(name, login)
}

// personName is the local part of a git author address — "darren@agrippa.org"
// becomes "darren". Returns "" for anything that is not an address, so a caller
// can fall back rather than print a fragment of one.
func personName(email string) string {
	i := strings.IndexByte(email, '@')
	if i <= 0 {
		return ""
	}
	return strings.ToLower(email[:i])
}

// prAuthorOf is the login that opened the branch's PR, falling back to the
// merged-PR authors that ride with the merged-head set when there is no open one
// — a landed branch whose PR has since closed still needs its author for
// ownership. "" when no PR is known at all. openPR is the open PR already
// resolved for this branch (nil if none); the open-PR list is not re-scanned
// here, since the caller has already paid for that scan once via openPRFor.
func (m Model) prAuthorOf(branch string, openPR *gh.OpenPR) string {
	if openPR != nil {
		return openPR.Author
	}
	return m.remote.MergedAuthors[branch]
}

// ownership resolves one branch's owner from the model's identity and snapshots.
// openPR is passed in rather than looked up again — see prAuthorOf.
func (m Model) ownership(branch string, openPR *gh.OpenPR) ownership {
	return ownerOf(m.identity, m.prAuthorOf(branch, openPR), m.local.firstAuthor[branch])
}
