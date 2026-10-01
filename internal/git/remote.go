package git

import (
	"context"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// Remote names the machine an origin URL points at. Host is the name as the URL
// writes it ("Tower", "git.example.com", "10.0.0.5"); Addr is what ssh resolves
// that name to, set only for an ssh URL whose host is an alias that resolves to
// something different.
type Remote struct {
	Host string
	Addr string
}

// ParseRemoteHost returns the host an origin URL names and whether the URL
// reaches it over ssh. A local path or file:// URL has no host and returns "".
func ParseRemoteHost(raw string) (host string, ssh bool) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", false
		}
		switch u.Scheme {
		case "ssh", "git+ssh", "ssh+git":
			return u.Hostname(), true
		case "file":
			return "", false
		}
		return u.Hostname(), false
	}
	// scp-style: [user@]host:path. A colon after a slash is part of a local path.
	colon := strings.Index(raw, ":")
	if colon <= 0 || strings.Contains(raw[:colon], "/") {
		return "", false
	}
	h := raw[:colon]
	if at := strings.LastIndex(h, "@"); at >= 0 {
		h = h[at+1:]
	}
	h = strings.Trim(h, "[]")
	return h, h != ""
}

// OriginRemote reads dir's origin URL and, for an ssh remote, asks ssh what its
// host resolves to. `ssh -G` reads ~/.ssh/config and prints the effective
// configuration without connecting. A repo with no origin, or one whose origin
// is a local path, returns the zero Remote.
func OriginRemote(ctx context.Context, dir string) Remote {
	raw, err := runner(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return Remote{}
	}
	host, viaSSH := ParseRemoteHost(raw)
	if host == "" {
		return Remote{}
	}
	r := Remote{Host: host}
	if viaSSH {
		r.Addr = resolveSSHHost(ctx, host)
	}
	return r
}

// resolveSSHHost returns the hostname `ssh -G host` reports, or "" when it
// cannot run, times out, or reports the same name it was given.
func resolveSSHHost(ctx context.Context, host string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ssh", "-G", host).Output()
	if err != nil {
		return ""
	}
	return parseSSHHostname(string(out), host)
}

// parseSSHHostname picks the `hostname` line out of `ssh -G` output, returning
// "" when it is missing or equal (case-insensitively) to host.
func parseSSHHostname(out, host string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if name, ok := strings.CutPrefix(line, "hostname "); ok {
			name = strings.TrimSpace(name)
			if strings.EqualFold(name, host) {
				return ""
			}
			return name
		}
	}
	return ""
}
