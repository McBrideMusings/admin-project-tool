package tui

import (
	"errors"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

type cleanupKind int

const (
	cleanupWorktree cleanupKind = iota
	cleanupBranch
	cleanupPrune
)

// cleanupTarget is one deletable thing. quarantined items are held by something —
// a paused operation, live use, uncommitted files, or local-only commits — and are
// never pre-selected; deleting one needs an extra confirm and removes its worktree
// with --force.
//
// dirty and unpushed are kept apart rather than collapsed into one "unsaved" flag
// because they are not equally recoverable. Local-only commits survive the delete:
// the branch ref goes, but the commits sit in the reflog and `git reflog` brings
// them back. Uncommitted files in a worktree do not survive at all — removing the
// directory is the end of them, with no reflog, stash, or object to recover from.
// One word for both understated the destructive case.
type cleanupTarget struct {
	id     string
	kind   cleanupKind
	branch string
	path   string
	reason string // why it is deletable — see deadReason
	op     string // paused operation holding the worktree — see git.Worktree.Op
	// conflicts is how many files in the worktree git records as unmerged. It is
	// tracked apart from dirty because it decides whether the delete's auto-stash
	// is even possible: `git stash push` must write one index entry per path, an
	// unmerged path has none, and git refuses the whole stash rather than skip it.
	// A worktree with conflicts is therefore the one case where the delete cannot
	// be made recoverable, and the stepper refuses it instead of destroying files.
	conflicts int
	// statusUnread is set when the worktree's status could not be read at all, so
	// conflicts is not zero-because-clean but zero-because-unknown. It rides with
	// conflicts everywhere, because both mean the same thing to the delete: do not
	// bet on the stash.
	statusUnread bool
	active       bool // in use right now — see isActive
	dirty        bool // uncommitted changes in the worktree; unrecoverable once removed
	changed      int  // how many files are uncommitted — shown to size the loss, never weighed
	unpushed     bool // commits that exist nowhere but here; recoverable via the reflog
	// quarantined is the delete-safety flag the buckets, the sweep, and the force
	// decision all read. Always held(), never set independently.
	quarantined bool
	// conf is which tier judged this dead — see confidence. Only confCertain is ever
	// swept in a batch; confLikely has to be picked by hand, however unheld it is.
	conf confidence
	// because names the circumstantial signals behind a confLikely verdict, for the
	// row to show its working. Empty when conf is confCertain.
	because []string
}

// sweepable reports whether a batch clean may take this target unattended: proven
// dead, and nothing holding it. Both halves are required and they are independent
// — a proof says no commits are lost, a hold says the workspace is in use — so
// this is the one place that decides, rather than each caller re-anding the pair.
func (t cleanupTarget) sweepable() bool {
	return !t.quarantined && (t.conf == confCertain || t.kind == cleanupPrune)
}

// applyWtState copies a worktree's status read onto the target. Going through one
// setter rather than three field assignments at each construction site is what
// stops a caller filling in dirty and forgetting conflicts — the split that let an
// unreadable worktree read as unconflicted.
func (t *cleanupTarget) applyWtState(st wtState) {
	t.dirty, t.conflicts, t.statusUnread = st.dirty, st.conflicts, st.unread
	t.changed = st.changed
}

// unstashable reports whether the delete's auto-stash cannot be trusted to run:
// either git records unmerged paths, so `git stash push` refuses outright, or the
// worktree's status could not be read, so nothing here knows whether it would.
// Both take the same route — refuse and offer the explicit force — because the
// files a wrong guess destroys have no reflog, stash, or object behind them.
func (t cleanupTarget) unstashable() bool { return t.conflicts > 0 || t.statusUnread }

// held reports whether anything about this target says leave it alone — the single
// definition of quarantine, so no caller can construct a target that is dangerous
// to delete but not flagged as such.
//
// unstashable() is in the list even though every real construction site also sets
// dirty (an unmerged path always shows in porcelain, and an unread status fails
// safe to dirty). Relying on that would make quarantine depend on an agreement
// held nowhere: a target built with conflicts but not dirty would go out with
// quarantined false, and the forced removal reads that flag as its --force — so
// the one delete that most needs forcing would be the one that goes out unforced.
func (t cleanupTarget) held() bool {
	return t.op != "" || t.active || t.dirty || t.unpushed || t.unstashable()
}

// staleAfter is how long a branch or worktree must sit untouched before the
// dashboard will treat it as leftovers rather than as work in progress. Anything
// newer is quarantined whatever the dead signals say, so a batch sweep cannot take
// a workspace out from under someone mid-task.
const staleAfter = 24 * time.Hour

// mergedPRLimit caps how many recent merged PRs the merged-set detection scans.
// The set catches squash-merged branches, whose commits never land on the default
// branch; the sync re-reads it every poll (see syncCmd).
const mergedPRLimit = 200

// cleanupSet is the categorized deletable items, computed without rendering so
// the tab badge can count them cheaply.
//
// Three buckets, because there are three genuinely different answers to "may I
// delete this": proven dead and nothing holding it (merged*), probably abandoned
// but only circumstantially (likely), and held by work that would be lost
// (quarantine). Only the first is ever swept in a batch. The middle one exists
// because the alternative is not caution, it is silence: before it, a branch that
// was never pushed and had no PR produced no signal at all and never appeared
// here, which is how a dozen of them pile up unnoticed.
//
// The likely bucket is narrow by construction, and that is not a mis-set weight.
// Work that reached origin gets a *proof* (`on origin`), and work that did not is
// held by definition — its commits are nowhere else — so almost everything the
// evidence tier catches lands in quarantine rather than here. What is left is the
// branch that is both on origin and ahead of it: pushed, then carried on locally,
// then declined. The tier's value is the surfacing, and surfacing happens in both
// buckets; do not "fix" the emptiness of this one by loosening the weights.
type cleanupSet struct {
	mergedWts      []cleanupTarget
	mergedBranches []cleanupTarget
	likely         []cleanupTarget
	quarantine     []cleanupTarget
	pruneCount     int
}

// all flattens the set into one target list (prune included as a synthetic target).
func (s cleanupSet) all() []cleanupTarget {
	out := append([]cleanupTarget{}, s.mergedWts...)
	out = append(out, s.mergedBranches...)
	out = append(out, s.likely...)
	out = append(out, s.quarantine...)
	if s.pruneCount > 0 {
		out = append(out, cleanupTarget{id: "prune", kind: cleanupPrune})
	}
	return out
}

// byBranch indexes the set's branch-bearing targets by branch name, so the
// Branches panel can mark a row using the exact classification the Cleanup box
// uses — one classifier, two renderers, no chance of the two disagreeing. The
// synthetic prune target has no branch and is absent here.
func (s cleanupSet) byBranch() map[string]cleanupTarget {
	out := map[string]cleanupTarget{}
	for _, t := range s.all() {
		if t.branch != "" {
			out[t.branch] = t
		}
	}
	return out
}

// cleanupTargets computes the Cleanup view's deletable items from local git state
// + merged-PR detection — the data, with no rendering.
func (m Model) cleanupTargets() cleanupSet {
	var set cleanupSet
	if !m.haveLocal {
		return set
	}
	byBranch := m.worktreeByBranch()

	// Worktrees whose branch is dead by any of the signals.
	for _, w := range m.local.wts {
		if w.Branch == "" || w.Path == m.repoRoot {
			continue
		}
		if !pathExists(w.Path) {
			set.pruneCount++
			continue
		}
		st := m.branchState(w.Branch)
		if st.reason == "" {
			continue
		}
		t := cleanupTarget{
			id: "wt:" + w.Path, kind: cleanupWorktree,
			branch: w.Branch, path: w.Path, reason: st.reason, op: w.Op,
			conf: st.conf, because: st.because,
			active:   m.isActive(w.Branch, w.Path),
			unpushed: m.local.branchUnsafe[w.Branch],
		}
		t.applyWtState(m.local.wtState[w.Path])
		t.quarantined = t.held()
		set.place(t)
	}

	// Local branches (no worktree) that are dead by any of the signals.
	for _, br := range m.local.branches {
		if br.Current || br.WorktreePath != "" || byBranch[br.Name] != "" {
			continue
		}
		st := m.branchState(br.Name)
		if st.reason == "" {
			continue
		}
		t := cleanupTarget{
			id: "br:" + br.Name, kind: cleanupBranch,
			branch: br.Name, reason: st.reason,
			conf: st.conf, because: st.because,
			active:   m.isActive(br.Name, ""),
			unpushed: m.local.branchUnsafe[br.Name],
		}
		t.quarantined = t.held()
		set.place(t)
	}
	return set
}

// place files a classified target into its bucket. Quarantine wins over
// confidence: something holding unsaved work is the more urgent fact about a row,
// and it is what decides whether a sweep may touch it, so a proven-dead worktree
// full of uncommitted files belongs with the held ones and not with the safe ones.
func (s *cleanupSet) place(t cleanupTarget) {
	switch {
	case t.quarantined:
		s.quarantine = append(s.quarantine, t)
	case t.conf == confLikely:
		s.likely = append(s.likely, t)
	case t.kind == cleanupWorktree:
		s.mergedWts = append(s.mergedWts, t)
	default:
		s.mergedBranches = append(s.mergedBranches, t)
	}
}

// mainTreeBranch is the branch checked out in the main working tree, or "" when it
// is detached. worktreeByBranch deliberately omits the main tree (nothing should
// offer to cd there or delete it), so this is the only way to see what it holds.
func (m Model) mainTreeBranch() string {
	for _, w := range m.local.wts {
		if w.Path == m.repoRoot {
			return w.Branch
		}
	}
	return ""
}

// worktreeOp is the paused operation holding the worktree at path (see
// git.Worktree.Op), read from the worktree list the panes already show.
func (m Model) worktreeOp(path string) string {
	for _, w := range m.local.wts {
		if w.Path == path {
			return w.Op
		}
	}
	return ""
}

// deadReason says why a branch is safe to drop, or "" when it is still live. It
// reads the branch's one classification record — see branchstate.go for the
// signals and their precedence.
func (m Model) deadReason(branch string) string {
	return m.branchState(branch).reason
}

// isActive reports whether something is working on this branch right now, which
// no dead signal can tell: a branch created minutes ago and a branch abandoned
// months ago are the same shape in the commit graph — zero commits, reachable from
// the default branch — and only their timestamps separate them.
//
// Three probes, any of which alone means hands off:
//
//   - herdr has a workspace open on the worktree (something is sitting in it now);
//   - the branch ref moved within staleAfter (its reflog, which for a branch that
//     has never been committed to records when it was created);
//   - the worktree was touched within staleAfter (its private index, or the
//     checkout directory).
//
// Being active never makes a branch live again — the reason word still says why it
// is deletable. It quarantines it, so a batch sweep passes it over and deleting it
// takes a deliberate pick. path is "" for a branch with no worktree.
func (m Model) isActive(branch, path string) bool {
	if path != "" && m.local.herdrOpen[path] {
		return true
	}
	t, ok := m.local.touched[branch]
	return ok && time.Since(t) < staleAfter
}

// cleanupKindLabel is the human label for a target, used in the delete modal.
func cleanupKindLabel(t cleanupTarget) string {
	switch t.kind {
	case cleanupWorktree:
		return "worktree " + t.branch
	case cleanupBranch:
		return "branch " + t.branch
	case cleanupPrune:
		return "prune stale records"
	}
	return t.branch
}

// cleanupReason is the short label shown for a target, in the Cleanup box and on
// its branch row. It reads the reason recorded at classification time rather than
// re-deriving it, so the two surfaces can never disagree about why something is
// deletable.
//
// Labels are ordered by how much the thing they name would cost to lose. A paused
// operation comes first: it names the git state that is actually stuck, and that is
// the thing to go deal with. "active" next, because something is using it right now.
// Then "uncommitted" ahead of "unpushed" — files that exist only in the worktree die
// with it, while local-only commits survive in the reflog. The dead reason comes
// last: what matters about an item something is holding is the hold.
func cleanupReason(t cleanupTarget) string {
	switch {
	case t.kind == cleanupPrune:
		return ""
	case t.op != "":
		return opLabel(t.op)
	case t.active:
		return "active"
	case t.dirty:
		// The count is the whole point of the word here: "uncommitted" reads the same
		// over one stray scratch file and over an unfinished feature, and those are
		// the two things the reader is actually trying to tell apart before deciding
		// whether to go and look.
		if t.changed > 0 {
			return "uncommitted " + strconv.Itoa(t.changed)
		}
		return "uncommitted"
	case t.unpushed:
		return "unpushed"
	}
	return t.reason
}

// cleanupDetail is the evidence behind a likely-stale row, as one line ("never
// pushed · 23d idle"), or "" for anything the proof tier decided.
//
// A guess has to show its working. "merged" names a fact the reader can go and
// check; "stale" alone is the dashboard asserting a mood, and the only way to
// judge it is to see which signals fired — that is the difference between a row
// someone can act on and one they have to go re-derive by hand.
//
// Held rows get the evidence too, and they are the ones that need it most. The
// common shape is a branch that was never pushed and has sat for weeks: it holds
// unpushed commits, so it is quarantined and the reason column says "unpushed" —
// which is true of this morning's work as well, and on its own gives the reader no
// way to tell the two apart. The hold word stays first (it is the safety fact),
// with the strongest single piece of evidence after it.
func cleanupDetail(t cleanupTarget) string {
	if t.conf != confLikely || len(t.because) == 0 {
		return ""
	}
	if !t.quarantined {
		return strings.Join(t.because, " · ")
	}
	// Held rows get the idle word by preference rather than the heaviest one. A hold
	// already says what is inside the branch, and the heaviest evidence usually says
	// the same thing again — "unpushed · never pushed" spends the column restating
	// itself. What the hold cannot say is whether anyone still wants it, and that is
	// exactly what idleness measures.
	word := t.because[0]
	for _, w := range t.because {
		if strings.HasSuffix(w, "idle") {
			word = w
			break
		}
	}
	return cleanupReason(t) + " · " + word
}

// opLabel turns a paused git operation into the continuous tense the reason column
// reads in — "rebase" is a noun the row could be mistaken for a plan, "rebasing"
// says the worktree is sitting in one right now.
func opLabel(op string) string {
	switch op {
	case git.OpRebase:
		return "rebasing"
	case git.OpMerge:
		return "merging"
	case git.OpCherryPick:
		return "cherry-picking"
	case git.OpRevert:
		return "reverting"
	case git.OpBisect:
		return "bisecting"
	}
	return op
}

// conflictNote renders a conflicted-file count as text a row can carry, e.g.
// "1 conflict" / "3 conflicts", and "" when nothing is conflicted.
func conflictNote(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n) + " conflict" + plural(n)
}

