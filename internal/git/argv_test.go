package git

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/argvscan"
)

// errOut in a test's out slice makes that read call return an error instead
// of stdout — for pinning the argv a command builds after an earlier read
// (e.g. show-ref reporting a ref absent) fails.
const errOut = "\x00err"

// call is one recorded git invocation. mutating says which seam it went through:
// a read whose stdout is parsed, or a mutation whose progress streams to the
// terminal. A command that swapped seams would keep its argv and change what the
// dashboard does with the output, so the flag is part of the record. The working
// directory is recorded as the `-C <dir>` the real seams put in front of the
// argv, so a call that ran in the wrong directory fails the same comparison.
type call struct {
	args     []string
	mutating bool
}

// recorder stands in for the runner and mutator vars: it records every call and
// replays canned stdout, so a test can assert the exact arguments a command
// function built without a git repository on disk.
type recorder struct {
	calls []call
	out   []string // stdout for call i; calls past the end get ""
}

// withDir is the argv the real seams run: `-C dir` in front of args.
func withDir(dir string, args []string) []string {
	return append([]string{"-C", dir}, args...)
}

func (r *recorder) run(_ context.Context, dir string, args ...string) (string, error) {
	r.calls = append(r.calls, call{args: withDir(dir, args)})
	if i := len(r.calls) - 1; i < len(r.out) {
		if r.out[i] == errOut {
			return "", errors.New("recorder: canned failure")
		}
		return r.out[i], nil
	}
	return "", nil
}

func (r *recorder) mutate(_ context.Context, dir string, args ...string) error {
	r.calls = append(r.calls, call{args: withDir(dir, args), mutating: true})
	return nil
}

// rd and mut are a read and a mutation run in /repo, the directory every table
// entry below hands the function under test unless it names another.
func rd(args ...string) call  { return call{args: withDir("/repo", args)} }
func mut(args ...string) call { return call{args: withDir("/repo", args), mutating: true} }

// rdIn is rd for a call that runs somewhere other than /repo.
func rdIn(dir string, args ...string) call { return call{args: withDir(dir, args)} }

