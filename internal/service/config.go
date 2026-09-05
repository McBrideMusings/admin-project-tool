// Package service manages an always-on user service driven by the `[launchd]`
// table in admin.toml — macOS launchd locally, Linux systemd --user locally,
// or either one on a remote host over SSH. The Go port of admin_lib/launchd.py
// (deleted in 71451f2 as "never had a kind factory"; kind="launchd" is that
// factory, and this is the restored implementation behind it).
//
// Any project shipping a long-running process the developer wants alive at
// login declares `[launchd]` and wires a `service` command; the plist/unit is
// generated here rather than committed, because every value in one is
// machine-specific (the binary lives wherever the toolchain installed it, the
// log dir is under $HOME).
//
// Orthogonal to the stack archetypes on purpose. A Go supervisor, a Python
// daemon and a shell script all register with launchd/systemd identically, so
// this is a module a project opts into, not an archetype.
//
// The `program` key is always the INSTALLED path the service runs from. An
// optional `source` key names a freshly built binary elsewhere; when set,
// `install` atomically swaps it into `program` (see installBinary) before
// touching the service — the safe way to redeploy a binary that may currently
// be running.
//
// Four things here are less obvious than they look, and all were paid for once:
//
// Minimal environment and login-shell-only dotenv files. launchd hands an
// agent /usr/bin:/bin:/usr/sbin:/sbin and HOME and nothing else — no secrets a
// developer keeps in ~/.claude/.env as `export FOO=bar` lines sourced by
// ~/.zshrc for *interactive* shells only. A non-interactive `admin deploy` (the
// shape every agent and CI run uses) sees zero of those vars, so
// env = { FOO = "${FOO}" } in [launchd] silently resolves to nothing.
// env_file + env_file_keys read the dotenv file directly at generation time
// instead of trusting the shell to have sourced it, tolerating the `export `
// prefix a plain KEY=value parser rejects.
//
// PATH. Same empty environment: a process that shells out to anything from
// Homebrew or a language toolchain starts fine and then fails on its first real
// unit of work, which reads as the program being broken rather than the
// environment being empty. inherit_path (default true) writes the PATH of the
// shell running `admin` into the plist.
//
// Re-registration. macOS 13+ Background Task Management records every launchd
// item and posts an "App Background Activity" notification the first time it
// sees one, keyed on the plist being installed rather than the process
// starting — so a deploy that rewrites an unchanged plist and does
// bootout+bootstrap produces a fresh notification every single time. Install
// renders the plist to bytes first and compares with what is on disk:
// byte-identical and already loaded means the only correct action is
// `launchctl kickstart -k`, which swaps the running process for the newly
// built binary and touches no BTM record.
//
// Binary swap under a live daemon. Writing through a running daemon's inode
// invalidates its mapped text pages: macOS refuses to spawn the new file with
// OS_REASON_CODESIGNING even though `codesign -v` passes; Linux hits ETXTBSY.
// installBinary writes a sibling temp file (its own inode) and renames it into
// place, then ad-hoc re-signs on macOS so the kernel accepts the swapped-in
// file regardless of how it was produced.
package service

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// Usage is what `admin service` with no verb prints.
const Usage = `usage: admin service <install|uninstall|status|restart|stop|start>

  install    write the LaunchAgent plist and load it (idempotent)
  uninstall  unload the agent and remove the plist
  status     report whether the agent is loaded, its pid and last exit code
  restart    kickstart the agent, replacing the running process
  stop       unload the agent but leave the plist in place
  start      load an already-installed agent`

// KnownKeys is the closed set of `[launchd]` keys. An unknown key is a config
// error, not a silently ignored line — a typo'd `stdout_path` that quietly
// produced a service with no log is the failure this prevents.
var KnownKeys = []string{
	"label", "program", "source", "args", "args_cmd", "env", "inherit_path", "inherit_home",
	"stdout", "stderr", "working_dir", "keep_alive", "run_at_load",
	"throttle", "process_type", "nice", "start_interval",
	"env_file", "env_file_keys",
}

// Reporter is the subset of the host's output surface this package needs, so
// it stays free of an internal/luahost import.
type Reporter interface {
	Info(msg string)
	Ok(msg string)
	Warn(msg string)
	Err(msg string)
}

// Config is the normalised `[launchd]` table.
type Config struct {
	Label         string
	Program       string
	Source        string
	Args          []string
	Env           map[string]string
	InheritPath   bool
	InheritHome   bool
	KeepAlive     bool
	RunAtLoad     bool
	Throttle      *int
	ProcessType   string
	Nice          *int
	StartInterval *int
	Stdout        string
	Stderr        string
	WorkingDir    string
}

// ReadConfig validates and normalises the `[launchd]` table for a local
// install — `args_cmd`, if set, runs on this machine.
func ReadConfig(raw map[string]any, r Reporter) (Config, error) {
	return readConfig(raw, "", r)
}

// ReadConfigForHost is ReadConfig for a remote install: `args_cmd` runs over
// SSH on host instead of on this machine, since a command like `tailscale ip
// -4` answers for whichever machine runs it — evaluating it locally silently
// bakes the DEPLOYING machine's address into the REMOTE service's bind flags.
func ReadConfigForHost(raw map[string]any, host string, r Reporter) (Config, error) {
	return readConfig(raw, host, r)
}

