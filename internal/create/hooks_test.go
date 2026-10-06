package create

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// post_create runs after populate, inside the new worktree, with the three
// ADR-0024 variables set — and only when the worktree is created: reopening
// it runs nothing.
func TestWorktreePostCreateRunsOnCreateOnly(t *testing.T) {
	clone, _ := setupRepo(t)
	ctx := git.WithQuiet(context.Background())
	writeFile(t, filepath.Join(clone, "admin.toml"), `project_name = "t"

[[populate]]
pattern = "admin.toml"
mode = "copy"

[worktrees.hooks]
post_create = 'echo "$ADMIN_WT_BRANCH|$ADMIN_WT_PATH|$ADMIN_REPO_ROOT|$(pwd -P)|$(test -f admin.toml && echo populated)" >> "$ADMIN_REPO_ROOT/hook.log"'
`)

	res, err := Worktree(ctx, clone, "feat-hook")
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := Worktree(ctx, clone, "feat-hook"); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(clone, "hook.log"))
	if err != nil {
		t.Fatalf("post_create never ran: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("post_create ran %d times, want 1 (reopen must not run it): %q", len(lines), data)
	}
	want := strings.Join([]string{"feat-hook", res.Path, clone, resolve(res.Path), "populated"}, "|")
	if lines[0] != want {
		t.Errorf("hook saw %q, want %q", lines[0], want)
	}
}

// A failing post_create keeps the worktree, reports the hook's output and
// exit status, and skips post_start.
func TestWorktreeFailedPostCreateKeepsWorktree(t *testing.T) {
	clone, _ := setupRepo(t)
	writeFile(t, filepath.Join(clone, "admin.toml"), `project_name = "t"

[worktrees.hooks]
post_create = "echo install-broke; exit 3"
post_start  = 'touch "$ADMIN_REPO_ROOT/post_start-ran"'
`)

	res, err := Worktree(git.WithQuiet(context.Background()), clone, "feat-fail")
	if err == nil {
		t.Fatal("Worktree with a failing post_create: want an error, got nil")
	}
	for _, want := range []string{"exit 3", "install-broke", res.Path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
	assertWorktreeOnBranch(t, res.Path, "feat-fail")
	if res.PostStartLog != "" {
		t.Errorf("PostStartLog = %q, want post_start skipped", res.PostStartLog)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(clone, "post_start-ran")); err == nil {
		t.Error("post_start ran after post_create failed")
	}
}

// post_start runs detached: Worktree returns while the hook is still
// running, and the hook's output lands in a log under the project's log
// directory.
func TestWorktreePostStartIsDetached(t *testing.T) {
	clone, _ := setupRepo(t)
	writeFile(t, filepath.Join(clone, "admin.toml"), `project_name = "t"

[worktrees.hooks]
post_start = 'sleep 1; echo "started on $ADMIN_WT_BRANCH"'
`)

	start := time.Now()
	res, err := Worktree(git.WithQuiet(context.Background()), clone, "feat-start")
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("Worktree took %s, want it to return before the 1s post_start finishes", elapsed)
	}
	if filepath.Dir(res.PostStartLog) != filepath.Join(clone, "tmp") {
		t.Fatalf("PostStartLog = %q, want a file under %s", res.PostStartLog, filepath.Join(clone, "tmp"))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(res.PostStartLog)
		if strings.Contains(string(data), "started on feat-start") && strings.Contains(string(data), "post_start: exit 0") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("post_start log never showed the hook's output and exit:\n%s", data)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
