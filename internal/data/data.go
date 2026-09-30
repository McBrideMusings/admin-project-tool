// Package data aggregates the remote (GitHub) state the dashboard shows and
// caches it on disk so the TUI can render instantly on launch, then refresh in
// the background.
package data

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
)

// TTL is how long cached data is trusted without a live fetch. It gates the
// launch sync — relaunching the dashboard in a fresh terminal (the common case:
// new session, same repo) renders straight off the disk cache instead of
// spending a round of GraphQL calls — and it is the age at which the header
// tints its freshness label yellow.
const TTL = 2 * time.Minute

// PollInterval is how often an open dashboard re-syncs everything in the
// background. It is deliberately longer than TTL: TTL answers "is this cache
// worth trusting right now", which a relaunch asks once, while this is a poll
// that keeps running for the whole life of the process and so pays a `gh` round
// trip every time it fires. The poll does not care which tab is on screen, so
// the section you are staring at is refreshed on the same clock as the ones you
// are not — `r`/`⇧R` force a sync immediately regardless.
const PollInterval = 5 * time.Minute

// Remote is the cached GitHub state for one repo. One sync fetches all three
// sections together, but each still carries its own FetchedAt/Error because they
// can fail independently: a section whose `gh` call errored keeps the rows and
// the stamp it already had, so the header can say how old that section really is.
//
// MergedHeads maps the branch name a merged PR had as its head to the commit
// GitHub merged it from — the only signal that catches squash merges (whose
// commits never land on the default branch). It is cached alongside the PR/Issue
// sections, so a relaunch strikes through squash-merged branches instantly from
// cache and a merge that happens while the dashboard is open re-registers on the
// next sync rather than at restart.
//
// The value is the head SHA rather than a bare `true` because the branch NAME
// alone cannot say whether the local ref still holds the commits that merged. A
// name reused after its merge points at new work, and the accumulated set never
// expires, so a name-only match would call that new work landed forever. The
// classifier compares this SHA against the local tip; see branchSignals.mergedTip.
//
// MergedByIssue indexes the same merged PRs the other way — by the issue number
// each one closed — and carries the author and head branch the head-only set
// drops. It is what the "superseded" probe reads: a merged PR someone else landed
// closes its issue, dropping that issue out of the open-issue cache, so this map
// is the only place the branch still tied to that issue can learn it was taken
// over. Fetched, staled, and cached on the same clock as MergedHeads.
//
// ClosedHeads is the same shape for PRs closed WITHOUT merging — the branches a
// human explicitly declined. It rides the merged section's clock and cache entry
// because it comes from the same fetch and answers the same question from the
// other side: MergedHeads says the work landed, ClosedHeads says it was dropped.
type Remote struct {
	PRs              []gh.OpenPR           `json:"prs"`
	Issues           []gh.IssueItem        `json:"issues"`
	MergedHeads      map[string]string     `json:"merged_head_oids"`
	MergedAuthors    map[string]string     `json:"merged_authors"`
	ClosedHeads      map[string]bool       `json:"closed_heads"`
	MergedByIssue    map[int][]MergedPRRef `json:"merged_by_issue"`
	ViewerLogin      string                `json:"viewer_login"`
	PRsFetchedAt     time.Time             `json:"prs_fetched_at"`
	IssuesFetchedAt  time.Time             `json:"issues_fetched_at"`
	MergedFetchedAt  time.Time             `json:"merged_fetched_at"`
	PRsFetchError    string                `json:"-"` // populated on a failed live fetch, never cached
	IssuesFetchError string                `json:"-"` // populated on a failed live fetch, never cached
	MergedFetchError string                `json:"-"` // populated on a failed live fetch, never cached
}

// MergedPRRef is a merged PR reduced to what the "superseded" probe needs: its
// author and head branch. Held in Remote.MergedByIssue keyed by the issue the PR
// closed, so a local branch can ask "did someone other than me land a PR, from a
// branch other than mine, for my issue?" without the full PR payload.
type MergedPRRef struct {
	Author     string `json:"author"`
	HeadBranch string `json:"head_branch"`
}

