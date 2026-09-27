// Package guards is the single home for guard handling across the whole
// pipeline. A guard is a named
// precondition referenced from a `requires` list on an action or a
// multi-target target. Before the action's run fires, each named guard is
// invoked in order (fail-fast); on failure the caller reports the guard's
// diagnostic and the process exits non-zero (mapping an error to an exit
// code is CLI wiring, out of this package's scope — same split
// internal/kinds.RunAction/RunCommand already draw).
//
// Two authoring paths share one name namespace:
//
//   - Built-in guards (this package): Go functions registered at import
//     time (git-clean, git-pushed, deps-fresh), free to produce dynamic
//     diagnostics (e.g. the current branch in a hint).
//   - User-defined guards (TOML): `[guards.<name>]` tables with `run` (a
//     shell expression) and `fail` (a static message) — parsed by
//     manifest.Parse into manifest.Manifest.Guards.
//
// This package owns every guard-specific step the pipeline needs:
//
//   - manifest.Parse (via manifest.parseGuardTables) validates `[guards.<name>]`
//     definitions at parse time — this package's ValidateRequires does not
//     duplicate that; it only checks that every `requires` reference resolves.
//   - ValidateRequires(m) — every `requires` name (action-level and
//     per-target) resolves to a known guard (resolve phase, called from
//     internal/archetype.Resolve).
//   - Register(guardDefs) — wire user-defined shell guards into the runtime
//     registry (interpreter registration phase). Built-ins register at
//     package init.
//   - RunRequires(names) — run a `requires` list, fail-fast (dispatch phase,
//     called from internal/kinds.RunAction and the multi-target factory).
//   - RunGlobalPreflight(command) — process-wide checks that run before any
//     command regardless of `requires` (deps_fresh.go).
//
// Two bugs fixed in git-pushed (admin-c8lc, admin-d03k) — see
// gitPushedGuard's doc comment for what changed and why.
package guards

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/timing"
)

// Func is a registered guard's implementation: no arguments, returns
// (ok, message). On success: (true, ""). On failure: (false, "<actionable
// message>") — conventionally prefixed with the guard's own name.
type Func func() (bool, string)

// Builtin is the set of guard names implemented in Go rather than in a
// manifest's [guards.<name>] table. manifest.parseGuardTables already
// rejects a TOML guard using one of these names; this is the same set
// re-exported so ValidateRequires and the interpreter don't hard-code a
// second copy.
var Builtin = map[string]bool{"git-clean": true, "git-pushed": true, "deps-fresh": true}

var (
	registryMu sync.Mutex
	registry   = map[string]Func{}
)

func init() {
	registerBuiltin("git-clean", gitCleanGuard)
	registerBuiltin("git-pushed", gitPushedGuard)
	registerBuiltin("deps-fresh", func() (bool, string) { return depsFreshCheck("") })
}

func registerBuiltin(name string, fn Func) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = fn
}

// RegisterGuard registers a guard under name, available to every later
// RunRequires call. Exported for tests and for a future host that wants to
// add a guard programmatically; ordinary manifests only ever add
// user-defined shell guards via Register.
func RegisterGuard(name string, fn Func) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = fn
}

// registerShellGuard registers a guard backed by a `/bin/sh -c <run>`
// invocation. cwd inherits from the process (the interpreter cd's to the
// project root at boot, so guards run there too). On non-zero exit, the
// captured output is prepended to the `fail` message —
// gives context (e.g. a `git status` dump) in front of the actionable
// instruction.
func registerShellGuard(name, run, fail string) {
	RegisterGuard(name, func() (bool, string) {
		cmd := exec.Command("/bin/sh", "-c", run)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return true, ""
		}
		message := fmt.Sprintf("%s: %s", name, fail)
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			message = trimmed + "\n" + message
		}
		return false, message
	})
}

// Register wires every user-defined shell guard from a manifest's Guards
// map into the runtime registry — the interpreter setup phase. Built-in
// guards are already registered at package init, so this only ever adds
// TOML-defined ones (parseGuardTables already rejected any name collision
// with a built-in at parse time).
func Register(guardDefs map[string]manifest.Guard) {
	for name, def := range guardDefs {
		registerShellGuard(name, def.Run, def.Fail)
	}
}

