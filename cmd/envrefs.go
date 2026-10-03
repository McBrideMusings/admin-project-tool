package cmd

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// envCheckedTables are the config tables whose string values reach
// manifest.ResolveEnv when the owning kind runs, so an unsatisfiable ${VAR}
// in one aborts that run.
var envCheckedTables = []string{
	"apple", "react_native", "server", "electron", "output", "logging",
	"dev_kill", "dev_precheck", "clean_dev", "vite_dev", "docker_dev",
	"deploy", "docker_run", "docker_build", "install_template", "fixtures",
	"docs", "go", "go_dist", "raylib", "launchd", "log_bridge", "worktree",
}

// envRef is one environment variable the manifest references: its default,
// when any reference supplies one, and every place that references it. An
// origin is "cmd:<action>", "cmd:<action>:<target>", "url:<key>",
// "logs.<source>[.<env>]", or a dotted config-table path like
// "apple.development_team".
type envRef struct {
	Name       string
	Default    string
	HasDefault bool
	UsedBy     []string
}

// envRefSet collects envRefs in first-seen order.
type envRefSet struct {
	byName map[string]*envRef
	order  []string
}

func (s *envRefSet) add(name, origin string, def string, hasDef bool) {
	if s.byName == nil {
		s.byName = map[string]*envRef{}
	}
	r, ok := s.byName[name]
	if !ok {
		r = &envRef{Name: name}
		s.byName[name] = r
		s.order = append(s.order, name)
	}
	if hasDef && !r.HasDefault {
		r.Default, r.HasDefault = def, true
	}
	for _, o := range r.UsedBy {
		if o == origin {
			return
		}
	}
	r.UsedBy = append(r.UsedBy, origin)
}

// scanString registers every ${VAR} in text, including one nested inside
// another's default (${A:-${B}/x} registers both A and B).
func (s *envRefSet) scanString(text, origin string) {
	for _, ref := range manifest.IterEnvRefs(text) {
		s.add(ref.Name, origin, ref.Default, ref.HasDefault)
		if ref.HasDefault {
			s.scanString(ref.Default, origin)
		}
	}
}

// luaEnvCallRE matches a Lua body reading a variable directly: env("NAME").
var luaEnvCallRE = regexp.MustCompile(`\benv\(\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\)`)

// luaCommentRE matches a Lua line comment, so a ${VAR} written in prose
// inside one is not reported as a reference.
var luaCommentRE = regexp.MustCompile(`--[^\n]*`)

// scanLua registers the variables a Lua body reads: env("NAME") calls, and
// ${VAR} references outside comments (a string handed to run_cmd is expanded
// by the shell).
func (s *envRefSet) scanLua(body, origin string) {
	code := luaCommentRE.ReplaceAllString(body, "")
	for _, m := range luaEnvCallRE.FindAllStringSubmatch(code, -1) {
		s.add(m[1], origin, "", false)
	}
	s.scanString(code, origin)
}

// scanConfig walks a config value, registering every ${VAR} in its strings
// and every `{env = "NAME", default = ...}` sub-table.
func (s *envRefSet) scanConfig(v any, origin string) {
	switch x := v.(type) {
	case string:
		s.scanString(x, origin)
	case map[string]any:
		if name, ok := x["env"].(string); ok && !strings.ContainsAny(name, "{$") {
			def, hasDef := x["default"]
			s.add(name, origin, fmt.Sprint(def), hasDef)
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s.scanConfig(x[k], origin+"."+k)
		}
	case []any:
		for i, e := range x {
			s.scanConfig(e, fmt.Sprintf("%s[%d]", origin, i))
		}
	case []map[string]any:
		for i, e := range x {
			s.scanConfig(e, fmt.Sprintf("%s[%d]", origin, i))
		}
	}
}

func (s *envRefSet) list() []*envRef {
	out := make([]*envRef, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.byName[n])
	}
	return out
}

// configEnvRefs scans only the config tables — what `admin check` validates.
func configEnvRefs(m *manifest.Manifest) []*envRef {
	var s envRefSet
	for _, table := range envCheckedTables {
		if tbl := kinds.ArchetypeConfigTable(m, table); len(tbl) > 0 {
			s.scanConfig(tbl, table)
		}
	}
	return s.list()
}

// projectEnvRefs scans everything the manifest can reference an environment
// variable from: action bodies (including an http action's body and each
// multi-target target), [status], [logs], and the config tables.
func projectEnvRefs(m *manifest.Manifest) []*envRef {
	var s envRefSet
	for _, name := range sortedActionNames(m.Actions) {
		a := m.Actions[name]
		origin := "cmd:" + name
		if run, ok := a["run"].(string); ok {
			if a["kind"] == "lua" {
				s.scanLua(run, origin)
			} else {
				s.scanString(run, origin)
			}
		}
		if body, ok := a["body"].(string); ok {
			s.scanString(body, origin)
		}
		if a["kind"] == "multi-target" {
			for _, t := range kinds.TargetKeys(a) {
				s.scanString(kinds.TargetSummary(a, t), origin+":"+t)
			}
		}
	}
	statusLabels := make([]string, 0, len(m.Status))
	for k := range m.Status {
		statusLabels = append(statusLabels, k)
	}
	sort.Strings(statusLabels)
	for _, k := range statusLabels {
		spec := m.Status[k]
		origin := "status:" + k
		s.scanString(spec.URL, origin)
		s.scanString(spec.Run, origin)
		headerNames := make([]string, 0, len(spec.Headers))
		for h := range spec.Headers {
			headerNames = append(headerNames, h)
		}
		sort.Strings(headerNames)
		for _, h := range headerNames {
			s.scanString(spec.Headers[h], origin)
		}
	}
	logNames := make([]string, 0, len(m.Logs))
	for k := range m.Logs {
		logNames = append(logNames, k)
	}
	sort.Strings(logNames)
	for _, name := range logNames {
		src := m.Logs[name]
		envNames := make([]string, 0, len(src.Envs))
		for e := range src.Envs {
			envNames = append(envNames, e)
		}
		sort.Strings(envNames)
		for _, e := range envNames {
			le := src.Envs[e]
			for _, field := range []string{le.Path, le.Host, le.User} {
				s.scanString(field, "logs."+name+"."+e)
			}
		}
		fieldNames := make([]string, 0, len(src.Fields))
		for f := range src.Fields {
			fieldNames = append(fieldNames, f)
		}
		sort.Strings(fieldNames)
		for _, f := range fieldNames {
			s.scanString(src.Fields[f], "logs."+name)
		}
	}
	for _, r := range configEnvRefs(m) {
		for _, o := range r.UsedBy {
			s.add(r.Name, o, r.Default, r.HasDefault)
		}
	}
	// A command's `env` table: its values can reference variables, and the
	// variables it sets are supplied by admin itself, never required from
	// the environment.
	provided := map[string]bool{}
	for _, cmds := range []map[string]manifest.Command{m.Overrides, m.Custom} {
		names := make([]string, 0, len(cmds))
		for n := range cmds {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			env := cmds[n].Env
			keys := make([]string, 0, len(env))
			for k := range env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				provided[k] = true
				s.scanString(env[k], "cmd:"+n+".env."+k)
			}
		}
	}
	var out []*envRef
	for _, r := range s.list() {
		if !provided[r.Name] {
			out = append(out, r)
		}
	}
	return out
}
