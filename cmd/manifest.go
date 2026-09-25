package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/compiledinfo"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/timing"
	"github.com/McBrideMusings/admin-project-tool/internal/worktree"
)

// reservedToolVerbs are the five verbs the project's design principle 5
// ("Tool verbs are reserved") sets aside for the tool itself, listed here
// independent of whether this Go binary implements each one: reserving a
// word before its command exists is what makes adding the command a
// non-breaking change instead of a silent shadow.
var reservedToolVerbs = []string{"new", "check", "compile", "branch", "worktree"}

// reservedCmdVerbs are cmd/'s own subcommands plus cobra's built-ins —
// ADR-0011 §2's explicit enumeration of what step 1 of the resolution order
// covers ("new, check, compile, branch, worktree, show, populate, plus
// cobra's help and completion") — and §3 extends the "tool verbs are
// reserved" rule to these too, rather than inventing a second concept.
//
// This list is intentionally the ADR's own enumeration rather than derived
// from rootCmd.Commands(): a cmd/ verb the ADR doesn't name (e.g.
// "archetypes", added by admin-3rgp.29) is still caught at dispatch time by
// dispatchArgs' generic loop over rootCmd.Commands() — it just isn't
// reported as a *reserved-name* violation by admin check, which checks
// against this literal list. Widening this list to every current cmd/ verb
// is a legitimate follow-up; it is out of this ticket's scope, which is to
// implement what the ADR says, not to extend it.
//
// "cmd" (admin-u7qi.1) was added alongside the ADR's original enumeration
// rather than left to fall into the "caught but not reported" gap the
// comment above describes, since it's a verb this ticket introduces itself.
var reservedCmdVerbs = []string{"show", "populate", "cmd", "help", "completion"}

// resolveProjectManifest loads dir's admin.toml (if any) and resolves it
// against its declared archetypes, using the same search path
// (archetypesListCmd) uses: home archetypes, then this checkout's own.
//
// ok is false only when neither dir nor any of its ancestors has an
// admin.toml at all — never paired with a
// non-nil err — so a caller can tell "no manifest here" (no project commands
// exist; an unrecognized word is simply unknown) apart from "a manifest is
// here and it's broken" (a real error the caller should not swallow the same
// way).
func resolveProjectManifest(ctx context.Context, dir string) (m *manifest.Manifest, ok bool, err error) {
	// A ./admin built by `admin compile` (cmd/compile.go) carries its
	// resolved manifest frozen inside the binary (internal/compiledinfo) —
	// that is the whole point of ADR-0006: the artifact runs standalone on a
	// machine with no ~/.admin and no admin.toml beside it. dir is ignored
	// entirely in that case, the same way the Python compiled zipapp ignores
	// cwd and uses argv[0]'s directory as project_root
	// (admin_lib/compile.py's _MAIN).
	if compiledinfo.Embedded() {
		return loadEmbeddedManifest()
	}
	return resolveProjectManifestFromDisk(ctx, dir)
}