// Stale reports whether any section is older than TTL (or was never fetched).
// One sync refreshes all three together, so the question the dashboard actually
// asks is "is this cache worth trusting", not "which section is due" — the
// oldest section is the answer for the whole cache.
//
// withIssues is false in a repo whose issues come from beads instead of GitHub.
// That repo never fetches the issue section, so its stamp stays zero forever,
// and counting it would make the cache read as stale on every launch and spend a
// round of `gh` calls the dashboard already had answers for.
func (r Remote) Stale(withIssues bool) bool {
	if withIssues && time.Since(r.IssuesFetchedAt) >= TTL {
		return true
	}
	return time.Since(r.PRsFetchedAt) >= TTL ||
		time.Since(r.MergedFetchedAt) >= TTL
}

func cachePath(nwo string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	safe := filepath.Join(dir, "repo")
	name := filepathSafe(nwo) + ".json"
	return filepath.Join(safe, name)
}

func filepathSafe(nwo string) string {
	out := make([]rune, 0, len(nwo))
	for _, r := range nwo {
		if r == '/' || r == '\\' {
			out = append(out, '-')
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}

// Load reads cached state for nwo. Returns a zero Remote (ok=false) when no
// cache exists yet.
func Load(nwo string) (Remote, bool) {
	var r Remote
	b, err := os.ReadFile(cachePath(nwo))
	if err != nil {
		return r, false
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, false
	}
	return r, true
}

// save writes state to the cache via a temp file + rename so a reader never sees
// a partially-written file. Errors are ignored — the cache is best-effort.
func save(nwo string, r Remote) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return
	}
	writeAtomic(cachePath(nwo), b)
}

// writeAtomic writes b to path via a temp file + rename, creating its directory.
// Errors are ignored — every cache here is best-effort.
func writeAtomic(path string, b []byte) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), path)
}

// ApplyTo overlays a complete sync result onto dst and is the single definition
// of what a sync does to existing data — the disk-cache merge (mergeSave) and the
// in-memory model update both go through it, so the two can't drift.
//
// src must be a whole result, not one section's fragment: it writes all three
// sections, so a value carrying only issue fields would blank the PR section's
// timestamp. FetchAll composes its three fragments with the per-section helpers
// below for exactly that reason.
func (src Remote) ApplyTo(dst *Remote) {
	src.applyPRsTo(dst)
	src.applyIssuesTo(dst)
	src.applyMergedTo(dst)
}

// applyPRsTo overlays the PR section only.
//
// A section that failed contributes only its error message: its rows and its
// FetchedAt stamp are left exactly as they were. Overwriting them would replace
// good cached rows with the empty list a failed fetch returns — every open PR
// vanishing from the list because one `gh` call timed out — and leaving the stamp
// stale is what makes the next poll retry it immediately.
func (src Remote) applyPRsTo(dst *Remote) {
	if src.PRsFetchError != "" {
		dst.PRsFetchError = src.PRsFetchError
		return
	}
	dst.PRs = src.PRs
	dst.PRsFetchedAt = src.PRsFetchedAt
	dst.PRsFetchError = ""
}

// applyIssuesTo overlays the issue section only — same failure rule as applyPRsTo,
// plus one of its own: a fragment carrying neither a stamp nor an error means the
// fetch was never attempted (a beads-backed repo, where GitHub issues aren't the
// tracker), and writing it would blank whatever the cache already held.
func (src Remote) applyIssuesTo(dst *Remote) {
	if src.IssuesFetchedAt.IsZero() && src.IssuesFetchError == "" {
		return
	}
	if src.IssuesFetchError != "" {
		dst.IssuesFetchError = src.IssuesFetchError
		return
	}
	dst.Issues = src.Issues
	dst.ViewerLogin = src.ViewerLogin
	dst.IssuesFetchedAt = src.IssuesFetchedAt
	dst.IssuesFetchError = ""
}

