// Package remotehost is the Go port of the slice of admin_lib/remote.py that
// is reachable through a registered action kind — IsLocalHost and ScpTo,
// which kind="install-template" (admin_lib/unraid.py's
// install_docker_template_from_config) needs to sync a template file to an
// Unraid host's Docker GUI.
//
// admin_lib/remote.py's other helpers (ssh_cmd, require_host,
// make_systemd_unit, systemd_user_install/status) and admin_lib/docker.py's
// remote-deploy pipeline (docker_deploy, remote_docker_build,
// remote_recreate_container, remote_logs, remote_container_diff) are not
// ported here: none of them is called from any kind admin_lib/interp.py
// registers (confirmed by grepping interp.py for "unraid"/"remote." —
// install_docker_template_from_config is the only hit). They were only
// reachable from a project's own kind="python" body, and kind="python" is
// gone under ADR-0010 — there is no Go home left for a project to call them
// from, so porting them now would be dead code with no caller.
package remotehost

import (
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
)

// IsLocalHost reports whether host (a bare address, or "user@address") names
// the machine this process is running on. The Go port of
// admin_lib/remote.py's _is_local_host.
//
// Primary check: the ADMIN_LOCAL env var (set by per-command env injection,
// or manually as DEPLOY_LOCAL=true → ADMIN_LOCAL=true). Secondary: comparing
// the address's resolved IPs against this machine's own interface addresses
// — a portable substitute for Python's `hostname -I` shell-out, which reads
// its own answer from ip(8)/ifconfig(8) output that isn't available (or
// meaningfully parseable) the same way across platforms; net.InterfaceAddrs
// answers the identical question ("which IPs does this machine own") without
// shelling out at all.
func IsLocalHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ADMIN_LOCAL"))) {
	case "1", "true", "yes":
		return true
	}

	addr := host
	if idx := strings.LastIndex(host, "@"); idx >= 0 {
		addr = host[idx+1:]
	}
	switch addr {
	case "localhost", "127.0.0.1", "::1":
		return true
	}

	resolved, err := net.LookupHost(addr)
	if err != nil {
		return false
	}
	local := localAddrSet()
	for _, ip := range resolved {
		if local[ip] {
			return true
		}
	}
	return false
}

// localAddrSet returns every IP address bound to a local network interface,
// as a set. Best-effort: an error midway through just yields whatever was
// already collected, matching the fail-open spirit of Python's
// `except Exception: pass` around its own hostname -I fallback.
func localAddrSet() map[string]bool {
	out := map[string]bool{"127.0.0.1": true, "::1": true}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil {
			out[ip.String()] = true
		}
	}
	return out
}

// Reporter is the small slice of luahost.Host's output surface ScpTo needs
// (Info/Ok/Err) — a structural interface, matching internal/reload.Reporter,
// so this package doesn't import internal/luahost just to call three
// methods *luahost.Host already has.
type Reporter interface {
	Info(msg string)
	Ok(msg string)
	Err(msg string)
}

// ScpTo copies the local file src to dest on host — a plain os-level copy
// when host is this machine, an actual `scp` invocation otherwise. Returns
// an error on failure (also reported through r, when non-nil) so a caller
// can decide whether that's fatal. The Go port of admin_lib/remote.py's
// scp_to.
func ScpTo(r Reporter, src, host, dest string) error {
	base := src
	if idx := strings.LastIndex(src, "/"); idx >= 0 {
		base = src[idx+1:]
	}

	if IsLocalHost(host) {
		if r != nil {
			r.Info("Copying " + base + " to " + dest + "...")
		}
		if err := copyFileLocal(src, dest); err != nil {
			if r != nil {
				r.Err("Copy failed: " + err.Error())
			}
			return err
		}
		if r != nil {
			r.Ok("Copied to " + dest)
		}
		return nil
	}

	if r != nil {
		r.Info("Copying " + base + " to " + host + ":" + dest + "...")
	}
	cmd := exec.Command("scp", src, host+":"+dest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if r != nil {
			r.Err("scp failed")
		}
		return err
	}
	if r != nil {
		r.Ok("Copied to " + host + ":" + dest)
	}
	return nil
}

// copyFileLocal copies src to dest, creating dest's parent directory if
// needed — the local-host branch of scp_to.
func copyFileLocal(src, dest string) error {
	if dir := dirOf(dest); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func dirOf(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return ""
	}
	return p[:idx]
}