// resolveProjectManifestFromDisk is resolveProjectManifest's body with no
// compiledinfo short-circuit — always reads dir's admin.toml off disk,
// regardless of whether the running binary itself carries an embedded
// manifest. runCompile (cmd/compile.go) calls this one directly, never
// resolveProjectManifest: `admin compile <DIR>` means "resolve DIR's own
// admin.toml", and if that were ever run from inside a compiled artifact
// (sourceDir baked in, compiledinfo.Embedded() also true — not possible
// today, since build_binary never sets sourceDir on a compile-time build of
// a compile-time build, but not a combination worth relying on staying
// impossible), resolveProjectManifest's short-circuit would silently ignore
// DIR and re-embed the artifact's own frozen manifest instead.
func resolveProjectManifestFromDisk(ctx context.Context, dir string) (m *manifest.Manifest, ok bool, err error) {
	// The nearest admin.toml at or above dir, not dir's own: a project's
	// commands mean the same thing from any directory inside it, and the
	// root the walk lands on is what every action's subprocess runs in.
	start := timing.Start()
	root, found := manifest.FindRoot(dir)
	timing.Phase("find admin.toml", start)
	// -w/--worktree's fallback: dir is the resolved worktree target (never
	// true for an ordinary call, since worktreeTarget is nil unless -w was
	// given) and it has no admin.toml of its own — a fresh worktree usually
	// won't, since admin.toml is never committed. Borrow the primary
	// checkout's manifest content, but keep running the target: execRoot
	// below overrides m.Root back to dir once Load/Resolve are done, so
	// every step, log and ${VAR} still resolves inside the worktree that was
	// actually asked for, never the primary checkout its admin.toml lives
	// in. See internal/worktree.ManifestContentRoot's own doc comment.
	execRoot := ""
	if !found && worktreeTarget != nil && worktreeTarget.Path == dir {
		if content, ok := worktree.ManifestContentRoot(dir, worktreePrimaryRoot); ok {
			root, found = content, true
			execRoot = dir
		}
	}
	if !found {
		return nil, false, nil
	}
	dir = root
	// Before Load, not after: archetype.Resolve eagerly resolves env-bearing
	// config (validate_url_refs, [log_bridge].hosts), so .env has to be in the
	// environment already or those ${VAR:-default} refs fall back to their
	// defaults. Same ordering as admin_lib/interp.py::interpret.
	start = timing.Start()
	manifest.LoadDotenv(dir)
	project, err := manifest.Load(dir)
	timing.Phase("parse manifest", start)
	if err != nil {
		return nil, true, err
	}
	start = timing.Start()
	// RepoRootFor(dir), not RepoRoot(ctx): the latter resolves the *process's*
	// cwd, which is wrong whenever dir is a -w target outside the invoking
	// shell's own repo (internal/worktree.Resolve's tier 1 explicitly allows
	// an existing directory in a different repo entirely). Archetype merge
	// must be keyed off the target, the same way effectiveRepoRoot in
	// cmd/root.go re-targets RepoRoot for the bare-dashboard case.
	repoRoot, _ := git.RepoRootFor(ctx, dir)
	sp, err := archetype.SearchPathFor(repoRoot)
	if err != nil {
		return nil, true, fmt.Errorf("could not resolve the installed archetypes directory: %w", err)
	}
	resolved, _, err := archetype.Resolve(project, sp)
	timing.Phase("resolve archetypes", start)
	if err != nil {
		return nil, true, err
	}
	if execRoot != "" {
		// -w's fallback ran: resolved.Root currently points at wherever the
		// borrowed admin.toml actually lives (the primary checkout). Every
		// downstream consumer of Root (internal/kinds.projectRoot: the
		// subprocess cwd for every step, the tmp/ log dir, ${VAR} resolution
		// against it) must instead see the worktree that was actually asked
		// for.
		resolved.Root = execRoot
	}
	return resolved, true, nil
}

// loadEmbeddedManifest decodes the manifest compiledinfo carries and points
// its Root at the running executable's own directory — argv[0]'s absolute
// path, not a symlink-resolved one, matching
// admin_lib/compile.py's `os.path.dirname(os.path.abspath(sys.argv[0]))` —
// so every action's `run` string executes relative to wherever this compiled
// ./admin was actually placed, never the directory admin compile happened to
// run in.
func loadEmbeddedManifest() (*manifest.Manifest, bool, error) {
	m, err := compiledinfo.Load()
	if err != nil {
		return nil, true, err
	}
	if abs, absErr := filepath.Abs(os.Args[0]); absErr == nil {
		m.Root = filepath.Dir(abs)
	}
	return m, true, nil
}

// reservedVerbCollisions returns every command name m declares (custom or
// archetype-provided) that collides with a reserved tool verb or an
// existing cmd/ verb, sorted and de-duplicated — ADR-0011 §3: "Tool verbs
// and cmd/ verbs win collisions, and admin check reports them."
func reservedVerbCollisions(m *manifest.Manifest) []string {
	reserved := map[string]bool{}
	for _, v := range reservedToolVerbs {
		reserved[v] = true
	}
	for _, v := range reservedCmdVerbs {
		reserved[v] = true
	}

	bad := map[string]bool{}
	for name := range m.Custom {
		if reserved[name] {
			bad[name] = true
		}
	}
	for name := range m.Overrides {
		if reserved[name] {
			bad[name] = true
		}
	}
	if len(bad) == 0 {
		return nil
	}
	names := make([]string, 0, len(bad))
	for name := range bad {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// checkReservedCollisions is the ADR-0011 §3 rule as an error: "a manifest
// declaring a command whose name collides with a tool verb ... or a cmd/
// verb ... is rejected with an error naming the rule and the colliding
// name." Full manifest schema validation (admin-3rgp.8) is not this
// ticket's job; this is the one check the identity/dispatch decision
// requires the check verb to carry.
func checkReservedCollisions(m *manifest.Manifest) error {
	bad := reservedVerbCollisions(m)
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf(
		"ADR-0011: tool verbs and cmd/ verbs win collisions — rename the "+
			"manifest command(s) %s, which collide with a reserved name",
		strings.Join(bad, ", "),
	)
}