// applyMergedTo overlays the merged-PR set only — same failure rule as applyPRsTo,
// and the one that matters most: blanking this set makes every squash-merged
// branch read as live again.
//
// The head sets accumulate rather than replace. Each fetch reads a fixed window of
// the most recent merged PRs, so in a busy repo that window is a sliding one: a
// branch merged two hundred PRs ago falls out of it, and a plain assignment would
// hand back the exact bug this set exists to prevent — the branch reading as live
// again, months after it landed. Assignment was in fact doing that. A merged or
// closed PR is also a fact that never expires, so there is nothing to un-learn;
// the set only ever grows, and the by-issue index (a fresh whole-window value) is
// the one part still replaced outright.
func (src Remote) applyMergedTo(dst *Remote) {
	if src.MergedFetchError != "" {
		dst.MergedFetchError = src.MergedFetchError
		return
	}
	dst.MergedHeads = unionHeads(dst.MergedHeads, src.MergedHeads)
	dst.MergedAuthors = unionHeads(dst.MergedAuthors, src.MergedAuthors)
	dst.ClosedHeads = unionHeads(dst.ClosedHeads, src.ClosedHeads)
	dst.MergedByIssue = src.MergedByIssue
	dst.MergedFetchedAt = src.MergedFetchedAt
	dst.MergedFetchError = ""
}

// headCap bounds an accumulated head set. Branch names are small and the set only
// grows, so this is a runaway guard rather than a budget: past it the accumulated
// half is dropped and the newest window kept, trading the oldest history for a
// cache that cannot grow without limit. No real repo reaches it.
const headCap = 5000

// unionHeads merges a freshly-fetched head set into the accumulated one, newest
// winning on the cap. Neither side is mutated — dst may be the cached map another
// reader is holding.
func unionHeads[T comparable](old, fresh map[string]T) map[string]T {
	if len(old)+len(fresh) > headCap {
		old = nil
	}
	out := make(map[string]T, len(old)+len(fresh))
	for k, v := range old {
		out[k] = v
	}
	for k, v := range fresh {
		out[k] = v
	}
	return out
}

// withCacheLock runs fn while holding an exclusive flock on nwo's lock file, so
// concurrent read-modify-write cache updates serialize instead of racing — not
// just goroutines within one process (a sync and a row-scoped refresh can land
// together), but separate `repo` processes too, e.g. one running from each of two
// worktrees of the same origin. flock is released when f closes,
// so a killed process can't leave the lock held.
func withCacheLock(nwo string, fn func()) {
	path := cachePath(nwo) + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fn()
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fn()
		return
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		fn()
		return
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	fn()
}

// mergeSave overlays the just-fetched fields onto the existing on-disk cache and
// rewrites it, rather than writing the fetched value on its own. A row-scoped
// refresh touches one PR or issue, and a section that failed its fetch touches
// nothing, so a save must preserve everything it did not just fetch.
func mergeSave(nwo string, apply func(*Remote)) {
	withCacheLock(nwo, func() {
		c, _ := Load(nwo) // existing cache, or a zero Remote when none exists yet
		apply(&c)
		save(nwo, c)
	})
}

