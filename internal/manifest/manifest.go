// Package manifest parses a project's admin.toml into a normalized Manifest.
// It reads one file into a data structure: no archetype merging, no full
// schema validation, and nothing is executed. ${VAR} / ${VAR:-default}
// placeholders are preserved verbatim — they are resolved at dispatch time,
// not here.
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

// FindRoot walks up from start looking for the nearest directory holding an
// admin.toml, and returns that directory (absolute) — the project root.
//
// The walk is what makes `admin <command>` mean the same thing from anywhere
// inside a project rather than only at its top directory, and it is what
// gives every action a project root that is not simply "wherever the binary
// was invoked from". Without it the two are the same value by accident, so
// every `run` string in a manifest has to carry its own absolute `cd` to be
// invocation-independent — and an absolute path is exactly what survives the
// straight file copy that seeds admin.toml into a new git worktree
// (archetypes/base/archetype.toml's [[populate]] copy mode), which is how
// `admin dev` inside a worktree ends up building the primary checkout.
func FindRoot(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		if st, err := os.Stat(filepath.Join(dir, Filename)); err == nil && !st.IsDir() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Spacer is the sentinel stored inside Manifest.Order for a display spacer —
// an empty "" or "---" entry in the raw `order` list normalizes to this.
const Spacer = ""

// Error is every parse-error message's shape: "<source>: <what about which
// key>".
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
	// (a command can set `log = false` to opt out). Kept untyped here; the
	// bool/string distinction is resolved at merge time, not parse time.
	Log       any
	HasLog    bool
	LogRetain int
	HasRetain bool
	// Preflight declares whether guards.RunGlobalPreflight applies to this
	// command — admin-tlhw.3: the tool no longer decides this by matching
	// the literal names "logs"/"kill"; a command that recovers from a stale
	// install (or is otherwise safe to run regardless) declares
	// `preflight = false` itself. HasPreflight distinguishes "declared
	// false" from "not declared" (which means preflight applies, the same
	// default every command had before this key existed).
	Preflight    bool
	HasPreflight bool
	// Raw holds every key not in the known Command key set (desc, steps,
	// log_file, log_retain, log, group, priority, env, preflight) —
	// including kind/run/targets when a manifest mistakenly puts them on a
	// command.
	Raw map[string]any
}

// Action is one [actions.X] entry, kept exactly as raw TOML — kind, run,
// targets, and everything else a kind cares about live here. Typing this
// further would pull archetype/kind-catalog knowledge into the parser (out
// of scope for this ticket).
type Action map[string]any

// Guard is a user-defined [guards.<name>] shell guard.
type Guard struct {
	Run  string
	Fail string
}

// Manifest is a project's admin.toml, parsed and normalized but not yet
// merged with any archetype.
type Manifest struct {
	// Root is the absolute directory admin.toml was read from — the project
	// root. Load sets it; Parse/ParsePartial leave it empty because an
	// already-decoded table (an archetype fragment, a test) has no directory
	// of its own. Every action's subprocess runs with this as its working
	// directory, so a `run` string is relative to the project, never to
	// whatever directory `admin` happened to be typed in.
	Root string

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

	// Settings holds every top-level admin.toml table that isn't one of the
	// structural or dedicated-decode keys parsed elsewhere in this function
	// (commands, actions, urls, guards, keys, identity, worktrees, beads,
	// workflow, terminal, ...), keyed by its admin.toml name — apple, go,
	// docker_dev, and any name a project invents for itself alike. A kind
	// reads its own table by name (kinds.ArchetypeConfigTable(m, "apple")) or
	// a project's own kind="lua" body reads an arbitrary one as
	// config.<name>. There is deliberately no closed list of permitted
	// names: a project adds [my_thing] to admin.toml and it is readable with
	// no change to the tool (admin-tlhw.4). Each stays a raw map (ADR-0009:
	// the manifest references, the archetype implements) — typing them here
	// would pull archetype knowledge into the parser.
	Settings map[string]map[string]any

	Logging   map[string]any
	LogBridge map[string]any
	Worktree  map[string]any

	// Identity, Worktrees, Beads, Workflow and Terminal are the config.Config
	// tables (internal/config): who "you" are, where worktrees are created,
	// the Beads tab's columns, the workflow dispatcher's default agent and
	// concurrency ceiling, and the ADR-0020 terminal launcher contract. Kept
	// raw here for the same reason as the archetype-specific tables above —
	// internal/manifest owns only parsing, internal/config owns the typed
	// decode — except that these six ARE the entire configuration surface
	// for the dashboard rather than one kind's fragment, per the
	// retire-the-config-tiers decision: there is no other file these can
	// live in.
	Identity  map[string]any
	Worktrees map[string]any
	Beads     map[string]any
	Workflow  map[string]any
	// Terminal carries the raw top-level [terminal] table (ADR-0020) — the
	// spawn/attach/kill/status launcher contract. Kept raw for the same
	// reason as Keys above: internal/manifest owns only parsing,
	// internal/validate.Terminal owns the schema, and internal/config owns
	// the typed decode.
	Terminal map[string]any
	// Populate is the [[populate]] array of tables — one entry per
	// gitignored file/glob to bring into a fresh worktree, decoded by
	// internal/config into []config.Entry.
	Populate []map[string]any

	// Raw is the originally-decoded TOML, kept for debugging.
	Raw map[string]any
	// Unknown lists top-level keys the parser doesn't recognize. The parser
	// leaves printing a warning for these to the caller.
	Unknown []string
}

