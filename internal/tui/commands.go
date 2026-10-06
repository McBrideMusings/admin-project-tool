package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/bd"
	"github.com/McBrideMusings/admin-project-tool/internal/create"
	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/resolve"
	"github.com/McBrideMusings/admin-project-tool/internal/shim"
	"github.com/McBrideMusings/admin-project-tool/internal/workusage"
)

// localData is everything the Local and Cleanup views need from git.
//
// It may hold ONLY what loadLocalCmd itself rebuilds. The model replaces it whole
// on every localDataMsg — and those land often: after every fetch --prune, after
// every mutation, on every `r` — so a field written by any other message is
// blanked by the next reload and nothing refills it. Anything from another source
// (the merged-PR set from GitHub, say) lives on the Model and is joined in at
// classification time; see branchstate.go.
type localData struct {
	status       git.Status
	branches     []git.BranchInfo
	commits      []git.Commit
	stashes      []git.Stash
	wts          []git.Worktree
	signals      map[string]gitSignals // branch -> its git-derived dead-or-alive evidence
	touched      map[string]time.Time  // branch -> when it or its worktree last saw activity
	herdrOpen    map[string]string     // worktree path -> id of the herdr workspace open on it
	wtState      map[string]wtState    // worktree path -> what its working-tree read found
	branchUnsafe map[string]bool       // branch -> has unpushed commits (would lose work)
	tips         map[string]string     // branch -> the SHA its ref points at, for the merged-PR proof
	// firstAuthor is the email of the earliest commit each branch added on top of
	// the default branch — the ownership rung that runs when a branch has no PR.
	firstAuthor map[string]string
	// branchNote is `branch.<name>.description`, and lastSubject the newest
	// non-merge commit subject. Top and bottom rungs of the purpose ladder.
	branchNote  map[string]string
	lastSubject map[string]string
	// unpushed is how many commits a branch holds that no origin ref and not the
	// default branch hold (git.CountUnpushed). Deliberately not BranchInfo.Ahead,
	// which counts against the branch's own upstream: that reads 0 for a branch with
	// none, and reads 1 for a branch that tracks origin/main but was pushed under
	// another name with every commit already on origin.
	unpushed map[string]int
	// containedIn is, per branch, every origin branch whose history holds its tip
	// and how many commits each carries beyond it (git.ContainingRemoteBranches).
	// Empty for a branch the default branch's origin copy already contains — that
	// one landed, it is not the base of a stack. openPRFor reads it to give a
	// stacked base with no PR of its own the PR of the branch built on it.
	containedIn map[string]map[string]int
	err         error
}

// wtState is one worktree's working-tree read, held as a single value rather than
// a field per question spread over parallel maps. The parallel form let a read
// that failed halfway fill one map and skip the other, and the map that was
// skipped then answered with its zero value — so an unreadable worktree reported
// "no conflicts" as confidently as a clean one, and the delete went ahead and
// attempted the stash that unreadable state was supposed to stop.
type wtState struct {
	dirty     bool // uncommitted changes present
	conflicts int  // files git records as unmerged
	// changed is how many files are uncommitted. Quarantine is binary and stays
	// that way — one stray file is still unrecoverable once the directory goes —
	// but the *size* of what would be lost is the difference between a scratch test
	// file and an unfinished feature, and `uncommitted` alone renders those two
	// identically. It is shown, never weighed.
	changed int
	// unread is set when the status read itself failed. It is not "clean" and not
	// "conflicted" — it is *unknown*, and every caller has to treat it as the worse
	// of the two, because the files a wrong guess destroys do not come back.
	unread bool
}

// localDataMsg carries one read of local git. seq is the order loadLocalCmd was
// called in, not the order the reads finish: a read that started before a delete
// can finish after the read that started once the delete was done, and applying it
// would put the deleted row back on screen. The model drops any read older than
// one it already applied. seq 0 is a message built by hand, never dropped.
type localDataMsg struct {
	d   localData
	seq uint64
}

// localSeq numbers loadLocalCmd calls. Process-wide and only ever compared against
// numbers drawn from itself, so two models never confuse each other.
var localSeq atomic.Uint64

