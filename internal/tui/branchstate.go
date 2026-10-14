package tui

import (
	"strconv"
	"strings"
	"time"
)

// Branch classification: one record per branch, one classifier, one place where
// the precedence between signals lives.
//
// The signals do not all arrive together. Everything git can see is read in a
// single pass inside loadLocalCmd and lands as `localData.signals`, which the
// next local reload replaces wholesale. Everything GitHub knows — today the
// merged-PR set — arrives on its own schedule and is held separately on the
// Model. Nothing merges the two into a stored struct: they are joined here, at
// read time, every time a branch is classified. That is the whole point. A
// classification cached in localData would be blanked by the next `fetch
// --prune` reload; a GitHub-derived field stored *in* localData would be blanked
// the same way, which is exactly the bug that made every squash-merged branch
// read as live for a whole session.
//
// To add a signal (a TTL on the merged-PR set, a "superseded" probe): add a
// field to gitSignals if git can see it, or to branchSignals if another source
// owns it, fill it in the gatherer below, and add its case to classifyBranch.
// Nothing else has to move.
//
// # Two tiers
//
// Classification runs in two tiers, and the difference between them is what kind
// of statement the signal can make.
//
// The proof tier is the switch in classifyBranch. Every case in it is a signal
// that ALONE guarantees no work is lost: the commits are on origin, or on the
// default branch, or a PR carrying them merged, or there were never any commits.
// A proof short-circuits — first match wins, most specific first — and the branch
// is certainly dead.
//
// The circumstantial tier (weighEvidence) runs only when no proof fires. Its
// signals prove nothing on their own — a branch that was never pushed might be
// this morning's work — but together they describe abandonment: never pushed,
// its PR closed unmerged, idle for weeks, hundreds of commits behind. Each
// carries an integer weight and the branch is *likely* dead once they sum past
// evidenceThreshold.
//
// The tiers do not compete and the score never overrides a proof. This split is
// the whole reason the tier exists: before it, deadness was binary, so a branch
// that had never been pushed and had no PR emitted no signal at all and was
// invisible to Cleanup forever — not treated conservatively, simply absent.
//
// Weights are integers and every verdict names the signals that fired (see
// branchState.because), so a row always explains itself in words. A float score
// shown as a number would be unfalsifiable, and the precedence documented on
// classifyBranch is worth more than the extra expressiveness would buy.

// gitSignals is one branch's git-derived evidence, gathered by loadLocalCmd over
// a single git snapshot so no two of these fields can disagree about how fresh
// they are.
type gitSignals struct {
	gone        bool // the branch was deleted on the remote
	fullyPushed bool // origin has it and the local ref is not ahead of it
	mergedLocal bool // reachable from origin/<default> (git branch --merged)
	empty       bool // no commit has ever landed on it
	ahead       bool // holds commits origin has never seen
	// upstream is the branch's configured upstream name with the remote stripped,
	// or "" — the name GitHub knows this branch's PR by when it was pushed under a
	// different name than the local one.
	upstream string

	// Circumstantial evidence — none of these proves anything alone; they are
	// weighed together by weighEvidence when no proof fires.

	// neverPushed is a branch origin has never carried under any name: no upstream
	// configured, no origin/<name>, and no record of one having been deleted. It is
	// deliberately narrower than "not on origin now" — a branch whose upstream was
	// deleted is `gone`, which is a proof, and must not be double-counted here.
	neverPushed bool
	// idle is how long since the branch ref or its worktree last moved. Zero when
	// activity could not be read, which weighs as no evidence rather than as
	// infinitely idle — an unreadable clock must never argue for deletion.
	idle time.Duration
	// behind is how many commits the DEFAULT branch has that this one does not —
	// deliberately not BranchInfo.Behind, which counts against the branch's own
	// origin copy and is therefore always 0 for exactly the branches this tier
	// exists to catch (no origin counterpart, no base to count against). Measuring
	// the wrong distance made the two heaviest circumstantial signals mutually
	// exclusive, so "never pushed" and "far behind" could never combine.
	//
	// It says the branch has been left where the work moved on from, which is weak
	// on its own (a branch cut this morning is already behind a busy repo) and only
	// counts at all past behindEvidenceFloor. Zero when it could not be read, which
	// weighs as no evidence.
	behind int
	// aheadDefault is how many commits this branch has that the default branch
	// does not — behind's other half. Never weighed; `admin worktree list` shows it.
	aheadDefault int
	// defaultRead is whether aheadDefault and behind were measured: false for the
	// default branch itself, with no default, or when the count failed.
	defaultRead bool
}

