package git

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestPullDivergedFailsWithoutReconcileHint pins the two halves of the divergent-pull
// failure the dashboard used to show: a bare `git pull` aborts on "Need to specify how
// to reconcile divergent branches" even when the user only ever wanted a fast-forward,
// and the refusal arrives as nine lines of hint paragraph that do not fit the status
// bar. Pull must fail on real divergence — never author a merge — and say why in one
// line that names the divergence, not the missing config.
func TestPullDivergedFailsWithoutReconcileHint(t *testing.T) {
	origin := t.TempDir()
	gitT(t, origin, "init", "-q", "--bare", "-b", "main")

	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "base")
	gitT(t, dir, "remote", "add", "origin", origin)
	gitT(t, dir, "push", "-q", "-u", "origin", "main")

	// A second clone pushes a commit origin-side...
	other := t.TempDir()
	gitT(t, other, "clone", "-q", origin, other)
	writeFile(t, filepath.Join(other, "theirs.txt"), "theirs\n")
	gitT(t, other, "add", "-A")
	gitT(t, other, "commit", "-qm", "theirs")
	gitT(t, other, "push", "-q", "origin", "main")

	// ...while dir commits its own work: the branches have truly diverged.
	writeFile(t, filepath.Join(dir, "mine.txt"), "mine\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "mine")

	t.Chdir(dir)
	err := Pull(WithQuiet(context.Background()))
	if err == nil {
		t.Fatal("Pull on diverged branches: want error, got nil (a merge commit was authored)")
	}
	msg := err.Error()
	if strings.Count(msg, "\n") != 0 {
		t.Errorf("Pull error spans multiple lines, would flood the status bar:\n%s", msg)
	}
	if strings.Contains(msg, "reconcile divergent branches") {
		t.Errorf("Pull error blames missing pull.rebase config rather than the divergence: %s", msg)
	}
	if !strings.Contains(msg, "fast-forward") {
		t.Errorf("Pull error does not name the fast-forward refusal: %s", msg)
	}
	// Trimming the status line must not destroy what git said — the rest is still
	// reachable for the error-detail overlay.
	var ce *CmdError
	if !errors.As(err, &ce) {
		t.Fatalf("Pull error is not a *CmdError, so its output is unreachable: %T", err)
	}
	if !strings.Contains(ce.Output, ce.Headline) {
		t.Errorf("CmdError.Output dropped the headline:\nheadline: %s\noutput: %s", ce.Headline, ce.Output)
	}
}

// TestCmdErrorKeepsOutputBehindHeadline pins the split the status bar depends on:
// Error() stays one line so it can't smear across the dashboard, while everything
// git wrote — including the lines that name the actual files — survives on Output.
func TestCmdErrorKeepsOutputBehindHeadline(t *testing.T) {
	full := "error: Your local changes to the following files would be overwritten by checkout:\n" +
		"\tinternal/git/git.go\n" +
		"Please commit your changes or stash them before you switch branches.\n" +
		"Aborting"
	ce := newCmdError(errors.New("exit status 1"), full)

	if got, want := ce.Headline, "error: Your local changes to the following files would be overwritten by checkout:"; got != want {
		t.Errorf("Headline = %q, want %q", got, want)
	}
	if strings.Count(ce.Error(), "\n") != 0 {
		t.Errorf("Error() spans multiple lines, would flood the status bar:\n%s", ce.Error())
	}
	if !strings.Contains(ce.Output, "internal/git/git.go") {
		t.Error("Output dropped the filename, the one line worth reading here")
	}
	if !ce.HasDetail() {
		t.Error("HasDetail() = false, so the overlay would never be offered")
	}

	// A refusal that is only its headline has nothing more to show.
	only := newCmdError(errors.New("exit status 1"), "fatal: not a git repository")
	if only.HasDetail() {
		t.Error("HasDetail() = true for a single-line refusal; the overlay would open on a repeat of the status bar")
	}
}

// TestPullFastForwards keeps the common case working: origin ahead, nothing local.
func TestPullFastForwards(t *testing.T) {
	origin := t.TempDir()
	gitT(t, origin, "init", "-q", "--bare", "-b", "main")

	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "base")
	gitT(t, dir, "remote", "add", "origin", origin)
	gitT(t, dir, "push", "-q", "-u", "origin", "main")

	other := t.TempDir()
	gitT(t, other, "clone", "-q", origin, other)
	writeFile(t, filepath.Join(other, "theirs.txt"), "theirs\n")
	gitT(t, other, "add", "-A")
	gitT(t, other, "commit", "-qm", "theirs")
	gitT(t, other, "push", "-q", "origin", "main")

	t.Chdir(dir)
	if err := Pull(WithQuiet(context.Background())); err != nil {
		t.Fatalf("Pull with a clean fast-forward available: %v", err)
	}
}

func TestErrHeadlineSkipsHintParagraph(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// git pull's real shape: advice first, the fatal last.
			name: "hints before fatal",
			in: "hint: You have divergent branches and need to specify how to reconcile them.\n" +
				"hint: You can do so by running one of the following commands sometime before\n" +
				"hint:\n" +
				"hint:   git config pull.rebase false  # merge\n" +
				"fatal: Need to specify how to reconcile divergent branches.",
			want: "fatal: Need to specify how to reconcile divergent branches.",
		},
		{
			name: "fatal wins over leading prose",
			in:   "From /tmp/origin\n * branch main -> FETCH_HEAD\nfatal: Not possible to fast-forward, aborting.",
			want: "fatal: Not possible to fast-forward, aborting.",
		},
		{
			name: "headline kept when there is no fatal",
			in:   "error is not the word\nsecond line",
			want: "error is not the word",
		},
		{
			name: "hints only still yields something",
			in:   "hint: nothing but advice\nhint: more advice",
			want: "hint: nothing but advice",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errHeadline(tc.in); got != tc.want {
				t.Errorf("errHeadline() = %q, want %q", got, tc.want)
			}
		})
	}
}
