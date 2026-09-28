package tui

import (
	"context"

	"github.com/McBrideMusings/admin-project-tool/internal/data"
)

// UseStaticRemote makes every remote read this package performs answer from r
// instead of reaching out: loadCacheCmd's on-disk cache read, syncCmd's `gh`
// round trip, and syncCmd's `git fetch --prune` all become no-ops that return
// r (or, for the prune, do nothing at all). It returns a restore func that
// puts the real implementations back — callers must defer it.
//
// This exists for a headless caller that wants to render Model/Frame/Frames
// against a fixture with no network and no on-disk state — today,
// docs/spikes/admin-tui-desktop's fixtures.go, which is package main and so
// cannot reach the unexported loadRemoteFn/fetchAllFn/fetchPruneFn vars in
// commands.go directly. Mirrors archetype.UseInstalledDir
// (internal/archetype/search.go).
//
// The three vars it sets are process-global, exactly like UseInstalledDir's
// InstalledArchetypesDir — a concurrent caller that also overrides them would
// race, so this is for single-threaded headless rendering, not for use
// alongside the live interactive program.
func UseStaticRemote(r data.Remote) (restore func()) {
	prevLoad, prevFetch, prevPrune := loadRemoteFn, fetchAllFn, fetchPruneFn
	loadRemoteFn = func(string) (data.Remote, bool) { return r, true }
	fetchAllFn = func(context.Context, string, int, bool) data.Remote { return r }
	fetchPruneFn = func(context.Context, string) {}
	return func() {
		loadRemoteFn, fetchAllFn, fetchPruneFn = prevLoad, prevFetch, prevPrune
	}
}
