package kinds

import (
	"fmt"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/service"
)

// makeLaunchd is kind="launchd"'s factory: manage an always-on user service
// from the `[launchd]` table — launchd on macOS, systemd --user on Linux, or
// either one on a remote host over SSH.
//
// admin_lib/launchd.py was deleted in 71451f2 with the reasoning that it
// "never had a kind factory in the Go runtime". Projects reached it from a
// kind="python" body, which was the dispatch shape at the time, so the
// deletion silently broke every `deploy server` in the fleet. internal/service
// is the restored implementation and this is the factory it was missing.
//
// Verbs come from the CLI, not from `run`: `admin service install`,
// `... uninstall|status|restart|stop|start`. A bare invocation prints usage.
//
// Remote: `admin service <verb> user@host` routes to the SSH path instead of
// the local one. Push needs the [go_dist] matrix, which the action reads from
// the same manifest, so a remote install is one command rather than a
// build-then-push pair the caller has to sequence by hand.
func makeLaunchd(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	raw := archetypeConfigTable(m, "launchd")
	dist := archetypeConfigTable(m, "go_dist")

	return func(args []string) error {
		if len(raw) == 0 {
			return fmt.Errorf("action %q: [launchd] table missing from admin.toml", name)
		}
		h := newHost()

		verb, host := "", ""
		for _, a := range args {
			switch {
			case a == "--uninstall":
				verb = "uninstall"
			case verb == "" && !isHostArg(a):
				verb = a
			case isHostArg(a):
				host = a
			}
		}

		if host != "" {
			if verb == "uninstall" {
				return service.UninstallRemote(raw, host, h)
			}
			distDir := cfgString(dist, "out_dir", "dist")
			binary := cfgString(dist, "binary", "")
			if binary == "" {
				return fmt.Errorf("action %q: remote deploy needs [go_dist].binary", name)
			}
			return service.DeployRemote(raw, host, manifest.ResolveComputed(distDir), binary, h)
		}

		if verb == "" && len(args) == 0 {
			return service.Run(raw, nil, h)
		}
		return service.Run(raw, []string{orDefault(verb, "install")}, h)
	}, nil
}

// isHostArg recognises the `user@host` / bare-hostname positional that routes
// a service verb to the SSH path. A bare word that is also a verb name is a
// verb; anything containing "@" or "." is a host.
func isHostArg(a string) bool {
	if a == "" || a[0] == '-' {
		return false
	}
	switch a {
	case "install", "uninstall", "status", "restart", "stop", "start":
		return false
	}
	for _, c := range a {
		if c == '@' || c == '.' {
			return true
		}
	}
	return false
}
