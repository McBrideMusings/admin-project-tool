// Package git wraps the git plumbing the dashboard needs: repo/worktree
// discovery, local status for the Local view, and the checkout/create/cleanup
// mutations. Carried over and extended from wtree's internal/gitwt.
package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrNotInRepo is returned when a command is run outside any git repository.
var ErrNotInRepo = errors.New("not inside a git repository")

type quietKey struct{}

// WithQuiet marks ctx so mutating git commands capture their progress output
// instead of streaming it to the terminal. The dashboard runs every git mutation
// under this: Bubble Tea renders to stderr, so a subprocess writing to stderr
// would smear over the live TUI. Captured output is surfaced only on failure.
func WithQuiet(ctx context.Context) context.Context {
	return context.WithValue(ctx, quietKey{}, true)
}

// IsQuiet reports whether ctx was marked by WithQuiet.
func IsQuiet(ctx context.Context) bool {
	q, _ := ctx.Value(quietKey{}).(bool)
	return q
}

// ErrNoDir is returned by every command in this package when it is handed an
// empty directory. `git -C ""` leaves the cwd unchanged, so an empty dir would
// silently read whatever checkout the process happens to be standing in.
var ErrNoDir = errors.New("git: no directory given")

// runner and mutator are the two seams every git command in this package goes
// through — the reads that capture stdout, and the mutations that stream their
// progress. They exist so a test can record the argv a call builds: a `-d` that
// became `-D`, a dropped `--no-ff` or a changed `for-each-ref` format is
// otherwise invisible, since the arguments never leave the process in a form
// anything can read. Every command takes the directory it runs in, and the seam
// passes it as `-C <dir>`: no command in this package reads the process cwd.
// See argv_test.go.
var (
	runner  = runGit
	mutator = runGitMutating
)

// gitCmd builds `git -C dir args...`, refusing an empty dir.
func gitCmd(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	if dir == "" {
		return nil, ErrNoDir
	}
	return exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...), nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd, err := gitCmd(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	cmd.Env = readOnlyGitEnv()
	out, err := cmd.Output()
	if err != nil {
		// Fold git's own message into the error. cmd.Output populates ExitError.Stderr,
		// and dropping it is how a refusal that names its cause ("cannot delete branch
		// 'x' used by worktree at '…'") reaches the user as a bare "exit status 1".
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				return "", newCmdError(err, msg)
			}
		}
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// CmdError is a failed git command: a one-line headline for the status bar and
// the full output kept alongside it. Error() is the headline alone, because the
// status bar and the delete modal each give an error a single line and a git
// refusal can run to a dozen — so the untrimmed text used to scroll the dashboard
// off screen. Output is what those lines said, for a caller that has somewhere to
// put them (the error-detail overlay).
type CmdError struct {
	Err      error  // the underlying *exec.ExitError
	Headline string // git's own one-line refusal
	Output   string // everything git wrote, hints and all
}

// Error is git's own words, not "exit status 1: " in front of them — the status
// bar has one line and every cell of it should say something the user can act on.
// The exit status stays reachable through Unwrap for anything matching on it.
func (e *CmdError) Error() string {
	if e.Headline == "" {
		return e.Err.Error()
	}
	return e.Headline
}

func (e *CmdError) Unwrap() error { return e.Err }

// HasDetail reports whether Output says more than Headline already does — the
// test for whether offering a detail view is worth it.
func (e *CmdError) HasDetail() bool {
	return strings.TrimSpace(e.Output) != "" && strings.TrimSpace(e.Output) != e.Headline
}

func newCmdError(err error, out string) *CmdError {
	out = strings.TrimSpace(out)
	return &CmdError{Err: err, Headline: errHeadline(out), Output: out}
}