// Reset clears every registered guard and re-registers the three built-ins.
// For tests that want a clean registry rather than one polluted by a
// previous test's Register/RegisterGuard calls.
func Reset() {
	registryMu.Lock()
	registry = map[string]Func{}
	registryMu.Unlock()
	registerBuiltin("git-clean", gitCleanGuard)
	registerBuiltin("git-pushed", gitPushedGuard)
	registerBuiltin("deps-fresh", func() (bool, string) { return depsFreshCheck("") })
}

// RunRequires runs a `requires` list, fail-fast. Returns nil on all-pass,
// or an error carrying the first failing guard's diagnostic (an unknown
// name counts as a failure — ValidateRequires should already have caught
// it at resolve time, so reaching this is itself a bug worth surfacing
// loudly).
func RunRequires(names []string) error {
	for _, name := range names {
		registryMu.Lock()
		fn, ok := registry[name]
		registryMu.Unlock()
		if !ok {
			return fmt.Errorf("requires: unknown guard %q", name)
		}
		start := timing.Start()
		passed, message := fn()
		timing.Phase("requires: "+name, start)
		if !passed {
			if message != "" {
				return fmt.Errorf("%s", message)
			}
			return fmt.Errorf("requires: guard %q failed", name)
		}
	}
	return nil
}

// RequiresOf reads the `requires` key off a raw manifest.Action as a list of
// strings. An absent key returns (nil, nil) — "no requires declared" is not
// an error. A present-but-malformed value (not a list of strings) is an
// error naming the culprit.
func RequiresOf(action manifest.Action) ([]string, error) {
	v, present := action["requires"]
	if !present {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("'requires' must be a list of strings")
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("'requires' must be a list of strings")
		}
		out = append(out, s)
	}
	return out, nil
}

// ValidateRequires audits every action's `requires` (and, for a
// kind="multi-target" action, every inline target's own `requires`
// override) against the known guard set — a built-in name or a key in
// guardDefs. Called from internal/archetype.Resolve (the resolve phase)
// after archetype merging. Reports every unknown reference it finds,
// sorted by action name,
// rather than stopping at the first — a merged manifest is small enough
// that surfacing every typo in one pass beats a fix-one-rerun loop.
func ValidateRequires(m *manifest.Manifest) error {
	known := make(map[string]bool, len(Builtin)+len(m.Guards))
	for name := range Builtin {
		known[name] = true
	}
	for name := range m.Guards {
		known[name] = true
	}

	var problems []string
	names := make([]string, 0, len(m.Actions))
	for name := range m.Actions {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, actionName := range names {
		action := m.Actions[actionName]
		reqs, err := RequiresOf(action)
		if err != nil {
			problems = append(problems, fmt.Sprintf("action %q: %s", actionName, err))
			continue
		}
		for _, req := range reqs {
			if !known[req] {
				problems = append(problems, fmt.Sprintf(
					"action %q: requires references unknown guard %q. Known: %s",
					actionName, req, sortedKnown(known)))
			}
		}

		for _, targetName := range sortedTargetKeys(action) {
			tbl, _ := action[targetName].(map[string]any)
			rv, present := tbl["requires"]
			if !present {
				continue // absent means "inherit the parent" — nothing to validate here
			}
			list, ok := rv.([]any)
			if !ok {
				problems = append(problems, fmt.Sprintf(
					"action %q: target %q requires must be a list of strings", actionName, targetName))
				continue
			}
			for _, item := range list {
				req, ok := item.(string)
				if !ok {
					problems = append(problems, fmt.Sprintf(
						"action %q: target %q requires must be a list of strings", actionName, targetName))
					continue
				}
				if !known[req] {
					problems = append(problems, fmt.Sprintf(
						"action %q: target %q requires references unknown guard %q. Known: %s",
						actionName, targetName, req, sortedKnown(known)))
				}
			}
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "\n"))
}

// sortedTargetKeys returns the subset of action's keys whose value is a
// table — the inline multi-target subtable shape — sorted, so
// ValidateRequires's error list is deterministic. It does not try to
// distinguish "this action is actually kind=multi-target" from "an action
// happens to carry a table-valued key" — a false positive here means one
// extra (harmless, unreachable) requires-shape check on a table value that
// no factory will ever read as a target, not a missed real one.
func sortedTargetKeys(action manifest.Action) []string {
	var out []string
	for k, v := range action {
		if k == "requires" {
			continue
		}
		if _, ok := v.(map[string]any); ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKnown(known map[string]bool) string {
	out := make([]string, 0, len(known))
	for k := range known {
		out = append(out, k)
	}
	sort.Strings(out)
	return "[" + strings.Join(out, " ") + "]"
}
