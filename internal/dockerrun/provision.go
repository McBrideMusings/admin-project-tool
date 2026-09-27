package dockerrun

import (
	"strconv"
	"strings"
)

// shq quotes s for a POSIX shell — matching the archetype Lua bodies' own
// shq, used only by this file's shell-snippet builders (ProvisionCommands,
// HealthCheckShell).
func shq(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()~&|;<>#") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ProvisionCommands returns shell commands that prepare bind-mount host
// dirs before `docker run` — one `mkdir -p` (and optional `chown`) per
// [[docker_run.bind_mounts]] entry declaring `mkdir = true` and/or
// `chown = "UID:GID"`. Pure: the caller runs each command on the host
// (locally or over ssh).
func ProvisionCommands(cfg map[string]any, envVars map[string]string, warn Warner) []string {
	if warn == nil {
		warn = noopWarn
	}
	var cmds []string
	for _, raw := range getSlice(cfg, "bind_mounts") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		host := getString(m, "host")
		wantChown := getString(m, "chown")
		wantMkdir := truthy(m["mkdir"])
		if host == "" || (!wantMkdir && wantChown == "") {
			continue
		}
		if strings.Contains(host, "${") {
			expanded, allOk := ExpandEnvRefs(host, envVars)
			if !allOk {
				warn("docker_run.bind_mounts: host '" + host + "' references unset env var — skipping provision")
				continue
			}
			host = expanded
		}
		hq := shq(host)
		parts := []string{"mkdir -p " + hq}
		if wantChown != "" {
			parts = append(parts, "chown "+shq(wantChown)+" "+hq)
		}
		cmds = append(cmds, strings.Join(parts, " && "))
	}
	return cmds
}

// HealthCheckShell returns a shell snippet that polls the container's
// health URL until it returns the expected HTTP status or times out, or ""
// when no [docker_run.health] table is declared (or its url references an
// unset var).
func HealthCheckShell(cfg map[string]any, envVars map[string]string, warn Warner) string {
	if warn == nil {
		warn = noopWarn
	}
	health := getMap(cfg, "health")
	if health == nil {
		return ""
	}
	url := getString(health, "url")
	if url == "" {
		return ""
	}
	if strings.Contains(url, "${") {
		expanded, allOk := ExpandEnvRefs(url, envVars)
		if !allOk {
			warn("docker_run.health: url '" + url + "' references unset env var — skipping health check")
			return ""
		}
		url = expanded
	}
	expect := 200
	if v, ok := health["expect"]; ok {
		expect = numOrZero(v)
	}
	timeout := 30
	if v, ok := health["timeout"]; ok {
		timeout = numOrZero(v)
	}
	interval := 2
	if v, ok := health["interval"]; ok {
		interval = numOrZero(v)
	}
	if interval < 1 {
		interval = 1
	}
	tries := timeout / interval
	if tries < 1 {
		tries = 1
	}
	uq := shq(url)
	return "for _ in $(seq 1 " + strconv.Itoa(tries) + "); do " +
		"code=$(curl -s -o /dev/null -w '%{http_code}' " + uq + " 2>/dev/null || true); " +
		`[ "$code" = "` + strconv.Itoa(expect) + `" ] && exit 0; sleep ` + strconv.Itoa(interval) + "; done; exit 1"
}

// DefaultContainerName derives a sensible default container name from an
// image reference.
func DefaultContainerName(image string) string {
	if idx := strings.Index(image, ":"); idx >= 0 {
		image = image[:idx]
	}
	if idx := strings.LastIndex(image, "/"); idx >= 0 {
		image = image[idx+1:]
	}
	return image
}

// RestoreImageFromInspect returns the image reference a restore script
// should use — the running container's Image SHA when present (so the
// script stays valid even after the tag is overwritten by a new docker
// load), else image itself.
func RestoreImageFromInspect(inspect map[string]any, image string) string {
	sha := strings.TrimSpace(getString(inspect, "Image"))
	if sha != "" {
		return sha
	}
	return image
}

// ParseEnvText parses KEY=value env-file text — # comments, blank lines,
// values NOT unquoted (docker's --env-file injects the post-'=' string
// verbatim). Shared by callers reading a local env_file (already-read text)
// or a remote remote_env_file (read over ssh by the Lua caller, then parsed
// here).
func ParseEnvText(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		k := strings.TrimSpace(line[:idx])
		v := strings.TrimSpace(line[idx+1:])
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// MountEnvDeps returns the env-var names optional_mounts/bind_mounts in cfg
// would need resolved, in declaration order (names may repeat). Empty when
// neither table references an env var at all.
func MountEnvDeps(cfg map[string]any) []string {
	var names []string
	for _, raw := range getSlice(cfg, "optional_mounts") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if env := getString(m, "env"); env != "" {
			names = append(names, env)
		}
	}
	for _, raw := range getSlice(cfg, "bind_mounts") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		host := getString(m, "host")
		if strings.Contains(host, "${") {
			names = append(names, refNames(host)...)
		}
	}
	return names
}

// refNames extracts every ${NAME} / ${NAME:-default} variable name
// referenced in value, in order. Used only by MountEnvDeps, which needs the
// names without resolving them.
func refNames(value string) []string {
	var names []string
	pos := 0
	for {
		s := strings.Index(value[pos:], "${")
		if s < 0 {
			break
		}
		s += pos
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
			break
		}
		inner := value[s+2 : close]
		name := inner
		if idx := strings.Index(inner, ":-"); idx >= 0 {
			name = inner[:idx]
		}
		names = append(names, name)
		pos = close + 1
	}
	return names
}
