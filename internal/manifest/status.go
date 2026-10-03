package manifest

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Status entry kinds. A [status.<label>] table names one built-in checker
// (internal/status) and configures it with the fields below.
const (
	StatusHTTP    = "http"
	StatusJSON    = "json"
	StatusCommand = "command"
)

// StatusSpec is one [status.<label>] table.
//
//	http     url                            up when the URL answers 2xx/3xx
//	json     url, pull, show, headers?      GET url, decode JSON, evaluate each
//	                                        pull JMESPath, fill show's {name}s
//	command  run                            first line of the command's stdout
//
// every is the refresh interval for all three; zero means the kind's default
// (Interval).
type StatusSpec struct {
	Kind    string
	URL     string
	Headers map[string]string
	Every   time.Duration
	Pull    map[string]string
	Show    string
	Run     string
}

// Interval is how often the entry refreshes: its own `every`, else 5s for an
// http check and 15s for a json or command readout.
func (s StatusSpec) Interval() time.Duration {
	if s.Every > 0 {
		return s.Every
	}
	if s.Kind == StatusHTTP {
		return 5 * time.Second
	}
	return 15 * time.Second
}

var statusKeys = map[string]bool{
	"kind": true, "url": true, "headers": true, "every": true, "pull": true, "show": true, "run": true,
}

func parseStatus(data map[string]any, source string) (map[string]StatusSpec, error) {
	v, present := data["status"]
	if !present || v == nil {
		return map[string]StatusSpec{}, nil
	}
	tbl, ok := v.(map[string]any)
	if !ok {
		return nil, errf("%s: 'status' must be a table of [status.<label>] tables", source)
	}
	out := make(map[string]StatusSpec, len(tbl))
	for label, raw := range tbl {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, errf("%s: [status.%s] must be a table", source, label)
		}
		spec, err := parseStatusEntry(label, entry, source)
		if err != nil {
			return nil, err
		}
		out[label] = spec
	}
	return out, nil
}

func parseStatusEntry(label string, e map[string]any, source string) (StatusSpec, error) {
	var spec StatusSpec
	for k := range e {
		if !statusKeys[k] {
			return spec, errf("%s: [status.%s] has unknown key %q (known: kind, url, headers, every, pull, show, run)", source, label, k)
		}
	}
	str := func(key string) (string, error) {
		v, ok := e[key]
		if !ok {
			return "", nil
		}
		s, ok := v.(string)
		if !ok {
			return "", errf("%s: [status.%s] %s must be a string", source, label, key)
		}
		return s, nil
	}
	var err error
	if spec.Kind, err = str("kind"); err != nil {
		return spec, err
	}
	if spec.URL, err = str("url"); err != nil {
		return spec, err
	}
	if spec.Show, err = str("show"); err != nil {
		return spec, err
	}
	if spec.Run, err = str("run"); err != nil {
		return spec, err
	}
	if everyRaw, err := str("every"); err != nil {
		return spec, err
	} else if everyRaw != "" {
		d, perr := time.ParseDuration(everyRaw)
		if perr != nil || d < time.Second {
			return spec, errf("%s: [status.%s] every %q must be a duration of at least 1s, like \"15s\" or \"1m\"", source, label, everyRaw)
		}
		spec.Every = d
	}
	if spec.Headers, err = stringMap(e, "headers", label, source); err != nil {
		return spec, err
	}
	if spec.Pull, err = stringMap(e, "pull", label, source); err != nil {
		return spec, err
	}
	for name := range spec.Pull {
		if !pullName.MatchString(name) {
			return spec, errf("%s: [status.%s] pull name %q must be letters, digits and underscores, so show can use {%s}", source, label, name, name)
		}
	}

	switch spec.Kind {
	case StatusHTTP:
		if spec.URL == "" {
			return spec, errf("%s: [status.%s] kind \"http\" needs a url", source, label)
		}
		if len(spec.Pull) > 0 || spec.Show != "" || spec.Run != "" {
			return spec, errf("%s: [status.%s] kind \"http\" takes only url, headers and every", source, label)
		}
	case StatusJSON:
		if spec.URL == "" || len(spec.Pull) == 0 || spec.Show == "" {
			return spec, errf("%s: [status.%s] kind \"json\" needs url, pull and show", source, label)
		}
		if spec.Run != "" {
			return spec, errf("%s: [status.%s] kind \"json\" does not take run", source, label)
		}
	case StatusCommand:
		if spec.Run == "" {
			return spec, errf("%s: [status.%s] kind \"command\" needs run", source, label)
		}
		if spec.URL != "" || len(spec.Headers) > 0 || len(spec.Pull) > 0 || spec.Show != "" {
			return spec, errf("%s: [status.%s] kind \"command\" takes only run and every", source, label)
		}
	case "":
		return spec, errf("%s: [status.%s] needs a kind (%s)", source, label, strings.Join([]string{StatusHTTP, StatusJSON, StatusCommand}, ", "))
	default:
		return spec, errf("%s: [status.%s] unknown kind %q (known: %s, %s, %s)", source, label, spec.Kind, StatusHTTP, StatusJSON, StatusCommand)
	}
	return spec, nil
}

func stringMap(e map[string]any, key, label, source string) (map[string]string, error) {
	v, ok := e[key]
	if !ok || v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errf("%s: [status.%s] %s must be a table of strings", source, label, key)
	}
	out := make(map[string]string, len(m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s, ok := m[k].(string)
		if !ok {
			return nil, errf("%s: [status.%s] %s.%s must be a string", source, label, key, k)
		}
		out[k] = s
	}
	return out, nil
}

var pullName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
