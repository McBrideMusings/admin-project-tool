package tui

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// WorktreeRow is one git worktree of the repo as `admin worktree list` prints
// it. Every value comes from the snapshot the Repo tab draws its branch rows
// from (localData, data.Remote, rowFor), so the two cannot disagree. A group
// that does not apply is nil — no upstream, no PR — never a zero that reads as
// a measurement.
type WorktreeRow struct {
	Path     string `json:"path"`
	Branch   string `json:"branch"` // "" when detached
	Detached bool   `json:"detached"`
	Current  bool   `json:"current"` // the checkout this command ran in
	Primary  bool   `json:"primary"` // the main working tree

	Status *WorktreeStatus `json:"status"` // nil when git could not read it
	Op     *string         `json:"op"`     // the paused operation: rebase, merge, cherry-pick, revert, bisect
	// Ignored lists the worktree's ignored paths the primary checkout lacks,
	// which removing it deletes (git.UnsharedIgnored); nil for the primary
	// checkout and when the status read failed.
	Ignored []string `json:"ignored"`
	// Kept is why the worker reaper left this worktree, when that was a
	// pre_remove veto or a failed removal (internal/keptnote); nil when there is
	// no note, or HEAD moved since it was written.
	Kept *WorktreeKept `json:"kept"`

	Default  *AheadBehind `json:"default"`  // against the default branch; nil when detached, on it, or unreadable
	Upstream *Upstream    `json:"upstream"` // nil when origin has never carried the branch
	PR       *WorktreePR  `json:"pr"`       // nil when no PR is known, or no GitHub data
}

// WorktreeStatus is the working tree's file counts, as `git status` gives them.
type WorktreeStatus struct {
	Staged     int `json:"staged"`
	Unstaged   int `json:"unstaged"`
	Untracked  int `json:"untracked"`
	Conflicted int `json:"conflicted"`
}

// WorktreeKept is the reaper's recorded keep reason and when it wrote it.
type WorktreeKept struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// AheadBehind is commits on this branch only, and on the other ref only.
type AheadBehind struct {
	Name   string `json:"name"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
}

// Upstream is the branch's origin counterpart. Gone means it had one and origin
// deleted it; Ahead and Behind are then 0.
type Upstream struct {
	Name   string `json:"name"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
	Gone   bool   `json:"gone"`
}

// WorktreePR is the branch's PR. Number, Checks and URL are known only for an
// open PR: the merged and closed sets hold just the head commit.
type WorktreePR struct {
	Number *int    `json:"number"`
	State  string  `json:"state"`  // open, draft, merged, closed
	Checks *string `json:"checks"` // passing, failing, pending, none
	URL    *string `json:"url"`
}

// localReadLimit bounds the wait for loadLocalCmd's git read.
const localReadLimit = 30 * time.Second

// WorktreeRows loads the dashboard headlessly, as ShowRows does, and returns one
// row per worktree in `git worktree list` order. It waits for the local read
// and then up to opts.Settle for the GitHub data; GitHub data that never comes
// (no gh, no GitHub origin, a failed fetch) leaves every PR nil rather than
// failing.
func WorktreeRows(m Model, opts FrameOptions) ([]WorktreeRow, error) {
	if opts.Width <= 0 {
		opts.Width = 400
	}
	if opts.Height <= 0 {
		opts.Height = 40
	}
	// The local read is what the rows are, so it gets its own bound; Settle is
	// only how long GitHub may take once it is in.
	p := loadedPump(m, opts)
	pumpUntil := func(done func() bool, d time.Duration) {
		deadline := time.Now().Add(d)
		for !done() && time.Now().Before(deadline) && atomic.LoadInt32(&p.inFlight) > 0 {
			p.drain(100 * time.Millisecond)
		}
	}
	pumpUntil(func() bool { return p.model.haveLocal }, localReadLimit)
	if !p.model.haveLocal {
		return nil, fmt.Errorf("the local git read did not finish (limit %s)", localReadLimit)
	}
	pumpUntil(func() bool { return p.model.haveData }, opts.Settle)
	m = p.model
	if m.local.err != nil {
		return nil, m.local.err
	}
	return m.worktreeRows(), nil
}