// FetchAll queries every remote section the dashboard shows — open PRs, open
// issues, and the merged-PR set — in one go, and writes all three into the cache
// under a single lock. This is the dashboard's only whole-repo fetch: the three
// sections used to be fetched separately, each on its own clock and each doing
// its own read-modify-write of the cache file, so which data was current depended
// on which tab you happened to be looking at.
//
// The three run concurrently because none of them needs another's answer, so the
// sync costs the slowest one rather than their sum. A section that fails carries
// its error back and changes nothing — see ApplyTo.
//
// withIssues is false in a beads-backed repo. There, GitHub issues are not the
// repo's issue tracker at all — the Issues tab doesn't exist and nothing reads
// the section — so the call is not made rather than made and discarded.
//
// nwo empty means there's no GitHub remote at all — nothing to sync there, not
// a failure — so every GitHub fetch is skipped the same way and the zero Remote
// carries no *FetchError for the header to render as "sync failed".
func FetchAll(ctx context.Context, nwo string, mergedLimit int, withIssues bool) Remote {
	if nwo == "" {
		return Remote{}
	}
	var prs, issues, merged Remote
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); prs = fetchPRs(ctx, nwo) }()
	go func() { defer wg.Done(); merged = fetchMerged(ctx, nwo, mergedLimit) }()
	if withIssues {
		wg.Add(1)
		go func() { defer wg.Done(); issues = fetchIssues(ctx, nwo) }()
	}
	wg.Wait()

	// Each fragment contributes its own section and nothing else. Composing these
	// with the whole-result ApplyTo would have the issue fragment write its zero
	// PR fields over the PR fetch that just succeeded.
	var r Remote
	prs.applyPRsTo(&r)
	issues.applyIssuesTo(&r)
	merged.applyMergedTo(&r)

	mergeSave(nwo, func(c *Remote) { r.ApplyTo(c) })
	return r
}

// fetchPRs queries every open PR in the repo in one paginated call. Mergeable
// states that come back "UNKNOWN" are re-polled with backoff. On error the
// returned Remote carries only PRsFetchError, leaving FetchedAt zero (maximally
// stale) so the next poll retries instead of trusting a cache that never landed.
// The returned Remote carries only the PR fields; the caller does the saving.
func fetchPRs(ctx context.Context, nwo string) Remote {
	var r Remote
	prs, err := gh.OpenPRs(ctx, nwo)
	if err != nil {
		r.PRsFetchError = err.Error()
		return r
	}
	resolveUnknownMergeStates(ctx, nwo, prs)
	r.PRs = prs
	r.PRsFetchedAt = time.Now()
	return r
}

// FetchOpenPR re-fetches one open PR and merges it into the cache in place — the
// row-scoped `r` refresh on the PRs tab. Leaves the rest of the cached list and
// the section's FetchedAt untouched. The PR's bucket is recomputed, so a change
// in assignment moves the row to the right section on the next render.
func FetchOpenPR(ctx context.Context, nwo string, number int) (gh.OpenPR, error) {
	pr, err := gh.OpenPRByNumber(ctx, nwo, number)
	if err != nil {
		return gh.OpenPR{}, err
	}
	mergeSave(nwo, func(c *Remote) {
		for i := range c.PRs {
			if c.PRs[i].Number == number {
				c.PRs[i] = pr
				return
			}
		}
	})
	return pr, nil
}

// resolveUnknownMergeStates re-polls GitHub for any PR whose mergeable state came
// back "UNKNOWN", retrying with backoff until every PR resolves or the retry
// budget runs out (the affected PRs are then left UNKNOWN, same as today). It
// mutates prs in place.
//
// This only applies to Mergeable, not the sibling async-computed Checks field:
// Checks has a legitimate "pending" state that the UI already renders correctly
// (a yellow ● badge) and that can take minutes to settle as CI runs, so polling
// it here would just add latency for no benefit. Mergeable's "UNKNOWN" has no
// valid UI representation and — unlike a still-running check — is expected to
// resolve within seconds of being queried.
func resolveUnknownMergeStates(ctx context.Context, nwo string, prs []gh.OpenPR) {
	backoff := []time.Duration{time.Second, 2 * time.Second}
	for _, wait := range backoff {
		var pending []int
		for _, p := range prs {
			if p.Mergeable == "UNKNOWN" {
				pending = append(pending, p.Number)
			}
		}
		if len(pending) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		states, err := gh.PRMergeStates(ctx, nwo, pending)
		if err != nil {
			continue
		}
		for i := range prs {
			if m, ok := states[prs[i].Number]; ok {
				prs[i].Mergeable = m
			}
		}
	}
}

