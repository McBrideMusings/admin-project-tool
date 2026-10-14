package tui

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
)

// showTabs maps the name `admin show <tab>` takes to the dashboard tab it
// prints. The names are the lowercase tab titles, with "repo" for the tab the
// dashboard's own bar calls "Local" (its header reads Repo).
var showTabs = []struct {
	name string
	tab  tab
}{
	{"commands", tabWork},
	{"workers", tabWorkers},
	{"repo", tabLocal},
	{"prs", tabPRs},
	{"issues", tabIssues},
	{"beads", tabBeads},
	{"config", tabConfig},
}

// ShowTabNames lists the tab names ShowRows accepts, in tab-bar order.
func ShowTabNames() []string {
	out := make([]string, len(showTabs))
	for i, t := range showTabs {
		out[i] = t.name
	}
	return out
}

// ShowRow is one row of a tab as `admin show <tab>` prints it. Every tab's rows
// come from the same functions the dashboard draws them from (Model.rows and
// the Repo tab's branchRows/localPanes, which is where that tab's panes get their
// data) — this type only flattens the result.
//
// Cells is the row's visible text, one entry per rendered segment; the fields
// after it are the row's payload and appear only when the row carries one.
type ShowRow struct {
	Section string   `json:"section,omitempty"`
	Kind    string   `json:"kind"`
	Cells   []string `json:"cells,omitempty"`
	Right   []string `json:"right,omitempty"`

	Branch   string `json:"branch,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	URL      string `json:"url,omitempty"`
	Number   int    `json:"number,omitempty"`
	Title    string `json:"title,omitempty"`
	ID       string `json:"id,omitempty"`

	PR *ShowPR `json:"pr,omitempty"`

	// Work tab.
	Command string `json:"command,omitempty"`
	Target  string `json:"target,omitempty"`

	// Config tab.
	Path   string `json:"path,omitempty"`
	Absent bool   `json:"absent,omitempty"`

	// Repo tab: the branch row's cells as drawn — owner (Reviewing only), the PR
	// cell, the git cell — plus what the sweep reads, and commit/stash fields.
	Owner   string   `json:"owner,omitempty"`
	State   string   `json:"state,omitempty"`
	PRCell  string   `json:"pr_cell,omitempty"`
	Git     string   `json:"git,omitempty"`
	Ahead   int      `json:"ahead,omitempty"`
	Behind  int      `json:"behind,omitempty"`
	Changed int      `json:"changed,omitempty"`
	Ready   string   `json:"ready,omitempty"`
	Holds   []string `json:"holds,omitempty"`
	Broken  []string `json:"broken,omitempty"`
	Purpose string   `json:"purpose,omitempty"`
	When    string   `json:"when,omitempty"`
}

// ShowPR is a PR row's classification by name. The internal enums are ints and
// the on-disk cache stores them, so the names live here rather than on gh's
// types.
type ShowPR struct {
	Bucket    string    `json:"bucket"`
	Review    string    `json:"review"`
	Checks    string    `json:"checks"`
	MyReview  string    `json:"myReview,omitempty"`
	Mergeable string    `json:"mergeable"`
	Conflicts bool      `json:"conflicts"`
	IsDraft   bool      `json:"isDraft"`
	Author    string    `json:"author"`
	Updated   time.Time `json:"updated"`
}

// ShowRows loads the dashboard exactly as Frame does (real Init commands, real
// Update loop, bounded by opts.Settle), selects the named tab and returns the
// rows that tab holds. A tab the project does not draw — Work without an
// admin.toml, Beads without the tracker, PRs and Issues without a GitHub
// origin — is an error, the same absence the tab bar shows.
func ShowRows(m Model, name string, opts FrameOptions) ([]ShowRow, error) {
	var want tab = -1
	for _, t := range showTabs {
		if t.name == strings.ToLower(name) {
			want = t.tab
		}
	}
	if want < 0 {
		return nil, fmt.Errorf("unknown tab %q — one of: %s", name, strings.Join(ShowTabNames(), ", "))
	}
	if opts.Width <= 0 {
		// Wide, so no cell is truncated to fit a terminal there isn't.
		opts.Width = 400
	}
	if opts.Height <= 0 {
		opts.Height = 40
	}
	if opts.Settle <= 0 {
		opts.Settle = 2 * time.Second
	}

	p := loadedPump(m, opts)
	if p.model.tabIndex(want) < 0 {
		return nil, fmt.Errorf("the %s tab is not shown in this project", name)
	}
	// A tab's data can arrive in several hops (the Beads probe, then its list),
	// so keep pumping until this tab has it, bounded by a further opts.Settle
	// that starts once the first load has finished.
	start := time.Now()
	deadline := start.Add(opts.Settle)
	// Idle with nothing in flight means nothing more can arrive: stop, don't spin.
	for !p.model.tabLoaded(want) && time.Now().Before(deadline) && atomic.LoadInt32(&p.inFlight) > 0 {
		p.drain(100 * time.Millisecond)
	}
	m = p.model
	if !m.tabLoaded(want) {
		if n := atomic.LoadInt32(&p.inFlight); n > 0 {
			return nil, fmt.Errorf("the %s tab had not loaded its data after %s (%d command(s) still in flight)", name, opts.Settle, n)
		}
		return nil, fmt.Errorf("the %s tab had not loaded its data, and nothing was left in flight to load it, after %s", name, time.Since(start).Round(time.Millisecond))
	}
	m.active = want
	return m.showRows(), nil
}

// tabLoaded reports whether the tab has the data it draws, so ShowRows never
// prints a "loading…" placeholder as if it were a row.
func (m Model) tabLoaded(t tab) bool {
	if tm := m.tabModel(t); tm != nil {
		return tm.loaded()
	}
	switch t {
	case tabLocal:
		return m.haveLocal
	case tabPRs, tabIssues:
		return m.haveData
	}
	return true
}

// showRows flattens the active tab. Header rows become the Section of the rows
// under them; rules and blank spacers carry nothing and are dropped.
func (m Model) showRows() []ShowRow {
	if m.active == tabLocal {
		return m.showRepoRows()
	}
	out := []ShowRow{}
	section := ""
	for _, r := range m.rows() {
		switch r.kind {
		case rowHeader:
			section = r.label
			continue
		case rowRule:
			continue
		case rowText:
			if r.label == "" && len(r.leftSegs) == 0 && len(r.rightSegs) == 0 {
				continue
			}
		}
		out = append(out, showRow(section, r))
	}
	return out
}

var rowKindNames = map[rowKind]string{
	rowCenter:        "message",
	rowBranch:        "branch",
	rowText:          "text",
	rowCommit:        "commit",
	rowStash:         "stash",
	rowPR:            "pr",
	rowIssue:         "issue",
	rowBead:          "bead",
	rowCommand:       "command",
	rowCommandTarget: "target",
	rowWorker:        "worker",
	rowConfigValue:   "value",
	rowConfigPath:    "path",
}

func showRow(section string, r rowItem) ShowRow {
	sr := ShowRow{
		Section:  section,
		Kind:     rowKindNames[r.kind],
		Cells:    segTexts(r.leftSegs),
		Right:    segTexts(r.rightSegs),
		Branch:   r.branch,
		Worktree: r.wtPath,
		URL:      r.url,
	}
	if r.label != "" {
		sr.Cells = append([]string{r.label}, sr.Cells...)
	}
	switch {
	case r.pr != nil:
		sr.Number = r.prNum
		sr.PR = &ShowPR{
			Bucket:    bucketName(r.pr.Bucket),
			Review:    reviewName(r.pr.Review),
			Checks:    checksName(r.pr.Checks),
			Mergeable: r.pr.Mergeable,
			Conflicts: r.pr.Mergeable == "CONFLICTING",
			IsDraft:   r.pr.IsDraft,
			Author:    r.pr.Author,
			Updated:   r.pr.Updated,
		}
		sr.Title = r.pr.Title
		if r.pr.Bucket != gh.BucketMine {
			sr.PR.MyReview = myReviewName(r.pr.MyReview)
		}
	case r.issueNum != 0:
		sr.Number, sr.Title = r.issueNum, r.issueTitle
	case r.beadID != "":
		sr.ID, sr.Title = r.beadID, r.beadTitle
	}
	if r.work != nil {
		sr.Command = r.work.cmdName
		sr.Target = r.work.targetKey
	}
	if r.cfg != nil {
		sr.Path, sr.Absent = r.cfg.path, r.cfg.absent
	}
	return sr
}

func segTexts(segs []seg) []string {
	var out []string
	for _, s := range segs {
		if t := strings.TrimSpace(s.t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// showRepoRows lists the Repo tab's three panes: the classified branch rows in
// the pane's own order, then commits and stashes.
func (m Model) showRepoRows() []ShowRow {
	out := []ShowRow{}
	for _, r := range m.branchRows() {
		sr := ShowRow{
			Section: sectionTitle[r.sec],
			Kind:    "branch",
			Branch:  r.name,
			Owner:   ownerCell(r),
			State:   r.state,
			PRCell:  prText(r),
			Git:     gitText(r),
			Ahead:   r.ahead,
			Behind:  r.behind,
			Changed: r.changed,
			Ready:   r.ready,
			Holds:   r.holds,
			Broken:  r.broken,
			Number:  r.prNum,
			Purpose: r.purpose.text,
		}
		if r.sec == secMain {
			sr.Section = "Main"
		}
		if r.prNum != 0 {
			sr.URL = gh.PRURL(m.nwo, r.prNum)
		}
		out = append(out, sr)
	}
	commits, stashes := m.localPanes()
	for _, c := range commits {
		out = append(out, ShowRow{Section: "Commits", Kind: "commit", ID: c.SHA, Title: c.Subject, When: c.When})
	}
	for _, s := range stashes {
		out = append(out, ShowRow{Section: "Stashes", Kind: "stash", ID: s.Ref, Title: s.Subject})
	}
	return out
}

func bucketName(b gh.PRBucket) string {
	switch b {
	case gh.BucketMine:
		return "mine"
	case gh.BucketAssigned:
		return "needs-me"
	case gh.BucketReviewed:
		return "waiting"
	default:
		return "other"
	}
}

func checksName(c gh.CheckState) string {
	switch c {
	case gh.ChecksPending:
		return "pending"
	case gh.ChecksPassing:
		return "passing"
	case gh.ChecksFailing:
		return "failing"
	default:
		return "none"
	}
}

func reviewName(r gh.ReviewClass) string {
	switch r {
	case gh.ReviewNeedsResponse:
		return "needs-response"
	case gh.ReviewChangesRequested:
		return "changes-requested"
	case gh.ReviewApproved:
		return "approved"
	default:
		return "in-review"
	}
}

func myReviewName(s gh.ReviewState) string {
	switch s {
	case gh.UpdatedSinceReview:
		return "updated-since-review"
	case gh.RepliedSinceReview:
		return "replied-since-review"
	case gh.ReviewedApproved:
		return "approved"
	case gh.ReviewedChangesRequested:
		return "changes-requested"
	case gh.ReviewedCommented:
		return "commented"
	default:
		return "not-reviewed"
	}
}