type cacheDataMsg struct {
	r  data.Remote
	ok bool
}

// syncDoneMsg carries the result of one whole-repo sync: every GitHub section
// that came back, and the fact that remote-tracking refs have been pruned. The
// model applies the sections through data.Remote.ApplyTo (which leaves a failed
// section's cached rows alone) and re-reads local git, since the prune moved the
// refs that decide whether a branch is "gone" or "on origin". at is when the
// sync finished, which the header prints beside a failure so an old one reads
// as old.
type syncDoneMsg struct {
	r  data.Remote
	at time.Time
}

// issueRowMsg / openPRRowMsg carry the result of a row-scoped `r` refresh — one
// targeted GraphQL call for a single row, applied in place onto the cached list
// rather than replacing the whole section. gen is the section's generation
// counter at dispatch time (m.issuesGen/m.prsGen); the model drops the result
// instead of applying it if a full refresh has since bumped the counter, so a
// slow row-fetch can't stomp fresher wholesale data.
type issueRowMsg struct {
	gen    int
	number int // the row's issue number, kept even when ok is false so the model can find it to remove
	item   gh.IssueItem
	ok     bool
	err    error
}
type openPRRowMsg struct {
	gen int
	pr  gh.OpenPR
	err error
}
type actionMsg struct {
	text  string
	err   error
	color lipgloss.TerminalColor // success-line color (nil → dim); errors render red
}