func readConfig(raw map[string]any, host string, r Reporter) (Config, error) {
	var unknown []string
	for k := range raw {
		known := false
		for _, valid := range KnownKeys {
			if k == valid {
				known = true
				break
			}
		}
		if !known {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return Config{}, fmt.Errorf("[launchd]: unknown keys: %v, valid: %v", unknown, KnownKeys)
	}

	rv := &resolver{}
	c := Config{
		Label:         rv.s(str(raw, "label")),
		Program:       rv.s(str(raw, "program")),
		Source:        rv.s(str(raw, "source")),
		Args:          rv.all(strList(raw, "args")),
		Env:           map[string]string{},
		InheritPath:   boolOr(raw, "inherit_path", true),
		InheritHome:   boolOr(raw, "inherit_home", true),
		KeepAlive:     boolOr(raw, "keep_alive", true),
		RunAtLoad:     boolOr(raw, "run_at_load", true),
		Throttle:      intPtr(raw, "throttle"),
		ProcessType:   orDefault(rv.s(str(raw, "process_type")), "Background"),
		Nice:          intPtr(raw, "nice"),
		StartInterval: intPtr(raw, "start_interval"),
		Stdout:        expand(rv.s(str(raw, "stdout"))),
		Stderr:        expand(rv.s(str(raw, "stderr"))),
		WorkingDir:    expand(rv.s(str(raw, "working_dir"))),
	}

	if rv.firstErr != nil {
		return Config{}, fmt.Errorf("[launchd]: %w", rv.firstErr)
	}
	if c.Label == "" {
		return Config{}, fmt.Errorf("[launchd]: 'label' is required (e.g. com.example.myserver)")
	}
	if c.Program == "" {
		return Config{}, fmt.Errorf("[launchd]: 'program' is required (absolute path to the executable)")
	}
	c.Program = expand(c.Program)
	if !filepath.IsAbs(c.Program) {
		return Config{}, fmt.Errorf("[launchd]: 'program' must be an absolute path, got %q", c.Program)
	}
	if c.Source != "" {
		c.Source = expand(c.Source)
	}

	// env_file/env_file_keys: lift an ALLOWLISTED set of names out of a dotenv
	// file at generation time rather than trusting the deploying shell to have
	// sourced it — see the package doc. The allowlist is mandatory: without
	// env_file_keys nothing is lifted, since a dotenv file used this way
	// typically holds many unrelated secrets that must never end up in a
	// LaunchAgent plist wholesale.
	envFile := expand(rv.s(str(raw, "env_file")))
	envKeys := strList(raw, "env_file_keys")
	if envFile != "" && len(envKeys) > 0 {
		loaded := parseEnvFile(envFile, r)
		for _, k := range envKeys {
			if v, ok := loaded[k]; ok && v != "" {
				c.Env[k] = v
			}
		}
	}
	// Explicit `env` wins over anything lifted from the file.
	for k, v := range mapOf(raw, "env") {
		c.Env[k] = rv.s(v)
	}

	// args_cmd: a shell command whose stdout is split on whitespace and
	// appended to args, evaluated at install time. Some services can only be
	// told what to bind to once the machine is in front of you — a tailnet
	// address that exists only while the VPN is up, a port picked from what is
	// free, an interface name. `${VAR}` cannot express that: it resolves
	// against the environment of a non-interactive `admin deploy`, which is
	// exactly the shell that knows none of it. A command that prints nothing,
	// or fails, contributes no args rather than failing the install — a
	// service that falls back to a narrower bind address is right, and one
	// that refuses to deploy because the VPN is down is not.
	if cmdStr := rv.s(str(raw, "args_cmd")); cmdStr != "" {
		var cmd *exec.Cmd
		if host != "" {
			cmd = exec.Command("ssh", host, cmdStr)
		} else {
			cmd = exec.Command("/bin/sh", "-c", cmdStr)
		}
		out, err := cmd.Output()
		if err != nil {
			r.Warn(fmt.Sprintf("[launchd]: args_cmd failed, no extra args added: %v", err))
		} else if extra := strings.Fields(string(out)); len(extra) > 0 {
			c.Args = append(c.Args, extra...)
		}
	}

	return c, nil
}

// parseEnvFile reads KEY=value lines out of a dotenv-style file. Export-prefix
// tolerant and quote-stripping. A missing file is not fatal — it just means the
// caller's allowlisted keys resolve to nothing.
func parseEnvFile(path string, r Reporter) map[string]string {
	out := map[string]string{}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		if path != "" {
			r.Warn(fmt.Sprintf("[launchd]: env_file %q not found — no values loaded from it", path))
		}
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimLeft(line, " \t")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == value[len(value)-1] && (value[0] == '"' || value[0] == '\'') {
			value = value[1 : len(value)-1]
		}
		if key != "" {
			out[key] = value
		}
	}
	return out
}

// --- small config readers -------------------------------------------------

// resolver expands ${VAR} with manifest.ResolveEnv — against the real
// environment — rather than ResolveComputed, which expands only the small
// ComputedVars set and deliberately leaves a plain ${HOME} intact for a shell
// to handle later. Nothing in this package reaches a shell: `program` is
// exec'd by launchd from a plist, so an unexpanded "${HOME}/..." is a literal
// path that does not exist. Errors are collected rather than returned per
// call, so ReadConfig's assignments stay one expression each; err() reports
// the first one.
type resolver struct{ firstErr error }

func (r *resolver) s(v string) string {
	out, err := manifest.ResolveEnv(v)
	if err != nil {
		if r.firstErr == nil {
			r.firstErr = err
		}
		return v
	}
	return out
}

func (r *resolver) all(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = r.s(v)
	}
	return out
}

func expand(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func strList(m map[string]any, key string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, fmt.Sprint(v))
	}
	return out
}

func mapOf(m map[string]any, key string) map[string]string {
	raw, ok := m[key].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, v := range raw {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func boolOr(m map[string]any, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

func intPtr(m map[string]any, key string) *int {
	switch v := m[key].(type) {
	case int64:
		n := int(v)
		return &n
	case int:
		return &v
	case float64:
		n := int(v)
		return &n
	}
	return nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