// KnownTrackers is the single source of truth for the accepted `tracker`
// list members (admin-j00d). An unknown value is a check error naming it
// against this set.
var KnownTrackers = []string{"beads", "github"}

// knownTopLevelKeys is every top-level admin.toml key the parser recognizes.
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
	"terminal": true,
	// Obsolete under interpreter mode (ADR-0006); tolerated silently.
	"generator_commit": true,
}

// nonSettingsTopLevelKeys lists every top-level admin.toml key that parse
// handles structurally or through one of the dedicated-decode fields
// (Logging, LogBridge, Worktree, Keys, Identity, Worktrees, Beads, Workflow,
// Terminal — each has a consumer outside this package that reads the typed
// field directly) or the specially-validated [deploy] table above. Anything
// NOT in this set is, by construction, a settings table: parse's final pass
// copies it into Manifest.Settings under its own name with no further
// involvement from the tool (admin-tlhw.4). This is deliberately the inverse
// of a permitted-names allowlist — it names the small fixed set of keys that
// are NOT generic settings, so a project can invent any other top-level
// table and have it be readable without editing this list.
var nonSettingsTopLevelKeys = map[string]bool{
	"project_name": true, "python_min": true, "tracker": true, "uses_pull_requests": true,
	"archetypes": true, "modules": true, "urls": true,
	"commands": true, "actions": true, "order": true, "logs": true,
	"name":             true,
	"exclude_modules":  true,
	"hide_commands":    true,
	"hide_urls":        true,
	"inline":           true,
	"guards":           true,
	"populate":         true,
	"generator_commit": true,
	"deploy":           true,
	"logging":          true,
	"log_bridge":       true,
	"worktree":         true,
	"keys":             true,
	"identity":         true,
	"worktrees":        true,
	"beads":            true,
	"workflow":         true,
	"terminal":         true,
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
	m, err := Parse(data, path)
	if err != nil {
		return nil, err
	}
	if abs, absErr := filepath.Abs(projectDir); absErr == nil {
		m.Root = abs
	} else {
		m.Root = projectDir
	}
	return m, nil
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
		{"logging", &m.Logging},
		{"log_bridge", &m.LogBridge},
		{"worktree", &m.Worktree},
		{"keys", &m.Keys},
		{"identity", &m.Identity},
		{"worktrees", &m.Worktrees},
		{"beads", &m.Beads},
		{"workflow", &m.Workflow},
		{"terminal", &m.Terminal},
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

	// A scalar under one of these names (a typo meant for a nested table, a
	// stray `docs = "x"` at the top level) is tolerated exactly like any
	// other unrecognized key — it falls through to the Unknown pass below
	// rather than hard-failing here. Before admin-tlhw.4, "docs"/"go"/etc.
	// were the only names the parser validated as tables (via the table()
	// helper), so a scalar there raised '[docs]' must be a table'; now that
	// no name is specially known to the parser, treating a settings-shaped
	// key differently from any other unknown key by type would just be the
	// closed registry again, one field at a time. This also warns rather than
	// fails on an unrecognized key regardless of its shape.
	m.Settings = map[string]map[string]any{"deploy": deploy}
	for k, v := range data {
		if nonSettingsTopLevelKeys[k] {
			continue
		}
		if tbl, ok := v.(map[string]any); ok {
			m.Settings[k] = tbl
		}
	}

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

// Starter is the narrow shape `admin new` (cmd/new.go) writes: a starter
// admin.toml built from detect.Run's proposal, before anything has ever run
// against it. It is deliberately not the full Manifest struct above — a
// starter manifest never has commands, guards, or any of the other tables a
// hand-edited admin.toml grows over time — so Dump only needs to serialize
// the fields `admin new` ever populates.
type Starter struct {
	ProjectName string
	Archetypes  []string
	Modules     []string
	Urls        map[string]string
	// Tables is detect.Result.Tables verbatim: one settings table per name,
	// each a map of string keys (e.g. "react_native" -> {"app_dir": "."}).
	Tables map[string]map[string]string
}

// Dump serializes a Starter to admin.toml text, scoped to only the fields a
// starter manifest carries (project_name, archetypes, modules, urls, and one
// level of scalar-valued settings tables) rather than the full multi-table
// dump `admin compile` grows into. Stdlib has no TOML writer, so this is a
// hand-rolled minimal emitter for exactly this shape, not a general-purpose
// serializer.
func Dump(s Starter) string {
	var b strings.Builder
	fmt.Fprintf(&b, "project_name = %s\n", tomlQuote(s.ProjectName))
	fmt.Fprintf(&b, "archetypes = %s\n", tomlStringList(s.Archetypes))
	if len(s.Modules) > 0 {
		fmt.Fprintf(&b, "modules = %s\n", tomlStringList(s.Modules))
	}
	b.WriteString("\n")

	if len(s.Urls) > 0 {
		b.WriteString("[urls]\n")
		for _, label := range sortedKeys(s.Urls) {
			fmt.Fprintf(&b, "%s = %s\n", label, tomlQuote(s.Urls[label]))
		}
		b.WriteString("\n")
	}

	for _, name := range sortedKeys(s.Tables) {
		tbl := s.Tables[name]
		fmt.Fprintf(&b, "[%s]\n", name)
		for _, key := range sortedKeys(tbl) {
			fmt.Fprintf(&b, "%s = %s\n", key, tomlQuote(tbl[key]))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// tomlStringList renders values as a TOML inline array of quoted strings.
func tomlStringList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = tomlQuote(v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// tomlQuote renders s as a TOML basic string. Go's fmt "%q" was tried here
// first and rejected: it escapes control bytes as \a, \v and \xHH, none of
// which are legal TOML string escapes (TOML's basic-string grammar only
// allows \b \t \n \f \r \" \\ \uXXXX \UXXXXXXXX) — a ProjectName or URL
// containing such a byte round-tripped through %q would write an admin.toml
// that this same package's own Parse (via github.com/BurntSushi/toml)
// rejects with "invalid escape in string".
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
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

// Command looks up name in Custom (project-declared, [commands.custom.X])
// then Overrides ([commands.X], project or archetype) — the same
// custom-shadows-archetype precedence resolveCommand applies at dispatch
// time — and reports whether it was found at all.
func (m *Manifest) Command(name string) (Command, bool) {
	if c, ok := m.Custom[name]; ok {
		return c, true
	}
	c, ok := m.Overrides[name]
	return c, ok
}

// SortCommandNames orders names by each command's declared Group, then
// Priority, then name — the fleet-standard build/dev/deploy layout (admin
// skill Phase 2c: group 1 = build/dev/deploy in that order, an automatic
// spacer, then group 2 sorted by priority). A name with no Group/Priority
// set (HasGroup/HasPriority false) carries the zero value, which sorts
// first — matching internal/tui's declaredCommands ordering exactly, so
// `admin cmd`'s picker and the full TUI's Work tab never disagree about
// command order. Stable: ties beyond the three keys keep their input order,
// though the third key (name) leaves no real ties to break.
func (m *Manifest) SortCommandNames(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		ci, _ := m.Command(names[i])
		cj, _ := m.Command(names[j])
		if ci.Group != cj.Group {
			return ci.Group < cj.Group
		}
		if ci.Priority != cj.Priority {
			return ci.Priority < cj.Priority
		}
		return names[i] < names[j]
	})
}

// knownCommandKeys is the ADR-0003 pure-routing key set.
var knownCommandKeys = map[string]bool{
	"desc": true, "steps": true, "log_file": true, "log_retain": true,
	"log": true, "group": true, "priority": true, "env": true,
	"preflight": true,
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
		case "preflight":
			b, ok := v.(bool)
			if !ok {
				return cmd, errf("'preflight' must be a boolean")
			}
			cmd.Preflight, cmd.HasPreflight = b, true
		default:
			cmd.Raw[k] = v
		}
	}
	_ = knownCommandKeys // referenced for documentation/symmetry
	return cmd, nil
}

// parseGuardTables parses the [guards.<name>] tables into user-defined Guards.
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
					"Built-in guards are registered by internal/guards and cannot be redefined in TOML.",
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

// guardsBuiltin names a [guards.<name>] table may not use.
var guardsBuiltin = map[string]bool{"git-clean": true, "git-pushed": true, "deps-fresh": true}

func sortedBuiltinGuards() []string {
	out := make([]string, 0, len(guardsBuiltin))
	for k := range guardsBuiltin {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- small shape-checking helpers ---

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

// truthy reports whether v is truthy for deploy.get(key) checks: nil/absent
// → false; string → v != ""; bool → v; []any/map[string]any → len > 0;
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

func sortedKeys[V any](m map[string]V) []string {
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
