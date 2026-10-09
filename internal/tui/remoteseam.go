package tui

import (
	"context"

	"github.com/McBrideMusings/admin-project-tool/internal/data"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// UseStaticRemote makes every remote read this package performs answer from r
// instead of reaching out: loadCacheCmd's on-disk cache read, syncCmd's `gh`
// round trip, syncCmd's `git fetch --prune` and the identity lookup all become
// no-ops that return r (or nothing at all), and the local reload resolves its
// fork base from refs already on disk (git.KnownBase) instead of fetching
// origin's default branch first. It returns a restore func that
// puts the real implementations back — callers must defer it.
//
// This is the seam for a headless caller that wants to render Model/Frame/
// Frames against a fixture with no network and no on-disk state (show_test.go
// is one). The unexported loadRemoteFn/fetchAllFn/fetchPruneFn vars in
// commands.go are out of reach of any caller outside this package. Mirrors
// archetype.UseInstalledDir (internal/archetype/search.go).
//
// The five vars it sets are process-global, exactly like UseInstalledDir's
// InstalledArchetypesDir — a concurrent caller that also overrides them would
// race, so this is for single-threaded headless rendering, not for use
// alongside the live interactive program.
func UseStaticRemote(r data.Remote) (restore func()) {
	prevLoad, prevFetch, prevPrune, prevIdentity, prevBase := loadRemoteFn, fetchAllFn, fetchPruneFn, fetchIdentityFn, forkBaseFn
	loadRemoteFn = func(string) (data.Remote, bool) { return r, true }
	fetchAllFn = func(context.Context, string, data.SyncOpts) data.Remote { return r }
	fetchPruneFn = func(context.Context, string) error { return nil }
	fetchIdentityFn = func(context.Context, string) (data.Identity, error) { return data.Identity{}, nil }
	forkBaseFn = git.KnownBase
	return func() {
		loadRemoteFn, fetchAllFn, fetchPruneFn, fetchIdentityFn, forkBaseFn = prevLoad, prevFetch, prevPrune, prevIdentity, prevBase
	}
}
