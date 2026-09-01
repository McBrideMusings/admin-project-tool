package manifest

import (
	"sort"

	"github.com/McBrideMusings/admin-project-tool/internal/dockerrun"
)

// LogKind is one [logs.<name>] source kind's flat-field shape — a port of
// admin_lib/kinds.py's LOG_KINDS catalog. "file" is special (per-environment
// path/host/user subtables) and is parsed on its own in parseFileLog; every
// other kind is flat string fields named by Required/Optional.
type LogKind struct {
	Name     string
	Required []string
	Optional []string
}

// LogKinds is the log-source kind catalog: file (default, per-env subtables),
// command (stream a command's stdout), docker (docker logs -f), pick (choose a
// file, then tail it).
var LogKinds = map[string]LogKind{
	"file":    {Name: "file"},
	"command": {Name: "command", Required: []string{"run"}, Optional: []string{"host", "user"}},
	"docker":  {Name: "docker", Required: []string{"container"}, Optional: []string{"host", "user"}},
	"pick":    {Name: "pick", Required: []string{"glob"}},
}

// LogFlatFields is every ${VAR}-bearing flat string field across all non-file
// log kinds, sorted — the set a later env-scanning ticket walks under
// logs.<name>. Derived from LogKinds so a new kind needs no separate edit.
var LogFlatFields = func() []string {
	seen := map[string]bool{}
	for _, lk := range LogKinds {
		for _, f := range lk.Required {
			seen[f] = true
		}
		for _, f := range lk.Optional {
			seen[f] = true
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}()

// DeployKeys are the keys accepted by the [deploy] table. It is dockerrun's
// list, not a copy of it: this package validates the table and dockerrun
// renders it, and a second hand-maintained copy is how `owns` came to be
// accepted by the renderer while `admin check` still rejected it.
var DeployKeys = dockerrun.DeployKeys

// LogEnv is one file-kind log source's environment: where to tail from, and
// optionally which host/user to SSH as.
type LogEnv struct {
	Path string
	Host string
	User string
	// HasHost/HasUser distinguish "not set" from "set to empty string", matching
	// Python's None vs "" distinction on these optional fields.
	HasHost bool
	HasUser bool
}

// LogSource is one [logs.<name>] section, normalized. Kind is always set;
// Highlight is always non-nil (possibly empty). For kind "file", Envs +
// DefaultEnv are populated and Fields is empty; for every other kind, Fields
// holds the kind's flat string fields (present keys only) and Envs is empty.
type LogSource struct {
	Kind       string
	Highlight  []string
	DefaultEnv string
	HasDefault bool
	Envs       map[string]LogEnv
	Fields     map[string]string
	HasField   map[string]bool
}

// logCommonKeys are keys that carry meaning for every log kind and are never
// treated as file-kind environment names — the port of
// admin_lib/manifest.py::_LOG_COMMON_KEYS.
var logCommonKeys = map[string]bool{"kind": true, "highlight": true}

// parseLogs normalizes the raw [logs] table into per-source LogSource specs —
// the port of admin_lib/manifest.py::_parse_logs.
func parseLogs(raw any, source string) (map[string]LogSource, error) {
	if raw == nil {
		return map[string]LogSource{}, nil
	}
	rawTable, ok := raw.(map[string]any)
	if !ok {
		return nil, &Error{msg: source + ": 'logs' must be a table"}
	}

	result := make(map[string]LogSource, len(rawTable))
	for _, name := range sortedKeys(rawTable) {
		targetAny := rawTable[name]
		target, ok := targetAny.(map[string]any)
		if !ok {
			return nil, &Error{msg: source + ": logs." + name + " must be a table"}
		}

		kind := "file"
		if k, present := target["kind"]; present {
			ks, ok := k.(string)
			if !ok {
				ks = ""
			}
			kind = ks
		}
		if _, known := LogKinds[kind]; !known {
			return nil, &Error{msg: source + ": logs." + name + ".kind must be one of " +
				joinKindNames() + ", got '" + kind + "'"}
		}

		var spec LogSource
		if kind == "file" {
			s, err := parseFileLog(target, source, name)
			if err != nil {
				return nil, err
			}
			spec = s
		} else {
			lk := LogKinds[kind]
			spec.Fields = map[string]string{}
			spec.HasField = map[string]bool{}
			for _, f := range lk.Required {
				v, err := logReqStr(target, f, source, name)
				if err != nil {
					return nil, err
				}
				spec.Fields[f] = v
				spec.HasField[f] = true
			}
			for _, f := range lk.Optional {
				v, present, err := logOptStr(target, f, source, name)
				if err != nil {
					return nil, err
				}
				if present {
					spec.Fields[f] = v
					spec.HasField[f] = true
				}
			}
		}

		spec.Kind = kind
		highlight, err := parseHighlight(target, source, name)
		if err != nil {
			return nil, err
		}
		spec.Highlight = highlight
		result[name] = spec
	}

	return result, nil
}

// parseFileLog parses a file-kind [logs.<name>] section into its default_env
// and per-environment path/host/user — the port of
// admin_lib/manifest.py::_parse_file_log.
func parseFileLog(target map[string]any, source, name string) (LogSource, error) {
	var spec LogSource
	spec.Envs = map[string]LogEnv{}

	if v, present := target["default_env"]; present {
		s, ok := v.(string)
		if !ok {
			return spec, &Error{msg: source + ": logs." + name + ".default_env must be a string"}
		}
		spec.DefaultEnv = s
		spec.HasDefault = true
	}

	for _, envName := range sortedKeys(target) {
		if envName == "default_env" || logCommonKeys[envName] {
			continue
		}
		envVal := target[envName]
		switch v := envVal.(type) {
		case string:
			spec.Envs[envName] = LogEnv{Path: v}
		case map[string]any:
			pathAny, hasPath := v["path"]
			path, ok := pathAny.(string)
			if !hasPath || !ok {
				return spec, &Error{msg: source + ": logs." + name + "." + envName + " must have a 'path' string"}
			}
			env := LogEnv{Path: path}
			if h, present := v["host"]; present {
				hs, ok := h.(string)
				if !ok {
					return spec, &Error{msg: source + ": logs." + name + "." + envName + ".host must be a string"}
				}
				env.Host, env.HasHost = hs, true
			}
			if u, present := v["user"]; present {
				us, ok := u.(string)
				if !ok {
					return spec, &Error{msg: source + ": logs." + name + "." + envName + ".user must be a string"}
				}
				env.User, env.HasUser = us, true
			}
			spec.Envs[envName] = env
		default:
			return spec, &Error{msg: source + ": logs." + name + "." + envName + " must be a string path or table"}
		}
	}

	if len(spec.Envs) == 0 {
		return spec, &Error{msg: source + ": logs." + name + " must declare at least one environment"}
	}
	if spec.HasDefault {
		if _, ok := spec.Envs[spec.DefaultEnv]; !ok {
			return spec, &Error{msg: source + ": logs." + name + ".default_env '" + spec.DefaultEnv +
				"' not in envs " + joinEnvNames(spec.Envs)}
		}
	}

	return spec, nil
}

func logReqStr(target map[string]any, key, source, name string) (string, error) {
	v, present := target[key]
	if !present {
		return "", &Error{msg: source + ": logs." + name + "." + key + " must be a non-empty string"}
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", &Error{msg: source + ": logs." + name + "." + key + " must be a non-empty string"}
	}
	return s, nil
}

func logOptStr(target map[string]any, key, source, name string) (string, bool, error) {
	v, present := target[key]
	if !present || v == nil {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", false, &Error{msg: source + ": logs." + name + "." + key + " must be a string"}
	}
	return s, true, nil
}

func parseHighlight(target map[string]any, source, name string) ([]string, error) {
	raw, present := target["highlight"]
	if !present || raw == nil {
		return []string{}, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, &Error{msg: source + ": logs." + name + ".highlight must be a list of strings"}
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, &Error{msg: source + ": logs." + name + ".highlight must be a list of strings"}
		}
		out = append(out, s)
	}
	return out, nil
}

func joinKindNames() string {
	names := make([]string, 0, len(LogKinds))
	for n := range LogKinds {
		names = append(names, n)
	}
	sort.Strings(names)
	out := "("
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += "'" + n + "'"
	}
	return out + ")"
}

func joinEnvNames(envs map[string]LogEnv) string {
	names := make([]string, 0, len(envs))
	for n := range envs {
		names = append(names, n)
	}
	sort.Strings(names)
	out := "["
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += "'" + n + "'"
	}
	return out + "]"
}