// TestCommandArgv pins the argv of every exported command in this package.
// #18 was a missing `--repo` on `gh pr list --state merged` in the sibling
// package: gh resolved the repo from the working directory, so in a fork the
// Cleanup box struck a live branch through as merged. The git argvs carry the
// same weight — a `-d` that silently becomes `-D`, a dropped `--no-track`, a
// changed `for-each-ref` format — and nothing else in the suite would see it.
//
// The returned values and errors are deliberately ignored — the canned stdout
// only has to get each function as far as its command calls.
func TestCommandArgv(t *testing.T) {
	tests := []struct {
		name string
		out  []string
		call func(ctx context.Context)
		want []call
	}{
		{
			name: "TopLevel",
			out:  []string{"/repo"},
			call: func(ctx context.Context) { TopLevel(ctx, "/repo") },
			want: []call{rd("rev-parse", "--show-toplevel")},
		},
		{
			name: "RepoRoot compares the worktree's git dir against the common one",
			out:  []string{"/repo", "/repo/.git", "/repo/.git"},
			call: func(ctx context.Context) { RepoRoot(ctx, "/repo") },
			want: []call{
				rd("rev-parse", "--show-toplevel"),
				rd("rev-parse", "--path-format=absolute", "--git-dir"),
				rd("rev-parse", "--path-format=absolute", "--git-common-dir"),
			},
		},
		{
			name: "RepoRoot from a linked worktree reads core.worktree from the common dir",
			out:  []string{"/wt", "/repo/.git/worktrees/wt", "/repo/.git"},
			call: func(ctx context.Context) { RepoRoot(ctx, "/wt") },
			want: []call{
				rdIn("/wt", "rev-parse", "--show-toplevel"),
				rdIn("/wt", "rev-parse", "--path-format=absolute", "--git-dir"),
				rdIn("/wt", "rev-parse", "--path-format=absolute", "--git-common-dir"),
				rdIn("/repo/.git", "config", "--file", "/repo/.git/config", "--get", "core.worktree"),
			},
		},
		{
			name: "CurrentBranch",
			out:  []string{"feat/x"},
			call: func(ctx context.Context) { CurrentBranch(ctx, "/repo") },
			want: []call{rd("rev-parse", "--abbrev-ref", "HEAD")},
		},
		{
			name: "OriginNWO",
			out:  []string{"git@github.com:o/r.git"},
			call: func(ctx context.Context) { OriginNWO(ctx, "/repo") },
			want: []call{rd("remote", "get-url", "origin")},
		},
		{
			name: "DefaultBranch",
			out:  []string{"refs/remotes/origin/main"},
			call: func(ctx context.Context) { DefaultBranch(ctx, "/repo") },
			want: []call{rd("symbolic-ref", "refs/remotes/origin/HEAD")},
		},
		{
			name: "ValidBranchName defers to git's own rules",
			call: func(ctx context.Context) { ValidBranchName(ctx, "/repo", "feat/x") },
			want: []call{rd("check-ref-format", "--branch", "feat/x")},
		},
		{
			name: "BranchExistsLocal",
			call: func(ctx context.Context) { BranchExistsLocal(ctx, "/repo", "feat/x") },
			want: []call{rd("show-ref", "--verify", "--quiet", "refs/heads/feat/x")},
		},
		{
			name: "BranchExistsRemote",
			call: func(ctx context.Context) { BranchExistsRemote(ctx, "/repo", "feat/x") },
			want: []call{rd("show-ref", "--verify", "--quiet", "refs/remotes/origin/feat/x")},
		},
		{
			name: "WorkingStatus reads the worktree it was handed, not the cwd",
			call: func(ctx context.Context) { WorkingStatus(ctx, "/wt") },
			want: []call{rdIn("/wt", "status", "--porcelain")},
		},
		{
			name: "AheadBehind counts both sides in one rev-list",
			out:  []string{"2\t3"},
			call: func(ctx context.Context) { AheadBehind(ctx, "/repo", "feat/x", "origin/feat/x") },
			want: []call{rd("rev-list", "--left-right", "--count", "feat/x...origin/feat/x")},
		},
		{
			name: "FirstAuthor skips merges and reads the earliest commit",
			out:  []string{"a@b"},
			call: func(ctx context.Context) { FirstAuthor(ctx, "/repo", "feat/x", "main") },
			want: []call{rd("log", "--no-merges", "--reverse", "--format=%ae", "main..feat/x")},
		},
		{
			name: "BranchNote",
			call: func(ctx context.Context) { BranchNote(ctx, "/repo", "feat/x") },
			want: []call{rd("config", "--get", "branch.feat/x.description")},
		},
		{
			name: "SetBranchNote writes the description",
			call: func(ctx context.Context) { SetBranchNote(ctx, "/repo", "feat/x", "why") },
			want: []call{rd("config", "branch.feat/x.description", "why")},
		},
		{
			name: "SetBranchNote unsets on an empty note",
			call: func(ctx context.Context) { SetBranchNote(ctx, "/repo", "feat/x", "  ") },
			want: []call{rd("config", "--unset", "branch.feat/x.description")},
		},
		{
			name: "LastSubject",
			out:  []string{"fix: x"},
			call: func(ctx context.Context) { LastSubject(ctx, "/repo", "feat/x", "main") },
			want: []call{rd("log", "-1", "--no-merges", "--format=%s", "main..feat/x")},
		},
		{
			name: "CountAheadOf",
			out:  []string{"4"},
			call: func(ctx context.Context) { CountAheadOf(ctx, "/repo", "main", "feat/x") },
			want: []call{rd("rev-list", "--count", "main..feat/x")},
		},
		{
			name: "HasUnpushedCommits measures a remoteless branch against the default",
			out:  []string{"1"},
			call: func(ctx context.Context) {
				HasUnpushedCommits(ctx, "/repo", BranchInfo{Name: "feat/x"}, "main")
			},
			want: []call{rd("rev-list", "--count", "main..feat/x")},
		},
		{
			name: "LocalBranches reads heads, then origin's names, then the counts",
			out: []string{
				"feat/x\t3 days ago\torigin/feat/x\t\tdeadbeef",
				"feat/x",
				"0\t0",
			},
			call: func(ctx context.Context) { LocalBranches(ctx, "/repo", "main", nil) },
			want: []call{
				rd("for-each-ref", "--sort=-committerdate",
					"--format=%(refname:short)%09%(committerdate:relative)%09%(upstream:short)%09%(upstream:track)%09%(objectname)",
					"refs/heads/"),
				rd("for-each-ref", "--format=%(refname:strip=3)", "refs/remotes/origin/"),
				rd("rev-list", "--left-right", "--count", "feat/x...origin/feat/x"),
			},
		},
		{
			name: "RecentCommits",
			call: func(ctx context.Context) { RecentCommits(ctx, "/repo", 5) },
			want: []call{rd("log", "-n", "5", "--format=%h%x09%s%x09%cr")},
		},
		{
			name: "Stashes",
			call: func(ctx context.Context) { Stashes(ctx, "/repo") },
			want: []call{rd("stash", "list", "--format=%gd%x09%gs")},
		},
		{
			name: "FetchPrune drops remote-tracking refs whose branch is gone",
			call: func(ctx context.Context) { FetchPrune(ctx, "/repo") },
			want: []call{rd("fetch", "--prune", "origin")},
		},
		{
			name: "MergedIntoDefault checks against origin's default branch",
			out:  []string{"feat/x"},
			call: func(ctx context.Context) { MergedIntoDefault(ctx, "/repo", "main") },
			want: []call{rd("branch", "--merged", "origin/main", "--format=%(refname:short)")},
		},
		{
			name: "Checkout",
			call: func(ctx context.Context) { Checkout(ctx, "/repo", "feat/x") },
			want: []call{mut("checkout", "feat/x")},
		},
		{
			name: "CheckoutNewBranch forks from an explicit base",
			call: func(ctx context.Context) { CheckoutNewBranch(ctx, "/repo", "feat/x", "origin/main") },
			want: []call{mut("checkout", "-b", "feat/x", "origin/main")},
		},
		{
			name: "CheckoutNewBranch omits the base when it has none",
			call: func(ctx context.Context) { CheckoutNewBranch(ctx, "/repo", "feat/x", "") },
			want: []call{mut("checkout", "-b", "feat/x")},
		},
		{
			name: "HasCommitsNotOnOrigin",
			out:  []string{"deadbeef"},
			call: func(ctx context.Context) { HasCommitsNotOnOrigin(ctx, "/repo", "feat/x") },
			want: []call{rd("rev-list", "refs/heads/feat/x", "--not", "--remotes=origin")},
		},
		{
			name: "Pull is fast-forward only",
			call: func(ctx context.Context) { Pull(ctx, "/repo") },
			want: []call{mut("pull", "--ff-only")},
		},
		{
			name: "DeleteBranch keeps git's merged check",
			call: func(ctx context.Context) { DeleteBranch(ctx, "/repo", "feat/x", false) },
			want: []call{rd("branch", "-d", "feat/x")},
		},
		{
			name: "DeleteBranch force skips it",
			call: func(ctx context.Context) { DeleteBranch(ctx, "/repo", "feat/x", true) },
			want: []call{rd("branch", "-D", "feat/x")},
		},
		{
			name: "AddNewBranch forks from an explicit base without tracking it",
			call: func(ctx context.Context) { AddNewBranch(ctx, "/repo", "/wt", "feat/x", "origin/main") },
			want: []call{mut("worktree", "add", "--no-track", "-b", "feat/x", "/wt", "origin/main")},
		},
		{
			name: "AddNewBranch omits the base when it has none",
			call: func(ctx context.Context) { AddNewBranch(ctx, "/repo", "/wt", "feat/x", "") },
			want: []call{mut("worktree", "add", "--no-track", "-b", "feat/x", "/wt")},
		},
		{
			name: "AddExisting",
			call: func(ctx context.Context) { AddExisting(ctx, "/repo", "/wt", "feat/x") },
			want: []call{mut("worktree", "add", "/wt", "feat/x")},
		},
		{
			name: "OriginBase refreshes the default branch before forking from it",
			out:  []string{"refs/remotes/origin/main"},
			call: func(ctx context.Context) { OriginBase(ctx, "/repo") },
			want: []call{
				rd("symbolic-ref", "refs/remotes/origin/HEAD"),
				rd("fetch", "origin", "main"),
				rd("show-ref", "--verify", "--quiet", "refs/remotes/origin/main"),
			},
		},
		{
			name: "FetchBranch",
			call: func(ctx context.Context) { FetchBranch(ctx, "/repo", "feat/x") },
			want: []call{mut("fetch", "origin", "feat/x")},
		},
		{
			name: "RemoveWorktree",
			call: func(ctx context.Context) { RemoveWorktree(ctx, "/repo", "/wt", false) },
			want: []call{rd("worktree", "remove", "/wt")},
		},
		{
			name: "RemoveWorktree force discards a dirty tree",
			call: func(ctx context.Context) { RemoveWorktree(ctx, "/repo", "/wt", true) },
			want: []call{rd("worktree", "remove", "/wt", "--force")},
		},
		{
			name: "StashWorktree includes untracked files",
			call: func(ctx context.Context) { StashWorktree(ctx, "/wt") },
			want: []call{rdIn("/wt", "stash", "push", "-u",
				"-m", "repo: auto-stash before worktree removal")},
		},
		{
			name: "PruneWorktrees",
			call: func(ctx context.Context) { PruneWorktrees(ctx, "/repo") },
			want: []call{rd("worktree", "prune")},
		},
		{
			name: "FetchPRHead does nothing when origin already has the branch",
			out:  []string{""}, // show-ref succeeds: origin/pr-7 exists
			call: func(ctx context.Context) { FetchPRHead(ctx, "/repo", 7, "pr-7") },
			want: []call{rd("show-ref", "--verify", "--quiet", "refs/remotes/origin/pr-7")},
		},
		{
			name: "FetchPRHead fetches a fork PR's head and points upstream at the pull ref",
			out:  []string{errOut}, // show-ref fails: no origin/pr-7
			call: func(ctx context.Context) { FetchPRHead(ctx, "/repo", 7, "pr-7") },
			want: []call{
				rd("show-ref", "--verify", "--quiet", "refs/remotes/origin/pr-7"),
				mut("fetch", "origin", "refs/pull/7/head:pr-7"),
				mut("config", "branch.pr-7.remote", "origin"),
				mut("config", "branch.pr-7.merge", "refs/pull/7/head"),
			},
		},
		{
			name: "BranchActivity locates the reflogs in the common git dir",
			out:  []string{"/repo/.git"},
			call: func(ctx context.Context) {
				BranchActivity(ctx, "/repo", []BranchInfo{{Name: "feat/x"}})
			},
			want: []call{rd("rev-parse", "--path-format=absolute", "--git-common-dir")},
		},
		{
			name: "WorktreeTouched asks the worktree for its own git dir",
			call: func(ctx context.Context) { WorktreeTouched(ctx, "/wt") },
			want: []call{rdIn("/wt", "rev-parse", "--absolute-git-dir")},
		},
		{
			name: "TrackedDirs lists tracked files NUL-separated under the top level",
			out:  []string{"/repo"},
			call: func(ctx context.Context) { TrackedDirs(ctx, "/repo/sub") },
			want: []call{
				rdIn("/repo/sub", "rev-parse", "--show-toplevel"),
				rd("ls-files", "-z"),
			},
		},
		{
			name: "Worktrees",
			call: func(ctx context.Context) { Worktrees(ctx, "/repo") },
			want: []call{rd("worktree", "list", "--porcelain")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{out: tt.out}
			restoreRun, restoreMut := runner, mutator
			runner, mutator = rec.run, rec.mutate
			defer func() { runner, mutator = restoreRun, restoreMut }()

			tt.call(context.Background())

			if len(rec.calls) != len(tt.want) {
				t.Fatalf("made %d git calls, want %d:\ngot  %v\nwant %v", len(rec.calls), len(tt.want), rec.calls, tt.want)
			}
			for i := range tt.want {
				if rec.calls[i].mutating != tt.want[i].mutating {
					t.Errorf("call %d went through the wrong seam: mutating=%v, want %v", i, rec.calls[i].mutating, tt.want[i].mutating)
				}
				if !reflect.DeepEqual(rec.calls[i].args, tt.want[i].args) {
					t.Errorf("call %d argv mismatch:\ngot  %q\nwant %q", i, rec.calls[i].args, tt.want[i].args)
				}
			}
		})
	}
}