// loadLocalCmd gathers all git-only state for the Local and Cleanup views.
// repoRoot is the main checkout, whose working status the Local view shows.
// checkout is the one the dashboard was opened in (Model.checkoutDir), and
// every other read runs there — so the commit list follows its HEAD.
func loadLocalCmd(ctx context.Context, repoRoot, checkout, current, def string) tea.Cmd {
	seq := localSeq.Add(1)
	return func() tea.Msg {
		var d localData
		wts, err := git.Worktrees(ctx, checkout)
		if err != nil {
			d.err = err
			return localDataMsg{d: d, seq: seq}
		}
		d.wts = wts
		byBranch := git.ByBranch(wts, repoRoot)
		if d.branches, err = git.LocalBranches(ctx, checkout, current, byBranch); err != nil {
			d.err = err
			return localDataMsg{d: d, seq: seq}
		}
		d.status, _ = git.WorkingStatus(ctx, repoRoot)
		// The commits strip is at most historyStripMaxH rows tall and scrolls, so 40
		// is more than it shows at once and enough to scroll through.
		d.commits, _ = git.RecentCommits(ctx, checkout, 40)
		d.stashes, _ = git.Stashes(ctx, checkout)

		// Safety signals for the Cleanup view: a worktree with uncommitted
		// changes, or a branch with unpushed commits, must never land in a
		// pre-selected delete bucket. Both counts come off one status read per
		// worktree — conflicted files are a subset of the dirty ones, so asking
		// twice would only be a second subprocess telling us the same thing.
		d.wtState = map[string]wtState{}
		for _, w := range wts {
			if w.Path == repoRoot {
				continue
			}
			st, err := git.WorkingStatus(ctx, w.Path)
			if err != nil {
				// Fail safe on both counts at once: dirty so nothing pre-selects it,
				// unread so the delete refuses rather than betting on a stash it has
				// no way to know will work.
				d.wtState[w.Path] = wtState{dirty: true, unread: true}
				continue
			}
			d.wtState[w.Path] = wtState{
				dirty:     !st.Clean(),
				conflicts: st.Conflicted,
				changed:   st.Staged + st.Unstaged + st.Untracked,
			}
		}
		d.branchUnsafe = map[string]bool{}
		d.tips = map[string]string{}
		d.firstAuthor = map[string]string{}
		d.branchNote = map[string]string{}
		d.lastSubject = map[string]string{}
		d.unpushed = map[string]int{}
		d.containedIn = map[string]map[string]int{}
		perBranchGitLoad(ctx, checkout, d.branches, def, &d)

		// Classification signals: every git-visible reason a branch might be
		// finished with, gathered per branch in one pass over this one snapshot so
		// no two of them can disagree about how fresh they are. The GitHub-derived
		// signal is deliberately absent — it joins at classification time, off a
		// value this reload cannot reach. See branchstate.go.
		//
		// Liveness (when each branch last saw activity, and which worktrees
		// something is sitting in right now) is a separate question: a branch is
		// deletable without losing commits long before it is safe to destroy the
		// workspace around it, and these are what tell the two apart.
		gone, fullyPushed := git.UpstreamState(d.branches)
		mergedLocal, _ := git.MergedIntoDefault(ctx, checkout, def)
		activity := git.BranchActivity(ctx, checkout, d.branches)
		d.signals = map[string]gitSignals{}
		d.touched = map[string]time.Time{}
		for _, br := range d.branches {
			act := activity[br.Name]
			d.signals[br.Name] = gitSignals{
				gone:        gone[br.Name],
				fullyPushed: fullyPushed[br.Name],
				mergedLocal: mergedLocal[br.Name],
				upstream:    br.Upstream,
				empty:       act.NeverCommitted,
				// "Holds commits origin has never seen" — which is what branchUnsafe
				// already measures, for both shapes. BranchInfo.Ahead alone was wrong
				// here: it is counted against an origin counterpart, so it reads 0 for a
				// branch that has none, and a branch name REUSED after an earlier merge
				// then sailed through the `mergedPR && !ahead` gate and reported "merged"
				// over commits that had never left the machine. Now that the head sets
				// accumulate and never expire, that stale verdict would have been
				// permanent rather than sliding out of the fetch window.
				ahead: d.branchUnsafe[br.Name],
				// Never pushed means origin has no counterpart AND never had one. The
				// second half is what keeps this off branches whose upstream was deleted:
				// those are `gone`, a proof, and counting them here as well would let a
				// proof's own evidence vote a second time in the tier below it.
				neverPushed: !br.HasRemote && !br.UpstreamGone,
				behind:      behindDefault(ctx, checkout, br, def),
			}
			if !act.LastTouched.IsZero() {
				d.touched[br.Name] = act.LastTouched
			}
		}
		for _, w := range wts {
			if w.Path == repoRoot || w.Branch == "" {
				continue
			}
			if t := git.WorktreeTouched(ctx, w.Path); t.After(d.touched[w.Branch]) {
				d.touched[w.Branch] = t
			}
		}
		// Idleness is folded in only now, after the worktree pass above has had its
		// say. A branch's own ref can sit still for weeks while someone works in its
		// worktree every day, so reading the ref alone would call live work abandoned.
		// A branch with no readable activity at all keeps a zero duration, which
		// weighs as no evidence rather than as maximally idle.
		for name, t := range d.touched {
			sig := d.signals[name]
			sig.idle = time.Since(t)
			d.signals[name] = sig
		}
		d.herdrOpen = shim.OpenWorkspaces()
		return localDataMsg{d: d, seq: seq}
	}
}

