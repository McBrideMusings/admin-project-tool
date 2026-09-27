package manifest

import (
	"regexp"
	"strings"
)

// EnvRef is one ${VAR} / ${VAR:-default} reference found in a string. Start and
// End are byte offsets into the source string; End is exclusive (the offset just
// past the closing brace).
type EnvRef struct {
	Start      int
	End        int
	Name       string
	Default    string
	HasDefault bool
}

var nameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IterEnvRefs finds every ${VAR} / ${VAR:-default} reference in s,
// brace-matched rather than regex-matched, so a default may itself contain a
// placeholder: ${GOPATH_BIN:-${HOME}/go/bin} yields one ref whose default is
// the whole ${HOME}/go/bin, not a truncated ${HOME.
//
// Anything that isn't a well-formed ${NAME} or ${NAME:-…} — an unbalanced
// brace, a shell construct like ${x%.c} — is skipped, left for whoever
// consumes the string next.
func IterEnvRefs(s string) []EnvRef {
	var refs []EnvRef
	i := 0
	for {
		rel := strings.Index(s[i:], "${")
		if rel < 0 {
			return refs
		}
		start := i + rel
		depth := 0
		close := -1
		for j := start + 1; j < len(s); j++ {
			switch s[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					close = j
				}
			}
			if close >= 0 {
				break
			}
		}
		if close < 0 {
			// Unbalanced from here on; nothing further is well-formed.
			return refs
		}
		inner := s[start+2 : close]
		sep := findDefaultSep(inner)
		var name, def string
		hasDefault := sep >= 0
		if sep < 0 {
			name = inner
		} else {
			name = inner[:sep]
			def = inner[sep+2:]
		}
		if nameRE.MatchString(name) {
			refs = append(refs, EnvRef{
				Start:      start,
				End:        close + 1,
				Name:       name,
				Default:    def,
				HasDefault: hasDefault,
			})
		}
		i = close + 1
	}
}

// findDefaultSep returns the index of the ":-" separating name from default, at
// brace depth 0 within inner.
func findDefaultSep(inner string) int {
	depth := 0
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '{':
			depth++
		case '}':
			depth--
		case ':':
			if depth == 0 && i+1 < len(inner) && inner[i+1] == '-' {
				return i
			}
		}
	}
	return -1
}
