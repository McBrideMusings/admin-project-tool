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
	"strings"

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
	Desc        string
	HasDesc     bool
	Steps       []string
	Group       int
	HasGroup    bool
	Priority    int
	HasPriority bool
	Env         map[string]string
	LogFile     string
	HasLogFile  bool
	// Log is the raw `log` value — a string (a log-source name) or a bool
	// (Python's admin_lib/archetypes.py:352 does bool(data["log"]) at merge
	// time rather than typing it at parse time, e.g. archetypes/docker.py's
	// [commands.run-args] sets `log = false`). Kept untyped here to match.
	Log       any
	HasLog    bool
	LogRetain int
	HasRetain bool
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

	// Trackers names which issue tracker(s) the project uses — a declared
	// list rather than something sniffed from repo layout (admin-3rgp.22).
	// Both tabs draw when both members are declared, in array order
	// (admin-j00d). HasTracker distinguishes "declared" from "not declared
	// at all" (an empty list is rejected at parse, same as any unknown
	// member), which the Config tab renders as "no tracker declared" rather
	// than guessing one.
	Trackers   []string
	HasTracker bool
	// UsesPullRequests declares whether the project's workflow goes through
	// pull requests, independent of Tracker — a repo can use beads for
	// issues and still open PRs, or use GitHub Issues with no PR step at
	// all. HasUsesPullRequests marks whether the manifest (or an archetype)
	// actually set it; the Config tab shows the false default as defaulted
	// when it is unset.
	UsesPullRequests    bool
	HasUsesPullRequests bool

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

	// Keys carries the raw top-level [keys.<row-kind>] table (ADR-0018) —
	// each row kind maps a key name to a command name. Kept raw (like the
	// archetype-specific tables above) because internal/manifest owns only
	// parsing; internal/validate.RowKeys interprets it against
	// internal/keymap's row-kind vocabulary.
	Keys map[string]any

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

	// Identity, Worktrees, Beads and Workflow are the config.Config tables
	// (internal/config): who "you" are, where worktrees are created, the
	// Beads tab's columns, and the workflow dispatcher's default agent and
	// concurrency ceiling. Kept raw here for the same reason as the
	// archetype-specific tables above — internal/manifest owns only parsing,
	// internal/config owns the typed decode — except that these five ARE the
	// entire configuration surface for the dashboard rather than one kind's
	// fragment, per the retire-the-config-tiers decision: there is no other
	// file these can live in.
	Identity  map[string]any
	Worktrees map[string]any
	Beads     map[string]any
	Workflow  map[string]any
	// Populate is the [[populate]] array of tables — one entry per
	// gitignored file/glob to bring into a fresh worktree, decoded by
	// internal/config into []config.Entry.
	Populate []map[string]any

	// Raw is the originally-decoded TOML, kept for debugging — mirrors
	// admin_lib.manifest.Manifest.raw.
	Raw map[string]any
	// Unknown lists top-level keys the parser doesn't recognize. Python
	// prints a warning to stderr for these; the Go parser leaves printing to
	// the caller.
	Unknown []string
}

// KnownTrackers is the single source of truth for the accepted `tracker`
// list members (admin-j00d). An unknown value is a check error naming it
// against this set.
var KnownTrackers = []string{"beads", "github"}

// knownTopLevelKeys mirrors admin_lib/manifest.py::_KNOWN_KEYS.
var knownTopLevelKeys = map[string]bool{
	"project_name": true, "python_min": true, "tracker": true, "uses_pull_requests": true,
	"archetypes": true, "modules": true, "urls": true,
	"commands": true, "actions": true, "order": true, "logs": true, "apple": true, "react_native": true,
	// An archetype.toml may name itself (internal/archetype reads the
	// directory name as canonical and only tolerates this key).
	"name":   true,
	"server": true, "output": true, "exclude_modules": true, "hide_commands": true, "hide_urls": true,
	"inline": true, "logging": true, "dev_kill": true, "dev_precheck": true, "clean_dev": true,
	"vite_dev": true, "docker_dev": true, "docker_run": true, "docker_build": true, "deploy": true,
	"install_template": true, "fixtures": true, "log_bridge": true, "guards": true, "docs": true,
	"go": true, "go_dist": true, "raylib": true, "worktree": true, "launchd": true, "electron": true,
	"keys":     true,
	"identity": true, "worktrees": true, "beads": true, "workflow": true, "populate": true,
	// Obsolete under interpreter mode (ADR-0006); tolerated silently.
	"generator_commit": true,
}

