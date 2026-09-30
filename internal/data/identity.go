package data

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
)

// DefaultHost is the GitHub host whose account the dashboard resolves.
const DefaultHost = "github.com"

// Identity is who "you" are on one GitHub host, as `gh api user` reports it. It
// decides whether a branch is your work or a co-contributor's. The zero Identity
// is "unknown": a cold cache or a failed `gh` call leaves it zero, and ownership
// then reads every attributed branch as unknown rather than guessed.
//
// One account spans every repo on its host, so the cache holds one entry per host
// (identity-<host>.json beside the per-repo files), not one per repo.
type Identity struct {
	Login     string    `json:"login"`
	Email     string    `json:"email"` // the public email; empty when the account has none
	ID        int64     `json:"id"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Known reports whether the identity has enough to answer ownership at all.
func (id Identity) Known() bool { return id.Login != "" }

// Stale reports whether the identity is older than TTL or was never fetched.
func (id Identity) Stale() bool { return time.Since(id.FetchedAt) >= TTL }

// Emails are the git author addresses that count as yours: the public email
// (when set) and GitHub's noreply form `<id>+<login>@users.noreply.github.com`.
func (id Identity) Emails() []string {
	if !id.Known() {
		return nil
	}
	var out []string
	if e := strings.TrimSpace(id.Email); e != "" {
		out = append(out, e)
	}
	if id.ID != 0 {
		out = append(out, fmt.Sprintf("%d+%s@users.noreply.github.com", id.ID, id.Login))
	}
	return out
}

// Owns reports whether a git author email is one of yours. Comparison is
// case-insensitive: git preserves the case a commit was authored with, and the
// same person's address turns up capitalized differently across machines.
func (id Identity) Owns(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, e := range id.Emails() {
		if strings.ToLower(e) == email {
			return true
		}
	}
	return false
}

// IsMe reports whether a GitHub login is yours. Empty never matches: an unknown
// login and your login are different facts, and treating a missing one as a match
// would hand you every branch whose PR author could not be read.
func (id Identity) IsMe(login string) bool {
	return id.Known() && strings.EqualFold(strings.TrimSpace(login), id.Login)
}

func identityPath(host string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "repo", "identity-"+filepathSafe(host)+".json")
}

// LoadIdentity reads the cached identity for host regardless of age. The zero
// Identity (ok=false) means nothing is cached yet.
func LoadIdentity(host string) (Identity, bool) {
	var id Identity
	b, err := os.ReadFile(identityPath(host))
	if err != nil || json.Unmarshal(b, &id) != nil || !id.Known() {
		return Identity{}, false
	}
	return id, true
}

func saveIdentity(host string, id Identity) {
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return
	}
	writeAtomic(identityPath(host), b)
}

// fetchViewerFn is the seam tests replace so no test reaches `gh`.
var fetchViewerFn = gh.Viewer

// FetchIdentity returns the identity for host: the cached one while it is within
// TTL, otherwise a fresh `gh api user` result, written back to the cache. When the
// fetch fails the zero Identity is returned with the error, whatever an expired
// cache entry held: after a sign-out or account switch that entry names someone
// else, and ownership stays unknown rather than guessed.
func FetchIdentity(ctx context.Context, host string) (Identity, error) {
	cached, _ := LoadIdentity(host)
	if cached.Known() && !cached.Stale() {
		return cached, nil
	}
	u, err := fetchViewerFn(ctx, host)
	if err != nil {
		return Identity{}, err
	}
	id := Identity{Login: u.Login, Email: u.Email, ID: u.ID, FetchedAt: time.Now()}
	saveIdentity(host, id)
	return id, nil
}
