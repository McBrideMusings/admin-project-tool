// Package config loads the populate patterns from a global and a per-repo TOML
// file. Per-repo entries are appended after global ones. There are no built-in
// defaults — with no config, populate does nothing.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
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
// added. Both are seeded on first run (see Seed) and editable afterwards.
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

// Config is the merged global + per-repo configuration.
type Config struct {
	Populate  []Entry   `toml:"populate"`
	Worktrees Worktrees `toml:"worktrees"`
	Identity  Identity  `toml:"identity"`
	Beads     Beads     `toml:"beads"`
	Workflow  Workflow  `toml:"workflow"`
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

// GlobalPath is ~/.config/repo/config.toml.
func GlobalPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "repo", "config.toml")
}

// RepoPath is <repoRoot>/.repo/config.toml.
func RepoPath(repoRoot string) string {
	return filepath.Join(repoRoot, ".repo", "config.toml")
}

// Load reads the global config then the per-repo config, appending per-repo
// entries after global ones. Missing files are not errors.
func Load(repoRoot string) (Config, error) {
	var cfg Config
	for _, path := range []string{GlobalPath(), RepoPath(repoRoot)} {
		if path == "" {
			continue
		}
		var part Config
		if _, err := toml.DecodeFile(path, &part); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return cfg, err
		}
		cfg.Populate = append(cfg.Populate, part.Populate...)
		// Per-repo root (read second) overrides the global one.
		if strings.TrimSpace(part.Worktrees.Root) != "" {
			cfg.Worktrees.Root = part.Worktrees.Root
		}
		// Identity merges the way the two files differ in kind: there is one of you,
		// so a per-repo login replaces the global one, but a work address is an
		// addition to your identity rather than a replacement for it — appending is
		// what lets `.repo/config.toml` say "here I also commit as pierce@work" without
		// disowning every branch authored under the personal address.
		if l := strings.TrimSpace(part.Identity.Login); l != "" {
			cfg.Identity.Login = l
		}
		cfg.Identity.Emails = appendEmails(cfg.Identity.Emails, part.Identity.Emails)
		// A column list is an ordering, so a per-repo one replaces the global list
		// outright: appending would interleave two orderings into a third nobody
		// wrote.
		if len(part.Beads.Columns) > 0 {
			cfg.Beads.Columns = part.Beads.Columns
		}
		// Workflow, like Identity.Login, is a single current value rather than
		// something to accumulate: a per-repo agent/ceiling replaces the global
		// one outright.
		if a := strings.TrimSpace(part.Workflow.Agent); a != "" {
			cfg.Workflow.Agent = a
		}
		if part.Workflow.Ceiling > 0 {
			cfg.Workflow.Ceiling = part.Workflow.Ceiling
		}
	}
	// Default any unset mode to symlink.
	for i := range cfg.Populate {
		if cfg.Populate[i].Mode == "" {
			cfg.Populate[i].Mode = Symlink
		}
	}
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

// appendEmails adds addresses not already present, case-insensitively. The same
// address reaching both files is the normal case (the global identity plus a
// per-repo file that repeats it), and a duplicate would be harmless but would show
// up doubled in the Config tab.
func appendEmails(dst, src []string) []string {
	for _, e := range src {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		dup := false
		for _, have := range dst {
			if strings.EqualFold(have, e) {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, e)
		}
	}
	return dst
}

// SeedIdentity writes a starter [identity] into the GLOBAL config when it has none,
// so the first launch classifies branches instead of showing everything as
// unowned. login and email come from the environment (`gh api user` and
// `git config user.email`); either may be empty, and nothing is written unless at
// least one of them resolved.
//
// It appends rather than rewriting: the file is hand-edited, and a config writer
// that reformats someone's comments away to add two lines is a worse trade than
// the duplicate-key risk, which the guard below removes anyway. Returns whether it
// wrote, so a caller can say so once rather than every launch.
//
// Only the global file is seeded. A per-repo identity means "in this repo I am
// also someone else", which is a deliberate statement no default can make.
func SeedIdentity(login, email string) (bool, error) {
	path := GlobalPath()
	if path == "" {
		return false, nil
	}
	login, email = strings.TrimSpace(login), strings.TrimSpace(email)
	if login == "" && email == "" {
		return false, nil
	}
	// Never seed over an identity that already exists — including one that names
	// only emails, since a login added by hand later must not be re-guessed.
	var have Config
	if _, err := toml.DecodeFile(path, &have); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if have.Identity.Known() {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	var b strings.Builder
	if existing, err := os.ReadFile(path); err == nil && len(existing) > 0 {
		b.Write(existing)
		if !strings.HasSuffix(string(existing), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("# Who \"you\" are: which branches the dashboard treats as your work.\n")
	b.WriteString("# Seeded from `gh api user` and `git config user.email` on first run.\n")
	b.WriteString("# Add any other address you commit under — a work address, another\n")
	b.WriteString("# machine, an agent run — to claim those branches too.\n")
	b.WriteString("[identity]\n")
	if login != "" {
		b.WriteString("login = " + quote(login) + "\n")
	}
	if email != "" {
		b.WriteString("emails = [" + quote(email) + "]\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// quote renders a TOML basic string. Logins and emails cannot contain a quote or a
// backslash, so escaping them is enough to keep a hand-edited file parseable.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
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
