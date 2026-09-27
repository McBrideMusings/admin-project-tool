package dockerrun

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// DeployKeys is every key [deploy] accepts — the single source both the
// unknown-key check and the manifest schema read from.
var DeployKeys = map[string]bool{
	"src": true, "dest": true, "host": true, "exclude": true,
	"delete": true, "chown": true, "post_sync": true, "owns": true,
}

// OwnsFilterArgs renders the rsync filter rules that restrict a sync to the
// src-relative paths in owns.
//
// An allow-list in rsync is include rules terminated by a catch-all exclude:
// the first matching rule wins, so anything the includes don't name falls
// through to `--exclude '*'`. That matters twice over, because an excluded
// path is also protected from --delete — which is what lets `delete = true`
// coexist with a destination that holds state the running app wrote.
//
// A directory entry needs both `/x` and `/x/**`, and a nested entry needs its
// ancestors named or rsync never descends far enough to reach it. src is used
// only to tell a file entry from a directory one; "" treats every entry as a
// directory.
func OwnsFilterArgs(src string, owns []string) []string {
	var rules []string
	seen := map[string]bool{}
	for _, entry := range owns {
		entry = strings.Trim(entry, "/")
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, "/")
		for i := 1; i < len(parts); i++ {
			ancestor := "/" + strings.Join(parts[:i], "/") + "/"
			if !seen[ancestor] {
				seen[ancestor] = true
				rules = append(rules, "--include "+shq(ancestor))
			}
		}
		leaf := "/" + entry
		if seen[leaf] {
			continue
		}
		seen[leaf] = true
		rules = append(rules, "--include "+shq(leaf))
		isDir := true
		if src != "" {
			if info, err := os.Stat(filepath.Join(src, entry)); err == nil {
				isDir = info.IsDir()
			}
		}
		if isDir {
			rules = append(rules, "--include "+shq(leaf+"/**"))
		}
	}
	rules = append(rules, "--exclude "+shq("*"))
	return rules
}

// validateOwns refuses an owns list that would make --delete destructive,
// returning user-facing error strings.
//
// The empty-directory case is the one that matters: mirroring an empty source
// with --delete removes everything under the corresponding destination path,
// and rsync reports that as success. A mirror directory a .gitignore keeps out
// of the repo is empty in exactly that way in a fresh clone or a git worktree.
func validateOwns(src string, owns []string) []string {
	var errs []string
	for _, entry := range owns {
		trimmed := strings.Trim(entry, "/")
		if trimmed == "" || strings.HasPrefix(entry, "/") || slices.Contains(strings.Split(trimmed, "/"), "..") {
			errs = append(errs, "[deploy] owns entry must be a relative path inside src: "+entry)
			continue
		}
		path := filepath.Join(src, trimmed)
		info, err := os.Stat(path)
		if err != nil {
			errs = append(errs, "[deploy] owns names a path that is not in src: "+trimmed+" (looked in "+src+")")
			continue
		}
		if info.IsDir() {
			entries, err := os.ReadDir(path)
			if err == nil && len(entries) == 0 {
				errs = append(errs, "[deploy] owns names an empty directory: "+trimmed+
					". Refusing to sync — with delete on, mirroring an empty source deletes"+
					" everything under the matching path at the destination. A fresh clone or a"+
					" git worktree gets an empty mirror when it is gitignored; run the deploy"+
					" from the checkout that has the real files.")
			}
		}
	}
	return errs
}

// UnknownDeployKeys returns any [deploy] key not in DeployKeys, sorted.
func UnknownDeployKeys(cfg map[string]any) []string {
	var out []string
	for _, k := range sortedKeys(cfg) {
		if !DeployKeys[k] {
			out = append(out, k)
		}
	}
	return out
}

// DeploySyncPlan is the fully-resolved plan for one [deploy] files sync,
// pure (no I/O, no ssh, no rsync execution): every ${VAR} expansion, the
// --chown flag, the --delete flag, the exclude flags, and the post_sync
// command are all rendered exactly once here, so a caller (the docker
// archetype's deploy action) cannot reach a second, competing
// implementation that silently drops chown/post_sync — the specific
// failure shape admin-9bex suspected. This package is the only
// implementation of a docker deploy sync, so that second path does not
// exist to be routed through.
type DeploySyncPlan struct {
	Src         string
	Dest        string
	Target      string // dest, or "host:dest" for a remote target
	IsRemote    bool
	Host        string // "" when [deploy] has no host key
	RsyncCmd    string // set only when IsRemote
	FilterArgs  string // rendered --exclude/--include chain, both branches
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
//
// `owns` is the one thing here that reads the filesystem: its entries have to
// exist under src, and a directory entry has to be non-empty, or a `delete`
// sync deletes rather than mirrors. That check lives inside this function
// rather than beside a caller for the reason the type comment above gives —
// a guard a second code path can skip is the failure shape this package was
// written to remove.
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

	var owns []string
	for _, raw := range getSlice(cfg, "owns") {
		if s, ok := raw.(string); ok {
			expanded, _ := ExpandEnvRefs(s, envVars)
			owns = append(owns, expanded)
		}
	}
	if errs := validateOwns(src, owns); len(errs) > 0 {
		return nil, errs
	}

	// Excludes first, then the owns includes: rsync takes the first matching
	// rule, so a carve-out inside an owned path only wins if it is emitted
	// ahead of the include that would otherwise claim it.
	var filters []string
	for _, e := range excludes {
		filters = append(filters, "--exclude "+shq(e))
	}
	if len(owns) > 0 {
		filters = append(filters, OwnsFilterArgs(src, owns)...)
	}

	plan := &DeploySyncPlan{Src: src, Dest: dest, Target: target, IsRemote: isRemote, Host: host, Chown: chown, DryRun: dryRun}
	plan.FilterArgs = strings.Join(filters, " ")

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
		parts = append(parts, filters...)
		parts = append(parts, shq(src)+"/", shq(target)+"/")
		plan.RsyncCmd = strings.Join(parts, " ")
	}

	if postSync := getString(cfg, "post_sync"); postSync != "" {
		expanded, _ := ExpandEnvRefs(postSync, envVars)
		plan.PostSyncCmd = expanded
	}

	return plan, nil
}

// isRemoteTarget checks for "user@host:/path" — a colon present, with an
// "@" somewhere before it.
func isRemoteTarget(target string) bool {
	idx := strings.Index(target, ":")
	if idx < 0 {
		return false
	}
	return strings.Contains(target[:idx], "@")
}
