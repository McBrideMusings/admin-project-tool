package git

import (
	"context"
	"path/filepath"
	"strings"
)

// TrackedDirs returns the absolute directories that contain tracked files in the
// current worktree, plus the worktree root. These are exactly the places where a
// change can affect the working tree, so a file watcher scoped to them ignores
// .git and gitignored trees (node_modules, build output) for free — .git is never
// listed by ls-files, so it is excluded here by construction. (The watcher's
// runtime filter for newly-created dirs lives in tui.isGitPath, since this initial
// scope can't see dirs that appear after startup.)
func TrackedDirs(ctx context.Context) ([]string, error) {
	top, err := TopLevel(ctx)
	if err != nil {
		return nil, err
	}
	out, err := runner(ctx, "-C", top, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{top: {}}
	for _, f := range strings.Split(out, "\x00") {
		if f == "" {
			continue
		}
		set[filepath.Join(top, filepath.Dir(f))] = struct{}{}
	}
	dirs := make([]string, 0, len(set))
	for d := range set {
		dirs = append(dirs, d)
	}
	return dirs, nil
}
