package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/timing"
)

// reservedToolVerbs are the five verbs the project's design principle 5
// ("Tool verbs are reserved") sets aside for the tool itself, listed here
// independent of whether this Go binary has implemented all of them yet —
// new, compile and worktree do not exist as cmd/ subcommands at this ticket
// (admin-3rgp.16 and others port them later), but a manifest still may not
// claim their names: reserving the word now is what makes adding the real
// command later a non-breaking change instead of a silent shadow.
var reservedToolVerbs = []string{"new", "check", "compile", "branch", "worktree"}

// reservedCmdVerbs are cmd/'s own subcommands plus cobra's built-ins —
// ADR-0011 §2's explicit enumeration of what step 1 of the resolution order
// covers ("new, check, compile, branch, worktree, prs, populate, plus
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
var reservedCmdVerbs = []string{"branch", "prs", "populate", "help", "completion"}

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
	// The nearest admin.toml at or above dir, not dir's own: a project's
	// commands mean the same thing from any directory inside it, and the
	// root the walk lands on is what every action's subprocess runs in.
	start := timing.Start()
	root, found := manifest.FindRoot(dir)
	timing.Phase("find admin.toml", start)
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
	repoRoot, _ := git.RepoRoot(ctx)
	sp, err := archetype.SearchPathFor(repoRoot)
	if err != nil {
		return nil, true, fmt.Errorf("could not resolve the installed archetypes directory: %w", err)
	}
	resolved, _, err := archetype.Resolve(project, sp)
	timing.Phase("resolve archetypes", start)
	if err != nil {
		return nil, true, err
	}
	return resolved, true, nil
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
