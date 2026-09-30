package validate

import "fmt"

// The two words a manifest can write for `locale`, on an action, a
// multi-target target, or [terminal]. A plain shell always runs inline and
// never reads the key, so "inline" is not a value: it would change nothing.
const (
	// LocaleWindow dispatches through the project's [terminal] launcher into a
	// window (or pane, or tab) that outlives the dashboard.
	LocaleWindow = "window"
	// LocaleHosted runs on a pty the dashboard owns; it dies with the dashboard.
	LocaleHosted = "hosted"
)

// CheckLocale reports why v is not a legal `locale` value, or "" when it is.
// It is the one place the vocabulary is written: internal/kinds' action and
// target checks and Terminal below all call it.
func CheckLocale(v any) string {
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("must be %q or %q (got %#v)", LocaleWindow, LocaleHosted, v)
	}
	switch s {
	case LocaleWindow, LocaleHosted:
		return ""
	case "inline":
		return fmt.Sprintf("%q is not a value: a plain shell always runs inline. Use %q or %q",
			s, LocaleWindow, LocaleHosted)
	}
	return fmt.Sprintf("%q is unknown: use %q or %q", s, LocaleWindow, LocaleHosted)
}
