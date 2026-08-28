// Package manifest parses a project's admin.toml into a normalized Manifest —
// the Go port of admin_lib/manifest.py::parse. It reads one file into a data
// structure: no archetype merging (admin_lib/archetypes.py), no full schema
// validation (admin_lib/validate.py), and nothing is executed. ${VAR} /
// ${VAR:-default} placeholders are preserved verbatim wherever the Python
// parser leaves them — they are resolved at dispatch time, not here.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/BurntSushi/toml"
)

// Filename is the manifest's on-disk name, always at a project's root.
const Filename = "admin.toml"

// Spacer is the sentinel stored inside Manifest.Order for a display spacer —
// an empty "" or "---" entry in the raw `order` list normalizes to this.
const Spacer = ""

// Error is ManifestError's Go counterpart: every message is
// "<source>: <what about which key>", matching admin_lib/manifest.py's
// ManifestError text so a Go and a Python parse of the same bad file report
// the same complaint.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func errf(format string, args ...any) *Error {
	return &Error{msg: fmt.Sprintf(format, args...)}
}

// Command is one [commands.X] / [commands.custom.X] entry, post-parse but
// pre-merge — the pure-routing shape ADR-0003 requires: desc/steps plus log
// overrides and display ordering. kind/run/targets belong on [actions.X], so
// they deliberately have no typed field here; if a manifest puts them on a
// command anyway, they land in Raw rather than raising — rejecting that is
// the later validation ticket's job, not the parser's.
type Command struct {
	Desc       string
	HasDesc    bool
	Steps      []string
	Group      int
	Priority   int
	Env        map[string]string
	LogFile    string
	HasLogFile bool
	Log        string
	HasLog     bool
	LogRetain  int
	HasRetain  bool
	// Raw holds every key not in the known Command key set (desc, steps,
	// log_file, log_retain, log, group, priority, env) — including kind/run/
	// targets when a manifest mistakenly puts them on a command.
	Raw map[string]any
}

// Action is one [actions.X] entry, kept exactly as raw TOML — kind, run,
// targets, and everything else a kind cares about live here, mirroring the
// Python parser's raw actions dict. Typing this further would pull archetype/
// kind-catalog knowledge into the parser (out of scope for this ticket).
type Action map[string]any

// Guard is a user-defined [guards.<name>] shell guard.
type Guard struct {
	Run  string
	Fail string
}

// Manifest is a project's admin.toml, parsed and normalized but not yet
// merged with any archetype.
type Manifest struct {
	ProjectName string
	PythonMin   string

	Archetypes     []string
	Modules        []string
	Order          []string
	ExcludeModules []string
	HideCommands   []string
	HideUrls       []string

	Urls map[string]string

	Overrides map[string]Command // [commands.X] entries (minus "custom")
	Custom    map[string]Command // [commands.custom.X] entries
	Actions   map[string]Action

	Logs   map[string]LogSource
	Guards map[string]Guard

	InlineFile string

	// The ~22 archetype-specific config tables. Each stays a raw map exactly
	// as the Python parser keeps it (ADR-0009: the manifest references, the
	// archetype implements) — typing them here would pull archetype
	// knowledge into the parser.
	Apple           map[string]any
	ReactNative     map[string]any
	Electron        map[string]any
	Server          map[string]any
	Output          map[string]any
	Logging         map[string]any
	DevKill         map[string]any
	DevPrecheck     map[string]any
	CleanDev        map[string]any
	ViteDev         map[string]any
	DockerDev       map[string]any
	Deploy          map[string]any
	DockerRun       map[string]any
	DockerBuild     map[string]any
	InstallTemplate map[string]any
	Fixtures        map[string]any
	Docs            map[string]any
	Go              map[string]any
	GoDist          map[string]any
	Raylib          map[string]any
	Launchd         map[string]any
	LogBridge       map[string]any
	Worktree        map[string]any

	// Raw is the originally-decoded TOML, kept for debugging — mirrors
	// admin_lib.manifest.Manifest.raw.
	Raw map[string]any
	// Unknown lists top-level keys the parser doesn't recognize. Python
	// prints a warning to stderr for these; the Go parser leaves printing to
	// the caller.
	Unknown []string
}