// IdentityKeys are the keys accepted by the [identity] table.
var IdentityKeys = map[string]bool{"login": true, "emails": true}

// WorktreesKeys are the keys accepted by the [worktrees] table.
var WorktreesKeys = map[string]bool{"root": true}

// BeadsKeys are the keys accepted by the [beads] table.
var BeadsKeys = map[string]bool{"columns": true}

// WorkflowKeys are the keys accepted by the [workflow] table.
var WorkflowKeys = map[string]bool{"agent": true, "ceiling": true}

// PopulateEntryKeys are the keys accepted by each [[populate]] entry.
var PopulateEntryKeys = map[string]bool{"pattern": true, "mode": true}

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
// project_name is required, matching admin_toml's own manifest.
func Parse(data map[string]any, source string) (*Manifest, error) {
	return parse(data, source, true)
}

// ParsePartial turns an already-decoded TOML table into a Manifest using the
// same grammar as Parse, but without requiring project_name — the shape an
// archetype.toml uses (internal/archetype), since an archetype contributes a
// partial manifest (ADR-0002) rather than a standalone project. Every other
// check is identical to Parse.
func ParsePartial(data map[string]any, source string) (*Manifest, error) {
	return parse(data, source, false)
}

// parse is Parse and ParsePartial's shared body. requireName gates only the
// project_name check; everything else is identical between the two callers.
func parse(data map[string]any, source string, requireName bool) (*Manifest, error) {
	m := &Manifest{Raw: data}

	name, ok := data["project_name"]
	nameStr, isStr := name.(string)
	if requireName {
		if !ok || !isStr || nameStr == "" {
			return nil, errf("%s: 'project_name' is required (string)", source)
		}
	}
	if isStr {
		m.ProjectName = nameStr
	}

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

	if v, present := data["tracker"]; present {
		if _, ok := v.(string); ok {
			return nil, errf("%s: 'tracker' must be a list, e.g. tracker = [\"beads\"] (got the string %q)", source, v)
		}
		list, ok := asStringList(v)
		if !ok {
			return nil, errf("%s: 'tracker' must be a list of strings", source)
		}
		if len(list) == 0 {
			return nil, errf("%s: 'tracker' must not be an empty list", source)
		}
		seen := make(map[string]bool, len(list))
		for _, t := range list {
			known := false
			for _, k := range KnownTrackers {
				if t == k {
					known = true
					break
				}
			}
			if !known {
				return nil, errf("%s: 'tracker' has an unknown value %q — accepted values are %s", source, t, strings.Join(KnownTrackers, ", "))
			}
			if seen[t] {
				return nil, errf("%s: 'tracker' lists %q more than once", source, t)
			}
			seen[t] = true
		}
		m.Trackers, m.HasTracker = list, true
	}
	if v, present := data["uses_pull_requests"]; present {
		b, ok := v.(bool)
		if !ok {
			return nil, errf("%s: 'uses_pull_requests' must be true or false", source)
		}
		m.UsesPullRequests, m.HasUsesPullRequests = b, true
	}

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
		{"keys", &m.Keys},
		{"identity", &m.Identity},
		{"worktrees", &m.Worktrees},
		{"beads", &m.Beads},
		{"workflow", &m.Workflow},
	} {
		v, err := table(data, t.key, source)
		if err != nil {
			return nil, err
		}
		*t.dst = v
	}

	for _, t := range []struct {
		table map[string]any
		keys  map[string]bool
		name  string
	}{
		{m.Identity, IdentityKeys, "[identity]"},
		{m.Worktrees, WorktreesKeys, "[worktrees]"},
		{m.Beads, BeadsKeys, "[beads]"},
		{m.Workflow, WorkflowKeys, "[workflow]"},
	} {
		if err := checkUnknownKeys(t.table, t.keys, t.name, source); err != nil {
			return nil, err
		}
	}

	if m.Populate, err = parsePopulate(data, source); err != nil {
		return nil, err
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

// CommandNames builds the resolved manifest's full command-name set —
// Overrides (project + archetype [commands.X]) plus Custom
// ([commands.custom.X]). This is the set a [keys.<kind>].<key> binding's
// value must name to be valid (validate.RowKeys' commands argument) — both
// cmd/check.go's admin check and internal/tui's row-key lookup build their
// command set from this single method rather than each walking Overrides/
// Custom on their own.
func (m *Manifest) CommandNames() map[string]bool {
	out := make(map[string]bool, len(m.Overrides)+len(m.Custom))
	for name := range m.Overrides {
		out[name] = true
	}
	for name := range m.Custom {
		out[name] = true
	}
	return out
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
			cmd.Group, cmd.HasGroup = n, true
		case "priority":
			n, ok := asInt(v)
			if !ok {
				return cmd, errf("'priority' must be an integer")
			}
			cmd.Priority, cmd.HasPriority = n, true
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
			switch v.(type) {
			case string, bool:
				cmd.Log, cmd.HasLog = v, true
			default:
				return cmd, errf("'log' must be a string or a boolean")
			}
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

// checkUnknownKeys rejects any key in t not present in allowed, matching the
// "'[deploy]' has unknown key(s): ..." shape the [deploy] table already uses
// (manifest.go's own parse, above) — the same rejection every other
// admin.toml table gets, which is what lets `admin check` reject an unknown
// [identity]/[worktrees]/[beads]/[workflow] key exactly like any other.
func checkUnknownKeys(t map[string]any, allowed map[string]bool, name, source string) error {
	if len(t) == 0 {
		return nil
	}
	var unknown []string
	for k := range t {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return errf("%s: '%s' has unknown key(s): %s", source, name, joinStrings(unknown, ", "))
}

// parsePopulate parses the [[populate]] array of tables — one entry per
// gitignored file/glob to bring into a fresh worktree. TOML decodes an
// array-of-tables into []map[string]any when the surrounding value is
// generic (Load's map[string]any target); a hand-built []any of tables
// (ParsePartial callers building data by hand, tests) is accepted the same
// way.
func parsePopulate(data map[string]any, source string) ([]map[string]any, error) {
	v, present := data["populate"]
	if !present || v == nil {
		return nil, nil
	}
	var raw []map[string]any
	switch list := v.(type) {
	case []map[string]any:
		raw = list
	case []any:
		raw = make([]map[string]any, 0, len(list))
		for _, item := range list {
			t, ok := item.(map[string]any)
			if !ok {
				return nil, errf("%s: '[[populate]]' entries must be tables", source)
			}
			raw = append(raw, t)
		}
	default:
		return nil, errf("%s: 'populate' must be a list of tables", source)
	}

	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if err := checkUnknownKeys(entry, PopulateEntryKeys, "[[populate]]", source); err != nil {
			return nil, err
		}
		if p, ok := entry["pattern"]; ok {
			if _, isStr := p.(string); !isStr {
				return nil, errf("%s: '[[populate]].pattern' must be a string", source)
			}
		}
		if mv, ok := entry["mode"]; ok {
			if _, isStr := mv.(string); !isStr {
				return nil, errf("%s: '[[populate]].mode' must be a string", source)
			}
		}
		out = append(out, entry)
	}
	return out, nil
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
