// Package validate is the ADR-0008 umbrella for manifest/action schema
// errors — the Go counterpart of admin_lib/validate.py's `ValidationError`.
//
// Python centralizes every "is this command/action well-formed" check
// behind validate_command/validate_action_keys/validate_action so the three
// pipeline sites that used to hand-roll them (manifest.parse,
// archetypes.load_archetype, archetypes._to_command) delegate to one place,
// and raises the caller-supplied phase exception (ManifestError at parse,
// ArchetypeError at resolve) so admin-run can still catch one umbrella type,
// ValidationError.
//
// Go has no exception hierarchy to mirror that with, and — unlike Python,
// where admin_lib.kinds carries no dependency on admin_lib.manifest —
// internal/kinds imports internal/manifest, so a validate package that
// itself imported internal/kinds (for the kind catalog) could not also be
// imported by internal/manifest without a cycle. This package therefore
// stays dependency-free: every check here takes plain data (a []string of
// leftover keys, a raw `any` footer value) rather than a *manifest.Manifest
// or *manifest.Command, and the packages that already own kind/urls
// knowledge (internal/frame, internal/guards) wrap their own findings as
// *Error via Wrap instead of importing this package's checks. What every
// validation failure shares, whichever package raised it, is the *Error
// type — so cmd/check.go (or any future caller) can tell a validation
// failure apart from an unrelated error with errors.As(err, &validationErr).
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

// knownCommandKeys is ADR-0003's pure-routing key set — the Go port of
// admin_lib/validate.py::VALID_COMMAND_KEYS. Kept here (rather than
// exported from internal/manifest, which builds its own copy for the
// type-checking toCommand does while parsing) so this package's
// CommandRouting stays the single description of the *rule*; the two
// lists must be kept in sync by hand, same as every other admin_lib
// constant this project ports into two languages during the migration.
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
