package kinds

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// newHost returns a luahost.Host rooted at m's project root — the directory
// admin.toml was read from — sharing the process-wide luahost.Process() log
// context rather than a private one of its own. Every transient-kind factory
// calls this rather than hand-rolling its own output/logging/shell-running
// code.
//
// The root comes from the manifest, never from os.Getwd(): Host.ProjectRoot
// is what luahost.RunCmd hands every child process as its working directory,
// and taking it from the process's own cwd made every action run wherever
// `admin` was typed. That is invisible while a manifest is only ever found
// in the cwd, and wrong the moment it is found by walking up
// (manifest.FindRoot) — a `run` string would then execute one directory
// below the project it belongs to. os.Getwd() remains the fallback for a nil
// or root-less manifest (Parse/ParsePartial set no Root), which is the shape
// a test constructs. luahost.Host already carries
// Info/Ok/Warn/Err (coloured output + log-file appends), RunCmd (stream a
// shell command, capture output), and PickTarget/OnlyTarget (the target
// picker) — every one of those was written once for the Lua host API and is
// exactly what a non-Lua kind factory needs too, so this reuses it rather
// than duplicating it a second time (the repo's "import it, don't rewrite
// it" rule from the OutputPump history applies here just as much as it did
// there).
//
// Sharing luahost.Process() — instead of each of a command's ~26 possible
// kind-factory Hosts getting its own private log context — is admin-rc82's
// whole point: a command's step-runner names the log once (dispatch.go's
// runSteps) and every step's own Host, whatever kind it dispatches to, must
// materialize into that same file rather than each opening (and announcing)
// its own. The shared context's announce line is what the pinned footer and
// the hosted-pty sniffer both read, so it must land on THIS Host's own
// Stderr, not wherever the context's Stderr happened to be pointed by
// whichever Host materialized it — every newHost caller wants the log path
// announced on its own stream.
func newHost(m *manifest.Manifest) *luahost.Host {
	h := luahost.New(projectRoot(m))
	ctx := luahost.Process()
	ctx.SetStderr(h.Stderr)
	h.UseLogContext(ctx)
	return h
}

// projectRoot is the directory every action's subprocess runs in: the
// directory m's admin.toml was read from. It is the single answer to "where
// does this action run", so a `run` string never needs its own `cd` to be
// independent of where `admin` was typed — which matters because admin.toml
// is seeded into a git worktree by a straight file copy, and an absolute
// `cd` baked into a run string survives that copy pointing at the checkout
// it was written in.
//
// The cwd fallback covers a manifest built by Parse/ParsePartial rather than
// Load (an archetype fragment, a test), which has no directory of its own.
func projectRoot(m *manifest.Manifest) string {
	if m != nil && m.Root != "" {
		return m.Root
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// resolveUnder turns a manifest-declared directory into an absolute path,
// interpreting a relative one against root (the project root) rather than
// against the process's cwd. An already-absolute value is returned as-is, so
// a manifest that names a path outside the project still reaches it.
func resolveUnder(root, dir string) string {
	if dir == "" {
		return root
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(root, dir)
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

// actionBool reads a bool-valued key off a raw manifest.Action. present is
// false both when the key is absent and when it holds a non-bool value —
// mirrors admin_lib's cmd.meta.get("footer") returning None either way; a
// caller that needs to tell "absent" from "wrong type" apart validates the
// manifest separately (admin-3rgp.8's job, not this ticket's).
func actionBool(a manifest.Action, key string) (val bool, present bool) {
	v, ok := a[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// actionURLs reads the raw `urls` key off a manifest.Action as the []any
// TOML decodes an array into — frame.ResolveURLs takes it from here.
// Absent or non-list is treated the same as "no urls declared" (nil); a
// non-list value is validate.go's job to reject, not this ticket's.
func actionURLs(a manifest.Action) []any {
	v, ok := a["urls"].([]any)
	if !ok {
		return nil
	}
	return v
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

// cfgString reads a string-valued key off a raw config table (as decoded
// from TOML into map[string]any by a [xxx_config]-style manifest table),
// returning def when the key is absent or not a string.
func cfgString(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return def
}

// cfgInt reads an int-valued key off a raw config table. TOML integers
// decode as int64 via BurntSushi/toml; int is accepted too for callers
// building a table by hand (tests).
func cfgInt(m map[string]any, key string, def int) int {
	switch n := m[key].(type) {
	case int64:
		return int(n)
	case int:
		return n
	default:
		return def
	}
}

// cfgBool reads a bool-valued key off a raw config table, returning def when
// the key is absent or not a bool.
func cfgBool(m map[string]any, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

// cfgList reads a list-valued key off a raw config table — TOML decodes a
// TOML array as []any. Returns nil for an absent or non-list key.
func cfgList(m map[string]any, key string) []any {
	v, _ := m[key].([]any)
	return v
}

// cfgMap reads a table-valued key off a raw config table — TOML decodes a
// TOML table as map[string]any. Returns an empty (never nil) map for an
// absent or non-table key, so callers can index it unconditionally.
func cfgMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

// cfgStrings reads a list-valued key off a raw config table as a []string,
// skipping any element that isn't a string — used for the plain string-list
// tables like [dev_kill].js_files/web_subdirs/runtimes.
func cfgStrings(m map[string]any, key string) []string {
	var out []string
	for _, v := range cfgList(m, key) {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// cfgStringMap reads every string-valued key off a raw table — used for
// entries like backend_extra_env / dev_env, which the manifest declares as
// {NAME = "value", ...}.
func cfgStringMap(m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
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

// envResolver expands ${VAR} in a CONFIG VALUE with manifest.ResolveEnv —
// against the real environment — rather than ResolveComputed, which expands
// only the small ComputedVars set and deliberately leaves a plain ${HOME}
// intact for a shell to expand later. That leniency is right for an action's
// `run` string, which does reach a shell, and wrong for every value consumed
// in Go: an unexpanded "${HOME}/..." is a literal path that does not exist,
// and a dev_env entry set to the literal "${CLIENT_URL:-http://...}" is what
// the child process then reads.
//
// Errors are collected rather than returned per call, so a config builder's
// assignments stay one expression each; the factory checks firstErr once.
type envResolver struct{ firstErr error }

func (r *envResolver) s(v string) string {
	out, err := manifest.ResolveEnv(v)
	if err != nil {
		if r.firstErr == nil {
			r.firstErr = err
		}
		return v
	}
	return out
}

func (r *envResolver) all(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = r.s(v)
	}
	return out
}