// perBranchGitLoad fills d.branchUnsafe/unpushed/containedIn/tips/firstAuthor/
// branchNote/lastSubject — five independent per-branch git reads (CountUnpushed,
// ContainingRemoteBranches, FirstAuthor, BranchNote, LastSubject; Tip needs none) — running up to 8 branches
// at once rather than one full pass at a time.
//
// The five are unrelated reads over the same ref, so nothing about running them
// concurrently changes what any of them answers; the only shared state is the
// destination maps, and every write below is keyed by that goroutine's own branch
// name, so two goroutines never touch the same key. On a repo with N branches this
// was N sequential passes of 5 git subprocess spawns each — the ownership and
// purpose rungs this pass feeds were the difference between a first paint bounded
// by 1 spawn per branch and one bounded by 5.
func perBranchGitLoad(ctx context.Context, dir string, branches []git.BranchInfo, def string, d *localData) {
	const maxConcurrent = 8
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, br := range branches {
		wg.Add(1)
		sem <- struct{}{}
		go func(br git.BranchInfo) {
			defer wg.Done()
			defer func() { <-sem }()

			unpushed, unpushedOK := git.CountUnpushed(ctx, dir, br.Name, def)
			var contained map[string]int
			if br.Name != def {
				contained = git.ContainingRemoteBranches(ctx, dir, br.Name)
				if _, inDefault := contained[def]; inDefault {
					contained = nil
				}
			}
			firstAuthor := git.FirstAuthor(ctx, dir, br.Name, def)
			note := git.BranchNote(ctx, dir, br.Name)
			subject := git.LastSubject(ctx, dir, br.Name, def)

			mu.Lock()
			// An unreadable count is unpushed, never clean — see git.CountUnpushed.
			d.branchUnsafe[br.Name] = !unpushedOK || unpushed > 0
			d.unpushed[br.Name] = unpushed
			if contained != nil {
				d.containedIn[br.Name] = contained
			}
			d.tips[br.Name] = br.Tip
			d.firstAuthor[br.Name] = firstAuthor
			d.branchNote[br.Name] = note
			d.lastSubject[br.Name] = subject
			mu.Unlock()
		}(br)
	}
	wg.Wait()
}

// behindDefault is how many commits the default branch holds that br does not —
// the "the work moved on without this one" measure the evidence tier weighs.
//
// BranchInfo.Behind cannot answer this: it is counted against the branch's own
// origin counterpart, so it is 0 for every branch that has none, which is exactly
// the never-pushed shape the evidence tier is for. Counting against the default
// branch takes one extra rev-list per branch, the same per-branch cost the
// unpushed check already pays.
//
// A branch that IS the default, or a repo with no default, has nothing to be
// behind. An unreadable count returns 0 and so weighs as no evidence — never as
// maximally behind.
func behindDefault(ctx context.Context, dir string, br git.BranchInfo, def string) int {
	if def == "" || def == br.Name {
		return 0
	}
	if _, behind, ok := git.AheadBehind(ctx, dir, br.Name, def); ok {
		return behind
	}
	return 0
}

// loadRemoteFn, fetchAllFn, fetchPruneFn and fetchIdentityFn are the seams a
// headless caller (show_test.go, via the exported UseStaticRemote in
// remoteseam.go) overrides to render the dashboard with no `gh` round trip,
// no `git fetch`, and no on-disk cache read keyed by nwo. Only
// UseStaticRemote overrides them, and it restores them on return, so the real
// program's behaviour is unchanged.
var (
	loadRemoteFn    = data.Load
	fetchAllFn      = data.FetchAll
	fetchPruneFn    = git.FetchPrune
	fetchIdentityFn = data.FetchIdentity
)

// identityMsg carries who `gh` says you are (data.FetchIdentity). id is the zero
// Identity when nothing is cached and gh failed, which leaves ownership unknown.
type identityMsg struct{ id data.Identity }

// fetchIdentityCmd resolves the signed-in GitHub account: the host's cached
// identity while it is within data.TTL, otherwise one `gh api user`. A failure
// is not surfaced: the zero identity it yields leaves ownership unknown.
func fetchIdentityCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		id, _ := fetchIdentityFn(ctx, data.DefaultHost)
		return identityMsg{id: id}
	}
}

// loadCacheCmd reads the on-disk cache so the views render instantly.
func loadCacheCmd(nwo string) tea.Cmd {
	return func() tea.Msg {
		r, ok := loadRemoteFn(nwo)
		return cacheDataMsg{r: r, ok: ok}
	}
}

