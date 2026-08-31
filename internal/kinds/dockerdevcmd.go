package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// buildDockerDevCmd renders the `docker build && docker run` shell script
// from a `[docker_dev]` config table — the Go port of
// admin_lib/docker.py::_docker_dev_build_cmd. Pure (no side effects beyond
// reading cfg); the caller hands the result to reload.RunWithReload.
//
// Schema (every field optional unless noted) — identical to the Python
// original's docstring:
//
//	image_tag          string        required; build+run tag
//	build_context      string        defaults to "."
//	target             string        multi-stage build stage
//	host_root          string        base for resolving relative bind-mount
//	                                 host paths; defaults to os.Getwd()
//	container_name     string        --name flag
//	gpu                bool|string|list
//	env_file           string        path; emitted as --env-file when the
//	                                 file exists at RUN time (relative to cwd)
//	ports              []{host, container}
//	bind_mounts        []{host, container, mode?, mkdir?}
//	optional_mounts    []{env, container, mode?}
//	devices            []{host, container?}
//	anonymous_volumes  []string | []{path}
//	env                []{name, value?}
func buildDockerDevCmd(cfg map[string]any) (string, error) {
	image := cfgString(cfg, "image_tag", "")
	if image == "" {
		return "", fmt.Errorf("'image_tag' is required")
	}
	context := cfgString(cfg, "build_context", "")
	if context == "" {
		context = "."
	}
	target := cfgString(cfg, "target", "")

	hostRoot := cfgString(cfg, "host_root", "")
	if hostRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			hostRoot = cwd
		}
	}

	// DOCKER_BUILDKIT=1 + --progress=plain are non-negotiable: the legacy
	// builder suppresses per-step output when stdout isn't a TTY (never true
	// inside RunWithReload, since stdout is a pipe), and BuildKit's
	// line-buffered progress survives the pipe and threads cleanly through
	// the (future) column formatter.
	targetFlag := ""
	if target != "" {
		targetFlag = " --target=" + shellQuote(target)
	}
	buildCmd := "DOCKER_BUILDKIT=1 docker build --progress=plain" + targetFlag +
		" -t " + shellQuote(image) + " " + shellQuote(context)

	runArgv := []string{"docker", "run", "--rm"}

	if name := cfgString(cfg, "container_name", ""); name != "" {
		runArgv = append(runArgv, "--name", name)
	}

	switch gpu := cfg["gpu"].(type) {
	case bool:
		if gpu {
			runArgv = append(runArgv, "--gpus", "all")
		}
	case string:
		if gpu == "all" {
			runArgv = append(runArgv, "--gpus", "all")
		} else if gpu != "" {
			runArgv = append(runArgv, "--gpus", gpu)
		}
	case []any:
		var devices []string
		for _, d := range gpu {
			if s, ok := d.(string); ok {
				devices = append(devices, s)
			}
		}
		if len(devices) > 0 {
			runArgv = append(runArgv, "--gpus", strings.Join(devices, ","))
		}
	}

	for _, p := range asMapSlice(cfg["ports"]) {
		hostPort, ok := cfgNumberString(p, "host")
		if !ok {
			continue
		}
		contPort, ok := cfgNumberString(p, "container")
		if !ok {
			contPort = hostPort
		}
		runArgv = append(runArgv, "-p", hostPort+":"+contPort)
	}

	for _, mnt := range asMapSlice(cfg["bind_mounts"]) {
		host := cfgString(mnt, "host", "")
		container := cfgString(mnt, "container", "")
		if host == "" || container == "" {
			continue
		}
		mode := cfgString(mnt, "mode", "")
		hostResolved := host
		if !filepath.IsAbs(host) {
			hostResolved = filepath.Clean(filepath.Join(hostRoot, host))
		}
		flag := hostResolved + ":" + container
		if mode != "" {
			flag += ":" + mode
		}
		runArgv = append(runArgv, "-v", flag)
	}

	// --device flags register the device with the container's cgroup so it's
	// actually usable, not just visible — required for GPU/VAAPI passthrough
	// (a bind mount alone doesn't work).
	for _, d := range asMapSlice(cfg["devices"]) {
		host := cfgString(d, "host", "")
		if host == "" {
			continue
		}
		container := cfgString(d, "container", "")
		if container == "" {
			container = host
		}
		flag := host
		if host != container {
			flag = host + ":" + container
		}
		runArgv = append(runArgv, "--device", flag)
	}

	// Anonymous volumes — pinned in-container paths with no host source,
	// used to mask a subtree of a bind mount (e.g. node_modules) so the
	// value baked into the image wins over what the host has.
	anonVolumes, _ := cfg["anonymous_volumes"].([]any)
	for _, v := range anonVolumes {
		var path string
		switch val := v.(type) {
		case string:
			path = val
		case map[string]any:
			path = cfgString(val, "path", "")
		}
		if path != "" {
			runArgv = append(runArgv, "-v", path)
		}
	}

	// Optional mounts gated on env vars. Can't expand env vars at render
	// time (would bake host-specific paths into the shell string and freeze
	// them for the loop's lifetime), so shell conditionals re-check at each
	// invocation, exactly as the Python original does.
	var optionalMountShell []string
	for _, m := range asMapSlice(cfg["optional_mounts"]) {
		env := cfgString(m, "env", "")
		container := cfgString(m, "container", "")
		if env == "" || container == "" {
			continue
		}
		suffix := ""
		if mode := cfgString(m, "mode", ""); mode != "" {
			suffix = ":" + mode
		}
		optionalMountShell = append(optionalMountShell,
			fmt.Sprintf(`[ -n "${%s:-}" ] && set -- "$@" -v "${%s}:%s%s"`, env, env, container, suffix))
	}

	envFileShell := ""
	if envFile := cfgString(cfg, "env_file", ""); envFile != "" {
		envFileShell = fmt.Sprintf(`[ -f %s ] && set -- "$@" --env-file %s`, shellQuote(envFile), shellQuote(envFile))
	}

	for _, e := range asMapSlice(cfg["env"]) {
		name := cfgString(e, "name", "")
		if name == "" {
			continue
		}
		if val, ok := e["value"]; !ok || val == nil {
			runArgv = append(runArgv, "-e", name)
		} else {
			runArgv = append(runArgv, "-e", name+"="+cfgString(e, "value", ""))
		}
	}

	// Stitch it together. `set --` resets $@ so the optional mount/env-file
	// conditionals can accumulate into it; the final `docker run … "$@"`
	// splices them in right before the image name, since docker rejects
	// flags placed after it.
	quotedArgvNoImage := make([]string, len(runArgv))
	for i, a := range runArgv {
		quotedArgvNoImage[i] = shellQuote(a)
	}
	parts := []string{"set -e", buildCmd, "set --"}
	parts = append(parts, optionalMountShell...)
	if envFileShell != "" {
		parts = append(parts, envFileShell)
	}
	parts = append(parts, "exec "+strings.Join(quotedArgvNoImage, " ")+` "$@" `+shellQuote(image))
	return strings.Join(parts, "\n"), nil
}
