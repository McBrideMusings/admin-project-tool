package tui

import (
	"strings"
)

// Purpose: the sentence under a branch row saying what the branch is FOR.
//
// A branch name is a handle, not a description. "1741-harmonize-promo-codes-
// mainframe-as" and "worktree/lucky-river-8944" are equally opaque a fortnight
// later, and the pane's whole claim — that you can see what you were doing —
// rests on a real sentence being there.
//
// Four rungs, most authoritative first, and the rung that answered is PRINTED
// beside the text. That matters: a PR title is a description of a change, a branch
// note is a statement of intent, and a commit subject is neither — it is the last
// thing that happened. Showing them identically would let the weakest of the three
// pass for the strongest.
type purpose struct {
	src  string // "note" | "pr" | "issue" | "commit" | ""
	text string
}

// purposeOf assembles the ladder from sources the model already holds, except the
// branch note, which is read from git config alongside the rest of the local load.
//
//  1. note   — `git config branch.<name>.description`, written by you (or by an
//     agent on branch creation). The only rung where somebody stated intent
//     rather than described a change, so it outranks everything.
//  2. pr     — the pull request's title.
//  3. issue  — the title of the issue the branch name points at, for a branch that
//     has no PR yet. This is the common shape for work in flight.
//  4. commit — the subject of the newest NON-MERGE commit the branch added.
//     Merges are skipped because "Merge origin/main into 1719-theme-equip" is a
//     branch describing its maintenance rather than its purpose; four of the
//     twenty-six branches this was built against said exactly that.
func (m repoView) purposeOf(branch string, prTitle string, issue int) purpose {
	if d := strings.TrimSpace(m.local.branchNote[branch]); d != "" {
		return purpose{src: "note", text: d}
	}
	if t := strings.TrimSpace(prTitle); t != "" {
		return purpose{src: "pr", text: t}
	}
	if issue > 0 {
		if t := m.issueTitle(issue); t != "" {
			return purpose{src: "issue", text: t}
		}
	}
	if s := strings.TrimSpace(m.local.lastSubject[branch]); s != "" {
		return purpose{src: "commit", text: s}
	}
	return purpose{}
}

// issueTitle finds an issue's title in the open-issue cache. A branch whose issue
// has since been closed finds nothing here and falls through to its commit
// subject, which is the right answer: the issue is done, so it no longer explains
// why the branch is still sitting there.
func (m repoView) issueTitle(n int) string {
	for i := range m.remote.Issues {
		if m.remote.Issues[i].Number == n {
			return m.remote.Issues[i].Title
		}
	}
	return ""
}

// issueOfBranch is the issue number a branch name points at, matching the naming
// that `repo branch` writes ("<number>-<slug>" or "issue-<number>"). Returns 0 when
// the name carries none. Thin wrapper over branchIssue in branchstate.go, which
// answers the same parse for a different question (the superseded probe) — one
// function owns the naming rule, so a change to it only has to move once.
func issueOfBranch(branch string) int {
	n, ok := branchIssue(branch)
	if !ok {
		return 0
	}
	return n
}