// syncCmd is the dashboard's one background sync: every GitHub section
// (data.FetchAll) and `git fetch --prune`, run together and reported as a single
// message.
//
// The prune belongs here rather than on a gate of its own because it answers the
// same question the GitHub fetch does — what does the remote look like now — and
// leaving it as a once-per-launch call meant a branch deleted on the remote read
// as alive for as long as the dashboard stayed open. The two run concurrently:
// neither needs the other's answer, and one waiting on the other would only make
// the spinner spin longer.
//
// A failed prune is logged (data.LogSync) and goes no further: a checkout with
// no origin fails it on every sync, so it cannot be what turns the header red.
func syncCmd(ctx context.Context, dir, nwo string, withIssues bool) tea.Cmd {
	return func() tea.Msg {
		var r data.Remote
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); r = fetchAllFn(ctx, nwo, mergedPRLimit, withIssues) }()
		go func() {
			defer wg.Done()
			start := time.Now()
			err := fetchPruneFn(ctx, dir)
			// The log keeps git's whole output; CmdError.Error() is only its headline.
			var ce *git.CmdError
			if errors.As(err, &ce) && ce.Output != "" {
				err = errors.New(ce.Output)
			}
			repo := nwo
			if repo == "" {
				repo = dir
			}
			data.LogSync(repo, "prune", time.Since(start), "", err)
		}()
		wg.Wait()
		return syncDoneMsg{r: r, at: time.Now()}
	}
}

// fetchIssueRowCmd re-fetches a single issue — the row-scoped `r` refresh on
// the Issues tab. gen is the Issues generation at dispatch time, echoed back
// on the message so a full refresh landing first makes this result a no-op.
func fetchIssueRowCmd(ctx context.Context, nwo string, number, gen int) tea.Cmd {
	return func() tea.Msg {
		item, ok, err := data.FetchIssue(ctx, nwo, number)
		return issueRowMsg{gen: gen, number: number, item: item, ok: ok, err: err}
	}
}

// fetchOpenPRRowCmd re-fetches a single open PR — the row-scoped `r` refresh on
// the PRs tab. gen is the PRs generation at dispatch time (see fetchIssueRowCmd).
func fetchOpenPRRowCmd(ctx context.Context, nwo string, number, gen int) tea.Cmd {
	return func() tea.Msg {
		pr, err := data.FetchOpenPR(ctx, nwo, number)
		return openPRRowMsg{gen: gen, pr: pr, err: err}
	}
}

// beadsInterval is how often an open dashboard re-reads the beads workspace. It
// is a local database query, not a `gh` round trip, so it runs on a clock of its
// own rather than the GitHub sync's — short enough that a bead an agent files
// mid-session appears while the pane is still on screen.
const beadsInterval = 15 * time.Second

// beadsPollTimeout bounds a single beads poll, mirroring agentPollTimeout's own
// per-query timeout in agentpoll.go. `bd list` is a local Dolt read that takes
// about 2s healthy and 6s when several dashboards and agents query the embedded
// Dolt (one writer at a time) together, so 15s covers contention. A wedged Dolt
// costs one beadsInterval: Model.beadsPolling skips the ticks that land while a
// poll is in flight, so polls never stack, and exec.CommandContext kills the
// child the moment this timeout's ctx is done.
const beadsPollTimeout = 15 * time.Second

// beadsDetectedMsg answers "does this repo keep its work in beads": present says
// a workspace was found, err says one was found but this build can't read it.
type beadsDetectedMsg struct {
	ws      bd.Workspace
	present bool
	err     error
}

// beadsMsg carries one read of the beads workspace.
type beadsMsg struct {
	beads []bd.Bead
	err   error
}

// beadsTickMsg fires on a repeating timer to re-read the beads workspace.
type beadsTickMsg struct{}

// beadsReprobeMsg re-runs the `bd where` probe after one timed out.
type beadsReprobeMsg struct{}

// beadsReprobeCmd schedules the next `bd where` probe one beadsInterval out
// (none for a headless frame).
func (m Model) beadsReprobeCmd() tea.Cmd {
	if m.headless {
		return nil
	}
	return tea.Tick(beadsInterval, func(time.Time) tea.Msg { return beadsReprobeMsg{} })
}

// beadsTimeoutText is what the Beads pane shows in place of rows when bd
// answered nothing at all — so a wedged bd reads as one, not as a slow load.
func beadsTimeoutText(verb string) string {
	return fmt.Sprintf("bd %s did not answer within %s — retrying every %s",
		verb, beadsPollTimeout, beadsInterval)
}

