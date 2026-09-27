package dockerrun

import "strings"

// MaskValue replaces a credential-looking value everywhere it's rendered
// for a human (issue #436).
const MaskValue = "••••"

var secretNameSuffixes = []string{"_TOKEN", "_KEY", "_SECRET", "_PASSWORD"}

// IsSecretEnvName reports whether name looks like it holds a credential, by
// suffix convention (*_TOKEN, *_KEY, *_SECRET, *_PASSWORD), case-insensitive.
// A name-only heuristic.
func IsSecretEnvName(name string) bool {
	if name == "" {
		return false
	}
	upper := strings.ToUpper(name)
	for _, suffix := range secretNameSuffixes {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}

// MaskEnvAssignment returns a "NAME=VALUE" env assignment with VALUE
// replaced by MaskValue when NAME looks like a credential; anything else
// (including a string with no "=") comes back unchanged.
func MaskEnvAssignment(assignment string) string {
	idx := strings.Index(assignment, "=")
	if idx < 0 {
		return assignment
	}
	name := assignment[:idx]
	if IsSecretEnvName(name) {
		return name + "=" + MaskValue
	}
	return assignment
}

// MaskArgvForDisplay returns a copy of a `docker run` argv-pieces list with
// secret-looking `-e NAME=VALUE` values replaced by MaskValue (issue #436).
// Display-only. Never call this on the argv actually passed to `docker run`
// or written to a restore script.
func MaskArgvForDisplay(argv []string) []string {
	masked := make([]string, len(argv))
	copy(masked, argv)
	for i := 0; i < len(masked)-1; i++ {
		if masked[i] == "-e" {
			masked[i+1] = MaskEnvAssignment(masked[i+1])
		}
	}
	return masked
}
