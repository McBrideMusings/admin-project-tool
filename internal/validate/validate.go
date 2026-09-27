// Package validate is the ADR-0008 umbrella for manifest/action schema
// errors, centralizing every "is this command/action well-formed" check so
// the pipeline sites that need them (manifest parsing, archetype resolution)
// delegate to one place, and wraps every failure as the single *Error type
// so a caller can catch one umbrella type regardless of which phase raised
// it.
//
// internal/kinds imports internal/manifest, so a validate package that
// itself imported internal/kinds (for the kind catalog) could not also be
// imported by internal/manifest without a cycle. This package therefore
// stays dependency-free apart from one ADR-0018-decided exception —
// internal/keymap, a leaf package that imports nothing internal itself, so
// no cycle risk exists in importing it here — every check here otherwise
// takes plain data (a []string of leftover keys, a raw `any` footer value)
// rather than a *manifest.Manifest or *manifest.Command, and the packages
// that already own kind/urls knowledge (internal/frame, internal/guards)
// wrap their own findings as *Error via Wrap instead of importing this
// package's checks. What every validation failure shares, whichever
// package raised it, is the *Error type — so cmd/check.go (or any future
// caller) can tell a validation failure apart from an unrelated error with
// errors.As(err, &validationErr).
package validate

import (
	"fmt"
	"sort"
	"strings"
)

// Error is the umbrella validation-error type. Every schema violation this
// package or a package that wraps into it (internal/frame's urls/footer
// checks) produces is one of these — never a bare fmt.Errorf — so a caller
// can distinguish "the manifest is malformed" from any other failure via
// errors.As. Combine several with the standard library's errors.Join: it
// implements Unwrap() []error, so errors.As still finds each *Error inside
// a joined result.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

// Errorf builds an *Error, mirroring fmt.Errorf.
func Errorf(format string, args ...any) *Error {
	return &Error{msg: fmt.Sprintf(format, args...)}
}

// Wrap marks err as a validation error, preserving its message verbatim. A
// nil err wraps to nil, and re-wrapping an existing *Error is a no-op — so
// a caller can unconditionally Wrap() every error it collects without
// double-tagging ones that already came from this package.
func Wrap(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	return &Error{msg: err.Error()}
}

// knownCommandKeys is ADR-0003's pure-routing key set. Kept here (rather
// than exported from internal/manifest, which builds its own copy for the
// type-checking toCommand does while parsing) so this package's
// CommandRouting stays the single description of the *rule*; the two lists
// must be kept in sync by hand.
var knownCommandKeys = map[string]bool{
	"desc": true, "steps": true, "log_file": true, "log_retain": true,
	"log": true, "group": true, "priority": true, "env": true,
}

// CommandRouting is ADR-0003 / design principle 2 as an error: a
// [commands.X] table may carry only desc/steps (+ log overrides, group,
// priority, env) — kind, run and targets belong on [actions.X]. extraKeys
// is whatever the caller found outside that known set (in practice,
// internal/manifest.Command.Raw's keys after parsing — anything toCommand
// didn't recognize lands there); hasSteps is whether the command ended up
// with any steps at all once its own overrides/archetype base are merged.
// lead names the offending command ("command \"deploy\"") so the message
// names both the key and the rule, as this ticket's acceptance criteria
// require. Returns nil when extraKeys is empty and hasSteps is true.
func CommandRouting(lead string, extraKeys []string, hasSteps bool) *Error {
	var problems []string
	if len(extraKeys) > 0 {
		bad := append([]string(nil), extraKeys...)
		sort.Strings(bad)
		hint := ""
		for _, k := range bad {
			if k == "kind" || k == "run" || k == "targets" {
				hint = " Move kind/run/targets to [actions]."
				break
			}
		}
		problems = append(problems, fmt.Sprintf(
			"%s: invalid key(s) %s — ADR-0003: commands are pure routing "+
				"(only 'desc', 'steps', and log overrides — log/log_file/log_retain/group/priority/env — are allowed).%s",
			lead, strings.Join(bad, ", "), hint))
	}
	if !hasSteps {
		problems = append(problems, fmt.Sprintf(
			"%s: requires 'steps' (a list of action names) — a command with no steps does nothing",
			lead))
	}
	if len(problems) == 0 {
		return nil
	}
	return &Error{msg: strings.Join(problems, "\n")}
}

// KnownCommandKeys exposes the pure-routing key set for a caller (a test,
// or a future site that wants to check membership directly) without
// duplicating the literal list a third time. Not consulted by
// CommandRouting itself, which takes the leftover keys pre-computed.
func KnownCommandKeys() map[string]bool {
	out := make(map[string]bool, len(knownCommandKeys))
	for k := range knownCommandKeys {
		out[k] = true
	}
	return out
}

