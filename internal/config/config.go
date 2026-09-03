// Package config is the typed view over the six admin.toml tables that
// configure the dashboard rather than any one command: [identity],
// [worktrees], [[populate]], [beads], [workflow] and [terminal]. It holds no
// file-reading logic of its own — Load resolves a project's manifest
// (internal/manifest + internal/archetype, merged through archetypes exactly
// like every other admin.toml key) and decodes those six tables into the
// typed shapes below. There are no built-in defaults for populate — with
// none declared, populate does nothing.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/timing"
)

// Mode is how a populate entry brings a file into a new worktree.
type Mode string

const (
	// Symlink points the worktree file at the primary repo's file (default).
	Symlink Mode = "symlink"
	// Copy duplicates the file so each worktree can diverge.
	Copy Mode = "copy"
)

// Entry is one populate rule: a glob pattern (relative to the repo root) and how
// to bring matches into a new worktree.
type Entry struct {
	Pattern string `toml:"pattern"`
	Mode    Mode   `toml:"mode"`
}

// Worktrees configures where worktrees are created. Root is the parent under
// which each repo gets a <root>/<repo>/<slug> tree; it is shared with sibling
// tools that honor the same root. Empty means the built-in default.
type Worktrees struct {
	Root string `toml:"root"`
}

// Identity is who "you" are, which decides whether a branch is your work or a
// co-contributor's. It is configuration rather than inference because the thing it
// settles is not answerable from the repository: a commit authored under a second
// email — a work address, another machine, an agent run — looks exactly like
// somebody else's commit, and only the person reading the dashboard knows which.
//
// Login is the GitHub account, matched against a PR's author. Emails are the git
// author addresses that count as yours, matched against the first commit a branch
// added. Both are declared in the project's admin.toml [identity] table.
type Identity struct {
	Login  string   `toml:"login"`
	Emails []string `toml:"emails"`
}

// Beads configures the Beads tab. Columns names the columns the pane renders and
// the order it renders them in; an empty list means the pane's own default set.
// It is configuration rather than a fixed layout because who else is in a
// workspace decides whether "owner" is information or a wasted column — solo work
// makes it nine cells of the same name on every row.
type Beads struct {
	Columns []string `toml:"columns"`
}

// Workflow configures dispatching a kind="workflow" action's targets
// (admin-3rgp.25): which herdr agent kind a target runs when its own action
// declares no `agent` override, and how many targets run at once when the
// action declares no ceiling of its own. Empty/zero mean "unset" — the
// dispatcher's own DefaultCeiling and a required, explicitly-set agent are
// what actually apply, not a value invented here.
type Workflow struct {
	Agent   string `toml:"agent"`
	Ceiling int    `toml:"ceiling"`
}

// Terminal configures the ADR-0020 launcher contract: how a dispatched
// action gets its own terminal instead of the dashboard's hosted pty. Spawn,
// Attach and Kill are required together (validated by internal/validate.
// Terminal); Status is optional. Empty means no launcher is declared, and
// nothing dispatches through this table yet (admin-ysxr.1 is parse+validate
// only).
type Terminal struct {
	Spawn  string `toml:"spawn"`
	Attach string `toml:"attach"`
	Kill   string `toml:"kill"`
	Status string `toml:"status"`
}

// Config is the decoded [identity]/[worktrees]/[[populate]]/[beads]/
// [workflow]/[terminal] tables of a resolved (archetype-merged) manifest.
type Config struct {
	Populate  []Entry   `toml:"populate"`
	Worktrees Worktrees `toml:"worktrees"`
	Identity  Identity  `toml:"identity"`
	Beads     Beads     `toml:"beads"`
	Workflow  Workflow  `toml:"workflow"`
	Terminal  Terminal  `toml:"terminal"`
}

// Owns reports whether a git author email is one of yours. Comparison is
// case-insensitive: git preserves the case a commit was authored with, and the
// same person's address turns up capitalized differently across machines.
func (id Identity) Owns(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, e := range id.Emails {
		if strings.ToLower(strings.TrimSpace(e)) == email {
			return true
		}
	}
	return false
}

// IsMe reports whether a GitHub login is yours. Empty never matches: an unknown
// login and your login are different facts, and treating a missing one as a match
// would hand you every branch whose PR author could not be read.
func (id Identity) IsMe(login string) bool {
	return id.Login != "" && strings.EqualFold(strings.TrimSpace(login), id.Login)
}

// Known reports whether the identity has enough to answer ownership at all.
func (id Identity) Known() bool { return id.Login != "" || len(id.Emails) > 0 }

// DefaultWorktreeRoot is where worktrees land when nothing overrides it.
const DefaultWorktreeRoot = "~/.worktrees"

