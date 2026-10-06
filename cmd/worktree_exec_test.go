package cmd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/livework"
)

// parseWorktree runs `admin <args...>` through cobra with worktreeRun swapped
// out, returning what it would have been called with.
func parseWorktree(t *testing.T, args ...string) (input, exec string, words []string) {
	t.Helper()
	orig := worktreeRun
	t.Cleanup(func() {
		worktreeRun = orig
		_ = worktreeCmd.Flags().Set("exec", "")
		_ = worktreeCmd.Flags().Set("yes", "false")
		rootCmd.SetArgs(nil)
	})
	called := false
	worktreeRun = func(_ context.Context, in string, _ bool, x string, w []string) error {
		called, input, exec, words = true, in, x, w
		return nil
	}
	rootCmd.SetArgs(args)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("admin %v: %v", args, err)
	}
	if !called {
		t.Fatalf("admin %v never reached runWorktree", args)
	}
	return input, exec, words
}

// `--` with nothing before it stays the escape for a branch named "rm", now
// that `admin worktree rm` is the removal subcommand.
func TestWorktreeDashEscapesRmBranch(t *testing.T) {
	input, exec, words := parseWorktree(t, "worktree", "-y", "--", "rm")
	if input != "rm" || exec != "" || len(words) != 0 {
		t.Fatalf("got input=%q exec=%q words=%v, want input=rm and no exec or words", input, exec, words)
	}
}

func TestWorktreeExecTakesPromptAfterDash(t *testing.T) {
	input, exec, words := parseWorktree(t, "worktree", "feat", "-x", "claude", "--", "fix it")
	if input != "feat" || exec != "claude" || !reflect.DeepEqual(words, []string{"fix it"}) {
		t.Fatalf("got input=%q exec=%q words=%v, want feat, claude, [fix it]", input, exec, words)
	}
}

// A word that is bare to shellQuote ("#12", "=ls") still must reach the agent
// as one literal argument: "#12" would otherwise comment out the rest of the
// line under sh.
func TestExecTargetQuotesEveryWord(t *testing.T) {
	got := kinds.NewExecTarget("feat", "claude", []string{"see", "#12", "=ls", "it's"}).Run
	want := `claude 'see' '#12' '=ls' 'it'\''s'`
	if got != want {
		t.Fatalf("Run = %q, want %q", got, want)
	}
}

func TestSplitWorktreeArgsRejects(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    []string
		dash    int
		hasExec bool
	}{
		{"words without -x", []string{"feat", "fix it"}, 1, false},
		{"-x without input", nil, -1, true},
		{"-x with only a prompt after --", []string{"fix the login bug"}, 0, true},
		{"two inputs", []string{"a", "b"}, -1, false},
	} {
		if _, _, err := splitWorktreeArgs(c.args, c.dash, c.hasExec); err == nil {
			t.Errorf("%s: splitWorktreeArgs(%v, %d, %v) = nil error", c.name, c.args, c.dash, c.hasExec)
		}
	}
}

// A project with no [terminal] launcher gets the workflow's named launcher
// error, and the agent is never run in this process's shell — not even when
// [terminal] makes hosted the project-wide default.
func TestWorktreeExecWithoutTerminalFails(t *testing.T) {
	for name, manifest := range map[string]string{
		"no [terminal]":          "project_name = \"t\"\n",
		"[terminal] hosted only": "project_name = \"t\"\n[terminal]\nlocale = \"hosted\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			repo, wt := rmFixture(t, "")
			writeFileCT(t, filepath.Join(repo, "admin.toml"), manifest)
			err := dispatchExec(repo, "feat", wt, "touch ran", nil)
			if err == nil || !strings.Contains(err.Error(), "requires a [terminal] launcher") {
				t.Fatalf("dispatchExec = %v, want the [terminal] launcher error", err)
			}
			if dirExists(filepath.Join(wt, "ran")) || dirExists(filepath.Join(repo, "ran")) {
				t.Fatal("the agent command ran inline")
			}
		})
	}
}

// With a [terminal] declared, the agent opens through its spawn in the
// worktree, carrying the worker env, and is registered as a live worker.
func TestWorktreeExecDispatchesThroughLauncher(t *testing.T) {
	repo, wt := rmFixture(t, "")
	got := filepath.Join(t.TempDir(), "spawned")
	writeFileCT(t, filepath.Join(repo, "admin.toml"), `project_name = "t"
[terminal]
spawn  = 'printf "%s\n%s" "$ADMIN_CWD" "$ADMIN_CMD" > `+got+` && echo pane-7'
attach = "true"
kill   = "true"
`)
	if err := dispatchExec(repo, "feat", wt, "claude", []string{"fix it"}); err != nil {
		t.Fatalf("dispatchExec: %v", err)
	}
	var entry *livework.Entry
	for _, e := range livework.Default.List() {
		if e.Worker != nil && e.Worker.Target == "feat" {
			entry = e
		}
	}
	if entry == nil {
		t.Fatal("no live worker entry for target feat")
	}
	t.Cleanup(func() {
		livework.Default.SetState(entry.ID, livework.StateExited, nil)
		kinds.ReleaseWorkerSlot(entry.ID)
	})
	if entry.Handle != "pane-7" {
		t.Fatalf("entry handle = %q, want pane-7", entry.Handle)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	cwd, cmdLine, _ := strings.Cut(string(b), "\n")
	wantWT, _ := filepath.EvalSymlinks(wt)
	if gotWT, _ := filepath.EvalSymlinks(cwd); gotWT != wantWT {
		t.Fatalf("ADMIN_CWD = %q, want the worktree %q", cwd, wt)
	}
	if !strings.HasSuffix(cmdLine, "; claude 'fix it'") || !strings.Contains(cmdLine, "ADMIN_WORKER_WORKTREE=") {
		t.Fatalf("ADMIN_CMD = %q, want the worker exports then claude 'fix it'", cmdLine)
	}
}
