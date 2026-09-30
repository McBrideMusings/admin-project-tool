package agent

import "strings"

// SanitizeName lowercases raw and turns everything outside [a-z0-9_-] into a
// hyphen, then trims leading/trailing hyphens. An empty result becomes
// fallback, and a result not starting with a letter gets prefix prepended
// (so prefix must itself start with a letter, such as "w-"). A positive
// max truncates the result to that many characters; max <= 0 leaves it
// uncapped.
//
// It is the one sanitizer behind every name derived from free text: herdr's
// agent name ([a-z][a-z0-9_-]{0,31}, so max = 32) and a workflow target's
// live-work ID (uncapped, so two long targets that differ late stay distinct).
func SanitizeName(raw, fallback, prefix string, max int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = fallback
	}
	if name[0] < 'a' || name[0] > 'z' {
		name = prefix + name
	}
	if max > 0 && len(name) > max {
		name = name[:max]
	}
	return name
}
