package kinds

import (
	"fmt"
	"sort"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// actionDetach reads action's own top-level `detach` key: val is only
// meaningful when present is true. Built on actionBool, which already
// returns this same (val, present) tri-state — present is false both when
// the key is absent and when it holds a non-bool value (ValidateDetach is
// what turns the latter into a real check error; this helper only decides
// routing).
func actionDetach(action manifest.Action) (val, present bool) {
	return actionBool(action, "detach")
}

// targetDetachOf reads one inline multi-target target subtable's own
// `detach` declaration off action[key] — mirroring targetFrameMetaOf's
// single-purpose extraction so makeMultiTarget's build loop and
// ValidateDetach read the identical shape. Returns present=false for a key
// that isn't an inline subtable at all (a flat `targets` table entry, or an
// unknown key) — matching resolveTargetFrame's "a flat-table target always
// just inherits the parent."
func targetDetachOf(action manifest.Action, key string) (val, present bool) {
	tbl, ok := action[key].(map[string]any)
	if !ok {
		return false, false
	}
	v, ok := tbl["detach"]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// resolveTargetDetach applies the inherit rule a picked target's `detach`
// follows: the parent action's own value/presence, unless the target's own
// inline subtable declared `detach` at all — declaring `detach = false` is
// a real override, not "nothing to inherit". Mirrors resolveTargetFrame's
// shape exactly (frame.go), the same None/true/false tri-state `requires`
// and `urls`/`footer` already use.
func resolveTargetDetach(parentVal, parentPresent bool, targetVal, targetPresent bool) bool {
	if targetPresent {
		return targetVal
	}
	return parentPresent && parentVal
}

// dispatchDetach resolves the effective `detach` decision for one dispatch
// of action, given the raw args a producer's launchCommand (dispatch.go) or
// RunActionRouted would hand it. This is the single place that resolution
// happens for BOTH callers, so a multi-target action's picked-target
// override can no longer be read correctly in one code path and skipped in
// another (admin-ysxr.4's regression: launchCommand read only
// actionDetach(action), which for kind="multi-target" is just the parent
// default a target can override).
//
// For any kind other than "multi-target" this is exactly actionDetach —
// there is no target to resolve, so the flat-action callers keep today's
// behaviour byte-for-byte.
//
// For kind="multi-target", the picked target is args[0] (via
// splitTargetArg, multitarget.go's own canonical parse — the same rule the
// Runner uses once it actually runs). Resolvable reports whether the
// picked target could be determined.
//
// A multi-target dispatch with NO target argument is resolvable exactly
// when the Runner's own auto-pick (multitarget.go's makeMultiTarget, near
// its `if picked == ""` branch) would resolve it deterministically too:
// the action declares exactly one target, per TargetKeys — the same
// enumeration the Runner builds its `order` from, so this can't drift into
// a second rule. Zero or more than one target means the Runner will raise
// a picker, so resolvable is false and the caller must fall through to
// running the command on its own default surface, exactly as if `detach`
// were absent.
//
// TargetKeys enumerates a flat `targets` table too (when there are no
// inline subtables), so a flat table with exactly one entry auto-picks the
// same key the Runner will and is resolvable here as well — its
// targetDetachOf present=false correctly means "inherit the parent",
// because a flat entry never carries its own detach override (only an
// inline subtable can). A flat table with zero or more than one entry
// stays unresolvable, same as the inline case. (Inline and flat can't both
// be declared on one action — makeMultiTarget rejects that combination at
// factory build time — so there is no ambiguity between the two shapes.)
//
// A picked name that doesn't match any declared target (a typo) resolves
// via targetDetachOf's present=false — "inherit the parent default" — same
// as an unknown inline key. That's not a second silent failure: the Runner
// performs its own `targets[picked]` lookup and returns "unknown target"
// when it actually runs, whichever surface it lands on.
func dispatchDetach(action manifest.Action, args []string) (detach, resolvable bool) {
	kind, _ := actionString(action, "kind")
	if kind != "multi-target" {
		val, present := actionDetach(action)
		return val, present
	}

	picked, _ := splitTargetArg(args)
	if picked == "" {
		order := TargetKeys(action)
		if len(order) != 1 {
			return false, false
		}
		picked = order[0]
	}

	parentVal, parentPresent := actionDetach(action)
	targetVal, targetPresent := targetDetachOf(action, picked)
	return resolveTargetDetach(parentVal, parentPresent, targetVal, targetPresent), true
}

// ValidateDetach rejects a non-bool `detach` on an action's own top level or
// on any of its inline target subtables — TOML has no truthy strings, so
// `detach = "true"` is a typo worth reporting rather than silently treating
// as unset (admin check's job, alongside kinds.ValidateWorkflows and
// validate.Terminal). Reports every offending action, not just the first.
func ValidateDetach(actions map[string]manifest.Action) error {
	names := make([]string, 0, len(actions))
	for n := range actions {
		names = append(names, n)
	}
	sort.Strings(names)

	var problems []string
	for _, name := range names {
		action := actions[name]
		if v, present := action["detach"]; present {
			if _, ok := v.(bool); !ok {
				problems = append(problems, fmt.Sprintf(
					"action %q: detach must be a boolean (got %#v)", name, v))
			}
		}
		for _, key := range sortedStringKeys(action) {
			tbl, ok := action[key].(map[string]any)
			if !ok || multiTargetReserved[key] {
				continue
			}
			v, present := tbl["detach"]
			if !present {
				continue
			}
			if _, ok := v.(bool); !ok {
				problems = append(problems, fmt.Sprintf(
					"action %q: target %q: detach must be a boolean (got %#v)", name, key, v))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	msg := problems[0]
	for _, p := range problems[1:] {
		msg += "\n" + p
	}
	return fmt.Errorf("%s", msg)
}