// blockedError is the delete stepper's refusal to touch a worktree whose
// auto-stash cannot be trusted — unresolved conflicts, which git refuses to
// stash, or a status it could not read at all. It is a distinct type rather than
// a plain message so the done phase can find these results again and offer the
// `f` retry that deletes anyway.
type blockedError struct {
	op        string
	conflicts int
	unread    bool // the status read failed, so the conflict count means nothing
}

func (e *blockedError) Error() string {
	// No paused operation is named when op is empty — an unmerged index can also
	// come from a `checkout -m`, and an unreadable worktree may have nothing paused
	// in it at all. Saying "paused operation" there would invent a cause.
	what := "conflicted"
	if e.op != "" {
		what = "paused " + e.op
	}
	if e.unread {
		return what + ", can't read its status — won't risk the stash, nothing deleted"
	}
	return what + ", " + conflictNote(e.conflicts) + " — can't stash, nothing deleted"
}

// isBlocked reports whether a delete result failed the conflict refusal rather
// than a real git error, so only those get re-offered under force.
func isBlocked(err error) bool {
	var be *blockedError
	return errors.As(err, &be)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// deleteBranchUnderCursor (x on the Branches pane) stages the selected branch for
// deletion together with the worktree holding it, if any, and enters the confirm
// phase. It reuses the Cleanup box's delete path, so one branch and its worktree go
// in the right order: the worktree is removed first, then the branch ref.
//
// Unlike the Cleanup box this is not limited to dead branches — the confirm modal,
// which flags anything holding unsaved work, is the safety. Only the branch you are
// standing on and the default branch are refused, since deleting either is a git
// error rather than a choice.
func (m Model) deleteBranchUnderCursor() (tea.Model, tea.Cmd) {
	// Addressed through branchRows, not localPanes: the pane groups by section, so
	// the cursor's Nth row is only the Nth branch when both use the same ordering.
	rows := m.branchRows()
	i := m.paneCursor[paneBranches]
	if i < 0 || i >= len(rows) {
		return m, nil
	}
	b := m.branchInfo(rows[i].name)
	switch {
	case b.Current || b.Name == m.mainTreeBranch():
		m.setStatus("can't delete a branch that's checked out", cYellow)
		return m, nil
	case b.Name == m.def || b.Name == "main" || b.Name == "master":
		m.setStatus("can't delete "+b.Name, cYellow)
		return m, nil
	}
	// branchRefused (below) is the exact same test, kept spelled out here so each
	// branch keeps its own status message — a checked-out branch and the default
	// branch are refused for different reasons and a reader should see which.
	m.confirming = true
	m.confirmTargets = []cleanupTarget{m.targetFor(b)}
	return m, nil
}

// branchRefused reports whether name may not be staged for deletion at all —
// the branch checked out right now, or the default branch — the one check shared
// by every path that stages a delete, so a selection and a single-row `x` can
// never disagree about what's off limits.
func (m Model) branchRefused(b git.BranchInfo) bool {
	return b.Current || b.Name == m.mainTreeBranch() ||
		b.Name == m.def || b.Name == "main" || b.Name == "master"
}

// toggleBranchSelection flips the row under the cursor in and out of the
// multi-select (`space`), refusing the same branches a single `x` refuses.
func (m *Model) toggleBranchSelection() {
	rows := m.branchRows()
	i := m.paneCursor[paneBranches]
	if i < 0 || i >= len(rows) {
		return
	}
	name := rows[i].name
	if rows[i].sec == secMain {
		m.setStatus("can't select "+name, cYellow)
		return
	}
	b := m.branchInfo(name)
	if m.branchRefused(b) {
		m.setStatus("can't delete a branch that's checked out", cYellow)
		return
	}
	if m.selected[name] {
		delete(m.selected, name)
	} else {
		m.selected[name] = true
	}
}

// deleteSelectedBranches (`x` with a nonempty selection) stages every hand-picked
// row into one confirm batch — a mixed pick spanning several sections or
// confidence tiers that neither a single `x` nor `⇧X` (every proven-ready row,
// unconditionally) can express. Rows are read from the current branchRows() build
// rather than trusted from the selection alone, so a branch that vanished between
// being selected and the delete (force-deleted elsewhere, renamed) is silently
// dropped instead of building a target for a name that no longer resolves to
// anything.
func (m Model) deleteSelectedBranches() (tea.Model, tea.Cmd) {
	var targets []cleanupTarget
	for _, r := range m.branchRows() {
		if !m.selected[r.name] || r.sec == secMain {
			continue
		}
		b := m.branchInfo(r.name)
		if m.branchRefused(b) {
			continue
		}
		targets = append(targets, m.targetFor(b))
	}
	if len(targets) == 0 {
		m.setStatus("nothing selected", cDim)
		return m, nil
	}
	m.confirming = true
	m.confirmTargets = targets
	return m, nil
}

// targetFor builds the cleanupTarget the delete stepper needs for one branch — the
// single place that assembles a target from a git.BranchInfo, so `x` on a branch
// row and a `⇧X` sweep can never build two different answers for the same branch.
// That used to be two separate readings: this one, and a second classification
// cleanAll ran independently over m.cleanupTargets(). They agreed almost always
// and diverged on the case that matters most — a branch classifyBranch calls
// fullyPushed ("on origin") keeps that proof even with an OPEN PR attached, because
// the proof tier fires before the openPR gate is ever checked; the sweep read that
// straight through and would delete a branch someone was still reviewing. Building
// every target through the same reader as the row itself is what makes that
// impossible now, not a second check bolted onto the old path.
func (m Model) targetFor(b git.BranchInfo) cleanupTarget {
	st := m.branchState(b.Name)
	t := cleanupTarget{
		id: "br:" + b.Name, kind: cleanupBranch, branch: b.Name,
		reason: st.reason, conf: st.conf, because: st.because,
		unpushed: m.local.branchUnsafe[b.Name],
	}
	// The branch's own WorktreePath comes from the same worktree list, but fall back
	// to the by-branch index so a branch held by a worktree the row didn't record
	// still takes its worktree with it. The main working tree is never removed.
	path := b.WorktreePath
	if path == "" {
		path = m.worktreeByBranch()[b.Name]
	}
	if path != "" && path != m.repoRoot {
		t.id, t.kind, t.path = "wt:"+path, cleanupWorktree, path
		t.applyWtState(m.local.wtState[path])
		t.active = m.isActive(b.Name, path)
		// A paused operation is what git refuses a plain `worktree remove` over, so
		// the row has to carry it: without the op the delete would go out unforced
		// and fail on exactly the worktree most in need of clearing.
		t.op = m.worktreeOp(path)
	}
	t.quarantined = t.held()
	return t
}

// cleanAll (⇧X) sweeps exactly the rows the Branches pane's Clean Up section shows
// as `ready` — the same ladder, the same landedProof, the same read as the row
// itself — plus orphaned worktree metadata (see pruneTargets). Held rows (`?
// uncommitted`, `? unpushed`, `active`) are excluded and must be deleted one at a
// time with `x`, a deliberate act rather than something a batch key can catch.
//
// This is the count the Branches box's own footer promises (see local.go's
// `footer`); the two are built from the same rows so they can't say different
// numbers, which is exactly the bug this replaced — the footer used to count every
// row in Clean Up (23 on a real repo) while the sweep took only what an entirely
// separate, older classifier called sweepable (2), because that older path didn't
// apply the open-PR veto the way landedProof does.
func (m Model) cleanAll() (tea.Model, tea.Cmd) {
	targets := m.pruneTargets()
	for _, r := range m.branchRows() {
		if r.sec == secCleanup && r.ready == "ready" {
			targets = append(targets, m.targetFor(m.branchInfo(r.name)))
		}
	}
	if len(targets) == 0 {
		m.setStatus("nothing to clean up", cDim)
		return m, nil
	}
	m.confirming = true
	m.confirmTargets = targets
	return m, nil
}

// pruneTargets is the synthetic delete for orphaned worktree metadata: entries
// `git worktree list` still carries for a directory that no longer exists on disk.
// This is not a branch classification — it never touches a ref — so it sits
// outside the section ladder and is always offered for sweep.
func (m Model) pruneTargets() []cleanupTarget {
	n := 0
	for _, w := range m.local.wts {
		if w.Branch != "" && w.Path != m.repoRoot && !pathExists(w.Path) {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return []cleanupTarget{{id: "prune", kind: cleanupPrune}}
}

func pathExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := statFn(p)
	return err == nil
}

// --- delete modal state machine ---

type deleteStatus int

const (
	stPending deleteStatus = iota
	stRunning
	stDone
	stFailed
)

// deleteResult is one target's live state inside the delete modal. stashRef is
// set when a forced worktree removal auto-stashed uncommitted files first, so the
// summary can tell the user which `stash@{n}` holds the recovered work.
type deleteResult struct {
	target   cleanupTarget
	status   deleteStatus
	err      error
	stashRef string
}

// startDelete leaves the confirm phase and kicks off the per-item stepper: the
// first target runs immediately and each completion fires the next, so the modal
// repaints between items with live progress.
func (m Model) startDelete() (tea.Model, tea.Cmd) {
	targets := m.confirmTargets
	m.confirming = false
	m.confirmTargets = nil
	m.selected = map[string]bool{}
	if len(targets) == 0 {
		return m, nil
	}
	return m.runDelete(targets, false)
}

// forceBlocked re-runs only the targets the last batch refused over unresolved
// conflicts, this time without the auto-stash. Selecting them, confirming the
// delete, and then pressing `f` on the refusal is three deliberate acts for the
// one delete that cannot be undone — which is the point: nothing else in the
// dashboard destroys files with no way back, so nothing else asks twice.
func (m Model) forceBlocked() (tea.Model, tea.Cmd) {
	targets := m.blockedTargets()
	if len(targets) == 0 {
		return m, nil
	}
	return m.runDelete(targets, true)
}

// runDelete arms the modal's stepper over targets and fires the first item. force
// is carried on the model because advanceDelete has to hand the same answer to
// every later step of the batch.
func (m Model) runDelete(targets []cleanupTarget, force bool) (tea.Model, tea.Cmd) {
	m.deleting = true
	m.deleteDone = false
	m.deleteForce = force
	m.deleteTargets = targets
	m.deleteIdx = 0
	m.deleteResults = make([]deleteResult, len(targets))
	for i, t := range targets {
		m.deleteResults[i] = deleteResult{target: t, status: stPending}
	}
	m.deleteResults[0].status = stRunning
	return m, deleteStepCmd(m.ctx, targets, 0, force)
}

// blockedTargets are the finished items refused over unresolved conflicts — what
// the force retry re-runs, and what the done phase counts to decide whether to
// offer it at all.
func (m Model) blockedTargets() []cleanupTarget {
	var targets []cleanupTarget
	for _, r := range m.deleteResults {
		if r.status == stFailed && isBlocked(r.err) {
			targets = append(targets, r.target)
		}
	}
	return targets
}

// deleteErrDetail gathers the full git output behind every failed item, for the
// `e` overlay the done phase offers. Each modal row holds one line and a git
// refusal runs to several — the row carries git's headline and the lines under it
// live here, the same split the status line already makes for a failed action.
// Returns "" when nothing failed with more to say than its row already shows.
func (m Model) deleteErrDetail() (title, detail string) {
	var b strings.Builder
	for _, r := range m.deleteResults {
		var ce *git.CmdError
		if r.status != stFailed || !errors.As(r.err, &ce) || !ce.HasDetail() {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(cleanupKindLabel(r.target) + "\n" + ce.Output)
	}
	if b.Len() == 0 {
		return "", ""
	}
	return "delete failed", b.String()
}

// blockedCount is how many finished items were refused over unresolved conflicts,
// so the done phase knows whether to offer the force retry.
func (m Model) blockedCount() int { return len(m.blockedTargets()) }

// advanceDelete records one finished item and either fires the next or, when the
// batch is exhausted, flips to the done phase and refreshes local git state.
func (m Model) advanceDelete(msg deleteStepMsg) (tea.Model, tea.Cmd) {
	if i := msg.idx; i >= 0 && i < len(m.deleteResults) {
		if msg.err != nil {
			m.deleteResults[i].status = stFailed
			m.deleteResults[i].err = msg.err
		} else {
			m.deleteResults[i].status = stDone
			m.deleteResults[i].stashRef = msg.stashRef
		}
	}
	next := msg.idx + 1
	if next < len(m.deleteTargets) {
		m.deleteIdx = next
		m.deleteResults[next].status = stRunning
		return m, deleteStepCmd(m.ctx, m.deleteTargets, next, m.deleteForce)
	}
	m.deleteDone = true
	return m, loadLocalCmd(m.ctx, m.repoRoot, m.current, m.def)
}
