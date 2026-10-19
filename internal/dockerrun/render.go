package dockerrun

import "strings"

// renderField renders one FieldRow's argv pieces from an already-normalized
// value, dispatching on Shape. Both BuildArgsFromCfg (via renderCfgField)
// and BuildArgsFromInspect (via renderInspectField) call this — see
// FieldTable's doc comment for why keeping the shape dispatch in one place
// is the point of the table.
func renderField(row FieldRow, value any) []string {
	switch row.Shape {
	case ShapeRepeated:
		items, _ := value.([]any)
		out := make([]string, 0, len(items)*2)
		for _, item := range items {
			out = append(out, row.Flag, toStr(item))
		}
		return out
	case ShapeBoolean:
		if truthy(value) {
			return []string{row.Flag}
		}
		return nil
	case ShapeKV:
		sep := row.KVSep
		if sep == "" {
			sep = "="
		}
		m, _ := value.(map[string]any)
		out := make([]string, 0, len(m)*2)
		for _, k := range sortedKeys(m) {
			v := m[k]
			if row.KVOmitSepIfEmpty && !truthy(v) {
				out = append(out, row.Flag, k)
			} else {
				out = append(out, row.Flag, k+sep+toStr(v))
			}
		}
		return out
	default: // ShapeSingle
		if truthy(value) {
			return []string{row.Flag, toStr(value)}
		}
		return nil
	}
}

func renderCfgField(row FieldRow, cfg map[string]any, envVars map[string]string) []string {
	value := cfg[row.Key]
	if truthy(value) && row.Expand {
		expanded, allResolved := ExpandEnvRefs(toStr(value), envVars)
		if !allResolved {
			return nil
		}
		value = expanded
	}
	return renderField(row, value)
}

func readInspectField(row FieldRow, hostCfg, config, netSettings map[string]any) any {
	if row.Reader != nil {
		return row.Reader(hostCfg, config, netSettings)
	}
	section := config
	if row.Section == "host" {
		section = hostCfg
	}
	return section[row.InspectKey]
}

func renderInspectField(row FieldRow, raw any) []string {
	if row.InspectTransform != nil {
		return renderField(row, row.InspectTransform(raw))
	}
	return renderField(row, raw)
}

func mountFlag(host, container, mode string) string {
	if mode != "" && mode != "rw" {
		return host + ":" + container + ":" + mode
	}
	return host + ":" + container
}

// Warner receives every warning BuildArgsFromCfg/BuildArgsFromInspect would
// otherwise have swallowed — the caller (internal/luahost's binding) routes
// it to h.Warn so a Lua body sees the identical diagnostic text.
type Warner func(string)

func noopWarn(string) {}

