package dockerrun

import "strings"

// DeployKeys is every key [deploy] accepts — ported from
// admin_lib/kinds.py's DEPLOY_KEYS, the single source deploy_files' own
// unknown-key check and the manifest schema both read from there.
var DeployKeys = map[string]bool{
	"src": true, "dest": true, "host": true, "exclude": true,
	"delete": true, "chown": true, "post_sync": true,
}

// UnknownDeployKeys returns any [deploy] key not in DeployKeys, sorted —
// ported from admin_lib/remote.py::deploy_files' `unknown = set(cfg) -
// _DEPLOY_KEYS` check.
func UnknownDeployKeys(cfg map[string]any) []string {
	var out []string
	for _, k := range sortedKeys(cfg) {
		if !DeployKeys[k] {
			out = append(out, k)
		}
	}
	return out
}

// DeploySyncPlan is the fully-resolved plan for one [deploy] files sync —
// the Go port of admin_lib/remote.py::deploy_files composed with
// admin_lib/core.py::sync, but pure (no I/O, no ssh, no rsync execution):
// every ${VAR} expansion, the --chown flag, the --delete flag, the exclude
// flags, and the post_sync command are all rendered exactly once here, so a
// caller (the docker archetype's deploy action) cannot reach a second,
// competing implementation that silently drops chown/post_sync — the
// specific failure shape admin-9bex suspected (interp.py's generic
// `_make_sync` reads none of `[deploy]`'s chown/post_sync/host keys; if a
// docker deploy were ever routed through it instead of deploy_dispatch,
// both would silently no-op). This package is deploy_dispatch's only Go
// counterpart, so that second path does not exist to be routed through.
type DeploySyncPlan struct {
	Src         string
	Dest        string
	Target      string // dest, or "host:dest" for a remote target
	IsRemote    bool
	Host        string // "" when [deploy] has no host key
	RsyncCmd    string // set only when IsRemote
	Chown       string // "" when unset (only meaningful when IsRemote)
	PostSyncCmd string // "" when unset; already ${VAR}-expanded
	DryRun      bool
}

// BuildDeploySyncPlan resolves cfg (the [deploy] table) plus isLocal (the
// caller's own is_local_host(host) verdict — this package doesn't duplicate
// that DNS-independent, ADMIN_LOCAL-aware check a third time; see the docker
// archetype Lua body's is_local_host) into a DeploySyncPlan, or a
// non-empty list of user-facing error strings when required keys are
// missing/unknown.
func BuildDeploySyncPlan(cfg map[string]any, envVars map[string]string, isLocal bool, dryRun bool) (*DeploySyncPlan, []string) {
	if unknown := UnknownDeployKeys(cfg); len(unknown) > 0 {
		return nil, []string{"[deploy]: unknown key(s): " + strings.Join(unknown, ", ")}
	}

	src := getString(cfg, "src")
	dest := getString(cfg, "dest")
	if src == "" || dest == "" {
		return nil, []string{"[deploy] needs both 'src' (local dir) and 'dest' (path on the target)"}
	}
	src, _ = ExpandEnvRefs(src, envVars)
	dest, _ = ExpandEnvRefs(dest, envVars)

	host := getString(cfg, "host")
	if host != "" {
		host, _ = ExpandEnvRefs(host, envVars)
	}

	var target string
	switch {
	case host == "":
		target = dest
	case isLocal:
		target = dest
	default:
		target = host + ":" + dest
	}
	isRemote := isRemoteTarget(target)

	var excludes []string
	for _, raw := range getSlice(cfg, "exclude") {
		if s, ok := raw.(string); ok {
			expanded, _ := ExpandEnvRefs(s, envVars)
			excludes = append(excludes, expanded)
		}
	}

	chown := getString(cfg, "chown")
	if chown != "" {
		chown, _ = ExpandEnvRefs(chown, envVars)
	}

	plan := &DeploySyncPlan{Src: src, Dest: dest, Target: target, IsRemote: isRemote, Host: host, Chown: chown, DryRun: dryRun}

	if isRemote {
		var parts []string
		parts = append(parts, "rsync -avz")
		if truthy(cfg["delete"]) {
			parts[0] += " --delete"
		}
		if chown != "" {
			parts[0] += " --chown=" + chown
		}
		if dryRun {
			parts[0] += " --dry-run"
		}
		for _, e := range excludes {
			parts = append(parts, "--exclude "+shq(e))
		}
		parts = append(parts, shq(src)+"/", shq(target)+"/")
		plan.RsyncCmd = strings.Join(parts, " ")
	}

	if postSync := getString(cfg, "post_sync"); postSync != "" {
		expanded, _ := ExpandEnvRefs(postSync, envVars)
		plan.PostSyncCmd = expanded
	}

	return plan, nil
}

// isRemoteTarget mirrors admin_lib/core.py::sync's is_remote check:
// "user@host:/path" — a colon present, with an "@" somewhere before it.
func isRemoteTarget(target string) bool {
	idx := strings.Index(target, ":")
	if idx < 0 {
		return false
	}
	return strings.Contains(target[:idx], "@")
}