// knownTopLevelKeys mirrors admin_lib/manifest.py::_KNOWN_KEYS.
var knownTopLevelKeys = map[string]bool{
	"project_name": true, "python_min": true, "archetypes": true, "modules": true, "urls": true,
	"commands": true, "actions": true, "order": true, "logs": true, "apple": true, "react_native": true,
	"server": true, "output": true, "exclude_modules": true, "hide_commands": true, "hide_urls": true,
	"inline": true, "logging": true, "dev_kill": true, "dev_precheck": true, "clean_dev": true,
	"vite_dev": true, "docker_dev": true, "docker_run": true, "docker_build": true, "deploy": true,
	"install_template": true, "fixtures": true, "log_bridge": true, "guards": true, "docs": true,
	"go": true, "go_dist": true, "raylib": true, "worktree": true, "launchd": true, "electron": true,
	// Obsolete under interpreter mode (ADR-0006); tolerated silently.
	"generator_commit": true,
}

var pythonMinRE = regexp.MustCompile(`^\d+\.\d+$`)

// Load reads <projectDir>/admin.toml and parses it.
func Load(projectDir string) (*Manifest, error) {
	path := filepath.Join(projectDir, Filename)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, errf("no %s at %s", Filename, projectDir)
		}
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data := map[string]any{}
	if _, err := toml.Decode(string(raw), &data); err != nil {
		return nil, errf("%s: invalid TOML: %s", path, err.Error())
	}
	return Parse(data, path)
}