// errHeadline picks the one line of git's output that names the refusal. Git wraps
// a refusal in hint/advice paragraphs, and they can come FIRST — `git pull` leads
// with "hint: You have divergent branches…", buries eight more hint lines, and only
// then prints the fatal — so advice is skipped and a fatal/error line wins over any
// other prose.
func errHeadline(s string) string {
	var head string
	for ln := range strings.SplitSeq(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "hint:") {
			continue
		}
		if strings.HasPrefix(ln, "fatal:") || strings.HasPrefix(ln, "error:") {
			return ln
		}
		if head == "" {
			head = ln
		}
	}
	if head != "" {
		return head
	}
	// Nothing but hints: fall back to the first line so the headline is never empty.
	h, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(h)
}

// readOnlyGitEnv is the environment for runGit's reads: GIT_OPTIONAL_LOCKS=0
// stops `git status` from refreshing the index, which it otherwise does under
// index.lock. The dashboard polls every worktree this way, so without it a
// poll collides with a commit happening in the same checkout, and a poll
// killed by its context mid-refresh leaves the lock file behind.
func readOnlyGitEnv() []string {
	return append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
}

// runMutating runs a git command that would otherwise stream progress to the
// terminal. Under WithQuiet it captures stdout+stderr and folds them into any
// error (silent on success); otherwise it streams live, as a CLI invocation
// should.
// runGitMutating builds a mutating git command from argv the same way runGit
// builds a read, so both seams record the same shape.
func runGitMutating(ctx context.Context, dir string, args ...string) error {
	cmd, err := gitCmd(ctx, dir, args...)
	if err != nil {
		return err
	}
	return runMutating(ctx, cmd)
}

