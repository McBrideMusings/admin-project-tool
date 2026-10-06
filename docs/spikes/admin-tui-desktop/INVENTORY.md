# repo-dash: complete render and capability inventory

Every surface `internal/tui` draws and every key it binds. The prototype beside this file must
be able to reach all of it — a mode the prototype cannot show is a mode the merge can silently
drop.

**Read `README.md` first: this directory is the design source of truth, not an expiring spike.**
The Commands and Workers rows below describe the design the app is being brought to; every other row
describes what the app draws today, rendered through `internal/tui.Frame`.

Read from the source, not from memory: tabs at `model.go`'s `tabNames`, visibility at its
`visibleTabs`, the tab row at `view.go`'s `tabBarSegs`, keymap at its `keymap`, footer states at `view.go:500`, overlay tabs at `beadoverlay.go:24`,
local panes at `local.go:16`.

## The Commands and Workers tabs

Two tabs, one layout: the tab's list above, a full-width band below previewing whatever the
tab's cursor is on. Commands lists every declared command, its targets under it, with a blank
row wherever the manifest's command group changes; Workers lists every live agent worker. There
is no Running section: a running command is its own row in the Commands list, wearing its state.
No row carries a kind column — the kind is a band field.

| Frame | What it shows |
| --- | --- |
| `commands.txt` | The Commands list, the band on the selected entry |
| `commands-running.txt` | The cursor on a command that IS running — the row carries its state, the band previews its output; each url row leads with the dashboard's reachability dot |
| `commands-status.txt` | `commands.txt` for a project declaring `[status]` entries — the Status strip (dot, label, readout) pinned above the list, its rows taken from the list, never the band |
| `commands-running-status.txt` | `commands-running.txt` for the same project — a url the `urls` list named by its `[status]` entry reads `● local  http://…` |
| `commands-empty.txt` | Nothing running and no workers |
| `commands-collapsed.txt` | Collapse mode (`c`): the group the cursor is in stays open; every other group is a closed row, `▸ dev (2)`, naming how many targets it hides. The footer reads `c expand` |
| `commands-launch.txt` | What admin opens on: focus on the tab bar. The active pill is the one solid element, no row wears `◆`, the band previews the first row, and the footer reads `↓/↵ body` (`↑` does nothing from there) with `1-5 jump`. One `↓` or `↵` lands in `commands.txt` |
| `commands-workflow-selected.txt` | A workflow entry selected — targets are tracker items, so `↵` dispatches an agent |
| `running.txt` | The Workers tab, cursor on a waiting worker; the band shows log, branch, worktree, session, now, tail |
| `swarm.txt` | The Workers tab, cursor on a failed worker; the same band fields |
| `workers-empty.txt` | The Workers tab with no workers, reached by its digit, so focus stays on the tab bar. The line under the header names where this manifest can start a worker — each query-sourced workflow command and each `[keys.<kind>]` binding to a row-sourced one — or says it declares no workflow. The band keeps its place: a `no worker` rule counting what is running, the empty field slot, and a last line naming what will appear. No row to enter, so the footer drops `↓/↵ body` and `c collapse` |
| `hosted.txt` / `hosted-empty.txt` | Attached to a hosted pty — full width, the child's own screen. No tab bar: that row carries the attach breadcrumb instead, and the band's own field rows (log, urls or the worker's five) sit directly under it |
| `hosted-quit.txt` | The quit confirm over live children |
| `hosted-quit-many.txt` | The quit confirm over more live children than it has rows for. The box never covers the header, tab bar and separator above it or the rule and keys below it; at that height it sits flush between them, lists the first processes and ends on `+N more running`. The title still counts every process, and `⏎` kills every one |
| `dispatch-sheet.txt` | Dispatching from the tracker tab |
| `no-manifest.txt` | A repo with no `admin.toml` |

**Commands/Workers keys.** `↑↓`/`jk` move the tab's cursor · `↵` the row's primary action, following its state · `x` kill (shown on a running row) · `a` attach ·
`c` collapse targets, so only the cursor's group stays open (Commands only) · `o` config · `←→` switch tabs · `q`/`esc` quit, or detach while attached. A multi-target parent is a
group row: no marker while open, `▸` and its hidden target count while closed, and the cursor skips over it.

## Tabs

`tabNames = {"Commands", "Workers", "Local", "PRs", "Issues", "Beads", "Config"}` — Local draws
as **Repo**. Commands and Workers are drawn only when the repo has an `admin.toml`. Digit keys
index the *drawn* list, not this one.

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
| Repo · Branches | `↑↓` move · `↵` checkout · `⌥↵`/`W` worktree · `w` new · `space` select · `x` delete · `p` pull · `⇥` pane · `c` history · `/` filter · `r` refresh · `←→` tab |
| Repo · Commits | `↑↓` move · `↵` copy sha · `o` open ↗ · `⇥` pane · `c` stashes · `/` filter · `r` refresh · `←→` tab |
| Repo · Stashes | `↑↓` move · `↵` details · `⇥` pane · `c` commits · `/` filter · `r` refresh · `←→` tab |
| PRs | `↑↓` move · `↵` checkout · `o` open · `i` issue · `/` filter · `r` refresh row · `⇧R` refresh all · `q` quit · `←→` tab |
| Issues | `↑↓` move · `↵` start work · `o` open · `/` filter · `r` refresh row · `⇧R` refresh all · `q` quit · `←→` tab |
| Beads | `↑↓` move · `↵` start work · **`d` detail** · **`g` tree** · `/` filter · `r` refresh · `q` quit · `←→` tab |
| Config | `⇥` global/local · `↵` edit · `q` quit · `←→` tab |

Global: `q`/`ctrl+c` quit · `1`–`4` tab by index · `tab`/`shift+tab` pane · `left`/`right`/`h`/`l` tab
· `j`/`k` move · `X` (capital) hard delete · `e` · `R` refresh all.

## Panes inside a tab

- **Repo** (`local.go:16`): `paneBranches`, `paneCommits`, `paneStashes`, `paneCleanup` — `⇥`
  cycles, but **two of the four are not views**:
  - Commits and Stashes share one strip under the full-width Branches pane, and `c` flips which
    of the two is drawn. `nextVisiblePane` (`local.go:700`) skips any pane with no selectable rows
    and any pane the toggle hides, so **Stashes is unreachable on a repo with no stashes**, and
    `⇥` never lands on the hidden one of the pair.
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

**PRs needed a third repo.** `wantsPRs()` is the manifest's declared `uses_pull_requests` key
(`model.go:551`) — the tab's presence follows that declaration alone, not whether the repo
happens to have any open PRs, but seeing it drawn still needs a repo whose manifest declares it
true, distinct from the ones declaring `tracker`. The tracker tweak therefore cycles three
fixture repos (`fixtures.go`, built in a temp dir — admin-p55x), one per tracker shape:

| `⌥r` | Repo | Draws |
|---|---|---|
| `beads` | fixture: `tracker = ["beads"]` | Repo · **Beads** · Config |
| `github` | fixture: `tracker = ["github"]` | Repo · **Issues** · Config |
| `prs` | fixture: `uses_pull_requests = true`, no `tracker` | Repo · **PRs** · Config |

That the tab bar changes shape per repo is the finding, not a workaround: three of the five
declared tabs are conditional, so "the tracker tab" is not one surface.