// BuildArgsFromCfg renders argv pieces (no `docker run -d --name X` prefix,
// no image) from a [docker_run] config table, reused by the docker
// archetype's run-args, deploy, and diff actions so the renderer has exactly
// one implementation (issue #388's own lesson about the two directions
// drifting apart applies just as much to "two languages implementing the
// same direction").
//
// envVars: nil means read the admin process's own environment (matches
// os.environ default). envFileVars: nil means "no env_file merge" — the
// caller decides whether/how to read one (BuildArgsFromCfg stays pure).
func BuildArgsFromCfg(cfg map[string]any, envVars map[string]string, envFileVars map[string]string, warn Warner) []string {
	if warn == nil {
		warn = noopWarn
	}
	var args []string

	for _, raw := range getSlice(cfg, "bind_mounts") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		host := getString(m, "host")
		container := getString(m, "container")
		if host == "" || container == "" {
			continue
		}
		if strings.Contains(host, "${") {
			expanded, allOk := ExpandEnvRefs(host, envVars)
			if !allOk {
				warn("docker_run.bind_mounts: host '" + host + "' references unset env var — skipping")
				continue
			}
			host = expanded
		}
		args = append(args, "-v", mountFlag(host, container, getString(m, "mode")))
	}

	for _, raw := range getSlice(cfg, "optional_mounts") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		envName := getString(m, "env")
		container := getString(m, "container")
		if envName == "" || container == "" {
			continue
		}
		host, _ := envLookup(envVars, envName)
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		args = append(args, "-v", mountFlag(host, container, getString(m, "mode")))
	}

	for _, raw := range getSlice(cfg, "anonymous_volumes") {
		var path string
		switch v := raw.(type) {
		case string:
			path = v
		case map[string]any:
			path = getString(v, "path")
		}
		if path != "" {
			args = append(args, "-v", path)
		}
	}

	for _, raw := range getSlice(cfg, "ports") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		hostPort, hasHost := m["host"]
		if !hasHost || hostPort == nil {
			continue
		}
		contPort := m["container"]
		if contPort == nil {
			contPort = hostPort
		}
		hostStr := toStr(hostPort)
		if strings.Contains(hostStr, "${") {
			expanded, allOk := ExpandEnvRefs(hostStr, envVars)
			if !allOk {
				warn("docker_run.ports: host '" + hostStr + "' references unset env var — skipping")
				continue
			}
			hostStr = expanded
		}
		hostIP := ""
		if idx := strings.LastIndex(hostStr, ":"); idx >= 0 {
			hostIP = hostStr[:idx]
			hostStr = hostStr[idx+1:]
		}
		var spec string
		if hostIP != "" {
			spec = hostIP + ":" + hostStr + ":" + toStr(contPort) + "/tcp"
		} else {
			spec = hostStr + ":" + toStr(contPort) + "/tcp"
		}
		args = append(args, "-p", spec)
	}

	seenEnvNames := map[string]bool{}
	for _, raw := range getSlice(cfg, "env") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := getString(m, "name")
		if name == "" {
			continue
		}
		seenEnvNames[name] = true
		value, hasValue := m["value"]
		if !hasValue || value == nil {
			resolved, ok := envLookup(envVars, name)
			if !ok {
				warn("docker_run.env: '" + name + "' has no value and is not in process env — skipping")
				continue
			}
			args = append(args, "-e", name+"="+resolved)
			continue
		}
		resolved, allOk := ExpandEnvRefs(toStr(value), envVars)
		if !allOk {
			warn("docker_run.env: " + name + " value '" + toStr(value) + "' references unset env var — skipping")
			continue
		}
		args = append(args, "-e", name+"="+resolved)
	}

	for _, name := range sortedStringKeys(envFileVars) {
		if seenEnvNames[name] {
			continue
		}
		args = append(args, "-e", name+"="+envFileVars[name])
	}

	if restart := getString(cfg, "restart"); restart != "" && restart != "no" {
		args = append(args, "--restart", restart)
	}
	if network := getString(cfg, "network"); network != "" && network != "default" {
		args = append(args, "--network", network)
	}

	for _, row := range FieldTable {
		args = append(args, renderCfgField(row, cfg, envVars)...)
	}

	for _, raw := range getSlice(cfg, "devices") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		host := getString(m, "host")
		if host == "" {
			continue
		}
		container := getString(m, "container")
		if container == "" {
			container = host
		}
		perms := getString(m, "permissions")
		if perms == "" {
			perms = "rwm"
		}
		spec := host + ":" + container
		if perms != "rwm" {
			spec += ":" + perms
		}
		args = append(args, "--device", spec)
	}

	switch gpu := cfg["gpu"].(type) {
	case bool:
		if gpu {
			args = append(args, "--gpus", "all")
		}
	case string:
		expanded, allOk := ExpandEnvRefs(gpu, envVars)
		expanded = strings.TrimSpace(expanded)
		switch {
		case !allOk || (expanded == "" && gpu != ""):
			warn("docker_run.gpu: '" + gpu + "' references unset or empty env var — skipping --gpus")
		case expanded == "all" || expanded == "true":
			args = append(args, "--gpus", "all")
		case expanded != "":
			args = append(args, "--gpus", expanded)
		}
	case []any:
		if len(gpu) > 0 {
			parts := make([]string, len(gpu))
			for i, g := range gpu {
				parts[i] = toStr(g)
			}
			args = append(args, "--gpus", strings.Join(parts, ","))
		}
	}

	for _, raw := range getSlice(cfg, "labels") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key := getString(m, "key")
		if key == "" {
			continue
		}
		value := ""
		if v, ok := m["value"]; ok && truthy(v) {
			expanded, allOk := ExpandEnvRefs(toStr(v), envVars)
			if !allOk {
				warn("docker_run.labels: " + key + " value '" + toStr(v) + "' references unset env var — skipping")
				continue
			}
			value = expanded
		}
		args = append(args, "--label", key+"="+value)
	}

	return args
}