// Parse turns an already-decoded TOML table into a Manifest. source is used
// only to prefix error messages (typically the file path Load read from).
func Parse(data map[string]any, source string) (*Manifest, error) {
	m := &Manifest{Raw: data}

	name, ok := data["project_name"]
	nameStr, isStr := name.(string)
	if !ok || !isStr || nameStr == "" {
		return nil, errf("%s: 'project_name' is required (string)", source)
	}
	m.ProjectName = nameStr

	pythonMin := "3.11"
	if v, present := data["python_min"]; present {
		s, ok := v.(string)
		if !ok {
			return nil, errf("%s: 'python_min' must be a string", source)
		}
		pythonMin = s
	}
	if !pythonMinRE.MatchString(pythonMin) {
		return nil, errf("%s: 'python_min' must be MAJOR.MINOR (e.g. '3.11'), got '%s'", source, pythonMin)
	}
	m.PythonMin = pythonMin

	var err error
	if m.Archetypes, err = stringSlice(data, "archetypes", source); err != nil {
		return nil, err
	}
	if m.Modules, err = stringSlice(data, "modules", source); err != nil {
		return nil, err
	}

	if m.Urls, err = parseUrls(data, source); err != nil {
		return nil, err
	}

	rawCommands, err := table(data, "commands", source)
	if err != nil {
		return nil, err
	}
	rawCommands = cloneMap(rawCommands)
	rawCustomAny, hadCustom := rawCommands["custom"]
	delete(rawCommands, "custom")
	var rawCustom map[string]any
	if hadCustom {
		rc, ok := rawCustomAny.(map[string]any)
		if !ok {
			return nil, errf("%s: 'commands.custom' must be a table", source)
		}
		rawCustom = rc
	}

	m.Overrides = map[string]Command{}
	for _, name := range sortedKeys(rawCommands) {
		val, ok := rawCommands[name].(map[string]any)
		if !ok {
			return nil, errf("%s: commands.%s must be a table with 'steps' (a list of action names)", source, name)
		}
		cmd, err := toCommand(val)
		if err != nil {
			return nil, errf("%s: commands.%s: %s", source, name, err.Error())
		}
		m.Overrides[name] = cmd
	}

	m.Custom = map[string]Command{}
	for _, name := range sortedKeys(rawCustom) {
		val, ok := rawCustom[name].(map[string]any)
		if !ok {
			return nil, errf(
				"%s: commands.custom.%s must be a table with 'steps' (a list of action names). "+
					"Move run/kind/targets to [actions].", source, name)
		}
		cmd, err := toCommand(val)
		if err != nil {
			return nil, errf("%s: commands.custom.%s: %s", source, name, err.Error())
		}
		m.Custom[name] = cmd
	}

	rawActions, err := table(data, "actions", source)
	if err != nil {
		return nil, err
	}
	m.Actions = map[string]Action{}
	for _, name := range sortedKeys(rawActions) {
		val, ok := rawActions[name].(map[string]any)
		if !ok {
			return nil, errf("%s: actions.%s must be a table", source, name)
		}
		m.Actions[name] = Action(val)
	}

	if inline, ok := data["inline"].(map[string]any); ok {
		if f, ok := inline["file"].(string); ok {
			m.InlineFile = f
		}
	}

	rawOrder, err := stringSlice(data, "order", source)
	if err != nil {
		return nil, err
	}
	m.Order = make([]string, 0, len(rawOrder))
	for _, entry := range rawOrder {
		if entry == "" || entry == "---" {
			m.Order = append(m.Order, Spacer)
		} else {
			m.Order = append(m.Order, entry)
		}
	}

	if m.Logs, err = parseLogs(data["logs"], source); err != nil {
		return nil, err
	}

	for _, t := range []struct {
		key string
		dst *map[string]any
	}{
		{"apple", &m.Apple},
		{"react_native", &m.ReactNative},
		{"electron", &m.Electron},
		{"server", &m.Server},
		{"output", &m.Output},
		{"dev_kill", &m.DevKill},
		{"dev_precheck", &m.DevPrecheck},
		{"clean_dev", &m.CleanDev},
		{"vite_dev", &m.ViteDev},
		{"docker_dev", &m.DockerDev},
		{"docker_run", &m.DockerRun},
		{"docker_build", &m.DockerBuild},
		{"install_template", &m.InstallTemplate},
		{"fixtures", &m.Fixtures},
		{"docs", &m.Docs},
		{"go", &m.Go},
		{"go_dist", &m.GoDist},
		{"raylib", &m.Raylib},
		{"launchd", &m.Launchd},
		{"logging", &m.Logging},
		{"log_bridge", &m.LogBridge},
		{"worktree", &m.Worktree},
	} {
		v, err := table(data, t.key, source)
		if err != nil {
			return nil, err
		}
		*t.dst = v
	}

	deploy, err := table(data, "deploy", source)
	if err != nil {
		return nil, err
	}
	if len(deploy) > 0 {
		var unknown []string
		for k := range deploy {
			if !DeployKeys[k] {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return nil, errf("%s: '[deploy]' has unknown key(s): %s", source, joinStrings(unknown, ", "))
		}
		for _, required := range []string{"src", "dest"} {
			if !truthy(deploy[required]) {
				return nil, errf("%s: '[deploy].%s' is required", source, required)
			}
		}
		if v, present := deploy["exclude"]; present && v != nil {
			if _, ok := v.([]any); !ok {
				return nil, errf("%s: '[deploy].exclude' must be a list", source)
			}
		}
		if v, present := deploy["delete"]; present && v != nil {
			if _, ok := v.(bool); !ok {
				return nil, errf("%s: '[deploy].delete' must be true or false", source)
			}
		}
	}
	m.Deploy = deploy

	guards, err := parseGuardTables(data, source)
	if err != nil {
		return nil, err
	}
	m.Guards = guards

	if m.ExcludeModules, err = stringSlice(data, "exclude_modules", source); err != nil {
		return nil, err
	}
	if m.HideCommands, err = stringSlice(data, "hide_commands", source); err != nil {
		return nil, err
	}
	if m.HideUrls, err = stringSlice(data, "hide_urls", source); err != nil {
		return nil, err
	}

	var unknown []string
	for k := range data {
		if !knownTopLevelKeys[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	m.Unknown = unknown

	return m, nil
}

// knownCommandKeys is the ADR-0003 pure-routing key set — the port of
// admin_lib/validate.py::VALID_COMMAND_KEYS.
var knownCommandKeys = map[string]bool{
	"desc": true, "steps": true, "log_file": true, "log_retain": true,
	"log": true, "group": true, "priority": true, "env": true,
}

// toCommand extracts the typed Command fields from one [commands.X] table,
// stashing anything outside the known key set into Raw instead of raising —
// rejecting kind/run/targets on a command is the validation ticket's job.
func toCommand(val map[string]any) (Command, error) {
	cmd := Command{Raw: map[string]any{}}
	for _, k := range sortedKeys(val) {
		v := val[k]
		switch k {
		case "desc":
			s, ok := v.(string)
			if !ok {
				return cmd, errf("'desc' must be a string")
			}
			cmd.Desc, cmd.HasDesc = s, true
		case "steps":
			list, ok := asStringList(v)
			if !ok {
				return cmd, errf("'steps' must be a list of strings")
			}
			cmd.Steps = list
		case "group":
			n, ok := asInt(v)
			if !ok {
				return cmd, errf("'group' must be an integer")
			}
			cmd.Group = n
		case "priority":
			n, ok := asInt(v)
			if !ok {
				return cmd, errf("'priority' must be an integer")
			}
			cmd.Priority = n
		case "env":
			table, ok := v.(map[string]any)
			if !ok {
				return cmd, errf("'env' must be a table")
			}
			env := make(map[string]string, len(table))
			for ek, ev := range table {
				es, ok := ev.(string)
				if !ok {
					return cmd, errf("'env.%s' must be a string", ek)
				}
				env[ek] = es
			}
			cmd.Env = env
		case "log_file":
			s, ok := v.(string)
			if !ok {
				return cmd, errf("'log_file' must be a string")
			}
			cmd.LogFile, cmd.HasLogFile = s, true
		case "log":
			s, ok := v.(string)
			if !ok {
				return cmd, errf("'log' must be a string")
			}
			cmd.Log, cmd.HasLog = s, true
		case "log_retain":
			n, ok := asInt(v)
			if !ok {
				return cmd, errf("'log_retain' must be an integer")
			}
			cmd.LogRetain, cmd.HasRetain = n, true
		default:
			cmd.Raw[k] = v
		}
	}
	_ = knownCommandKeys // referenced for documentation/symmetry with Python's key set
	return cmd, nil
}

// parseGuardTables is the port of admin_lib/guards.py::parse_guard_tables.
func parseGuardTables(data map[string]any, source string) (map[string]Guard, error) {
	raw, err := table(data, "guards", source)
	if err != nil {
		return nil, errf("%s: '[guards]' must be a table", source)
	}
	guards := make(map[string]Guard, len(raw))
	for _, name := range sortedKeys(raw) {
		def, ok := raw[name].(map[string]any)
		if !ok {
			return nil, errf("%s: '[guards.%s]' must be a table with 'run' and 'fail'", source, name)
		}
		if guardsBuiltin[name] {
			return nil, errf(
				"%s: '[guards.%s]' collides with a built-in guard name (%s). "+
					"Built-in guards are registered by admin_lib/guards.py and cannot be redefined in TOML.",
				source, name, joinStrings(sortedBuiltinGuards(), ", "))
		}
		var unknown []string
		for k := range def {
			if k != "run" && k != "fail" {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return nil, errf("%s: '[guards.%s]' unknown keys: %s, valid: [fail run]", source, name, joinStrings(unknown, ", "))
		}
		for _, req := range []string{"run", "fail"} {
			v, present := def[req]
			if !present {
				return nil, errf("%s: '[guards.%s]' missing required key '%s'", source, name, req)
			}
			s, ok := v.(string)
			if !ok || s == "" {
				return nil, errf("%s: '[guards.%s].%s' must be a non-empty string", source, name, req)
			}
		}
		guards[name] = Guard{Run: def["run"].(string), Fail: def["fail"].(string)}
	}
	return guards, nil
}

// guardsBuiltin mirrors admin_lib/guards.py::GUARDS_BUILTIN — names a
// [guards.<name>] table may not use.
var guardsBuiltin = map[string]bool{"git-clean": true, "git-pushed": true, "deps-fresh": true}

func sortedBuiltinGuards() []string {
	out := make([]string, 0, len(guardsBuiltin))
	for k := range guardsBuiltin {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- small shape-checking helpers, mirroring the Python parser's inline checks ---

func table(data map[string]any, key, source string) (map[string]any, error) {
	v, present := data[key]
	if !present || v == nil {
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errf("%s: '[%s]' must be a table", source, key)
	}
	return m, nil
}

func stringSlice(data map[string]any, key, source string) ([]string, error) {
	v, present := data[key]
	if !present || v == nil {
		return []string{}, nil
	}
	list, ok := asStringList(v)
	if !ok {
		return nil, errf("%s: '%s' must be a list of strings", source, key)
	}
	return list, nil
}

// truthy mirrors Python's truthiness for deploy.get(key) checks:
// nil/absent → false; string → v != ""; bool → v; []any/map[string]any → len > 0;
// int64/float64 → != 0; anything else → true.
func truthy(v any) bool {
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case string:
		return val != ""
	case bool:
		return val
	case []any:
		return len(val) > 0
	case map[string]any:
		return len(val) > 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	default:
		return true
	}
}

func parseUrls(data map[string]any, source string) (map[string]string, error) {
	v, present := data["urls"]
	if !present || v == nil {
		return map[string]string{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errf("%s: 'urls' must be a table", source)
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		out[k] = stringify(val)
	}
	return out, nil
}

func asStringList(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
}

func stringify(v any) string {
	switch s := v.(type) {
	case string:
		return s
	default:
		return fmt.Sprintf("%v", s)
	}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func joinStrings(items []string, sep string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