func runMutating(ctx context.Context, cmd *exec.Cmd) error {
	if IsQuiet(ctx) {
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Run(); err != nil {
			// A CmdError, same as runGit: the status bar shows the headline and the
			// overlay can still reach the lines behind it.
			if out := strings.TrimSpace(buf.String()); out != "" {
				return newCmdError(err, out)
			}
			return err
		}
		return nil
	}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// TopLevel returns the absolute path of the top-level dir of the worktree
// containing dir.
func TopLevel(ctx context.Context, dir string) (string, error) {
	top, err := runner(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ErrNotInRepo
	}
	return top, nil
}

// RepoRoot returns the main repo's top-level directory for the repo containing
// dir, resolving correctly even when dir is inside a linked worktree.
func RepoRoot(ctx context.Context, dir string) (string, error) {
	top, err := runner(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ErrNotInRepo
	}
	gitDir, err := runner(ctx, dir, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return top, nil
	}
	commonDir, err := runner(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return top, nil
	}
	if gitDir != commonDir {
		return primaryWorktreeOf(ctx, commonDir), nil
	}
	return top, nil
}

// primaryWorktreeOf maps a linked worktree's common git dir to the working tree
// the primary checkout keeps there.
//
// For an ordinary repo the common dir is <primary>/.git, so the parent is the
// answer. A submodule breaks that: its common dir is
// <super>/.git/modules/<name>, whose parent is <super>/.git/modules — a
// directory holding no working tree at all. Every populate pattern then globs
// against nothing and the whole job is a silent no-op.
//
// core.worktree is what git itself uses to find a submodule's working tree, and
// it is set only when the git dir lives away from it, so its presence is also
// the test for that case. It is stored relative to the common dir
// (../../../skills), so resolve it from there.
func primaryWorktreeOf(ctx context.Context, commonDir string) string {
	wt, err := runner(ctx, commonDir, "config", "--file", filepath.Join(commonDir, "config"), "--get", "core.worktree")
	if err != nil || wt == "" {
		return filepath.Dir(commonDir)
	}
	if !filepath.IsAbs(wt) {
		wt = filepath.Join(commonDir, wt)
	}
	return filepath.Clean(wt)
}

// CurrentBranch returns the checked-out branch at dir, or "" if detached.
func CurrentBranch(ctx context.Context, dir string) string {
	out, err := runner(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	if b := strings.TrimSpace(out); b != "HEAD" {
		return b
	}
	return ""
}

// OriginNWO returns "owner/repo" parsed from the origin remote URL.
func OriginNWO(ctx context.Context, dir string) (string, error) {
	url, err := runner(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return "", errors.New("could not determine origin remote URL")
	}
	url = strings.TrimSuffix(url, ".git")
	switch {
	case strings.HasPrefix(url, "git@github.com:"):
		return strings.TrimPrefix(url, "git@github.com:"), nil
	case strings.HasPrefix(url, "https://github.com/"):
		return strings.TrimPrefix(url, "https://github.com/"), nil
	default:
		return "", errors.New("origin is not a recognized GitHub URL")
	}
}

// DefaultBranch returns the repo's default branch name.
func DefaultBranch(ctx context.Context, dir string) string {
	out, err := runner(ctx, dir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		parts := strings.Split(strings.TrimSpace(out), "/")
		if len(parts) >= 2 {
			return parts[len(parts)-1]
		}
	}
	for _, name := range []string{"main", "master", "develop"} {
		if BranchExistsLocal(ctx, dir, name) {
			return name
		}
	}
	return ""
}

// ValidBranchName reports whether name is a syntactically valid git branch name,
// deferring to `git check-ref-format --branch` so the rules match git's own (a
// leading dash, whitespace, `~^:?*[`, `..`, a trailing `.lock`, etc. all fail).
func ValidBranchName(ctx context.Context, dir, name string) bool {
	_, err := runner(ctx, dir, "check-ref-format", "--branch", name)
	return err == nil
}

// BranchExistsLocal reports whether refs/heads/<branch> exists.
func BranchExistsLocal(ctx context.Context, dir, branch string) bool {
	_, err := runner(ctx, dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// BranchExistsRemote reports whether refs/remotes/origin/<branch> exists.
func BranchExistsRemote(ctx context.Context, dir, branch string) bool {
	_, err := runner(ctx, dir, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return err == nil
}

// Status summarizes the working tree at a path. Conflicted counts paths git
// records as unmerged, and is kept apart from Staged/Unstaged (which a conflicted
// path also increments) because it is the one count that says a git command
// touching the index will be refused outright rather than merely have work to do.
type Status struct {
	Staged     int
	Unstaged   int
	Untracked  int
	Conflicted int
	// IgnoredPaths lists the paths git ignores, relative to the worktree
	// root (a wholly ignored directory appears once, with a trailing slash).
	// Only WorkingStatusIgnored fills it.
	IgnoredPaths []string
}

// Clean reports whether the working tree has no changes at all.
func (s Status) Clean() bool { return s.Staged == 0 && s.Unstaged == 0 && s.Untracked == 0 }

// WorkingStatus parses `git status --porcelain` at path into staged/unstaged/
// untracked/conflicted counts.
func WorkingStatus(ctx context.Context, path string) (Status, error) {
	return workingStatus(ctx, path, "--porcelain")
}

// WorkingStatusIgnored is WorkingStatus that also lists ignored paths. It
// lists every ignored file or directory (node_modules, build output, .env),
// so it is slower than WorkingStatus; use it only where a deletion must not
// take ignored files with it.
func WorkingStatusIgnored(ctx context.Context, path string) (Status, error) {
	return workingStatus(ctx, path, "--porcelain", "--ignored=matching")
}

func workingStatus(ctx context.Context, path string, flags ...string) (Status, error) {
	out, err := runner(ctx, path, append([]string{"status"}, flags...)...)
	if err != nil {
		return Status{}, err
	}
	var s Status
	for line := range strings.SplitSeq(out, "\n") {
		if len(line) < 2 {
			continue
		}
		x, y := line[0], line[1]
		if x == '!' && y == '!' {
			s.IgnoredPaths = append(s.IgnoredPaths, strings.TrimSpace(line[2:]))
			continue
		}
		if x == '?' && y == '?' {
			s.Untracked++
			continue
		}
		if unmerged(x, y) {
			s.Conflicted++
		}
		if x != ' ' && x != '?' {
			s.Staged++
		}
		if y != ' ' && y != '?' {
			s.Unstaged++
		}
	}
	return s, nil
}

// unmerged reports whether a porcelain XY pair marks an unresolved conflict.
// Git spells these seven ways and no others: DD, AU, UD, UA, DU, AA, UU — every
// pair where at least one side is U, plus both-deleted and both-added.
func unmerged(x, y byte) bool {
	return x == 'U' || y == 'U' || (x == 'D' && y == 'D') || (x == 'A' && y == 'A')
}

// AheadBehind returns how many commits branch is ahead/behind base. base is an
// explicit ref rather than @{upstream} because a branch's counterpart on origin is
// often not its configured upstream — a branch pushed without -u has no upstream
// at all, yet origin/<name> holds its work all the same.
func AheadBehind(ctx context.Context, dir, branch, base string) (ahead, behind int, ok bool) {
	out, err := runner(ctx, dir, "rev-list", "--left-right", "--count", branch+"..."+base)
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Fields(out)
	if len(parts) != 2 {
		return 0, 0, false
	}
	ahead, _ = strconv.Atoi(parts[0])
	behind, _ = strconv.Atoi(parts[1])
	return ahead, behind, true
}

// FirstAuthor returns the author email of the earliest commit branch added on top
// of base — who *started* this work.
//
// Deliberately the first commit and not "any commit by you", which is the same
// question asked in a way that keeps answering yes: merging the default branch
// into a long-lived branch writes your address into its history, so an any-commit
// test claims every colleague's branch you have ever helped keep up to date. On
// the repo this was built against that was five of six teammates' branches.
//
// Merges are skipped for the same reason — the merge commit is authored by
// whoever ran the merge, not by whoever wrote the work. Returns "" when the branch
// adds no non-merge commit of its own (nothing to attribute) or the read fails.
func FirstAuthor(ctx context.Context, dir, branch, base string) string {
	if base == "" || base == branch {
		return ""
	}
	out, err := runner(ctx, dir, "log", "--no-merges", "--reverse", "--format=%ae", base+".."+branch)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(out, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// BranchNote is a branch's own description — `git branch --edit-description`,
// stored as branch.<name>.description. It is git's native place for "what is this
// branch for", so the dashboard reads it rather than inventing a sidecar file:
// nothing to sync, nothing to gitignore, and it survives every operation except
// deleting the branch. Empty when unset.
func BranchNote(ctx context.Context, dir, branch string) string {
	out, err := runner(ctx, dir, "config", "--get", "branch."+branch+".description")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// SetBranchNote writes a branch's description, or clears it when note is empty.
func SetBranchNote(ctx context.Context, dir, branch, note string) error {
	key := "branch." + branch + ".description"
	if strings.TrimSpace(note) == "" {
		_, err := runner(ctx, dir, "config", "--unset", key)
		if isUnsetMissingKey(err) {
			// The caller asked for "no note", and a key that was never set means that
			// is already the state — not a failure to report.
			return nil
		}
		return err
	}
	_, err := runner(ctx, dir, "config", key, note)
	return err
}

// isUnsetMissingKey reports whether err is `git config --unset`'s specific
// "the section or key is invalid" refusal (exit 5) — as opposed to a real
// failure (permissions, a read-only filesystem, a lock held by another process),
// which must reach the caller rather than being read as "already cleared".
func isUnsetMissingKey(err error) bool {
	var ce *CmdError
	if !errors.As(err, &ce) {
		return false
	}
	var ee *exec.ExitError
	return errors.As(ce.Err, &ee) && ee.ExitCode() == 5
}

// LastSubject is the subject of the newest non-merge commit branch added on top of
// base — the bottom rung of the purpose ladder. Merges are excluded because a
// merge subject describes the branch's upkeep ("Merge origin/main into x") rather
// than its reason for existing.
func LastSubject(ctx context.Context, dir, branch, base string) string {
	if base == "" || base == branch {
		return ""
	}
	out, err := runner(ctx, dir, "log", "-1", "--no-merges", "--format=%s", base+".."+branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// CountAheadOf returns how many commits branch is ahead of base (base..branch).
// ok is false when the count could not be read at all, which callers must not
// treat as zero — see HasUnpushedCommits.
func CountAheadOf(ctx context.Context, dir, base, branch string) (n int, ok bool) {
	out, err := runner(ctx, dir, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return 0, false
	}
	n, err = strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, false
	}
	return n, true
}

// HasUnpushedCommits reports whether a branch holds commits origin has never
// seen. With a counterpart on origin that is simply its ahead count; without one
// it is every commit beyond the default branch, since nothing on the remote holds
// the work.
//
// A count that cannot be read answers true, not false. This is the flag that
// quarantines a branch out of the batch sweep, and it is load-bearing on its own
// for a branch the evidence tier only *guesses* is dead — no proof is standing
// behind that one, so a failed `rev-list` reading as "nothing unpushed" would let
// a transient git error hand a branch full of local-only commits to a sweep. Same
// fail-safe as an unreadable worktree status (see wtState.unread): the direction
// that costs a wasted keystroke is always preferred over the one that costs work.
func HasUnpushedCommits(ctx context.Context, dir string, bi BranchInfo, def string) bool {
	if bi.HasRemote {
		return bi.Ahead > 0
	}
	if def == "" || def == bi.Name {
		return false
	}
	n, ok := CountAheadOf(ctx, dir, def, bi.Name)
	return !ok || n > 0
}

// BranchInfo is a local branch with the annotations the Local view shows.
type BranchInfo struct {
	Name    string
	Current bool
	Ahead   int
	Behind  int
	// HasRemote is whether origin holds a branch this one can be compared against —
	// its configured upstream, or, for a branch pushed without -u, origin/<name>.
	// Ahead/Behind are counted against that ref.
	HasRemote bool
	// UpstreamGone is whether this branch had an upstream configured and that
	// remote branch has since been deleted.
	UpstreamGone bool
	LastCommit   string // relative, e.g. "3 days ago"
	WorktreePath string // non-empty if this branch lives in a worktree
	// Tip is the full SHA the branch ref points at. It is what lets a merged PR
	// prove *this* branch landed rather than a branch that once had the same name:
	// GitHub records the head commit it merged, and a name reused after that merge
	// points somewhere else. See branchSignals.mergedTip.
	Tip string
	// Upstream is the configured upstream's branch name with the remote stripped
	// ("feat/x" for origin/feat/x); empty when none is configured. A branch pushed
	// as `git push -u origin HEAD:feat/x` carries a different name on the remote
	// than locally, and GitHub knows its PR only by the remote name.
	Upstream string
}

// remoteBranchNames is the set of branch names origin currently has, read from the
// remote-tracking refs. A local branch's relationship to origin is judged against
// this rather than against its upstream config: `git switch -c x && git push
// origin x` leaves no upstream configured at all, yet origin/x holds the work.
func remoteBranchNames(ctx context.Context, dir string) map[string]bool {
	out, err := runner(ctx, dir, "for-each-ref", "--format=%(refname:strip=3)", "refs/remotes/origin/")
	if err != nil {
		return map[string]bool{}
	}
	set := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		if name := strings.TrimSpace(line); name != "" && name != "HEAD" {
			set[name] = true
		}
	}
	return set
}

// LocalBranches lists local branches with ahead/behind and last-commit dates.
// worktreeByBranch maps branch name -> worktree path for annotation.
func LocalBranches(ctx context.Context, dir, current string, worktreeByBranch map[string]string) ([]BranchInfo, error) {
	const fmtStr = "%(refname:short)%09%(committerdate:relative)%09%(upstream:short)%09%(upstream:track)%09%(objectname)"
	out, err := runner(ctx, dir, "for-each-ref", "--sort=-committerdate", "--format="+fmtStr, "refs/heads/")
	if err != nil {
		return nil, err
	}
	onRemote := remoteBranchNames(ctx, dir)
	var branches []BranchInfo
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 1 {
			continue
		}
		name := cols[0]
		bi := BranchInfo{Name: name, Current: name == current}
		if len(cols) > 1 {
			bi.LastCommit = cols[1]
		}
		var upstream string
		if len(cols) > 2 {
			upstream = strings.TrimSpace(cols[2])
		}
		if _, name, ok := strings.Cut(upstream, "/"); ok {
			bi.Upstream = name
		}
		if len(cols) > 3 {
			bi.UpstreamGone = strings.Contains(cols[3], "gone")
		}
		if len(cols) > 4 {
			bi.Tip = strings.TrimSpace(cols[4])
		}
		// The ref to measure against: the configured upstream while it still
		// exists, else origin/<name> when origin has one.
		base := ""
		switch {
		case upstream != "" && !bi.UpstreamGone:
			base = upstream
		case onRemote[name]:
			base = "origin/" + name
		}
		if base != "" {
			bi.HasRemote = true
			if a, b, ok := AheadBehind(ctx, dir, name, base); ok {
				bi.Ahead, bi.Behind = a, b
			}
		}
		if wt, ok := worktreeByBranch[name]; ok {
			bi.WorktreePath = wt
		}
		branches = append(branches, bi)
	}
	return branches, nil
}

// Commit is one entry in the recent-commits list.
type Commit struct {
	SHA     string
	Subject string
	When    string // relative
}

// RecentCommits returns the last n commits on dir's HEAD.
func RecentCommits(ctx context.Context, dir string, n int) ([]Commit, error) {
	out, err := runner(ctx, dir, "log", "-n", strconv.Itoa(n), "--format=%h%x09%s%x09%cr")
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.SplitN(line, "\t", 3)
		c := Commit{SHA: cols[0]}
		if len(cols) > 1 {
			c.Subject = cols[1]
		}
		if len(cols) > 2 {
			c.When = cols[2]
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// Stash is one entry from `git stash list`.
type Stash struct {
	Ref     string
	Subject string
}

// Stashes returns the stash list.
func Stashes(ctx context.Context, dir string) ([]Stash, error) {
	out, err := runner(ctx, dir, "stash", "list", "--format=%gd%x09%gs")
	if err != nil {
		return nil, err
	}
	var stashes []Stash
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.SplitN(line, "\t", 2)
		s := Stash{Ref: cols[0]}
		if len(cols) > 1 {
			s.Subject = cols[1]
		}
		stashes = append(stashes, s)
	}
	return stashes, nil
}

// UpstreamState classifies branches into the two states that make one safe to
// drop:
//
//   - gone: the branch had an upstream, that remote branch was deleted, and origin
//     has nothing under this name now. A branch whose upstream is gone but which
//     origin still carries under its own name (re-pushed without -u, say) is not
//     gone — its work is still on the remote.
//   - fullyPushed: origin has a counterpart and the branch is not ahead of it, so
//     every local commit already lives on origin. Deleting the local ref loses
//     nothing.
//
// This reads only what LocalBranches already measured, which judged each branch
// against the remote-tracking refs currently in .git — so a branch deleted on the
// remote since the last fetch still reads as alive until the next fetch --prune.
func UpstreamState(branches []BranchInfo) (gone, fullyPushed map[string]bool) {
	gone, fullyPushed = map[string]bool{}, map[string]bool{}
	for _, bi := range branches {
		switch {
		case bi.UpstreamGone && !bi.HasRemote:
			gone[bi.Name] = true
		case bi.HasRemote && bi.Ahead == 0:
			fullyPushed[bi.Name] = true
		}
	}
	return gone, fullyPushed
}

// FetchPrune updates the remote-tracking refs and drops the ones whose remote
// branch is gone. Without it "upstream gone" and "fully pushed to origin" are
// judged against whatever refs happen to be in .git, so a branch deleted on the
// remote since the last fetch still reads as alive. It never mutates the remote or
// any local branch — only refs/remotes. Errors are swallowed: no origin or no
// network just means classifying against the refs already known.
func FetchPrune(ctx context.Context, dir string) {
	_, _ = runner(ctx, dir, "fetch", "--prune", "origin")
}

// MergedIntoDefault returns the local branches whose commits are all reachable
// from the default branch — the branches `git branch --merged` finds. This catches
// ordinary (non-squash) merges and anything merged by someone else without a PR,
// neither of which shows up in the merged-PR list. Checked against
// origin/<default> so a stale local default branch can't hide a merge, falling
// back to the local ref when the remote-tracking one is unavailable.
func MergedIntoDefault(ctx context.Context, dir, def string) (map[string]bool, error) {
	merged := map[string]bool{}
	if def == "" {
		return merged, nil
	}
	out, err := runner(ctx, dir, "branch", "--merged", "origin/"+def, "--format=%(refname:short)")
	if err != nil {
		out, err = runner(ctx, dir, "branch", "--merged", def, "--format=%(refname:short)")
	}
	if err != nil {
		return merged, err
	}
	for b := range strings.SplitSeq(out, "\n") {
		if b = strings.TrimSpace(b); b != "" {
			merged[b] = true
		}
	}
	return merged, nil
}

// --- mutations ---

// Checkout switches the checkout at dir to branch. Used when the branch is not
// in a worktree.
func Checkout(ctx context.Context, dir, branch string) error {
	return mutator(ctx, dir, "checkout", branch)
}

// CheckoutNewBranch creates branch in the checkout at dir and switches to it,
// forked from base (or, when base is "", from HEAD). Unlike AddNewBranch this is
// the in-place counterpart: no worktree, just `git checkout -b`.
func CheckoutNewBranch(ctx context.Context, dir, branch, base string) error {
	args := []string{"checkout", "-b", branch}
	if base != "" {
		args = append(args, base)
	}
	return mutator(ctx, dir, args...)
}

// CheckoutOrCreate creates/checks out branch in the checkout at dir — no
// worktree — and reports which it did. Attachment is derived from git's own
// state, never guessed by a caller: already on it is a no-op; existing locally
// is a plain switch; existing only on origin is fetched then switched
// (auto-tracking); existing nowhere forks a new branch from OriginBase
// (origin/<default>, or HEAD when there is no default to fork from).
func CheckoutOrCreate(ctx context.Context, dir, branch string) (action string, err error) {
	if CurrentBranch(ctx, dir) == branch {
		return "already on", nil
	}
	if BranchExistsLocal(ctx, dir, branch) {
		if err := Checkout(ctx, dir, branch); err != nil {
			return "", err
		}
		return "switched to", nil
	}
	if BranchExistsRemote(ctx, dir, branch) {
		if err := FetchBranch(ctx, dir, branch); err != nil {
			return "", err
		}
		if err := Checkout(ctx, dir, branch); err != nil {
			return "", err
		}
		return "checked out", nil
	}
	if err := CheckoutNewBranch(ctx, dir, branch, OriginBase(ctx, dir)); err != nil {
		return "", err
	}
	return "created", nil
}

// Pull runs `git pull --ff-only` in the checkout at dir. A dashboard keypress
// must never silently author a merge commit or rewrite local commits, and
// without an explicit strategy git refuses outright ("Need to specify how to
// reconcile divergent branches") whenever pull.rebase is unset — which is the
// default. Fast-forward or fail, and the failure names a real divergence
// instead of a missing config.
func Pull(ctx context.Context, dir string) error {
	return mutator(ctx, dir, "pull", "--ff-only")
}

// DeleteBranch deletes a local branch. force uses -D (skip merged check).
func DeleteBranch(ctx context.Context, dir, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := runner(ctx, dir, "branch", flag, branch)
	return err
}

// AddNewBranch creates branch at base and adds a worktree for it at path. base
// is the commit-ish new work forks from; if empty, git uses the current HEAD.
// New worktrees must always pass an explicit base (origin/<default>) so a branch
// never inherits whatever the main working tree happens to be checked out on.
//
// --no-track because base is a remote-tracking ref (origin/main), and git's
// default would make the new branch track *that* — so `git push` on it would
// aim at main and `git status` would report ahead/behind against main instead
// of against the branch's own remote. A new branch has no upstream until it is
// first pushed with -u, which is the correct state to leave it in.
func AddNewBranch(ctx context.Context, dir, path, branch, base string) error {
	args := []string{"worktree", "add", "--no-track", "-b", branch, path}
	if base != "" {
		args = append(args, base)
	}
	return mutator(ctx, dir, args...)
}

// AddExisting checks out an existing branch (local, or a remote-tracking branch
// git will DWIM into a local one) into a new worktree at path.
func AddExisting(ctx context.Context, dir, path, branch string) error {
	return mutator(ctx, dir, "worktree", "add", path, branch)
}

// OriginBase returns the ref new branches should fork from: it fetches the
// default branch from origin (best effort) and returns "origin/<default>" when
// that remote-tracking ref exists, falling back to the local default branch
// name, or "" when there is no default branch at all (git then uses HEAD).
func OriginBase(ctx context.Context, dir string) string {
	def := DefaultBranch(ctx, dir)
	if def == "" {
		return ""
	}
	// Best effort: refresh origin/<default> so we fork from current origin, not a
	// stale snapshot. Offline or no-origin repos just fork from what's known.
	_, _ = runner(ctx, dir, "fetch", "origin", def)
	if BranchExistsRemote(ctx, dir, def) {
		return "origin/" + def
	}
	return def
}

// FetchBranch fetches a single branch from origin so a remote-only branch can be
// checked out into a worktree.
func FetchBranch(ctx context.Context, dir, branch string) error {
	return mutator(ctx, dir, "fetch", "origin", branch)
}

// RemoveWorktree removes the worktree at path from the repo at dir. force
// discards a dirty tree.
func RemoveWorktree(ctx context.Context, dir, path string, force bool) error {
	args := []string{"worktree", "remove", path}
	if force {
		args = append(args, "--force")
	}
	_, err := runner(ctx, dir, args...)
	return err
}

// StashWorktree saves a worktree's uncommitted changes onto the repo's shared
// stash list before a forced removal discards them for good — `-u` includes
// untracked files, which `worktree remove --force` would otherwise delete with no
// reflog or object behind them.
//
// It runs through the read seam like every other git call here specifically so its
// failures come back as a *CmdError: git's refusals run to several lines and the
// delete modal gives each item one, so the headline goes on the row and the rest
// stays reachable in the detail overlay. Invoked directly, this was the one git
// call in the codebase whose error was the raw multi-line output, which the row
// then clipped to its first word.
func StashWorktree(ctx context.Context, path string) error {
	_, err := runner(ctx, path, "stash", "push", "-u",
		"-m", "repo: auto-stash before worktree removal")
	return err
}

// PruneWorktrees removes administrative metadata for worktrees whose dirs are gone.
func PruneWorktrees(ctx context.Context, dir string) error {
	_, err := runner(ctx, dir, "worktree", "prune")
	return err
}

// FetchPRHead prepares a PR's head for AddExisting. When origin already has a
// remote-tracking branch of the same name (a same-repo PR), it does nothing
// and returns: AddExisting's DWIM creates the local branch straight from
// origin/<branch> and wires its upstream itself, so fetching the pull ref
// first would only pre-empt that DWIM by creating the local branch before it
// runs. Only a fork PR — where no origin/<branch> exists for the DWIM to work
// from — needs help: this fetches the PR head into a local branch and points
// the branch's upstream at refs/pull/<N>/head, so `git pull` keeps following
// the PR instead of freezing at whatever commit this fetch happened to catch.
// Safe to call when the local branch already exists.
func FetchPRHead(ctx context.Context, dir string, prNum int, branch string) error {
	if BranchExistsRemote(ctx, dir, branch) {
		return nil
	}
	pullRef := "refs/pull/" + strconv.Itoa(prNum) + "/head"
	if err := mutator(ctx, dir, "fetch", "origin", pullRef+":"+branch); err != nil && !BranchExistsLocal(ctx, dir, branch) {
		return err
	}
	if err := mutator(ctx, dir, "config", "branch."+branch+".remote", "origin"); err != nil {
		return err
	}
	return mutator(ctx, dir, "config", "branch."+branch+".merge", pullRef)
}