// detectBeadsCmd runs the `bd where` probe. It asks bd rather than testing for a
// .beads directory because a worktree has none of its own — bd's own discovery
// walks to the main repository — and a directory test would hide the Beads tab in
// every worktree of a beads repo. It is bounded by beadsPollTimeout for the same
// reason loadBeadsCmd is.
func detectBeadsCmd(ctx context.Context, repoRoot string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, beadsPollTimeout)
		defer cancel()
		ws, err := bd.Detect(ctx, repoRoot)
		if errors.Is(err, bd.ErrNoWorkspace) {
			return beadsDetectedMsg{}
		}
		return beadsDetectedMsg{ws: ws, present: true, err: err}
	}
}

// loadBeadsCmd reads every open bead. Nothing is cached to disk: unlike the
// GitHub sections this is a local read with no rate limit behind it, so the
// freshest answer is always one query away.
//
// The query is bounded by beadsPollTimeout rather than run on the model's own
// (effectively unbounded) lifetime ctx — a busy embedded Dolt (one writer at a
// time) can wedge `bd list` indefinitely, and exec.CommandContext only kills
// the child when ITS ctx expires. Without this, a wedged poll never returns
// and never lets the in-flight guard (Model.beadsPolling) clear.
func loadBeadsCmd(ctx context.Context, repoRoot string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, beadsPollTimeout)
		defer cancel()
		beads, err := bd.List(ctx, repoRoot)
		return beadsMsg{beads: beads, err: err}
	}
}

// recordUsageCmd writes a successful dispatch to the Work tab's MRU list. The
// write reads, rewrites and renames a file, so it runs in a Cmd rather than on
// the update goroutine. It reports nothing back: nothing in the dashboard
// reads the list yet.
func recordUsageCmd(nwo, invocation string) tea.Cmd {
	return func() tea.Msg {
		workusage.Record(nwo, invocation)
		return nil
	}
}

// beadsTickCmd schedules the next beads read (none for a headless frame).
func (m Model) beadsTickCmd() tea.Cmd {
	if m.headless {
		return nil
	}
	return tea.Tick(beadsInterval, func(time.Time) tea.Msg { return beadsTickMsg{} })
}

// pullCmd runs git pull in the checkout at dir and reports the outcome.
func pullCmd(ctx context.Context, dir string) tea.Cmd {
	return func() tea.Msg {
		// Quiet: the dashboard owns the screen, so git progress must be captured,
		// not streamed to stderr where the TUI renders.
		if err := git.Pull(git.WithQuiet(ctx), dir); err != nil {
			return actionMsg{text: "pull failed", err: err}
		}
		return actionMsg{text: "pulled", color: cGreen}
	}
}

// checkoutInPlaceCmd switches the checkout at dir to branch in place (no
// worktree, no cd).
func checkoutInPlaceCmd(ctx context.Context, dir, branch string) tea.Cmd {
	return func() tea.Msg {
		if err := git.Checkout(git.WithQuiet(ctx), dir, branch); err != nil {
			return actionMsg{text: "checkout failed", err: err}
		}
		return actionMsg{text: "checked out " + branch, color: cGreen}
	}
}

// nameResolvedMsg reports what the new-worktree prompt's text resolved to. err
// is set for a repo mismatch, a failed issue/PR lookup, or text git won't accept
// as a ref — all shown inline under the prompt so the input can be fixed in
// place rather than failing after the dashboard tears down.
type nameResolvedMsg struct {
	plan resolve.Plan
	err  error
}

// resolveNameCmd resolves the new-worktree prompt's text off the UI goroutine.
// It has to be a command rather than an inline call in the key handler: a number
// or an issue/PR URL costs a gh lookup, which would freeze the dashboard mid-key
// for as long as the network takes.
func resolveNameCmd(ctx context.Context, dir, nwo, input string) tea.Cmd {
	return func() tea.Msg {
		plan, err := resolve.BranchInput(ctx, dir, nwo, input)
		return nameResolvedMsg{plan: plan, err: err}
	}
}

