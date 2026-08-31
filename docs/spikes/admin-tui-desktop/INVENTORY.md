# repo-dash: complete render and capability inventory

Every surface `internal/tui` draws and every key it binds, as of `da92d07`. The prototype
beside this file must be able to reach all of it — a mode the prototype cannot show is a mode
the merge can silently drop.

Read from the source, not from memory: tabs at `model.go:27`, visibility at `model.go:352`,
keymap at `view.go:591`, footer states at `view.go:500`, overlay tabs at `beadoverlay.go:24`,
local panes at `local.go:16`.

## Tabs

`tabNames = {"Local", "PRs", "Issues", "Beads", "Config"}` — Local draws as **Repo**
(`view.go:389`). Digit keys `1`–`4` index the *drawn* list, not this one.

**Which tabs are drawn is derived, never configured** (`visibleTabs`, `model.go:352`):

| Tab | Drawn when |
|---|---|
| Repo | always |
| PRs | `nwo != ""` and `usesPRs()` |
| Issues | `nwo != ""` and **not** `hasBeads` |
| Beads | `hasBeads` |
| Config | always |

So Issues and Beads are mutually exclusive, and a repo with `.beads/` has no Issues tab at
all. This is why the prototype's tracker tweak swaps *repo* rather than a setting.

## Per-tab keymaps (`view.go:591`)

| Tab / pane | Keys |
|---|---|
| Repo · Branches | `↑↓` move · `↵` checkout · `⌥↵`/`W` worktree · `w` new · `space` select · `x` delete · `p` pull · `⇥` pane · `/` filter · `r` refresh · `←→` tab |
| Repo · Commits | `↑↓` move · `↵` copy sha · `o` open ↗ · `⇥` pane · `/` filter · `r` refresh · `←→` tab |
| Repo · Stashes | `↑↓` move · `↵` details · `⇥` pane · `/` filter · `r` refresh · `←→` tab |
| PRs | `↑↓` move · `↵` checkout · `o` open · `i` issue · `/` filter · `r` refresh row · `⇧R` refresh all · `q` quit · `←→` tab |
| Issues | `↑↓` move · `↵` start work · `o` open · `/` filter · `r` refresh row · `⇧R` refresh all · `q` quit · `←→` tab |
| Beads | `↑↓` move · `↵` start work · **`d` detail** · **`g` tree** · `/` filter · `r` refresh · `q` quit · `←→` tab |
| Config | `⇥` global/local · `↵` edit · `q` quit · `←→` tab |

Global: `q`/`ctrl+c` quit · `1`–`4` tab by index · `tab`/`shift+tab` pane · `left`/`right`/`h`/`l` tab
· `j`/`k` move · `X` (capital) hard delete · `e` · `R` refresh all.

## Panes inside a tab

- **Repo** (`local.go:16`): `paneBranches`, `paneCommits`, `paneStashes`, `paneCleanup` — `⇥`
  cycles, but **two of the four are not views**:
  - `nextVisiblePane` (`local.go:708`) skips any pane with no selectable rows, so **Stashes is
    unreachable on a repo with no stashes** — `⇥` walks straight past it.
  - **`paneCleanup` draws nothing of its own at all** (`local.go:701`): every deletable branch
    is marked in place in the Branches list, `x` deletes the row under the cursor, and `⇧X`
    sweeps every ready row from any pane. It is a mode of the Branches list, not a pane with a
    body.

  Measured on this repo: `⇥` reaches Branches and Commits only, and a third and fourth press
  wrap back to them. The prototype therefore carries `repo` and `repo-commits` and no others —
  a screen for a pane that cannot be focused would be a frame of the wrong pane wearing the
  right name.
- **Config**: Global / Local, `⇥` switches.

## Overlays and modal states (`view.go:500`, in precedence order)

| State | Entered by | Footer it draws |
|---|---|---|
| **Bead overlay** | `d` on Beads | `←→ switch · ↑↓ scroll · esc close` |
| Error detail | a failed action | `any key close` |
| Deleting | `x` then confirm | `deleting…` → `press any key to dismiss` |
| Naming a worktree | `w` on Branches | `new worktree: ▌ (name, #number or URL · enter · esc)`, plus bead-search candidate rows |
| Confirming | `x` | `y confirm · any key cancel` |
| Filtering | `/` | `/<query>  N matches · esc clear · enter apply` |

### The bead overlay has three tabs of its own (`beadoverlay.go:24`)

`beadTabDetail` → `beadTabGraph` → `beadTabBlocking`, cycled with `←→`, drawn as
`┌─[ Bead ]─[ Graph ]─[ Blocking ]───[ ✕ ]─┐`.

- **Bead** — title, `task · P1 · open · owner`, the full description, scrolling with
  `↓ N more — ↑↓ scrolls`.
- **Graph** — the dependency DAG drawn as boxes and connectors (`beadgraph.go`), with a
  `refusal` path when it will not fit (`beadgraph.go:217`).
- **Blocking** — the flat list of what this bead blocks.

Box geometry is derived from the terminal and never from the content (`beadOverlaySize`), so
switching tabs does not move the box.

### Tree mode is separate from the overlay

`g` on the Beads tab (`beadtree.go`) draws the hierarchy inline in the list — the `└╴` nesting
with `+N` counts — rather than in a modal. It is a *list rendering mode*, not an overlay, which
is why `d` and `g` are two different keys.

## Row rendering

- `rowKind` (`model.go:41`) drives what `↵`/`o`/`i` do per row.
- Sections (`section.go:30`): `secMain` pins the default branch above everything, then the
  grouped sections — "Continue Working", "Reviewing", "Merged" — each with a right-aligned count.
- Bead columns (`beadcolumns.go`) are fixed-width and computed once: id, priority, type, title,
  blocked-count, labels, age.
- Branch confidence (`branchstate.go:145`) grades a row's state; `⇅ next action` and `● yours N`
  ride in the Branches pane header.

## What the prototype must therefore reach

Ten states are the committed reference frames. Everything above needs a screen value too, or
the prototype cannot show that the merge preserved it. The gap this inventory was written to
close: **tree mode, the bead detail overlay, its Graph and Blocking tabs, the filter, and the
new-worktree prompt were all unreachable**, because the prototype only ever pressed the tab
digit and then threw away the footer those modes are drawn in.

**PRs needed a third repo.** `usesPRs()` is `len(remote.PRs) > 0` (`model.go:333`) — the tab
exists only while the repo actually has open PRs, so it cannot be configured into view. The
tracker tweak therefore cycles three repos, one per tracker shape:

| `⌥r` | Repo | Draws |
|---|---|---|
| `beads` | admin-project-tool | Repo · **Beads 227** · Config |
| `github` | etv-station | Repo · **Issues** · Config |
| `prs` | nts-desktop | Repo · **PRs** · Config |

That the tab bar changes shape per repo is the finding, not a workaround: three of the five
declared tabs are conditional, so "the tracker tab" is not one surface.
