package manifest

import (
	"fmt"
	"os"
	"strings"
)

// ComputedVars maps a computed ${VAR} name (one not read from the plain
// environment) to the function that computes it — the Go port of
// admin_lib/core.py's COMPUTED_VARS dict, which today holds only
// {"LAN_IP": lan_ip}. Left empty here: porting the network-interface lookup
// that computes LAN_IP is a tracked gap, not a silent one — it is consumed
// by the log bridge and the web/interactive-shell kinds (admin-3rgp.14,
// .18), neither of which is in this ticket's eight transient kinds. An
// explicit environment value still wins over a computed one, exactly as in
// Python, so nothing here needs to change once LAN_IP is ported: a future
// ComputedVars["LAN_IP"] = lanIP just starts being consulted.
var ComputedVars = map[string]func() string{}

// ResolveEnv expands every ${VAR} / ${VAR:-default} in s against the process
// environment, falling back to ComputedVars and then to the reference's own
// default, and erroring on a name that resolves to none of the three. This
// is the Go port of admin_lib/core.py::resolve_env — except Python calls
// sys.exit(2) directly on an unresolved required var; the Go port returns
// the error instead so a caller (a kind factory, ultimately the dispatch
// entry point) decides how to fail rather than the library killing the
// process out from under it.
//
// A string with no "${" is returned unchanged without scanning it — matches
// Python's fast path and keeps this a no-op on the overwhelming majority of
// strings that never contain a placeholder.
func ResolveEnv(s string) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
	refs := IterEnvRefs(s)
	var b strings.Builder
	pos := 0
	for _, ref := range refs {
		b.WriteString(s[pos:ref.Start])
		if v, ok := os.LookupEnv(ref.Name); ok {
			b.WriteString(v)
		} else if fn, ok := ComputedVars[ref.Name]; ok {
			b.WriteString(fn())
		} else if ref.HasDefault {
			resolved, err := ResolveEnv(ref.Default)
			if err != nil {
				return "", err
			}
			b.WriteString(resolved)
		} else {
			return "", fmt.Errorf("required env var %s is not set", ref.Name)
		}
		pos = ref.End
	}
	b.WriteString(s[pos:])
	return b.String(), nil
}

// ResolveComputed expands ONLY ${COMPUTED_VARS} entries (e.g. ${LAN_IP}),
// leaving every other ${VAR} untouched for the shell that eventually runs
// the string to expand itself. The Go port of
// admin_lib/core.py::resolve_computed: ResolveEnv would be wrong for a
// `run` string handed to a shell, since it errors on any name it can't
// resolve — a shell-local like ${x} from `x=$(...)` would kill the action.
// The shell can expand its own variables; it cannot compute LAN_IP, so only
// that class of reference is substituted here.
//
// A reference with a default that is itself passed through unresolved keeps
// that default reconstructed as "${NAME:-default}" (recursing into the
// default so a nested computed var is still substituted), matching Python's
// reconstruction exactly rather than emitting the original source slice.
func ResolveComputed(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	refs := IterEnvRefs(s)
	var b strings.Builder
	pos := 0
	for _, ref := range refs {
		b.WriteString(s[pos:ref.Start])
		if fn, ok := ComputedVars[ref.Name]; ok {
			if v, present := os.LookupEnv(ref.Name); present {
				b.WriteString(v)
			} else {
				b.WriteString(fn())
			}
		} else if !ref.HasDefault {
			b.WriteString(s[ref.Start:ref.End])
		} else {
			b.WriteString("${" + ref.Name + ":-" + ResolveComputed(ref.Default) + "}")
		}
		pos = ref.End
	}
	b.WriteString(s[pos:])
	return b.String()
}