// coveredCommands is every exported function in this package that runs a git
// command. TestArgvCoverage fails when the package grows one that isn't listed,
// so a new command can't ship without its argv pinned above.
var coveredCommands = map[string]bool{
	"AddExisting":           true,
	"AddNewBranch":          true,
	"AheadBehind":           true,
	"BranchActivity":        true,
	"BranchExistsLocal":     true,
	"BranchExistsRemote":    true,
	"BranchNote":            true,
	"Checkout":              true,
	"CheckoutNewBranch":     true,
	"CheckoutOrCreate":      true,
	"ClassifyDead":          true,
	"CountAheadOf":          true,
	"HasCommitsNotOnOrigin": true,
	"CurrentBranch":         true,
	"DefaultBranch":         true,
	"DeleteBranch":          true,
	"FetchBranch":           true,
	"FetchPRHead":           true,
	"FetchPrune":            true,
	"FirstAuthor":           true,
	"HasUnpushedCommits":    true,
	"LastSubject":           true,
	"LocalBranches":         true,
	"MergedIntoDefault":     true,
	"OriginBase":            true,
	"OriginNWO":             true,
	"PruneWorktrees":        true,
	"Pull":                  true,
	"RecentCommits":         true,
	"RemoveWorktree":        true,
	"RepoRoot":              true,
	"SetBranchNote":         true,
	"StashWorktree":         true,
	"Stashes":               true,
	"TopLevel":              true,
	"TrackedDirs":           true,
	"ValidBranchName":       true,
	"WorkingStatus":         true,
	"WorktreeTouched":       true,
	"Worktrees":             true,
}

// TestEmptyDirIsRefused pins the seams' refusal of an empty directory. `git -C
// ""` leaves the cwd unchanged, so without the refusal a caller that forgot its
// dir would silently read whatever checkout the process stands in.
func TestEmptyDirIsRefused(t *testing.T) {
	ctx := context.Background()
	if _, err := runGit(ctx, "", "rev-parse", "--show-toplevel"); !errors.Is(err, ErrNoDir) {
		t.Errorf("runGit with an empty dir: err = %v, want ErrNoDir", err)
	}
	if err := runGitMutating(ctx, "", "status"); !errors.Is(err, ErrNoDir) {
		t.Errorf("runGitMutating with an empty dir: err = %v, want ErrNoDir", err)
	}
}

func TestArgvCoverage(t *testing.T) {
	argvscan.Check(t, argvscan.Config{
		Tool:    "git",
		Seams:   []string{"runner(", "mutator("},
		Covered: coveredCommands,
	})
}
