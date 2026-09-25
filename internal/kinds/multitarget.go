package kinds

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/guards"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// multiTargetReserved are the top-level keys on a kind="multi-target" action
// that are never themselves a target name — everything else whose value is
// a table is an inline target subtable (admin_lib/validate.py's
// inline_targets: "keys whose value is a dict and not a reserved action
// key"). "urls"/"footer" joined this set with admin-3rgp.11 and "requires"
// with admin-3rgp.10 — still narrower than Python's full VALID_ACTION_KEYS,
// because "keys" (admin-3rgp.18) hasn't landed yet and so stays usable as an
// ordinary map value on an action without being misidentified as a target
// until that ticket claims it too.
// "requires" is listed even though its value (a list, per guards.RequiresOf)
// already keeps it out of the map[string]any inline scan below — belt and
// suspenders against a malformed manifest that puts a table there instead.
var multiTargetReserved = map[string]bool{
	"kind": true, "desc": true, "targets": true, "urls": true, "footer": true, "requires": true, "detach": true,
}

// multiTargetEntry is one resolved target: either a shell command to run
// directly, or the name of another declared action to delegate to.
// requiresSet distinguishes "this target declared its own requires"
// (including an empty list — the explicit opt-out) from "no override was
// given, inherit the parent's default" — the same None-vs-[]-vs-list
// tri-state the pinned-footer urls/footer design (project CLAUDE.md) uses
// for target_urls/target_footer. detach/detachSet is the identical
// tri-state for ADR-0020's per-target `detach` override.
type multiTargetEntry struct {
	run         string
	action      string
	requires    []string
	requiresSet bool
	detach      bool
	detachSet   bool
}

// targetFrameMeta is one target's own urls/footer declaration, captured at
// build time from its inline subtable — the per-target half of
// admin_lib/interp.py::_make_multi_target's resolved_target_urls/
// resolved_target_footer. hasURLs/hasFooter distinguish "declared, even as
// an empty list / false" from "absent, inherit the parent" — a target with
// urls = [] deliberately opts OUT of an inherited footer, which is not the
// same thing as never mentioning urls at all.
type targetFrameMeta struct {
	urls      []any
	hasURLs   bool
	footer    bool
	hasFooter bool
}

// targetFrameMetaOf reads one inline target subtable's own urls/footer
// declaration off action — the single place that tri-state extraction
// happens, so makeMultiTarget's own build loop and FrameURLs (frame.go),
// which needs the identical read for a render-time, no-dispatch lookup,
// can never drift apart. Returns the zero value (inherit everything) for a
// key that isn't an inline subtable at all — a flat `targets` table entry,
// or an unknown key — matching makeMultiTarget's own "a flat-table target
// always just inherits the parent's urls/footer."
func targetFrameMetaOf(action manifest.Action, key string) targetFrameMeta {
	tbl, ok := action[key].(map[string]any)
	if !ok {
		return targetFrameMeta{}
	}
	var tm targetFrameMeta
	if u, ok := tbl["urls"].([]any); ok {
		tm.urls, tm.hasURLs = u, true
	}
	if f, ok := tbl["footer"].(bool); ok {
		tm.footer, tm.hasFooter = f, true
	}
	return tm
}

// resolveTargetFrame applies the inherit rule a picked target's urls/footer
// follow: parent default, unless the target's own inline subtable declared
// the key at all (tm.hasURLs / tm.hasFooter) — declaring it as an empty
// list or an explicit `footer = false` is a real override, not "nothing to
// inherit". Pulled out of makeMultiTarget's closure as a pure function so
// the inherit rule itself — the thing this ticket's acceptance criteria
// actually name — is unit-testable without standing up a Host or a Runner.
func resolveTargetFrame(parentURLs []any, parentHasFooter, parentFooterVal bool, tm targetFrameMeta) (urls []any, hasFooter, footerVal bool) {
	urls, hasFooter, footerVal = parentURLs, parentHasFooter, parentFooterVal
	if tm.hasURLs {
		urls = tm.urls
	}
	if tm.hasFooter {
		hasFooter, footerVal = true, tm.footer
	}
	return
}