// Load resolves repoRoot's admin.toml against its declared archetypes — the
// same resolution every other manifest-driven feature uses (see
// internal/tui/work.go's loadWorkManifest, which this mirrors) — and decodes
// the six config tables from the merged result.
//
// A repoRoot with no admin.toml at all returns a zero Config and a nil
// error: create.Worktree and the TUI both depend on this degrade path, since
// a fresh worktree with no manifest of its own must fall back to the
// env-or-default worktree root and skip populate entirely, rather than
// erroring out of worktree creation altogether.
func Load(repoRoot string) (Config, error) {
	if _, err := os.Stat(filepath.Join(repoRoot, manifest.Filename)); err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	manifest.LoadDotenv(repoRoot)
	project, err := manifest.Load(repoRoot)
	if err != nil {
		return Config{}, err
	}
	sp, err := archetype.SearchPathFor(repoRoot)
	if err != nil {
		return Config{}, err
	}
	merged, _, err := archetype.Resolve(project, sp)
	if err != nil {
		return Config{}, err
	}
	return FromManifest(merged)
}

// FromManifest decodes the six config tables from an already-resolved
// manifest. Exported so a caller that already holds a resolved manifest (the
// TUI's Work tab, which loads and merges one at New for its own purposes)
// can build a Config from it without a second parse+resolve pass.
func FromManifest(m *manifest.Manifest) (Config, error) {
	if m == nil {
		return Config{}, nil
	}
	start := timing.Start()
	cfg := Config{
		Populate:  decodePopulate(m.Populate),
		Worktrees: decodeWorktrees(m.Worktrees),
		Identity:  decodeIdentity(m.Identity),
		Beads:     decodeBeads(m.Beads),
		Workflow:  decodeWorkflow(m.Workflow),
		Terminal:  decodeTerminal(m.Terminal),
	}
	timing.Phase("decode config tables", start)
	return cfg, nil
}

// WorktreeRoot resolves the parent directory for worktrees from an
// already-loaded config: the WORKTREE_ROOT env override wins, then the
// [worktrees] root config key, then DefaultWorktreeRoot. A leading ~ is expanded
// to the home dir. Pass a zero Config to force the env-or-default path (callers
// that failed to load config do this so a partial parse never leaks a root).
func WorktreeRoot(cfg Config) string {
	if env := strings.TrimSpace(os.Getenv("WORKTREE_ROOT")); env != "" {
		return expandHome(env)
	}
	if r := strings.TrimSpace(cfg.Worktrees.Root); r != "" {
		return expandHome(r)
	}
	return expandHome(DefaultWorktreeRoot)
}

// expandHome replaces a leading ~ or ~/ with the user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
		}
	}
	return p
}

// decodeIdentity, decodeWorktrees, decodeBeads, decodeWorkflow and
// decodePopulate pull the typed fields out of a manifest's raw
// map[string]any tables. internal/manifest keeps these tables untyped (ADR-
// 0009: the manifest references, the archetype implements) and
// internal/manifest.parse already rejects any key outside each table's known
// set, so decoding here only needs to handle the keys that survived parsing.

func decodeIdentity(raw map[string]any) Identity {
	var id Identity
	if v, ok := raw["login"].(string); ok {
		id.Login = v
	}
	if v, ok := raw["emails"]; ok {
		id.Emails = toStringList(v)
	}
	return id
}

func decodeWorktrees(raw map[string]any) Worktrees {
	var w Worktrees
	if v, ok := raw["root"].(string); ok {
		w.Root = v
	}
	return w
}

func decodeBeads(raw map[string]any) Beads {
	var b Beads
	if v, ok := raw["columns"]; ok {
		b.Columns = toStringList(v)
	}
	return b
}

func decodeWorkflow(raw map[string]any) Workflow {
	var w Workflow
	if v, ok := raw["agent"].(string); ok {
		w.Agent = v
	}
	if v, ok := raw["ceiling"]; ok {
		w.Ceiling = toInt(v)
	}
	return w
}

func decodeTerminal(raw map[string]any) Terminal {
	var t Terminal
	if v, ok := raw["spawn"].(string); ok {
		t.Spawn = v
	}
	if v, ok := raw["attach"].(string); ok {
		t.Attach = v
	}
	if v, ok := raw["kill"].(string); ok {
		t.Kill = v
	}
	if v, ok := raw["status"].(string); ok {
		t.Status = v
	}
	return t
}

func decodePopulate(raw []map[string]any) []Entry {
	out := make([]Entry, 0, len(raw))
	for _, e := range raw {
		var entry Entry
		if v, ok := e["pattern"].(string); ok {
			entry.Pattern = v
		}
		if v, ok := e["mode"].(string); ok {
			entry.Mode = Mode(v)
		}
		if entry.Mode == "" {
			entry.Mode = Symlink
		}
		out = append(out, entry)
	}
	return out
}

func toStringList(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func toInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}