// KeyBinding is one entry in an interactive-shell/bridge-listen action's
// `keys` table, normalized by ParseKeys. Exactly one of
// Run/Action/Signal is set (ParseKeys enforces that); PidFile is set
// together with Signal, never alone.
type KeyBinding struct {
	Label   string
	Run     string
	Action  string
	Signal  string
	PidFile string
}

// validKeyBindingFields is the set of fields a key binding may declare.
var validKeyBindingFields = map[string]bool{
	"label": true, "run": true, "action": true, "signal": true, "pid_file": true,
}

// ParseKeys validates the raw `keys` table on an interactive-shell/
// bridge-listen action and returns a normalized copy keyed by the single
// keychar. raw is whatever TOML decoded action["keys"] into (map[string]any, each value
// itself a map[string]any) — nil/absent is not this function's job to
// distinguish from "declared empty"; a caller with an absent `keys` key
// should skip calling ParseKeys entirely and use a nil map. lead names the
// action ("action \"dev\"") so every message names both the key and the
// action it came from.
func ParseKeys(raw any, lead string) (map[string]KeyBinding, *Error) {
	rawMap, ok := raw.(map[string]any)
	if !ok {
		return nil, Errorf("%s: 'keys' must be a table", lead)
	}
	out := make(map[string]KeyBinding, len(rawMap))
	for keychar, rawBinding := range rawMap {
		if len([]rune(keychar)) != 1 {
			return nil, Errorf("%s: key %q must be a single character", lead, keychar)
		}
		if keychar == "\x03" || keychar == "\x04" {
			return nil, Errorf("%s: key %q is reserved (Ctrl-C / Ctrl-D)", lead, keychar)
		}
		binding, ok := rawBinding.(map[string]any)
		if !ok {
			return nil, Errorf("%s: keys.%s must be a table", lead, keychar)
		}
		var unknown []string
		for k := range binding {
			if !validKeyBindingFields[k] {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return nil, Errorf("%s: keys.%s has unknown fields %s, valid: [action label pid_file run signal]",
				lead, keychar, unknown)
		}
		var targets []string
		for _, f := range []string{"run", "action", "signal"} {
			if _, ok := binding[f]; ok {
				targets = append(targets, f)
			}
		}
		if len(targets) == 0 {
			return nil, Errorf("%s: keys.%s must have one of 'run', 'action', or 'signal'", lead, keychar)
		}
		if len(targets) > 1 {
			sort.Strings(targets)
			return nil, Errorf("%s: keys.%s has conflicting targets %s — pick one", lead, keychar, targets)
		}
		_, hasSignal := binding["signal"]
		_, hasPidFile := binding["pid_file"]
		if hasSignal && !hasPidFile {
			return nil, Errorf("%s: keys.%s has 'signal' but no 'pid_file'", lead, keychar)
		}
		if hasPidFile && !hasSignal {
			return nil, Errorf("%s: keys.%s has 'pid_file' but no 'signal'", lead, keychar)
		}
		kb := KeyBinding{}
		strField := func(name string, dst *string) *Error {
			v, ok := binding[name]
			if !ok {
				return nil
			}
			s, ok := v.(string)
			if !ok {
				return Errorf("%s: keys.%s.%s must be a string", lead, keychar, name)
			}
			*dst = s
			return nil
		}
		for _, f := range []struct {
			name string
			dst  *string
		}{
			{"label", &kb.Label}, {"run", &kb.Run}, {"signal", &kb.Signal}, {"pid_file", &kb.PidFile},
		} {
			if err := strField(f.name, f.dst); err != nil {
				return nil, err
			}
		}
		if err := strField("action", &kb.Action); err != nil {
			return nil, err
		}
		kb.Action = strings.TrimPrefix(kb.Action, "@")
		out[keychar] = kb
	}
	return out, nil
}

// Footer rejects a `footer` value that is present but not a plain bool.
// TOML has no truthy strings, so `footer = "true"` is a typo worth telling
// the author about rather than silently accepted as true (or, worse,
// silently treated as absent) by a loose cast. lead names the action or
// target the value came from ("action \"dev\"" or "action \"dev\" target
// \"cf\""). Returns nil when value is exactly a Go bool.
func Footer(lead string, value any) *Error {
	if _, ok := value.(bool); ok {
		return nil
	}
	return Errorf(
		"%s: 'footer' must be true or false (got %#v) — true pins the footer even with no urls to show",
		lead, value)
}
