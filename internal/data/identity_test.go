package data

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
)

func isolateCache(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func stubViewer(t *testing.T, u gh.User, err error) *int {
	t.Helper()
	calls := 0
	prev := fetchViewerFn
	fetchViewerFn = func(context.Context, string) (gh.User, error) { calls++; return u, err }
	t.Cleanup(func() { fetchViewerFn = prev })
	return &calls
}

// TestIdentityOwnsPublicAndNoreplyAddresses pins the two owned addresses: the
// public email and the `<id>+<login>@users.noreply.github.com` form, matched
// case-insensitively, with an empty value never matching.
func TestIdentityOwnsPublicAndNoreplyAddresses(t *testing.T) {
	id := Identity{Login: "Octocat", Email: "Pierce@Example.com", ID: 4242}
	for _, e := range []string{"pierce@example.com", "4242+octocat@users.noreply.github.com"} {
		if !id.Owns(e) {
			t.Errorf("Owns(%q) = false", e)
		}
	}
	if id.Owns("") || id.Owns("chris@example.com") || id.IsMe("") {
		t.Error("an empty or foreign value matched")
	}
	if !id.IsMe("octocat") {
		t.Error("a login differing only in case did not match")
	}
	noPublic := Identity{Login: "p", ID: 7}
	if got := noPublic.Emails(); len(got) != 1 || got[0] != "7+p@users.noreply.github.com" {
		t.Errorf("Emails() with no public email = %v, want only the noreply form", got)
	}
	if (Identity{}).Known() || (Identity{}).Owns("x@y") {
		t.Error("the zero identity claimed something")
	}
}

// TestFetchIdentityCachesWithinTTL pins that a second call inside TTL reads the
// cache instead of calling gh, and that a call past TTL fetches again.
func TestFetchIdentityCachesWithinTTL(t *testing.T) {
	isolateCache(t)
	calls := stubViewer(t, gh.User{Login: "pierce", Email: "p@example.com", ID: 9}, nil)

	id, err := FetchIdentity(context.Background(), DefaultHost)
	if err != nil || id.Login != "pierce" || id.ID != 9 {
		t.Fatalf("first fetch = %+v, %v", id, err)
	}
	if _, err := FetchIdentity(context.Background(), DefaultHost); err != nil || *calls != 1 {
		t.Fatalf("second fetch inside TTL: err=%v, gh calls=%d, want 1", err, *calls)
	}
	id.FetchedAt = time.Now().Add(-2 * TTL)
	saveIdentity(DefaultHost, id)
	if _, err := FetchIdentity(context.Background(), DefaultHost); err != nil || *calls != 2 {
		t.Fatalf("fetch past TTL: err=%v, gh calls=%d, want 2", err, *calls)
	}
}

// TestFetchIdentityFailureIsNeverGuessed pins that a cold cache plus a gh failure
// yields the zero identity, and so does an expired cache plus a failure.
func TestFetchIdentityFailureIsNeverGuessed(t *testing.T) {
	isolateCache(t)
	stubViewer(t, gh.User{}, errors.New("not logged in"))

	id, err := FetchIdentity(context.Background(), DefaultHost)
	if err == nil || id.Known() {
		t.Fatalf("cold cache + gh failure = %+v, %v; want unknown and an error", id, err)
	}

	saveIdentity(DefaultHost, Identity{Login: "pierce", ID: 9, FetchedAt: time.Now().Add(-2 * TTL)})
	id, err = FetchIdentity(context.Background(), DefaultHost)
	if err == nil || id.Known() {
		t.Fatalf("stale cache + gh failure = %+v, %v; want unknown and an error", id, err)
	}
}
