package dockerrun

import (
	"errors"
	"strings"
)

// preflightDropFlags are the run flags a preflight container must not carry:
// each one would collide with the container still running beside it (a
// published port, a fixed hostname or MAC) or outlive the check (a restart
// policy), and -i/-t would wait on a terminal nobody is attached to.
var preflightDropFlags = map[string]bool{
	"-p":            true,
	"--restart":     true,
	"--hostname":    true,
	"--mac-address": true,
	"-i":            true,
	"-t":            true,
}

// PreflightArgv returns the `docker run --rm` argv (unquoted, so a caller can
// mask it for display the way it masks the run command) that runs the
// [docker_run.preflight] table's entrypoint and args from image, or nil when
// no preflight is declared. argv is BuildArgsFromCfg's output for the same
// cfg, so the check sees exactly the mounts, env, devices, user and runtime
// the deployed container will, minus preflightDropFlags.
//
// Deploy runs it after the new image reaches the target and before the
// running container is stopped: a non-zero exit refuses the deploy with the
// old container still serving. A declared table with no entrypoint is an
// error rather than a skipped check, since a preflight that silently never
// runs is the failure it exists to prevent.
func PreflightArgv(cfg map[string]any, argv []string, image string) ([]string, error) {
	pre := getMap(cfg, "preflight")
	if pre == nil {
		return nil, nil
	}
	entrypoint := strings.TrimSpace(getString(pre, "entrypoint"))
	if entrypoint == "" {
		return nil, errors.New("[docker_run.preflight] is declared but has no entrypoint")
	}

	parts := []string{"docker", "run", "--rm", "--entrypoint", entrypoint}
	for i := 0; i < len(argv); {
		flag, value, hasValue, next := ArgvPairAt(argv, i)
		i = next
		if preflightDropFlags[flag] {
			continue
		}
		if flag == "-v" && hasValue {
			value = readOnlyMount(value)
		}
		parts = append(parts, flag)
		if hasValue {
			parts = append(parts, value)
		}
	}
	parts = append(parts, image)
	for _, a := range getSlice(pre, "args") {
		parts = append(parts, toStr(a))
	}
	return parts, nil
}

// readOnlyMount rewrites a `-v host:container[:opts]` spec to mount read-only,
// keeping every other option (`z`, `Z`, propagation). The preflight shares the
// running container's volumes, and nothing it checks has any business
// writing to them. An anonymous volume (`-v /path`) has no host side and is
// left alone.
func readOnlyMount(spec string) string {
	fields := strings.Split(spec, ":")
	switch len(fields) {
	case 2:
		return spec + ":ro"
	case 3:
		opts := []string{"ro"}
		for _, o := range strings.Split(fields[2], ",") {
			if o != "" && o != "rw" && o != "ro" {
				opts = append(opts, o)
			}
		}
		return fields[0] + ":" + fields[1] + ":" + strings.Join(opts, ",")
	default:
		return spec
	}
}

// RollbackTag is the tag deploy pins the outgoing image to before replacing
// it — image's repository with the tag `previous`. `docker load` and an
// on-host build both move image's own tag to the new build, leaving the old
// one dangling, and the post-deploy `docker image prune` deletes dangling
// images. A tagged image survives the prune, so the restore script always
// has something to start; the next deploy moves the tag on, and the image it
// leaves behind is pruned then.
func RollbackTag(image string) string {
	repo := image
	if at := strings.Index(repo, "@"); at >= 0 {
		repo = repo[:at]
	}
	slash := strings.LastIndex(repo, "/")
	if colon := strings.LastIndex(repo, ":"); colon > slash {
		repo = repo[:colon]
	}
	return repo + ":previous"
}