// branchSignals is the complete input to the classifier: the git snapshot's
// signals plus the ones only the Model can supply, each from its own source.
type branchSignals struct {
	gitSignals
	protected bool // the default branch, or whatever the main working tree holds
	mergedPR  bool // a merged PR had this branch as its head (GitHub, not git)
	// mergedTipMatch is whether the local ref points at the exact commit GitHub
	// merged. It is what makes mergedPR a proof for a squash-merged branch whose
	// remote copy is gone: those commits are on no other ref, so `ahead` is true and
	// the not-ahead gate below would veto the proof forever. Matching the SHA
	// answers the question the gate was really asking — is this the work that
	// landed, or a branch that reused the name afterwards — without also vetoing
	// every squash merge.
	mergedTipMatch bool
	superseded     bool // this branch's issue has someone else's PR (open or merged), from a different head (GitHub, not git)
	// closedPR is set when a PR with this branch as its head was closed WITHOUT
	// merging (GitHub, not git). On its own it is circumstantial, not a proof: a PR
	// is sometimes closed to be reopened from the same branch, or the branch may
	// have gained commits since. But it is the one signal where a human has
	// explicitly said "not this" about exactly these commits, which is why it
	// weighs heaviest — and when closedTipMatch also holds it becomes a proof: see
	// closedTipMatch.
	closedPR bool
	// closedTipMatch is whether the local ref points at the exact commit GitHub had
	// as the closed PR's head. GitHub keeps a closed PR's commits at
	// refs/pull/N/head, so when the local tip equals that head, deleting the local
	// ref loses nothing — the commits are still reachable there. A branch that
	// gained commits after the close has a different tip, closedTipMatch is false,
	// and closedPR stays circumstantial. Mirrors mergedTipMatch.
	closedTipMatch bool
	// openPR is set when an OPEN PR has this branch as its head. It is not evidence
	// of anything — it is a veto. A branch under review is live work no matter how
	// long it has sat or how far behind it has fallen, and waiting on a reviewer is
	// the single most common way a legitimate branch goes quiet for weeks.
	openPR bool
}

// branchState is the classifier's verdict for one branch: the reason it is safe
// to drop ("" when it is still live), and the signals that produced that reason.
// Callers read this record rather than the raw signals, so the Cleanup box, the
// Branches list, and the delete modal cannot reach different conclusions.
type branchState struct {
	signals branchSignals
	reason  string
	// conf is how sure the reason is. certain comes from the proof tier, likely
	// from the circumstantial one. Callers that destroy things read this, not the
	// reason word: a batch sweep takes only certain, while likely has to be picked
	// by hand.
	conf confidence
	// because names the circumstantial signals that fired, in weight order, for the
	// row to render ("never pushed · 23d idle"). Empty on a certain verdict, whose
	// reason word already is the explanation.
	because []string
}

// confidence is how strongly a branchState's reason is held — which tier produced
// it. It is ordered, so `>= confLikely` reads as "the dashboard thinks this is
// deletable" and `== confCertain` as "and it can prove it".
type confidence int

const (
	// confLive means no signal fired: as far as anything here knows, real work.
	confLive confidence = iota
	// confLikely means circumstantial evidence crossed evidenceThreshold. Shown,
	// never swept — deleting one is always a deliberate pick.
	confLikely
	// confCertain means a proof fired: deleting the ref loses no work.
	confCertain
)

// Circumstantial weights. Integers, and small on purpose: the threshold is
// reachable by more than one combination, so no single signal is a hidden switch.
// The one exception is closedPR + any idleness, which is meant to clear on its
// own — a PR someone closed unmerged weeks ago is the least ambiguous form of
// abandonment GitHub records.
const (
	evidenceThreshold = 4 // sum at or above this reads as likely-dead
	wNeverPushed      = 2 // exists nowhere but this machine
	wClosedPR         = 3 // a human closed its PR without merging
	wIdleLong         = 2 // untouched beyond idleLong
	wIdleShort        = 1 // untouched beyond idleShort
	wBehind           = 1 // left far behind the default branch

	idleShort = 7 * 24 * time.Hour
	idleLong  = 14 * 24 * time.Hour
	// behindEvidenceFloor is how far behind the default branch a branch must fall
	// before that counts as evidence at all. Set well above a normal review cycle:
	// in a busy repo a branch cut this morning can be a hundred commits behind by
	// evening, and that says nothing about whether anyone still wants it.
	behindEvidenceFloor = 300
)

