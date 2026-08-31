package kinds

import (
	"fmt"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/guards"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// multiTargetReserved are the top-level keys on a kind="multi-target" action
// that are never themselves a target name — everything else whose value is
// a table is an inline target subtable (admin_lib/validate.py's
// inline_targets: "keys whose value is a dict and not a reserved action
// key"). Narrower than Python's full VALID_ACTION_KEYS since this ticket
// doesn't implement urls/footer/keys (admin-3rgp.11/.18) — those stay
// usable as ordinary map values on an action without being misidentified as
// a target, because callers of this ticket's factories don't parse them
// anyway; the day one of those tickets lands, its own keys join this set.
// "requires" is listed here too even though its value (a list, per
// guards.RequiresOf) already keeps it out of the map[string]any inline scan
// below — belt and suspenders against a malformed manifest that puts a
// table there instead.
var multiTargetReserved = map[string]bool{
	"kind": true, "desc": true, "targets": true, "requires": true,
}

// multiTargetEntry is one resolved target: either a shell command to run
// directly, or the name of another declared action to delegate to.
// requiresSet distinguishes "this target declared its own requires"
// (including an empty list — the explicit opt-out) from "no override was
// given, inherit the parent's default" — the same None-vs-[]-vs-list
// tri-state the pinned-footer urls/footer design (project CLAUDE.md) uses
// for target_urls/target_footer.
type multiTargetEntry struct {
	run         string
	action      string
	requires    []string
	requiresSet bool
}

// makeMultiTarget is kind="multi-target"'s factory — the Go port of
// admin_lib/interp.py::_make_multi_target, minus per-target urls/footer
// inheritance (admin-3rgp.11, out of this ticket's scope). Per-target
// `requires` (admin-3rgp.10) IS implemented here: an inline target subtable
// may carry its own `requires`, which replaces (never merges with) the
// action-level default — ADR-0005's "multi-target inheritance is
// replace-not-merge".
//
// Both target shapes Python supports are ported: inline subtables
// ([actions.dev.cf] run = "...") and the flat `targets` table (name = "..."
// or name = "@other-action" for delegation) — the two are mutually
// exclusive, matching validate_action's "cannot mix targets with inline
// target subtables".
func makeMultiTarget(name string, action manifest.Action, m *manifest.Manifest) (Runner, error) {
	desc, _ := actionString(action, "desc")
	if desc == "" {
		desc = name
	}

	parentRequires, err := guards.RequiresOf(action)
	if err != nil {
		return nil, fmt.Errorf("action %q: %w", name, err)
	}

	inline := map[string]map[string]any{}
	for k, v := range action {
		if multiTargetReserved[k] {
			continue
		}
		if tbl, ok := v.(map[string]any); ok {
			inline[k] = tbl
		}
	}
	flatRaw, hasFlat := action["targets"].(map[string]any)
	if len(inline) > 0 && hasFlat {
		return nil, fmt.Errorf(
			"action %q: cannot mix inline target subtables with a flat 'targets' table", name)
	}

	targets := map[string]multiTargetEntry{}
	var order []string

	if len(inline) > 0 {
		for _, k := range sortInlineKeys(inline) {
			tbl := inline[k]
			run, hasRun := tbl["run"].(string)
			act, hasAct := tbl["action"].(string)
			if hasRun == hasAct {
				return nil, fmt.Errorf(
					"action %q: target %q must have exactly one of 'run' or 'action'", name, k)
			}
			entry := multiTargetEntry{run: run, action: act}
			if rv, present := tbl["requires"]; present {
				list, ok := rv.([]any)
				if !ok {
					return nil, fmt.Errorf(
						"action %q: target %q requires must be a list of strings", name, k)
				}
				reqs := make([]string, 0, len(list))
				for _, item := range list {
					s, ok := item.(string)
					if !ok {
						return nil, fmt.Errorf(
							"action %q: target %q requires must be a list of strings", name, k)
					}
					reqs = append(reqs, s)
				}
				entry.requires, entry.requiresSet = reqs, true
			}
			targets[k] = entry
			order = append(order, k)
		}
	} else if hasFlat {
		for _, k := range sortedStringKeys(flatRaw) {
			s, ok := flatRaw[k].(string)
			if !ok {
				return nil, fmt.Errorf(
					"action %q: targets.%s must be a string (a shell command, or \"@other-action\")", name, k)
			}
			if strings.HasPrefix(s, "@") {
				targets[k] = multiTargetEntry{action: strings.TrimPrefix(s, "@")}
			} else {
				targets[k] = multiTargetEntry{run: s}
			}
			order = append(order, k)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf(`action %q: kind="multi-target" needs at least one target`, name)
	}

	return func(args []string) error {
		h := newHost()

		var picked string
		rest := args
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			picked, rest = args[0], args[1:]
		}

		if picked == "" {
			if len(order) == 1 {
				picked = order[0]
				h.Info(fmt.Sprintf("%s: %s is this project's only target", name, picked))
			} else {
				entries := make([]luahost.TargetEntry, 0, len(order))
				for _, k := range order {
					entries = append(entries, luahost.TargetEntry{Key: k, Description: k})
				}
				key, ok := h.PickTarget(entries, desc+" — select target")
				if !ok {
					return fmt.Errorf(
						"action %q: no target given and none could be picked "+
							"(pass one as an argument, or run from a TTY)", name)
				}
				picked = key
			}
		}

		t, ok := targets[picked]
		if !ok {
			return fmt.Errorf("action %q: unknown target %q", name, picked)
		}
		h.SetLogContext([]string{name, picked}, 0)

		effectiveRequires := parentRequires
		if t.requiresSet {
			effectiveRequires = t.requires
		}
		if err := guards.RunRequires(effectiveRequires); err != nil {
			return fmt.Errorf("action %q: target %q: %w", name, picked, err)
		}

		if t.action != "" {
			if currentDispatch == nil {
				return fmt.Errorf(
					"action %q: target %q delegates to action %q, which requires dispatching "+
						"through kinds.RunAction/RunCommand rather than calling this factory's "+
						"Runner directly", name, picked, t.action)
			}
			return currentDispatch(t.action, rest)
		}

		run := strings.TrimSpace(manifest.ResolveComputed(t.run))
		if run == "" {
			h.Err(emptyCmdErr)
			return fmt.Errorf("action %q: target %q: %s", name, picked, emptyCmdErr)
		}
		h.Info(fmt.Sprintf("running [%s:%s]: %s", name, picked, run))
		res, err := h.RunCmd(run, luahost.RunCmdOptions{CaptureLog: true})
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if !res.Ok {
			h.Err(name + " failed")
			return fmt.Errorf("action %q failed", name)
		}
		h.Ok(name + " complete")
		return nil
	}, nil
}

// sortInlineKeys returns m's keys sorted, for deterministic target-picker
// display order given a map[string]map[string]any (inline target
// subtables) rather than sortedStringKeys' map[string]any.
func sortInlineKeys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