func (m Model) worktreeRows() []WorktreeRow {
	branches := map[string]git.BranchInfo{}
	for _, b := range m.local.branches {
		branches[b.Name] = b
	}
	out := make([]WorktreeRow, 0, len(m.local.wts))
	for _, w := range m.local.wts {
		if w.Bare {
			continue
		}
		row := WorktreeRow{
			Path:     w.Path,
			Branch:   w.Branch,
			Detached: w.Detached,
			Current:  git.SamePath(w.Path, m.checkoutDir()),
			Primary:  git.SamePath(w.Path, m.repoRoot),
		}
		if st, ok := m.worktreeStatus(w.Path, row.Primary); ok {
			row.Status = &st
		}
		if ws, ok := m.local.wtState[w.Path]; ok && !row.Primary && !ws.unread {
			row.Ignored = append([]string{}, ws.ignored...)
		}
		if n := m.local.wtState[w.Path].kept; n != nil {
			row.Kept = &WorktreeKept{Reason: n.Reason, At: n.At}
		}
		if w.Op != "" {
			op := w.Op
			row.Op = &op
		}
		b, known := branches[w.Branch]
		if w.Branch != "" && known {
			row.Default, row.Upstream = m.worktreeDistances(b)
			row.PR = m.worktreePR(b.Name)
		}
		out = append(out, row)
	}
	return out
}

// worktreeStatus is the counts loadLocalCmd read for path. The main working tree
// is read once, into local.status, and has no wtState of its own.
func (m Model) worktreeStatus(path string, primary bool) (WorktreeStatus, bool) {
	st := m.local.status
	if !primary {
		ws, ok := m.local.wtState[path]
		if !ok || ws.unread {
			return WorktreeStatus{}, false
		}
		st = ws.status
	}
	return WorktreeStatus{Staged: st.Staged, Unstaged: st.Unstaged, Untracked: st.Untracked, Conflicted: st.Conflicted}, true
}

func (m Model) worktreeDistances(b git.BranchInfo) (*AheadBehind, *Upstream) {
	var def *AheadBehind
	if sig := m.local.signals[b.Name]; sig.defaultRead {
		def = &AheadBehind{Name: m.def, Ahead: sig.aheadDefault, Behind: sig.behind}
	}
	var up *Upstream
	switch {
	case b.UpstreamGone:
		up = &Upstream{Name: upstreamName(b), Gone: true}
	case b.HasRemote:
		up = &Upstream{Name: upstreamName(b), Ahead: b.Ahead, Behind: b.Behind}
	}
	return def, up
}

// upstreamName is origin's name for b: its configured upstream, or its own
// name when it was pushed without -u.
func upstreamName(b git.BranchInfo) string {
	if b.Upstream != "" {
		return "origin/" + b.Upstream
	}
	return "origin/" + b.Name
}

// worktreePR is branch's PR: its open PR through openPRFor's matching (the
// Repo tab row's), else a merged or closed one GitHub recorded at its tip.
func (m Model) worktreePR(branch string) *WorktreePR {
	if pr := m.repo().openPRFor(branch); pr != nil {
		n, checks, url := pr.Number, checksName(pr.Checks), gh.PRURL(m.nwo, pr.Number)
		state := "open"
		if pr.IsDraft {
			state = "draft"
		}
		return &WorktreePR{Number: &n, State: state, Checks: &checks, URL: &url}
	}
	// Only a PR whose head is this branch's tip is this branch's: a name reused
	// after a merge points somewhere else (branchState's tip-match rule).
	tip := m.local.tips[branch]
	if oid, _ := m.repo().mergedHead(branch, m.local.signals[branch].upstream); oid != "" && oid == tip {
		return &WorktreePR{State: "merged"}
	}
	if oid := m.remote.ClosedHeads[branch]; oid != "" && oid == tip {
		return &WorktreePR{State: "closed"}
	}
	return nil
}
