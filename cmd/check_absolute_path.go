package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// absolutePathTokenRe finds absolute-path-shaped tokens inside a `run`
// string: a `/` preceded by start-of-string, whitespace, a quote, `(`, or
// `=` (so `--flag=/abs/path` is caught, not only a bare leading path),
// running to the next whitespace, quote, or colon. The colon boundary
// (tighter than the interim mitigation hook's own candidate regex in
// ~/.claude/hooks/admin-relative-path-guard.sh, which this check replaces)
// stops the match at a shell list separator — `PATH=/home/p/bin:$PATH`
// captures the absolute path "/home/p/bin" rather than swallowing the
// ":$PATH" tail into what would otherwise become a bogus candidate string
// walked ancestor-by-ancestor in nearestExistingAncestor.
var absolutePathTokenRe = regexp.MustCompile(`(?:^|[\s"'(=])(/[^\s"':]+)`)

// absolutePathCandidates pulls every absolute-path-shaped token out of run,
// trimming trailing shell separators (`,`/`;`/`&`/`|`/`)`) that are never
// part of a path.
func absolutePathCandidates(run string) []string {
	matches := absolutePathTokenRe.FindAllStringSubmatch(run, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.TrimRight(m[1], ",;&|)"))
	}
	return out
}

// checkAbsoluteRunPaths is admin-1xgi: admin check fails, rather than
// silently passing, when an action's `run` line names an absolute path that
// resolves inside a git checkout. `admin` chdirs to the directory holding
// admin.toml before running anything, so a repo-relative path is always
// correct from any subdirectory — an absolute one pointing into a checkout
// is never necessary, and `admin worktree` COPIES admin.toml into every new
// worktree rather than symlinking it, so a baked-in absolute path keeps
// running against whichever checkout it names (usually the primary) even
// from inside an unrelated worktree that shares no files with it. The
// failure is otherwise silent and convincing: `admin dev`/`test`/`vet`/`db`
// all succeed, ports open, the database migrates, and nothing in the output
// says which checkout actually ran.
//
// Only kind="shell"/"shell-passthrough" and kind="multi-target" run strings
// are examined — collectRunRefs' own ADR-0010 scope, the same set
// checkAntiPatterns already walks. Runs against the resolved (archetype-
// merged) manifest, since that is what actually executes; an archetype is
// shared across projects and would never hardcode a path into one
// particular checkout, but the resolved manifest is the one true source of
// what a `run` line will be at dispatch time regardless of where it came
// from.
//
// Candidates are scoped to paths under $HOME, mirroring the interim
// mitigation hook: "inside a git checkout" is not by itself a safe test,
// because plenty of legitimate system tooling is ALSO a git checkout on
// disk (on Apple Silicon, /opt/homebrew is itself a homebrew/brew git
// clone, so /opt/homebrew/bin/psql would otherwise be flagged as
// "resolves inside a checkout" and admin check would refuse to pass any
// project that shells out to it). A personal checkout — and the
// worktree-copy hazard this check exists for — only ever lives under
// $HOME, so requiring that prefix removes that class of false positive
// without hardcoding Homebrew, or anything else outside $HOME, by name.
func checkAbsoluteRunPaths(ctx context.Context, m *manifest.Manifest) error {
	var problems []string
	for _, name := range sortedActionNames(m.Actions) {
		for _, ref := range collectRunRefs(name, m.Actions[name]) {
			for _, cand := range absolutePathCandidates(ref.run) {
				root, ok := checkoutRootUnderHome(ctx, cand)
				if !ok {
					continue
				}
				problems = append(problems, fmt.Sprintf(
					"action %q: run names an absolute path %q that resolves inside a git checkout (%s) — "+
						"admin chdirs to the directory holding admin.toml before running anything, and "+
						"admin worktree COPIES admin.toml rather than symlinking it, so this line will keep "+
						"running against %s even from an unrelated worktree that shares no files with it; "+
						"use a path relative to admin.toml's own directory instead",
					ref.path, cand, root, root,
				))
				break // one report per run string is enough
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return validate.Errorf("%s", strings.Join(problems, "\n"))
}

// checkoutRootUnderHome reports whether path — an absolute-path-shaped
// candidate pulled off a `run` line — sits under $HOME and resolves inside
// a git checkout, returning that checkout's top-level directory when it
// does. A path outside $HOME, one with no existing ancestor to walk up to
// (nothing on disk, or the machine has no $HOME), or one whose nearest
// existing ancestor is not inside any checkout at all, reports ok=false.
func checkoutRootUnderHome(ctx context.Context, path string) (root string, ok bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	if path != home && !strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "", false
	}
	dir := nearestExistingAncestor(path)
	if dir == "" {
		return "", false
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	top, err := git.TopLevel(ctx, dir)
	if err != nil || top == "" {
		return "", false
	}
	return top, true
}

// nearestExistingAncestor walks up from path (inclusive) to the nearest
// directory entry that actually exists on disk, returning "" only if
// nothing above it exists either — which does not happen in practice, since
// the filesystem root always exists.
func nearestExistingAncestor(path string) string {
	d := path
	for {
		if _, err := os.Stat(d); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}