// fetchIssues queries every open issue in the repo and resolves the checks/
// draft/mergeable status of each one's linked PR (batched, not one call per
// issue). The returned Remote carries only the issue fields; the caller saves.
func fetchIssues(ctx context.Context, nwo string) Remote {
	var r Remote

	issues, viewerLogin, err := gh.AllOpenIssues(ctx, nwo)
	if err != nil {
		// Leave IssuesFetchedAt zero (maximally stale) rather than stamping now, so
		// the next poll retries immediately. ApplyTo is what keeps this empty result
		// from wiping the good cached issues.
		r.IssuesFetchError = err.Error()
		return r
	}
	r.ViewerLogin = viewerLogin

	var linkedPRs []int
	for _, it := range issues {
		if it.PR != nil {
			linkedPRs = append(linkedPRs, it.PR.Number)
		}
	}
	if statuses, err := gh.IssuePRStatuses(ctx, nwo, linkedPRs); err == nil {
		for i := range issues {
			if issues[i].PR == nil {
				continue
			}
			if status, ok := statuses[issues[i].PR.Number]; ok {
				issues[i].PR = &status
			}
		}
	}
	r.Issues = issues
	r.IssuesFetchedAt = time.Now()
	return r
}

// FetchIssue re-fetches a single issue and merges it into the cache in place
// — the row-scoped `r` refresh on the Issues tab. Leaves every other cached
// issue and the section's FetchedAt untouched, so it doesn't reset the TTL
// for the rest of the list. When the issue has since closed (ok=false), it's
// dropped from the cached list instead of updated.
func FetchIssue(ctx context.Context, nwo string, number int) (item gh.IssueItem, ok bool, err error) {
	item, ok, err = gh.IssueByNumber(ctx, nwo, number)
	if err != nil {
		return gh.IssueItem{}, false, err
	}
	mergeSave(nwo, func(c *Remote) {
		for i := range c.Issues {
			if c.Issues[i].Number != number {
				continue
			}
			if !ok {
				c.Issues = append(c.Issues[:i], c.Issues[i+1:]...)
			} else {
				c.Issues[i] = item
			}
			return
		}
	})
	return item, ok, nil
}

// fetchMerged asks GitHub which recent PRs merged and records both the head-branch
// set (MergedHeads) and the by-issue index (MergedByIssue) from one query. This is
// the only signal that sees squash merges — whose commits never land on the default
// branch — which is why the sync keeps re-running it while the dashboard stays open
// rather than reading it once at launch. On error the previously-cached maps and
// timestamp both survive (so squash-merged branches keep reading as merged); see
// ApplyTo. The returned Remote carries only the merged fields; the caller saves.
func fetchMerged(ctx context.Context, nwo string, limit int) Remote {
	prs, err := gh.MergedPRs(ctx, nwo, limit)
	if err != nil {
		return Remote{MergedFetchError: err.Error()}
	}
	heads := map[string]string{}
	authors := map[string]string{}
	byIssue := map[int][]MergedPRRef{}
	for _, pr := range prs {
		if pr.HeadBranch != "" {
			heads[pr.HeadBranch] = pr.HeadOID
			authors[pr.HeadBranch] = pr.Author
		}
		ref := MergedPRRef{Author: pr.Author, HeadBranch: pr.HeadBranch}
		for _, iss := range pr.Issues {
			byIssue[iss] = append(byIssue[iss], ref)
		}
	}
	// The closed-unmerged set is best-effort: it feeds circumstantial evidence, not
	// a proof, so failing to read it should cost that one signal's weight and
	// nothing else. Failing the whole merged section over it would blank the squash-
	// merge detection — a far more expensive loss than missing an "abandoned" hint.
	closed, err := gh.ClosedPRHeads(ctx, nwo, limit)
	if err != nil {
		closed = nil
	}
	return Remote{
		MergedHeads:     heads,
		MergedAuthors:   authors,
		ClosedHeads:     closed,
		MergedByIssue:   byIssue,
		MergedFetchedAt: time.Now(),
	}
}