// TargetKeys returns a kind="multi-target" action's target names in the
// order the picker offers them: inline target subtables first (sorted by
// sortInlineKeys), otherwise the flat `targets` table's keys. Returns nil
// for an action that declares neither, so a caller can use a non-empty
// result as "this action has subcommands" without a second kind check.
//
// makeMultiTarget builds its own `order` from this function rather than
// re-deriving the same two sorts, so what the TUI lists under an expanded
// command and what the picker offers cannot drift apart.
func TargetKeys(action manifest.Action) []string {
	inline := map[string]map[string]any{}
	for k, v := range action {
		if multiTargetReserved[k] {
			continue
		}
		if tbl, ok := v.(map[string]any); ok {
			inline[k] = tbl
		}
	}
	if len(inline) > 0 {
		return sortInlineKeys(inline)
	}
	if flat, ok := action["targets"].(map[string]any); ok {
		return sortedStringKeys(flat)
	}
	return nil
}

// TargetSummary returns what one target of a multi-target action actually
// does: its `run` shell command, or "@<action>" when it delegates to
// another declared action. Empty for a key that is neither (a malformed
// target — makeMultiTarget is where that becomes an error; a display caller
// just shows nothing rather than refusing to draw the row).
func TargetSummary(action manifest.Action, key string) string {
	if tbl, ok := action[key].(map[string]any); ok && !multiTargetReserved[key] {
		if s, ok := tbl["run"].(string); ok {
			return s
		}
		if s, ok := tbl["action"].(string); ok {
			return "@" + s
		}
		return ""
	}
	if flat, ok := action["targets"].(map[string]any); ok {
		if s, ok := flat[key].(string); ok {
			return s
		}
	}
	return ""
}

// TargetDescription is the line a display caller shows beside a target's key:
// the target's own `desc` when it declares one, else — for a target that
// delegates — the delegated action's `desc`, else what TargetSummary says the
// target runs. A delegating target with no description anywhere shows the
// delegated action's own `run` rather than its bare "@name", which restates
// the row's key.
func TargetDescription(m *manifest.Manifest, action manifest.Action, key string) string {
	if tbl, ok := action[key].(map[string]any); ok && !multiTargetReserved[key] {
		if s, ok := tbl["desc"].(string); ok && s != "" {
			return s
		}
	}
	summary := TargetSummary(action, key)
	name, delegates := strings.CutPrefix(summary, "@")
	if !delegates || m == nil {
		return summary
	}
	inner, ok := m.Actions[name]
	if !ok {
		return summary
	}
	if s, ok := actionString(inner, "desc"); ok && s != "" {
		return s
	}
	if s, ok := actionString(inner, "run"); ok && s != "" {
		return s
	}
	return summary
}