// BuildArgsFromInspect translates `docker inspect` JSON (single-container,
// already decoded via json.decode) into argv pieces reproducing the
// container's runtime config. Field-table rows are
// read via the same FieldTable BuildArgsFromCfg renders from, so the live
// side can never carry a field the declared side has no way to express
// (issue #388).
func BuildArgsFromInspect(inspect map[string]any) []string {
	hostCfg := getMap(inspect, "HostConfig")
	config := getMap(inspect, "Config")
	netSettings := getMap(inspect, "NetworkSettings")
	var args []string

	for _, raw := range getSlice(hostCfg, "Binds") {
		if bind, ok := raw.(string); ok {
			args = append(args, "-v", bind)
		}
	}

	portBindings := getMap(hostCfg, "PortBindings")
	for _, cport := range sortedKeys(portBindings) {
		raw := portBindings[cport]
		bindings, _ := raw.([]any)
		for _, br := range bindings {
			b, ok := br.(map[string]any)
			if !ok {
				continue
			}
			hostIP := getString(b, "HostIp")
			hostPort := getString(b, "HostPort")
			var spec string
			if hostIP != "" {
				spec = hostIP + ":" + hostPort + ":" + cport
			} else {
				spec = hostPort + ":" + cport
			}
			args = append(args, "-p", spec)
		}
	}

	for _, raw := range getSlice(config, "Env") {
		if e, ok := raw.(string); ok {
			args = append(args, "-e", e)
		}
	}

	if rp := getMap(hostCfg, "RestartPolicy"); rp != nil {
		name := getString(rp, "Name")
		if name != "" && name != "no" {
			if name == "on-failure" && truthy(rp["MaximumRetryCount"]) {
				args = append(args, "--restart", "on-failure:"+toStr(rp["MaximumRetryCount"]))
			} else {
				args = append(args, "--restart", name)
			}
		}
	}

	if netMode := getString(hostCfg, "NetworkMode"); netMode != "" && netMode != "default" {
		args = append(args, "--network", netMode)
	}

	for _, raw := range getSlice(hostCfg, "Devices") {
		d, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		pathOnHost := getString(d, "PathOnHost")
		pathInContainer := getString(d, "PathInContainer")
		perms := getString(d, "CgroupPermissions")
		spec := pathOnHost + ":" + pathInContainer
		if perms != "" && perms != "rwm" {
			spec += ":" + perms
		}
		args = append(args, "--device", spec)
	}

	for _, raw := range getSlice(hostCfg, "DeviceRequests") {
		req, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		driver := getString(req, "Driver")
		if driver != "" && driver != "nvidia" {
			continue
		}
		deviceIDs := getSlice(req, "DeviceIDs")
		count := numOrZero(req["Count"])
		if len(deviceIDs) > 0 {
			ids := make([]string, len(deviceIDs))
			for i, id := range deviceIDs {
				ids[i] = toStr(id)
			}
			args = append(args, "--gpus", "device="+strings.Join(ids, ","))
		} else if count == -1 {
			args = append(args, "--gpus", "all")
		} else if count > 0 {
			args = append(args, "--gpus", toStr(count))
		}
	}

	for _, k := range sortedKeys(getMap(config, "Labels")) {
		args = append(args, "--label", k+"="+toStr(getMap(config, "Labels")[k]))
	}

	for _, row := range FieldTable {
		raw := readInspectField(row, hostCfg, config, netSettings)
		args = append(args, renderInspectField(row, raw)...)
	}

	return args
}