// weighEvidence sums the circumstantial signals and names the ones that fired.
// The names are the row's explanation, so they are phrased as observations
// ("never pushed", "23d idle") rather than as conclusions.
//
// Order of the returned words is weight order, heaviest first, so a truncated row
// keeps the part that carried the verdict.
func weighEvidence(sig branchSignals) (weight int, because []string) {
	add := func(w int, word string) {
		weight += w
		because = append(because, word)
	}
	if sig.closedPR {
		add(wClosedPR, "PR closed")
	}
	if sig.neverPushed {
		add(wNeverPushed, "never pushed")
	}
	switch {
	case sig.idle >= idleLong:
		add(wIdleLong, compactDays(sig.idle)+" idle")
	case sig.idle >= idleShort:
		add(wIdleShort, compactDays(sig.idle)+" idle")
	}
	if sig.behind >= behindEvidenceFloor {
		add(wBehind, strconv.Itoa(sig.behind)+" behind")
	}
	return weight, because
}

// compactDays renders a duration as whole days ("23d"), the only resolution the
// evidence words need — the thresholds are in weeks, so hours would be noise.
func compactDays(d time.Duration) string {
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

// classifyBranch turns one branch's signals into its state. Each signal alone
// means no work is lost by deleting the local ref:
//
//   - "gone"      — the branch was deleted on the remote.
//   - "empty"     — no commit has ever landed on it. Checked *before* the merged
//     signals on purpose: `git branch --merged origin/<default>` lists
//     a branch created off the default branch and never committed to,
//     so such a branch used to report "merged" — reading as work that
//     landed when in fact nothing ever happened on it. It is still
//     deletable (there are no commits to lose), but only once it has
//     gone stale; see isActive.
//   - "merged"    — a PR with this head branch merged on GitHub (catches squash
//     merges, whose commits never land on the default branch), or the
//     branch is reachable from origin/<default> (catches ordinary
//     merges and anything merged by someone else without a PR).
//   - "superseded" — the issue this branch was created for already has a PR someone
//     else landed or opened, from a branch that isn't this one. Someone
//     took the work over, so the local branch is leftovers with no
//     waiting period — unlike an empty branch, which only goes stale on
//     the clock. Gated on not-ahead, same as "merged": a branch with
//     unpushed commits is still live work whatever anyone else did.
//   - "closed"    — a PR with this branch as its head was closed WITHOUT merging, and
//     the local tip is the exact commit that PR's head pointed at. GitHub
//     keeps a closed PR's commits at refs/pull/N/head, so deleting the
//     local ref loses nothing; a branch that gained commits after the
//     close has a different tip and stays live (circumstantial only).
//     Checked before "on origin"/"merged (local)" so a pushed branch
//     whose PR was closed unmerged is not shadowed by the weaker "on
//     origin" reading.
//   - "on origin" — origin has this branch and the local ref is not ahead of it, so
//     every local commit already exists on origin. Judged against
//     origin/<name> when no upstream is configured, so a branch pushed
//     without -u still counts as safely on the remote.
//
// The merged-PR and superseded signals are gated on the branch not being ahead: a
// merged PR whose branch has since gained new local commits is live work again, not
// leftovers. Order is deliberate — the reason shown is the most specific one that
// applies. "merged" (this branch's own PR landed) outranks "superseded" (someone
// else's did) so a branch you merged yourself never reads as taken over.
//
// A protected branch is never dead, whatever else fired. `git branch --merged
// origin/<default>` lists the default branch itself, and a checked-out branch is
// fully pushed as often as not — so without this the dashboard would strike
// through `main` and offer to delete it.
func classifyBranch(sig branchSignals) branchState {
	st := branchState{signals: sig}
	switch {
	case sig.protected:
	case sig.gone:
		st.reason = "gone"
	case sig.mergedPR && (sig.mergedTipMatch || !sig.ahead):
		st.reason = "merged"
	case sig.superseded && !sig.ahead:
		st.reason = "superseded"
	case sig.closedPR && sig.closedTipMatch:
		st.reason = "closed"
	case sig.empty:
		st.reason = "empty"
	case sig.mergedLocal:
		st.reason = "merged"
	case sig.fullyPushed:
		st.reason = "on origin"
	}
	if st.reason != "" {
		st.conf = confCertain
		return st
	}
	// No proof. A protected branch stops here — it is never dead by any route, and
	// letting evidence speak for it would put `main` in Cleanup the moment the repo
	// went quiet for a fortnight. Same for a branch with an open PR: under review is
	// live work, and review is exactly when a branch sits still.
	if sig.protected || sig.openPR {
		return st
	}
	if weight, because := weighEvidence(sig); weight >= evidenceThreshold {
		st.reason, st.conf, st.because = "stale", confLikely, because
	}
	return st
}

// branchState gathers one branch's signals from the model's separately-held
// snapshots and classifies them. This is the join: the git snapshot supplies its
// half, GitHub's merged-PR set supplies the other, and neither is ever written
// into the other's storage.
//
// The `br.Current` flag on a branch row is not enough to decide `protected`: it
// only knows the branch at the *cwd*, which is a linked worktree's branch
// whenever the dashboard is launched from one, so the main working tree's branch
// has to be looked up.
func (m repoView) branchState(branch string) branchState {
	sig := m.local.signals[branch]
	mergedOID, mergedPR := m.mergedHead(branch, sig.upstream)
	closedOID, closedPR := m.remote.ClosedHeads[branch]
	return classifyBranch(branchSignals{
		gitSignals: sig,
		protected:  branch == m.def || branch == m.mainTreeBranch(),
		mergedPR:   mergedPR,
		// An empty OID on either side is never a match: a PR fetched before this
		// carried the SHA, or a branch whose tip could not be read, must fall back to
		// the not-ahead gate rather than pass on two blanks comparing equal.
		mergedTipMatch: mergedOID != "" && mergedOID == m.local.tips[branch],
		superseded:     m.supersededBy(branch),
		closedPR:       closedPR,
		// Same empty-OID guard as mergedTipMatch, for the same reason.
		closedTipMatch: closedOID != "" && closedOID == m.local.tips[branch],
		openPR:         m.hasOpenPR(branch),
	})
}

// mergedHead finds the merged PR for a branch by its local name and by its
// upstream name, since a branch pushed as `HEAD:other-name` is known to GitHub only
// by the latter. When both names have a merged PR, the one whose head is this
// branch's tip wins, so the tip comparison runs against the entry that describes
// this branch; otherwise the local name's entry is used.
func (m repoView) mergedHead(branch, upstream string) (oid string, ok bool) {
	tip := m.local.tips[branch]
	oid, ok = m.remote.MergedHeads[branch]
	if ok && oid != "" && oid == tip {
		return oid, true
	}
	if upstream == "" || upstream == branch {
		return oid, ok
	}
	uo, uok := m.remote.MergedHeads[upstream]
	if uok && (!ok || (uo != "" && uo == tip)) {
		return uo, true
	}
	return oid, ok
}

// hasOpenPR reports whether an open PR in the repo has this branch as its head.
// Read from the open-PR list the PRs tab already holds rather than from a set of
// its own: one fetch, and the veto can never disagree with what that tab shows —
// or with the row, since both go through openPRFor's matching.
func (m repoView) hasOpenPR(branch string) bool {
	return m.openPRFor(branch) != nil
}

// supersededBy reports whether the issue this branch was created for already has a
// PR someone other than the viewer landed or opened, from a branch that isn't this
// one. Two sources, because a merged PR closes its issue and drops it out of the
// open-issue cache:
//
//   - m.remote.MergedByIssue — merged PRs indexed by the issue they closed, the
//     only place a merged-by-someone-else PR can still be tied back to this branch;
//   - m.remote.Issues + m.remote.PRs — for an issue still open, the issue carries
//     its linked open PR's number and the open-PR list carries that PR's author and
//     head branch.
//
// The author check keeps the viewer's own PR (from a differently-named branch) from
// reading as superseded; the head-branch check keeps a branch that IS the PR's head
// (the viewer's own pushed work) out. A missing viewer login makes "someone else"
// unprovable, so the signal stays off rather than misfire on the viewer's own PR.
func (m repoView) supersededBy(branch string) bool {
	me := m.remote.ViewerLogin
	if me == "" {
		return false
	}
	n, ok := branchIssue(branch)
	if !ok {
		return false
	}
	for _, ref := range m.remote.MergedByIssue[n] {
		if ref.Author != me && ref.HeadBranch != branch {
			return true
		}
	}
	for i := range m.remote.Issues {
		it := m.remote.Issues[i]
		if it.Number != n || it.PR == nil {
			continue
		}
		for j := range m.remote.PRs {
			pr := m.remote.PRs[j]
			if pr.Number == it.PR.Number && pr.Author != me && pr.HeadBranch != branch {
				return true
			}
		}
	}
	return false
}

// branchIssue extracts the issue number a local branch was created for, matching
// slug.FromIssue's naming ("<number>-<slug>", or "issue-<number>" when the title
// yields no usable words). Returns (0, false) when the name carries no issue number.
func branchIssue(branch string) (int, bool) {
	name := strings.TrimPrefix(branch, "issue-")
	if i := strings.IndexByte(name, '-'); i >= 0 {
		name = name[:i]
	}
	n, err := strconv.Atoi(name)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