// worktreeReq is an in-herdr worktree action dispatched from the live dashboard:
// open an already-existing worktree, create one for an issue branch, or check
// out a PR head into one — then open it as a focused herdr workspace. Used only
// when shim.Available(); the non-herdr path still quits and cds via the sentinel.
type worktreeReq struct {
	kind   string // "open" (existing Path), "create" (new Branch), "pr" (PRNum head)
	branch string
	path   string // for "open"
	prNum  int
}

// worktreeCmd runs a worktreeReq off the UI goroutine and reports the outcome as
// an actionMsg (which clears the creating state, sets the status line, and
// refreshes the local view). The herdr open is what moves focus to the new
// workspace; the dashboard process stays alive in its own pane.
func worktreeCmd(ctx context.Context, repoRoot string, req worktreeReq) tea.Cmd {
	return func() tea.Msg {
		// Quiet: git's fetch/worktree-add progress must be captured, not streamed
		// to stderr where the live dashboard renders.
		ctx = git.WithQuiet(ctx)
		path := req.path
		switch req.kind {
		case "create":
			res, err := create.Worktree(ctx, repoRoot, req.branch)
			if err != nil {
				return actionMsg{text: "worktree failed", err: err}
			}
			path = res.Path
		case "pr":
			if err := git.FetchPRHead(ctx, repoRoot, req.prNum, req.branch); err != nil {
				return actionMsg{text: "fetch PR failed", err: err}
			}
			res, err := create.Worktree(ctx, repoRoot, req.branch)
			if err != nil {
				return actionMsg{text: "worktree failed", err: err}
			}
			path = res.Path
		}
		// A worktree herdr already has a space on is focused, never opened a second
		// time. The lookup is made now rather than read from the dashboard's last
		// poll, so a space opened or closed since then cannot misdirect the click.
		if id, open := shim.OpenWorkspaces()[path]; open && req.kind == "open" {
			if err := shim.FocusWorkspace(id); err != nil {
				return actionMsg{text: "focus failed", err: fmt.Errorf("herdr focus failed: %w", err)}
			}
			return actionMsg{text: "focused " + req.branch + " in herdr", color: cGreen}
		}
		if err := shim.OpenInHerdr(repoRoot, path); err != nil {
			// The worktree already exists; only the herdr open failed. Surface its
			// path so the user can still reach it (a retry hits the idempotent
			// no-op and re-attempts the open).
			return actionMsg{text: "worktree ready at " + path, err: fmt.Errorf("herdr open failed: %w", err)}
		}
		label := req.branch
		if label == "" {
			label = path
		}
		return actionMsg{text: "opened " + label + " in herdr", color: cGreen}
	}
}

// deleteStepMsg reports the outcome of one delete-modal step. stashRef is set
// only when a forced worktree removal first auto-stashed uncommitted files, and
// carries the `stash@{n}` those files landed in so the summary can point to them.
type deleteStepMsg struct {
	idx      int
	err      error
	stashRef string
}

