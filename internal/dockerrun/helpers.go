package dockerrun

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// numOrZero reads a JSON-decoded number (float64) or int, defaulting to 0
// for anything else (missing key, wrong type) — mirrors Python's
// u.get("Soft", 0).
func numOrZero(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func formatNum(n int) string {
	return strconv.Itoa(n)
}

// truthy mirrors admin_lib/docker.py's _is_trivial(v) inverted (and the Lua
// run-args body's `truthy`): nil, false, "", 0, and an empty
// list/map are all falsy; everything else — including a non-empty list/map
// — is truthy.
func truthy(v any) bool {
	switch val := v.(type) {
	case nil:
		return false
	case bool:
		return val
	case string:
		return val != ""
	case float64:
		return val != 0
	case int:
		return val != 0
	case []any:
		return len(val) > 0
	case map[string]any:
		return len(val) > 0
	default:
		return true
	}
}

// toStr renders a JSON/TOML-decoded scalar the way Python's str(value) or
// Lua's tostring(value) would for the values this package actually sees:
// strings pass through, bools render as "true"/"false" (docker flag values
// are never fed a raw Go bool otherwise), and a float64 that is a whole
// number (every TOML/JSON integer becomes float64 after the Lua round trip)
// prints without a trailing ".0" so e.g. a port number renders "8080", not
// "8080.0".
func toStr(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", val)
	}
}

func getMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	sub, _ := m[key].(map[string]any)
	return sub
}

func getSlice(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	s, _ := m[key].([]any)
	return s
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// EnvLookup resolves one env var name to (value, present). Passing a nil
// envVars map to the functions in this package means "fall back to the
// admin process's own environment" (os.LookupEnv) — the Go equivalent of
// admin_lib/docker.py defaulting env_vars=None to os.environ. A non-nil map
// (built from a parsed remote_env_file, or from process env captured by the
// caller) is consulted instead of — never merged with — process env, same
// as the Python original.
func envLookup(envVars map[string]string, name string) (string, bool) {
	if envVars == nil {
		return os.LookupEnv(name)
	}
	v, ok := envVars[name]
	return v, ok
}

// ExpandEnvRefs substitutes ${VAR} and ${VAR:-default} references in value
// from envVars (or process env when envVars is nil). Returns (substituted,
// allResolved) — allResolved=false means at least one reference had neither
// a value nor a default, so the caller should drop the entry rather than
// emit a literal ${VAR}. Ported from admin_lib/docker.py::_expand_env_refs /
// the docker archetype's Lua expand_refs — brace-matched, so a default value
// may itself contain nested ${...} references. Does not implement
// COMPUTED_VARS (e.g. ${LAN_IP}) — same simplification the existing Lua port
// already made (see docs/architecture/lua-host-api.md's run-args section):
// a computed name comes back unresolved, same as any other unset var with
// no default.
func ExpandEnvRefs(value string, envVars map[string]string) (string, bool) {
	if !strings.Contains(value, "${") {
		return value, true
	}
	var out strings.Builder
	allResolved := true
	pos := 0
	for {
		s := strings.Index(value[pos:], "${")
		if s < 0 {
			out.WriteString(value[pos:])
			break
		}
		s += pos
		out.WriteString(value[pos:s])
		depth := 1
		j := s + 2
		close := -1
		for j < len(value) {
			switch value[j] {
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
			j++
		}
		if close < 0 {
			out.WriteString(value[s:])
			pos = len(value)
			break
		}
		inner := value[s+2 : close]
		sepDepth := 0
		sep := -1
		for k := 0; k < len(inner); k++ {
			switch inner[k] {
			case '{':
				sepDepth++
			case '}':
				sepDepth--
			case ':':
				if sepDepth == 0 && k+1 < len(inner) && inner[k+1] == '-' {
					sep = k
				}
			}
			if sep >= 0 {
				break
			}
		}
		var name, defaultVal string
		hasDefault := sep >= 0
		if hasDefault {
			name = inner[:sep]
			defaultVal = inner[sep+2:]
		} else {
			name = inner
		}
		if v, ok := envLookup(envVars, name); ok {
			out.WriteString(v)
		} else if hasDefault {
			sub, subOk := ExpandEnvRefs(defaultVal, envVars)
			out.WriteString(sub)
			if !subOk {
				allResolved = false
			}
		} else {
			allResolved = false
			out.WriteString(value[s : close+1])
		}
		pos = close + 1
	}
	return out.String(), allResolved
}

// sortedKeys returns m's keys in sorted order — used everywhere this
// package needs deterministic iteration over a map[string]any decoded from
// Lua/JSON (Go map iteration order is randomized).
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