// makeMultiTarget is kind="multi-target"'s factory — the Go port of
// admin_lib/interp.py::_make_multi_target, with BOTH per-target inheritance
// mechanisms:
//   - urls/footer (admin-3rgp.11): parent default, child override, child
//     opt-out with an empty list or an explicit `footer = false`.
//   - requires (admin-3rgp.10): an inline target subtable may carry its own
//     `requires`, which replaces (never merges with) the action-level default
//     — ADR-0005's "multi-target inheritance is replace-not-merge".
//
// The two are independent: each has its own tri-state (targetFrameMeta's
// hasURLs/hasFooter, multiTargetEntry's requiresSet) distinguishing "declared,
// even as empty" from "absent, inherit the parent".
//
// Both target shapes Python supports are ported: inline subtables
// ([actions.dev.cf] run = "...") and the flat `targets` table (name = "..."
// or name = "@other-action" for delegation) — the two are mutually
// exclusive, matching validate_action's "cannot mix targets with inline
// target subtables". Per-target urls/footer overrides only exist on the
// inline-subtable shape, which is the only one with room for extra keys; a
// flat-table target always just inherits the parent's urls/footer.
func makeMultiTarget(name string, action manifest.Action, m *manifest.Manifest, surf *Surface) (Runner, error) {
	desc, _ := actionString(action, "desc")
	if desc == "" {
		desc = name
	}
	parentURLs := actionURLs(action)
	parentFooterVal, parentHasFooter := actionBool(action, "footer")

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
	frameMeta := map[string]targetFrameMeta{}
	order := TargetKeys(action)

	if len(inline) > 0 {
		for _, k := range order {
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
			if dv, present := tbl["detach"]; present {
				b, ok := dv.(bool)
				if !ok {
					return nil, fmt.Errorf(
						"action %q: target %q: detach must be a boolean", name, k)
				}
				entry.detach, entry.detachSet = b, true
			}
			targets[k] = entry
			frameMeta[k] = targetFrameMetaOf(action, k)
		}
	} else if hasFlat {
		for _, k := range order {
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
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf(`action %q: kind="multi-target" needs at least one target`, name)
	}

	return func(args []string) error {
		h := newHost(m)

		picked, rest := splitTargetArg(args)

		if picked == "" {
			if len(order) == 1 {
				picked = order[0]
				h.Info(fmt.Sprintf("%s: %s is this project's only target", name, picked))
			} else {
				entries := make([]luahost.TargetEntry, 0, len(order))
				for _, k := range order {
					entries = append(entries, luahost.TargetEntry{Key: k, Description: k})
				}
				key, outcome := h.PickTarget(entries, desc+" — select target")
				switch outcome {
				case luahost.PickCancelled:
					// The user answered the question by dismissing the picker.
					// Nothing failed, so say so the way `admin prune` does and
					// exit clean rather than reporting a missing argument.
					fmt.Fprintln(os.Stderr, "cancelled")
					return nil
				case luahost.PickUnavailable:
					// PickTarget already printed why it could not prompt.
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
		h.SetLogContext([]string{name, picked}, luahost.RetainUnset)

		// Guards run BEFORE the frame goes up. A guard is a precondition that
		// decides whether the target runs at all, so raising a DECSTBM scroll
		// region and pinning a footer only to tear both down when a guard
		// refuses is strictly worse than checking first — and a guard's
		// diagnostic reads cleanly on an unframed terminal. This ordering is
		// the one real judgment call in merging admin-3rgp.10 with .11; the
		// two tickets were written independently and neither states it.
		effectiveRequires := parentRequires
		if t.requiresSet {
			effectiveRequires = t.requires
		}
		if err := guards.RunRequires(effectiveRequires); err != nil {
			return fmt.Errorf("action %q: target %q: %w", name, picked, err)
		}

		// The footer belongs to the target the user picked, whether that
		// target runs a shell string itself or delegates to another
		// action.
		urlsForTarget, hasFooterForTarget, footerForTarget := resolveTargetFrame(
			parentURLs, parentHasFooter, parentFooterVal, frameMeta[picked])
		f := enterFrame(h, fmt.Sprintf("%s:%s", name, picked), urlsForTarget, hasFooterForTarget, footerForTarget, m)
		defer frame.Deactivate(f)

		if t.action != "" {
			if surf == nil {
				return fmt.Errorf(
					"action %q: target %q delegates to action %q, which requires dispatching "+
						"through kinds.RunAction/RunCommand rather than calling this factory's "+
						"Runner directly", name, picked, t.action)
			}
			return surf.action(t.action, rest)
		}

		expanded, err := manifest.ResolveEnv(t.run)
		if err != nil {
			return fmt.Errorf("action %q: target %q: %w", name, picked, err)
		}
		run := strings.TrimSpace(expanded)
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

// splitTargetArg parses a multi-target dispatch's argv the one way this
// package recognizes: args[0] names the target unless it looks like a flag
// (starts with '-'), in which case no target was named at all and every arg
// is passed through as rest. This is the canonical definition — the Runner
// above and dispatch.go's dispatchDetach (which must know which target a
// launcher decision resolves against before the Runner ever runs) both call
// it, so "which arg names the target" cannot drift into two answers the way
// the detach resolution itself once did (admin-ysxr.4).
func splitTargetArg(args []string) (picked string, rest []string) {
	rest = args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		picked, rest = args[0], args[1:]
	}
	return picked, rest
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
