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
