# live-frames — not reference frames

The five files here (`repo.txt`, `repo-commits.txt`, `repo-filter.txt`,
`repo-new-worktree.txt`, `no-manifest.txt`) are excluded from the reference
set the parent directory's README describes. They are not dumped by
`-dump` and a re-shoot never touches them.

## Why

They all render the Repo tab, which draws through `internal/tui.Frame`
against whatever repository the dump process's working directory sits in
(`internal/git`'s functions shell out to `git` with no `-C`/`Dir` — the
package assumes the caller already `cd`'d to the project root, the same
assumption the installed `admin` CLI makes at boot). That is more than a
timestamp problem: the Repo tab shows the developer's actual local branches,
their ahead/behind counts against origin, their merged/reviewing/clean-up
grouping (which reads GitHub PR state), and their worktree paths — a
snapshot of one machine's working set at one moment, not a fixture a commit
to this repo could invalidate in a knowable way.

Pinning it would mean building a synthetic git history *and* a synthetic
worktree layout *and* a synthetic GitHub PR/review state that reproduces
this specific screen's grouping logic — in effect a second implementation
of the Repo tab's own classification, kept in sync with the first by hand.
A frame generated that way stops proving what a reference frame is for
(the real seam, undistorted): it would drift from `internal/tui` in ways a
byte-diff cannot catch, since nothing but eyeballing would show the fixture
had stopped matching the real branch-grouping rules.

## What these files are instead

Illustrative captures, each taken once from a real checkout with real
branches, showing the Repo tab's shape and columns. They will not match
what `internal/tui` currently draws — treat them as reference for layout
only, never as a pass/fail target. To see the current, real Repo tab,
run `admin prototype admin-tui-desktop` and press `2`.
