package manifest

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// lanIP returns the address another device on this network reaches this machine
// at, by asking the routing table which source address it would use to reach an
// off-link host. It does not actually send traffic — UDP has no handshake, so
// connect() only does the route lookup. The target is TEST-NET-1 (192.0.2.1),
// which is reserved and unroutable.
//
// If the primary lookup fails (no route, offline, all interfaces down), falls
// back to resolving the local hostname, and finally falls back to 127.0.0.1 if
// that fails too.
//
// This is the Go port of admin_lib/core.py::lan_ip.
func lanIP() string {
	conn, err := net.Dial("udp", "192.0.2.1:1")
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil {
			return addr.IP.String()
		}
	}

	// Fallback: resolve the local hostname
	if hostname, err := os.Hostname(); err == nil {
		if addr, err := net.LookupHost(hostname); err == nil && len(addr) > 0 {
			return addr[0]
		}
	}

	// Ultimate fallback
	return "127.0.0.1"
}

// ComputedVars maps a computed ${VAR} name (one not read from the plain
// environment) to the function that computes it — the Go port of
// admin_lib/core.py's COMPUTED_VARS dict, which holds {"LAN_IP": lanIP}.
// An explicit environment value still wins over a computed one, exactly as in
// Python, so a project can pin one when the automatic answer is wrong.
var ComputedVars = map[string]func() string{
	"LAN_IP": lanIP,
}

// ResolveEnv expands every ${VAR} / ${VAR:-default} in s against the process
// environment, falling back to ComputedVars and then to the reference's own
// default, and erroring on a name that resolves to none of the three. This
// is the Go port of admin_lib/core.py::resolve_env — except Python calls
// sys.exit(2) directly on an unresolved required var; the Go port returns
// the error instead so a caller (a kind factory, ultimately the dispatch
// entry point) decides how to fail rather than the library killing the
// process out from under it.
//
// A variable set to the empty string counts as unset, which is what POSIX ${VAR:-}
// already means and what the placeholder syntax advertises. Returning "" instead
// hands the caller a value it cannot tell from an absent key, and callers drop it:
// that is how `development_team = "${HERMES_DEV_TEAM}"` reached xcodebuild as no
// DEVELOPMENT_TEAM at all, silently downgrading a "BUILD SUCCEEDED" app to ad-hoc
// signing with every provisioning-profile entitlement stripped (oa-jsy.2). An
// explicitly written empty default (${VAR:-}) is a deliberate empty and is kept.
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
		v, isSet := os.LookupEnv(ref.Name)
		if v != "" {
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
			state := "not set"
			if isSet {
				state = "set but empty"
			}
			return "", fmt.Errorf("required env var %s is %s", ref.Name, state)
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
