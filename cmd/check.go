package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// checkCmd is a reserved tool verb (design principle 5) — reserving it here,
// as a registered cobra command, is what makes dispatchArgs' step 1 (tool
// verbs win collisions) actually true for "check" specifically, and is
// itself the thing ADR-0011 §3 requires: "admin check rejects it with an
// error naming the rule and the colliding name."
//
// admin-3rgp.8: full schema validation — command pure-routing (ADR-0003),
// reserved-name collisions (ADR-0011 §3), urls reference resolution, and
// footer shape — parses [DIR] (or the cwd), resolves it against its
// archetypes, and reports every problem found, not only the first. The
// ADR-0009 anti-pattern report (react-native-cli lints etc.) is
// admin-3rgp.9's job and is deliberately not here.
var checkCmd = &cobra.Command{
	Use:   "check [DIR]",
	Short: "Validate a project's admin.toml (parses, merges archetypes, and reports every problem)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCheck(cmd, args)
	},
}

func init() {
	rootCmd.AddCommand(checkCmd)
}

func runCheck(cmd *cobra.Command, args []string) error {
	dir, err := checkTargetDir(args)
	if err != nil {
		return err
	}
	m, ok, err := resolveProjectManifest(cmd.Context(), dir)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "no admin.toml here — nothing to check")
		return nil
	}

	var problems []error
	if err := checkReservedCollisions(m); err != nil {
		problems = append(problems, validate.Wrap(err))
	}
	if err := checkCommandRouting(m); err != nil {
		problems = append(problems, validate.Wrap(err))
	}
	if err := checkUnknownKinds(m); err != nil {
		problems = append(problems, validate.Wrap(err))
	}
	if err := frame.ValidateURLRefs(m.Actions, m.Urls); err != nil {
		problems = append(problems, validate.Wrap(err))
	}
	if err := frame.ValidateFooterRefs(m.Actions); err != nil {
		problems = append(problems, validate.Wrap(err))
	}
	if err := kinds.ValidateWorkflows(m.Actions); err != nil {
		problems = append(problems, validate.Wrap(err))
	}
	if err := checkQueryWorkflowTracker(m); err != nil {
		problems = append(problems, validate.Wrap(err))
	}
	shadows, err := checkRowKeys(m)
	if err != nil {
		problems = append(problems, validate.Wrap(err))
	}
	for _, s := range shadows {
		fmt.Fprintln(os.Stderr, s)
	}

	// ADR-0009's own anti-patterns are reported, never failed (admin-3rgp.9):
	// the project's *own* admin.toml is checked against the un-merged
	// manifest.Load result, not the resolved m above — an archetype's job is
	// to carry implementation, so an archetype-provided action with a
	// multi-line body or a script reference is not a project's anti-pattern
	// at all and must never be flagged. reportAntiPatterns prints to
	// stderr/stdout directly and never contributes to problems/exit code.
	project, err := manifest.Load(dir)
	if err != nil {
		// Unreachable in practice: resolveProjectManifest above already
		// called manifest.Load(dir) and would have returned this same error
		// before we got here. Guarded rather than ignored so a future
		// refactor of resolveProjectManifest can't silently turn this into a
		// nil-pointer panic on project.Actions below.
		return errors.Join(append(problems, validate.Wrap(err))...)
	}
	reportAntiPatterns(checkAntiPatterns(project))

	if len(problems) == 0 {
		fmt.Fprintln(os.Stderr, "admin.toml: valid — no problems found")
		return nil
	}
	return errors.Join(problems...)
}

