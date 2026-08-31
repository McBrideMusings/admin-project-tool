package kinds

import (
	"os"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// newHost returns a luahost.Host rooted at the process's current directory.
// Every transient-kind factory shares this rather than hand-rolling its own
// output/logging/shell-running code: the interpreter cd's to the project
// root at boot (documented in this repo's Guards section — "the cwd is
// inherited") exactly as the Python interpreter's own module-level
// _project_root does, so os.Getwd() at Runner-invocation time IS the
// project root. luahost.Host already carries Info/Ok/Warn/Err (coloured
// output + log-file appends), RunCmd (stream a shell command, capture
// output), and PickTarget/OnlyTarget (the target picker) — every one of
// those was written once for the Lua host API and is exactly what a
// non-Lua kind factory needs too, so this reuses it rather than
// duplicating it a second time (the repo's "import it, don't rewrite it"
// rule from the OutputPump history applies here just as much as it did
// there).
func newHost() *luahost.Host {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	return luahost.New(cwd)
}

// actionString reads a string-valued key off a raw manifest.Action. ok is
// false both when the key is absent and when it holds a non-string value —
// callers that need to tell those apart (none currently do) can read the
// map directly instead.
func actionString(a manifest.Action, key string) (string, bool) {
	v, present := a[key]
	if !present {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// actionStringList reads a []string-valued key off a raw manifest.Action —
// TOML decodes an array as []any, so each element is type-asserted
// individually. Returns (nil, true) for an absent key (no excludes/hosts/
// etc. declared is not an error) and (nil, false) only when the key is
// present but not a list of strings.
func actionStringList(a manifest.Action, key string) ([]string, bool) {
	v, present := a[key]
	if !present {
		return nil, true
	}
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

// orDefault returns s unless it's empty, in which case it returns def.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// shellQuote wraps s in single quotes for safe inclusion in a /bin/sh -c
// command line, escaping any single quote it contains — the same
// shlex.quote job the Python interpreter's kind factories lean on for
// shell-passthrough argument forwarding and kind="go"'s -o path.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, "\t\n '\"$&*()[]{}|;<>?!~`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sortedStringKeys returns m's keys sorted — used wherever a kind factory
// needs a stable display or dispatch order over a raw decoded TOML table
// (multi-target's inline target subtables and flat targets table alike),
// since map iteration order is not.
func sortedStringKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
