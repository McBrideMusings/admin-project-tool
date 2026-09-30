package kinds

import (
	"fmt"
	"sort"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// actionLocale reads action's own top-level `locale` key: "" when the key is
// absent or holds a non-string (ValidateLocale is what turns the latter into
// a real check error; this helper only decides routing).
func actionLocale(action manifest.Action) string {
	s, _ := actionString(action, "locale")
	return s
}

// targetLocaleOf reads one inline multi-target target subtable's own
// `locale` declaration off action[key] — mirroring targetFrameMetaOf's
// single-purpose extraction so makeMultiTarget's build loop and
// ValidateLocale read the identical shape. Returns "" for a key that isn't an
// inline subtable at all (a flat `targets` table entry, or an unknown key) —
// matching resolveTargetFrame's "a flat-table target always just inherits the
// parent."
func targetLocaleOf(action manifest.Action, key string) string {
	tbl, ok := action[key].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := tbl["locale"].(string)
	return s
}

// firstLocale is the inherit rule every level of the chain follows: the most
// specific non-empty declaration wins — target, then action, then [terminal].
// "" means nothing declared at that level, never a value.
func firstLocale(levels ...string) string {
	for _, l := range levels {
		if l != "" {
			return l
		}
	}
	return ""
}

// dispatchLocale resolves the effective `locale` for one dispatch of action,
// given the raw args launchCommand (dispatch.go) would hand it — its only
// caller, reached only from the dashboard (a plain shell runs inline and never
// reads the key). This is the single place that resolution happens, so a
// multi-target action's picked-target override can no longer be read
// correctly in one code path and skipped in another (admin-ysxr.4's
// regression: launchCommand read only the action's own key, which for
// kind="multi-target" is just the parent default a target can override).
//
// A workflow action always resolves to hosted here: the action itself runs on
// the dashboard's own surface, because its Runner prints the resolved target
// set and reads a yes/no from that terminal's stdin before dispatching
// anything. Its workers take the workflow's own `locale` instead
// (workflowdispatch.go's DispatchTargets): a window launcher, or a hosted pty.
//
// For any other kind except "multi-target" this is the action's own locale,
// else terminalDefault — there is no target to resolve.
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
// running the command on its own default surface, exactly as if `locale`
// were absent.
//
// TargetKeys enumerates a flat `targets` table too (when there are no
// inline subtables), so a flat table with exactly one entry auto-picks the
// same key the Runner will and is resolvable here as well — its
// targetLocaleOf "" correctly means "inherit the parent", because a flat
// entry never carries its own locale override (only an inline subtable can).
// A flat table with zero or more than one entry stays unresolvable, same as
// the inline case. (Inline and flat can't both be declared on one action —
// makeMultiTarget rejects that combination at factory build time — so there
// is no ambiguity between the two shapes.)
//
// A picked name that doesn't match any declared target (a typo) resolves
// via targetLocaleOf's "" — "inherit the parent default" — same as an unknown
// inline key. That's not a second silent failure: the Runner performs its own
// `targets[picked]` lookup and returns "unknown target" when it actually
// runs, whichever surface it lands on.
//
// terminalDefault is [terminal].locale, the base of the chain: an action (or
// a multi-target's parent) that doesn't declare `locale` reads it, and an
// action's or target's own declaration beats it. A flat action is therefore
// always resolvable: with no declaration of its own it resolves to
// terminalDefault, not to "unresolvable". "" out means nothing declared
// anywhere, which the dashboard runs on its hosted pty.
func dispatchLocale(action manifest.Action, args []string, terminalDefault string) (locale string, resolvable bool) {
	kind, _ := actionString(action, "kind")
	switch kind {
	case "workflow":
		return validate.LocaleHosted, true
	case "multi-target":
	default:
		return firstLocale(actionLocale(action), terminalDefault), true
	}

	picked, _ := splitTargetArg(args)
	if picked == "" {
		order := TargetKeys(action)
		if len(order) != 1 {
			return "", false
		}
		picked = order[0]
	}
	return firstLocale(targetLocaleOf(action, picked), actionLocale(action), terminalDefault), true
}

// multiTargetLiveID resolves the live-work registry id for one dispatch of
// command name, whose first step is action, given the raw args
// launchCommand (dispatch.go) or StartHostedPTY (hostedpty.go) would hand
// it — the id both producers use to key their
// recordLiveWork/recordHostedPTYWork entry.
//
// For a first step that isn't a genuine multi-target MENU — a flat action,
// or a multi-target action with at most one declared target
// (TargetKeys(action) <= 1) — this is exactly name: the command identifies
// its run.
//
// For a genuine menu (TargetKeys(action) > 1) with a target resolvable from
// args (via splitTargetArg — multitarget.go's own canonical parse, the same
// rule dispatchLocale and the Runner itself use), the id becomes
// "<name>:<target>".
//
// When no target is resolvable from args (several targets declared, none
// named — the Runner will raise its own picker once it actually runs), this
// returns name unchanged: there is nothing more specific to key on yet, and
// the entry stays attributed to the parent until a target is known.
func multiTargetLiveID(name string, action manifest.Action, args []string) string {
	kind, _ := actionString(action, "kind")
	if kind != "multi-target" {
		return name
	}
	if len(TargetKeys(action)) <= 1 {
		return name
	}
	picked, _ := splitTargetArg(args)
	if picked == "" {
		return name
	}
	return TargetLiveKey(name, picked)
}

// TargetLiveKey builds the live-work registry id for one resolved target of
// a command whose first step is a multi-target menu: "<command>:<target>".
// multiTargetLiveID above is the only writer of this key shape;
// internal/tui/work.go's targetRows is the reader, and it calls this same
// function rather than rebuilding the string itself.
func TargetLiveKey(command, target string) string {
	return command + ":" + target
}

// ValidateLocale rejects a `locale` that is not "window" or "hosted" on an
// action's own top level or on any of its inline target subtables, and a
// leftover `detach` key on either — silently ignoring one would change where
// a command runs with no warning. A workflow action's workers run in windows
// only, so it accepts nothing but "window" (admin check's job, alongside
// kinds.ValidateWorkflows and validate.Terminal). Reports every offending
// action, not just the first.
func ValidateLocale(actions map[string]manifest.Action) error {
	names := make([]string, 0, len(actions))
	for n := range actions {
		names = append(names, n)
	}
	sort.Strings(names)

	var problems []string
	for _, name := range names {
		action := actions[name]
		problems = append(problems, localeProblems(fmt.Sprintf("action %q", name), action)...)
		for _, key := range sortedStringKeys(action) {
			tbl, ok := action[key].(map[string]any)
			if !ok || multiTargetReserved[key] {
				continue
			}
			problems = append(problems, localeProblems(fmt.Sprintf("action %q: target %q", name, key), tbl)...)
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

// localeProblems checks one table (an action or an inline target) for a
// `locale` outside the vocabulary and for the retired `detach` key.
func localeProblems(where string, tbl map[string]any) []string {
	var out []string
	if _, present := tbl["detach"]; present {
		out = append(out, fmt.Sprintf(
			"%s: detach is not a key — write locale = %q or %q", where, validate.LocaleWindow, validate.LocaleHosted))
	}
	v, present := tbl["locale"]
	if !present {
		return out
	}
	if why := validate.CheckLocale(v); why != "" {
		out = append(out, fmt.Sprintf("%s: locale %s", where, why))
	}
	return out
}