// AntiPattern is one ADR-0009 observation: the project's own admin.toml
// carries a piece of implementation the ADR says belongs in an archetype
// instead. Rule is a stable machine-readable slug (never prose) so a
// consumer like the variance-audit skill can match on it without parsing
// Message. Reporting one never fails `admin check` — ADR-0009's "considered
// and rejected" section: disallowing the escape hatch outright just pushes
// the same implementation into a Makefile the tool can no longer see.
type AntiPattern struct {
	Action  string `json:"action"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

const (
	// AntiPatternMultilineRun: an action's `run` is more than one line of
	// shell — ADR-0009's "a `run` that grows past one line is the trigger to
	// ask which archetype it belongs to."
	AntiPatternMultilineRun = "multiline-run"
	// AntiPatternSidecarScript: an action's `run` invokes a script file the
	// project keeps alongside admin.toml — ADR-0009's "a project may
	// reference a sidecar script" escape hatch, permitted but reported.
	AntiPatternSidecarScript = "sidecar-script"
)

// sidecarScriptRe matches a run string invoking a script file the project
// keeps on disk: either a relative path ("./tools/dev-run.sh", the ADR's own
// example) or a bare path containing at least one directory separator
// ("tools/dev-run.sh", "bash scripts/build.py"), ending in a common script
// extension. It deliberately does not require a command boundary before the
// match (no lookbehind in RE2) — a false positive would need an unrelated
// word to end in "/something.sh" inside a run string, which does not happen
// in practice.
var sidecarScriptRe = regexp.MustCompile(
	`(?:\.{1,2}/[\w./-]+|[\w-]+(?:/[\w./-]+)+)\.(?:sh|bash|zsh|py|rb|pl|js|mjs|ts|lua)\b`,
)

// multiTargetRunRefReserved mirrors internal/kinds.multiTargetReserved (kept
// as its own small copy rather than exported from kinds: this check reads
// the project's raw, un-merged manifest.Action before any kind-specific
// factory ever sees it, which is a different job from kinds' resolution of
// an already-merged action — exporting kinds' reserved set would couple two
// things that change for different reasons).
var multiTargetRunRefReserved = map[string]bool{
	"kind": true, "desc": true, "targets": true, "urls": true, "footer": true, "requires": true,
}

// runRef is one `run` string found in a project-declared action, tagged with
// the dotted path checkAntiPatterns reports it under ("dev" for a flat
// action, "dev.cf" for an inline multi-target subtable).
type runRef struct {
	path string
	run  string
}

// collectRunRefs pulls every `run` string out of one project-declared
// action — ADR-0010 narrows this to two kinds that hold a `run` at all:
// kind="shell"/"shell-passthrough" hold it directly, and kind="multi-target"
// holds one per target (an inline subtable's "run" key, or a flat
// `targets.<name>` string not prefixed "@", which delegates to another
// action instead of running a command). kind="lua" is deliberately excluded
// — ADR-0010's makeLua comment: a lua body's `run` is always inline source,
// never a script-path reference, so the sidecar-script rule cannot fire on
// it, and a multi-line lua body is not itself the anti-pattern ADR-0009
// names (a project authoring lua at all, versus in an archetype, is a
// different and un-ticketed question — out of this ticket's scope).
func collectRunRefs(name string, action manifest.Action) []runRef {
	kindStr, _ := action["kind"].(string)
	switch kindStr {
	case "shell", "shell-passthrough":
		if run, ok := action["run"].(string); ok {
			return []runRef{{path: name, run: run}}
		}
	case "multi-target":
		var refs []runRef
		for _, k := range sortedActionKeys(action) {
			if multiTargetRunRefReserved[k] {
				continue
			}
			tbl, ok := action[k].(map[string]any)
			if !ok {
				continue
			}
			if run, ok := tbl["run"].(string); ok {
				refs = append(refs, runRef{path: name + "." + k, run: run})
			}
		}
		if flat, ok := action["targets"].(map[string]any); ok {
			for _, k := range sortedActionKeys(flat) {
				s, ok := flat[k].(string)
				if !ok || strings.HasPrefix(s, "@") {
					continue
				}
				refs = append(refs, runRef{path: name + "." + k, run: s})
			}
		}
		return refs
	}
	return nil
}

func sortedActionKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// checkAntiPatterns is admin-3rgp.9: it reports, for the project's own
// un-merged admin.toml only, every `run` that either spans more than one
// line or invokes a sidecar script the project keeps — ADR-0009's two named
// anti-patterns. project must be the manifest.Load result (pre-archetype-
// merge): an archetype's own actions are supposed to carry implementation
// (that is ADR-0009's whole point), so running this against a resolved,
// merged manifest would flag every archetype's Lua/Bash body as a project
// anti-pattern, which is exactly backwards.
func checkAntiPatterns(project *manifest.Manifest) []AntiPattern {
	var found []AntiPattern
	for _, name := range sortedActionNames(project.Actions) {
		for _, ref := range collectRunRefs(name, project.Actions[name]) {
			if strings.Contains(ref.run, "\n") {
				found = append(found, AntiPattern{
					Action: ref.path,
					Rule:   AntiPatternMultilineRun,
					Message: fmt.Sprintf(
						"ADR-0009: action %q: run is a multi-line string — implementation belongs in an archetype, not the manifest",
						ref.path,
					),
				})
			}
			if script := sidecarScriptRe.FindString(ref.run); script != "" {
				found = append(found, AntiPattern{
					Action: ref.path,
					Rule:   AntiPatternSidecarScript,
					Message: fmt.Sprintf(
						"ADR-0009: action %q: run references script %q the project keeps — consider promoting it into an archetype",
						ref.path, script,
					),
				})
			}
		}
	}
	return found
}

// reportAntiPatterns prints checkAntiPatterns' findings in the two forms
// admin-3rgp.9's acceptance criteria require: one human-readable line per
// finding on stderr (alongside every other admin check message), and the
// same findings as a JSON array on stdout for a program — the variance-audit
// skill named in the ticket — to consume without re-deriving the ADR-0009
// rules itself. Prints nothing at all when found is empty, matching "a
// manifest with neither reports nothing."
func reportAntiPatterns(found []AntiPattern) {
	if len(found) == 0 {
		return
	}
	for _, ap := range found {
		fmt.Fprintln(os.Stderr, ap.Message)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(found)
}

// checkTargetDir resolves `admin check [DIR]`'s optional positional
// argument: the named directory when given, otherwise the cwd.
func checkTargetDir(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	return os.Getwd()
}

// checkUnknownKinds verifies that every action's kind exists in the kinds
// catalog — admin-3rgp.50. Uses kinds.Declared() to check if a kind exists.
// Reports every offending action, not just the first.
func checkUnknownKinds(m *manifest.Manifest) error {
	var problems []string
	for _, name := range sortedActionNames(m.Actions) {
		action := m.Actions[name]
		kindVal, ok := action["kind"]
		if !ok {
			// kind is required by validation elsewhere; skip here if missing
			continue
		}
		kindStr, ok := kindVal.(string)
		if !ok {
			// kind must be a string; skip non-string kinds (validation error elsewhere)
			continue
		}
		_, declared := kinds.Declared(kindStr)
		if !declared {
			problems = append(problems, fmt.Sprintf("action %q: unknown kind %q", name, kindStr))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return validate.Errorf("%s", strings.Join(problems, "\n"))
}

// checkQueryWorkflowTracker is admin-3rgp.53: a query-sourced workflow
// (`over = { query = "..." }`) resolves its targets against the beads
// tracker only (internal/kinds/workflowquery.go's ResolveQuery) — there is
// no GitHub-backed resolver, and the user's 2026-08-31 decision on this
// ticket was to fail loudly at check time rather than build one. A project
// that declares tracker = "github" (or any non-beads value) and also
// declares a query-sourced workflow has an action that provably cannot
// resolve its targets and therefore cannot run, so — matching
// checkUnknownKinds' precedent (admin-3rgp.50: a manifest that cannot run
// must not pass validation) — this is an ERROR, not a warning.
//
// A project that declares no tracker at all (m.HasTracker == false) must
// not start failing check over this: HasTracker false means "not declared",
// and the tool's own default resolution path is beads regardless, so only a
// project that explicitly opts into a non-beads tracker is reported.
// Reports every offending action, not just the first.
func checkQueryWorkflowTracker(m *manifest.Manifest) error {
	if !m.HasTracker || m.Tracker == "beads" {
		return nil
	}
	var problems []string
	for _, name := range sortedActionNames(m.Actions) {
		action := m.Actions[name]
		kindStr, _ := action["kind"].(string)
		if kindStr != "workflow" {
			continue
		}
		target, err := kinds.ParseWorkflowOver(name, action)
		if err != nil {
			// Malformed `over` is kinds.ValidateWorkflows' problem to report;
			// nothing more to check here for this action.
			continue
		}
		if target.Source != kinds.WorkflowSourceQuery {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"action %q: over.query %q can only resolve against the beads tracker, but tracker = %q — "+
				"there is no GitHub-backed query resolver (admin-3rgp.53); use over = \"selection\" instead or switch tracker to \"beads\"",
			name, target.Query, m.Tracker,
		))
	}
	if len(problems) == 0 {
		return nil
	}
	return validate.Errorf("%s", strings.Join(problems, "\n"))
}

// checkCommandRouting is ADR-0003 / design principle 2 (checkReservedCollisions'
// sibling rule) over every resolved command, custom and archetype-provided
// alike: a [commands.X] table may carry only desc/steps/log-overrides, and a
// final command must end up with at least one step. manifest.toCommand
// already stashes any key outside that known set into Command.Raw instead
// of raising at parse time (rejecting it is this ticket's job, not the
// parser's) — so an offending key surviving all the way to a resolved
// manifest shows up here, in Raw, however it got there (the project's own
// [commands.X], or an archetype's, merged straight through by
// internal/archetype.merge's whole-value replacement when the project never
// overrides that name). Reports every offending command, not just the
// first.
func checkCommandRouting(m *manifest.Manifest) error {
	var problems []string
	for _, name := range sortedCommandNames(m.Overrides) {
		cmd := m.Overrides[name]
		if err := validate.CommandRouting(fmt.Sprintf("command %q", name), rawKeys(cmd.Raw), len(cmd.Steps) > 0); err != nil {
			problems = append(problems, err.Error())
		}
	}
	for _, name := range sortedCommandNames(m.Custom) {
		cmd := m.Custom[name]
		if err := validate.CommandRouting(fmt.Sprintf("command %q (commands.custom)", name), rawKeys(cmd.Raw), len(cmd.Steps) > 0); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return validate.Errorf("%s", strings.Join(problems, "\n"))
}

// checkRowKeys is admin-l6kb.1: validates the resolved manifest's top-level
// [keys.<row-kind>] table (ADR-0018) via validate.RowKeys, against the set
// of command names the resolved manifest actually declares — Overrides plus
// Custom, so an archetype-provided command a project never overrides still
// counts as a valid binding target. Returns the shadow lines to print
// (never failing) alongside the validation error, if any.
func checkRowKeys(m *manifest.Manifest) ([]string, error) {
	commands := m.CommandNames()
	_, shadows, err := validate.RowKeys(m.Keys, commands)
	if err != nil {
		return shadows, err
	}
	return shadows, nil
}

func sortedCommandNames(cmds map[string]manifest.Command) []string {
	names := make([]string, 0, len(cmds))
	for name := range cmds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedActionNames(actions map[string]manifest.Action) []string {
	names := make([]string, 0, len(actions))
	for name := range actions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func rawKeys(raw map[string]any) []string {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	return keys
}