// deleteStepCmd performs exactly one cleanup target (targets[i]) and reports its
// result. The model fires these one at a time so the UI repaints between items,
// driving the delete modal's live per-item progress.
//
// force is set only by the `f` retry the done phase offers over blocked items. It
// means "delete without the auto-stash, knowing the conflicted files are gone" —
// the deliberate second act that stands in for the recovery this one case cannot
// have. It is never set on a first run.
func deleteStepCmd(ctx context.Context, dir string, targets []cleanupTarget, i int, force bool) tea.Cmd {
	return func() tea.Msg {
		t := targets[i]
		var err error
		var stashRef string
		switch t.kind {
		case cleanupWorktree:
			// A forced worktree removal discards uncommitted files with no reflog,
			// stash, or object to recover from. When the tree holds such files,
			// stash them first (`-u` includes untracked) so the delete is
			// recoverable. Clean trees have nothing to save, and an unpushed-only
			// worktree keeps its commits in the reflog — t.dirty is exactly the
			// "uncommitted files present" case, so it alone gates the stash. If the
			// stash fails, abort the removal rather than destroy the files anyway.
			//
			// Unresolved conflicts are the one tree where that stash provably cannot
			// run: git has to write one index entry per path and an unmerged path has
			// none, so `git stash push` refuses the whole thing with "could not write
			// index / <path>: needs merge". Attempting it anyway is how this used to
			// surface — a clipped copy of git's refusal, with no way forward. Say what
			// is paused instead, and leave the delete to the explicit `f` retry. A
			// worktree whose status could not be read at all takes the same route: it
			// might hold conflicts, and guessing wrong costs files nothing brings back.
			if t.unstashable() && !force {
				return deleteStepMsg{idx: i, err: &blockedError{
					op: t.op, conflicts: t.conflicts, unread: t.statusUnread,
				}}
			}
			// pre_remove (ADR-0024) goes before the stash and the herdr close, so
			// a veto leaves the worktree exactly as it was. The veto is a refusal
			// the done phase re-offers under `f`, and that forced retry still runs
			// the hook but removes whatever it returns.
			if err = preRemoveFn(git.WithQuiet(ctx), dir, t.path, force); err != nil {
				return deleteStepMsg{idx: i, err: err}
			}
			switch {
			case t.unstashable():
				// Forced: no stash is possible, so the removal below is the whole step.
			case t.dirty:
				if err = stashWorktreeFn(ctx, t.path); err != nil {
					return deleteStepMsg{idx: i, err: err}
				}
				stashRef = stashRefAfter(targets, i)
			}
			// Quarantined worktrees are exactly the ones a plain remove refuses —
			// dirty, or holding a paused operation. Selecting one is the
			// deliberate act quarantine exists to require, so honour it with --force;
			// without it the delete can only ever fail.
			// A herdr workspace open on the worktree closes with it: its panes would
			// otherwise sit in a directory that no longer exists. If herdr refuses
			// to close it the step stops, leaving the worktree intact.
			if _, cerr := shim.CloseWorkspaceAt(t.path); cerr != nil {
				return deleteStepMsg{idx: i, err: fmt.Errorf("closing the herdr workspace: %w", cerr)}
			}
			if err = removeWorktreeFn(ctx, dir, t.path, t.quarantined); err == nil {
				err = git.DeleteBranch(ctx, dir, t.branch, true)
			}
		case cleanupBranch:
			err = git.DeleteBranch(ctx, dir, t.branch, true)
		case cleanupPrune:
			err = git.PruneWorktrees(ctx, dir)
		}
		return deleteStepMsg{idx: i, err: err, stashRef: stashRef}
	}
}

// stashRefAfter returns the `stash@{n}` the target at index i just stashed into.
// A fresh `stash push` always lands at stash@{0}, and each later dirty-worktree
// delete in the same batch stashes on top and pushes this one down by one — so
// its resting position is the count of dirty worktree targets still to come.
// Unstashable ones are not counted: they never reach the stash, whether they
// refuse or are forced through, so counting them would point the summary at a
// stash entry that does not exist.
func stashRefAfter(targets []cleanupTarget, i int) string {
	n := 0
	for j := i + 1; j < len(targets); j++ {
		if t := targets[j]; t.kind == cleanupWorktree && t.dirty && !t.unstashable() {
			n++
		}
	}
	return fmt.Sprintf("stash@{%d}", n)
}

// pbcopy writes text to the system clipboard. It is a var, not a plain
// function, so drag-to-copy's tests can swap it instead of shelling out for
// real — macOS-only and pbcopy-only, per the tool this dashboard runs on.
var pbcopy = func(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// copyClipboardCmd copies text to the system clipboard, reporting okMsg on success.
func copyClipboardCmd(text, okMsg string) tea.Cmd {
	return func() tea.Msg {
		var name string
		var args []string
		switch runtime.GOOS {
		case "darwin":
			name = "pbcopy"
		case "windows":
			name = "clip"
		default:
			if _, err := exec.LookPath("wl-copy"); err == nil {
				name = "wl-copy"
			} else {
				name, args = "xclip", []string{"-selection", "clipboard"}
			}
		}
		cmd := exec.Command(name, args...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			return actionMsg{text: "clipboard unavailable", err: err}
		}
		return actionMsg{text: okMsg, color: cCyan}
	}
}
