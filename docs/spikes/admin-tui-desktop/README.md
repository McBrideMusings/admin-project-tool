# admin-tui-desktop — the design source of truth for `internal/tui`

**This is the design source of truth for the admin dashboard. It is not a spike with an expiry.**

It is maintained alongside the code and changed *first* when the design changes; the code
follows it. Cutting tickets from it does not end it — the next slate cites this same directory.
**Never file a teardown issue for it.**

## What is here

- `variants.go` — the design. One variant, `Detail-right`, because there is nothing left to
  compare: every direction this directory once carried side by side has been decided.
- `harness.go` — supplied by `spike tui`. Do not edit; rebuild instead.
- `fixtures.go` — three temp-dir repos, one per tracker shape, built on demand.
- `*.txt` — 19 reference frames, one per screen. **Dumps of the running program, never
  hand-written.** Re-shoot all of them after any change: a directory holding one fresh frame
  and 18 stale ones is worse than 19 stale ones, because nothing on disk marks which is which.
- `live-frames/` — five more `.txt` captures that are **not** reference frames: `repo.txt`,
  `repo-commits.txt`, `repo-filter.txt`, `repo-new-worktree.txt`, `no-manifest.txt`. They
  render the Repo tab against whatever repository the process happens to be sitting in, so no
  re-shoot can pin them; `live-frames/README.md` says why and what they're for instead. The
  dump still has to generate these five to reach them at all (see `variants.go`'s screen-axis
  comment), so a re-shoot regenerates them at the top level too, same filenames as here — that
  copy is gitignored and never the one to look at.
- `INVENTORY.md` — every surface `internal/tui` draws and every key it binds.

```
admin prototype admin-tui-desktop      # drive it: ⌥s screen · ⌥e data · ⌥r tracker
go run ./docs/spikes/admin-tui-desktop -dump -dir ./docs/spikes/admin-tui-desktop
go test ./docs/spikes/admin-tui-desktop
```

## Re-shooting

The 19 reference frames are reproducible from any checkout — the primary one or a linked
worktree, since every `implement` pass runs in one. A clean re-shoot is: run the command
above once, `git add -A`, run it again, and `git status --short docs/spikes/admin-tui-desktop`
comes back empty. Any of the 19 reference frames showing a diff on that second run means
something in the render path started reading live, non-deterministic, or checkout-relative
state again — fix that before committing, don't just take the second run's output.
`live-frames/` is expected to sit there unchanged; the dump never writes into it.

The Repo, tracker and Config tabs are **not** redrawn here — they render through
`internal/tui.Frame`, the real headless seam, so those frames show what the app actually
draws rather than a reconstruction that drifts. Only the Work tab is designed in this file.

## Decisions this directory now records rather than asks

Every one of these used to be a live axis or a competing variant. They are settled, the
alternatives are deleted, and the frames depict the answer:

| Question | Answer |
| --- | --- |
| Flat command list or a palette? | **The list.** A palette was tried here and removed: it made every command a thing you have to remember the name of before you can reach it, and the Commands surface is where you go precisely when you do not. The list is a section like Running and Workers, under the same cursor. |
| Where does live output go? | **A full-width band pinned under the list**, always on, for whatever the cursor is on. The `off / auto-tail / split-pane / inline-peek` axis is gone. |
| Side-by-side or stacked? | **Neither.** The band is full width at every geometry, so the list gives up height and never width. The `w/2.3` orientation switch is gone. |
| Can the cursor land on a multi-target parent? | **No.** It is a group: it holds targets and runs nothing itself, so it carries no marker — the marker column is what promises `↵` will run the row. |
| What does `↵` do? | **The row's one primary action, following its state:** attach while it runs, dispatch while it does not, open the transcript of a worker that has finished. |
| Where is kill bound? | **`x`.** `k` is cursor-up everywhere in this dashboard, so a footer advertising `k kill` was a collision. |
| Are workers commands? | **No.** Separate section, own state words, own fields (branch, worktree, session), own verbs. |
| Does a running command get a second row? | **No.** It is the row it always was, wearing its state in the columns the grid already reserves. A Running section under its own heading said the same thing twice. |

Two rules everything on the Work tab obeys:

- **The band's field block is a fixed slot** (`fieldRowsH`). The eye reads motion as meaning,
  so a block that grew a row for a command that had actually been run once slid everything
  below it down and claimed a state change that had not happened. `TestBandDoesNotShift` pins
  it.
- **Nothing is said twice on one screen.** The previewed thing is named once, on the band's own
  title rule, with the count of everything else running on the right of that same rule. There
  is no chip row and no tab strip: the cursor is the selector, so a second row of names only
  reprinted the list.

## Log paths are absolute

Everywhere, browse and attached. Where one will not fit, whole middle *segments* are elided —
`/Users/…/logs/docs.log` — so it still reads as a path. A mid-rune cut produces
`/Users/dev/Proj…tool/` and reads as corruption.
