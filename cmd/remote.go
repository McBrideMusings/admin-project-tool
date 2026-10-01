package cmd

import (
	"context"

	"github.com/McBrideMusings/admin-project-tool/internal/git"
	"github.com/McBrideMusings/admin-project-tool/internal/tui"
)

// remoteOption names a non-GitHub origin's host for the dashboard header. It
// returns an option that changes nothing when origin is GitHub (nwo set), absent
// or a local path, so the ssh lookup runs only for a remote the header would
// otherwise leave unnamed.
func remoteOption(ctx context.Context, checkout, nwo string) tui.Option {
	if nwo != "" {
		return tui.WithRemote("", "")
	}
	r := git.OriginRemote(ctx, checkout)
	return tui.WithRemote(r.Host, r.Addr)
}
