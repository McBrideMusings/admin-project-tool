# admin-project-tool — Project Command Center PRD

## Problem Statement

Two tools split one job. `admin` interprets a project's **Manifest** and runs its **Commands**;
`repo-dash` reads the same repo's git and GitHub state and navigates it. Because they are
separate programs in separate languages, neither can use what the other knows. `repo-dash`
guesses at facts the Manifest could state outright — whether the repo uses an issue tracker,
which one, whether it uses pull requests, whose branches are "yours" — and gets some of them
wrong. `admin` cannot offer a **Command** in the context of the issue or PR it is for. And the
gesture the user actually performs most — spinning up five to ten worktrees and giving every
one of them the same instruction — has no home in either tool, so it is done by hand, one
worktree at a time, every time.

## Solution

One Go binary, installed by `go install`, that is both the Manifest interpreter and the repo
dashboard. It opens on a full-screen **Dashboard** whose landing tab is the **Command** list,
with a resolved-action reader beside it; further tabs cover repo state, the issue tracker, live
work, and the project's own configuration. Every assumption `repo-dash` infers today becomes an
explicit Manifest key. A new **Workflow** kind expresses "run this same thing over many
targets", and declares where its targets come from — a selection the user makes on a row, or a
query the tool resolves itself. Long-running work is dispatched into its own Herdr pane and
tracked in one list alongside agent workers, so a dev server and a coding agent are both things
this repo started that are still alive, and both are one keystroke from being attached to.

## User Stories

1. As a developer, I want one binary installed by `go install`, so that a new machine needs no Python, no `~/.admin` directory and no PATH shim.
2. As a developer, I want `admin` with no arguments to open a full-screen Dashboard, so that one keystroke reaches everything about the project I am standing in.
3. As a developer, I want the **Command** list to be the landing tab, so that the thing I want most often is on screen before I navigate anywhere.
4. As a developer, I want a resolved-action reader beside the Command list, so that I can see an action's kind, run line, steps, originating **Archetype**, and last run without leaving the Dashboard or running `admin check`.
5. As a developer, I want to run a Command from the Dashboard, so that I do not drop back to a shell to type what is already listed in front of me.
6. As a developer, I want a transient Command's output inline, so that a four-second `test` reports where I am already looking.
7. As a developer, I want a long-running Command dispatched into its own Herdr pane, so that a dev server does not take over the Dashboard I am trying to use.
8. As a developer working without Herdr, I want long-running Commands to fall back to running inline with the pinned footer, so that the tool still works over SSH and in a bare terminal.
9. As a developer, I want the repo tab to show branch state, branches, recent commits and stashes, so that everything `repo-dash`'s Local view gave me survives the merge.
10. As a developer, I want an issue tab backed by whichever tracker the Manifest names, so that a beads repo and a GitHub-issues repo both work without the tool guessing.
11. As a developer, I want the Manifest to declare whether the project uses pull requests, so that the tool stops inferring it from branch protection and being wrong.
12. As a developer, I want my identity declared in configuration, so that "is this branch mine" is answered by a stated rule rather than by commit archaeology.
13. As a developer, I want worktree root and populate patterns to live in the Manifest, so that worktree creation behaves per project instead of per machine.
14. As a developer, I want to declare what `Enter` does on an issue row, a PR row and a branch row, so that a project where review merges and a project where review only comments can each behave correctly.
15. As a developer, I want to define a **Workflow** in the Manifest, so that "review each of these and leave the branch alone" is a named thing I can invoke rather than a prompt I retype.
16. As a developer, I want a Workflow to declare its **target source**, so that the tool knows whether it needs a selection from me or can resolve its own targets.
17. As a developer, I want a query-sourced Workflow to appear in the Command list and run bare, so that "review everything waiting on me" is one keystroke.
18. As a developer, I want a bare-run Workflow to show me the resolved target set before dispatching, so that a query matching nine things cannot surprise me.
19. As a developer, I want a selection-sourced Workflow to be absent from the Command list and reachable only from a row, so that I am never offered a Command that cannot say what it will act on.
20. As a developer, I want `admin check` to reject a Workflow with no target source, so that the ambiguity is caught at validation rather than at dispatch.
21. As a developer, I want to multi-select issues and run a Workflow over them, so that ten worktrees take one gesture instead of ten.
22. As a developer, I want each Workflow target to run in its own git worktree, so that parallel agents do not collide in one checkout.
23. As a developer, I want a concurrency ceiling, so that dispatching ten targets does not start ten agents at once.
24. As a developer, I want one tab listing everything this repo started that is still alive — agent workers and long-running processes together — so that I have one place to look when several things are in flight.
25. As a developer, I want each live row to show what it is doing right now, so that "waiting for input" is distinguishable from "still working" without attaching.
26. As a developer, I want to attach to any live row's pane from the Dashboard, so that jumping into a stuck agent or a noisy dev server is one keystroke.
27. As a developer, I want to kill or retry a live row, so that recovery does not mean hunting for a pid.
28. As a developer, I want a configuration tab showing the project's resolved settings, so that I can see what the tool believes about this project without opening the Manifest.
29. As a developer, I want the Dashboard driven entirely from the keyboard, so that it is usable over SSH and without a pointer.
30. As a developer, I want mouse clicks to move the cursor and activate rows, so that the existing click and drag-to-copy behaviour survives the merge.
31. As a developer, I want the tool to work in a repo with no Manifest, so that the dashboard half is useful before the task-runner half is configured.
32. As a maintainer, I want the Python implementation deleted as each Go equivalent lands, so that there is never a period where two implementations of one behaviour both exist.
33. As a maintainer, I want `repo-dash` archived and never imported, so that this is one tool rather than two with a dependency between them.

## Implementation Decisions

**Language and distribution.** The tool becomes a single Go module in this repository,
installed with `go install`. [ADR-0001](adr/0001-stdlib-only.md) (stdlib-only) is superseded:
its purpose was keeping the generated artifact dependency-free, and a static Go binary achieves
that more completely. Third-party Go dependencies are permitted; a dependency on any other
repository of the user's is not.

**Repository shape.** `repo-dash`'s Go source is moved into this repository as first-class
source and edited in place thereafter. `repo-dash` is archived. Nothing in this repository
imports `github.com/McBrideMusings/repo` at any point — the merge is a move, not a dependency.
The Python `admin_lib/` is ported module by module and each Python file is deleted in the same
change that lands its Go equivalent.

**Manifest is still truth.** [ADR-0006](adr/0006-interpreter-mode.md) is unchanged: the binary
reads `admin.toml` at runtime, merges **Archetypes**, and dispatches. **Commands** remain pure
routing. The **Kind** catalog remains the closed single source of truth and keeps its invariant
that every declared kind has a registered factory.

**Dashboard.** A full-screen tabbed surface. Tab order: Commands (landing), Repo, the tracker
tab, the live-work tab, Config. The Commands tab is a two-column split — the Command list on
the left, a resolved-action reader on the right showing the selected Command's steps, each
step's kind and run line, which Archetype contributed it, last run and log path. Tabs the
project cannot support are absent, not disabled: no Manifest means no Commands tab, no tracker
means no tracker tab.

**Kind gains a lifetime.** `KINDS` carries a third flag beside `configless` and
`consumes_targets`: whether a kind is long-running. Transient kinds run inline in the existing
frame. Long-running kinds are dispatched into a Herdr pane and registered as live work; with no
Herdr available they fall back to running inline exactly as today.

**Workflow.** A new Kind. A Workflow declares a **target source**: either a selection the user
makes on a row, or a query the tool resolves. A selection-sourced Workflow does not appear in
the Command list and is invoked by a hotkey from a tracker or repo row. A query-sourced
Workflow appears in the Command list, resolves its query on invocation, and shows the resolved
target set for confirmation before dispatching. Each target runs in its own git worktree, under
a concurrency ceiling. Validation rejects a Workflow with no target source.

**Live work.** One registry holding both agent workers and long-running processes, each with a
state, a one-line description of what it is doing now, and a handle to the pane it runs in.
Rows can be attached to, killed and retried.

**Configuration.** Every value the dashboard infers today becomes an explicit key: tracker,
whether the project uses pull requests, identity login and emails, worktree root, populate
patterns, per-row-type Enter actions, default agent, and concurrency ceiling. The Config tab
renders the resolved values.

**Naming — resolved by dissolution.** This was an open question: the live-work tab's name, with
"Workers" wrong for a dev server and "Swarm" a candidate. It no longer needs answering, because
the prototype settled that there is no live-work tab to name. Commands and live work are one
list under one cursor on the **Work** tab, with a detail pane whose shape follows the row's
kind. The tab set is Work, Repo, the tracker tab, Config.

## Testing Decisions

Two seams, both of which already exist in the two codebases being merged.

**The binary, driven by a Manifest.** Python's `tests/test_archetypes.py::_run_interpreter`
writes a temporary `admin.toml`, runs it through the interpreter and asserts on the result.
That is the highest seam available for anything about parsing, archetype merging, kind
dispatch, guards, validation and Workflow target resolution, and the Go port keeps it: write a
Manifest, run the binary, assert on behaviour. Tests go through this rather than calling
internal functions, so the port can restructure internals freely.

**A headless rendered frame.** `repo-dash` renders one frame with no TTY and dumps it as text.
That is the seam for everything visual — tab presence and order, pane layout, column alignment,
what a row shows in each state. `repo-dash`'s existing row-layout tests already pin fixed-width
columns this way and are carried over rather than rewritten.

Nothing is tested by asserting on internal function calls. The existing Go TUI tests move with
the source and keep passing; a Python test is deleted in the same change that deletes the module
it covered, replaced by a Go test at one of the two seams above.

## Out of Scope

- **The orchestrator that keeps work moving on its own.** Watching workers, deciding one is
  stuck, nudging or closing it, and re-dispatching as a dependency frontier advances. The live
  registry is the substrate it will need; the deciding is not in this spec.
- **Editing the Manifest from the Config tab.** The tab renders resolved values; writing them
  back is later work.
- **`admin compile`'s replacement.** A Go binary is already standalone, so the zipapp path has
  no successor. Removing the verb is in scope; anything replacing it is not.
- **Making `admin.toml` committable.** The secrets problem that keeps it untracked is
  independent of this merge and unchanged by it.
- **New archetypes or detectors.** The existing library ports as data; growing it is separate.

## Further Notes

`archetypes/` and `detectors/` are Python modules today, not data files. Porting them is part of
the work and their on-disk format is an open implementation question — the spec requires only
that the merged Archetype set and the detection results stay equivalent.

The Manifest is not committed in any repository except this one (see `CLAUDE.md`). Nothing in
this spec changes that, and the Config tab reads a file that is, in most repos, untracked.

Reference frames for every Dashboard state described here live in `docs/spikes/admin-tui-desktop/`.
They are text frames rather than screenshots, because the artifact is a character grid and
`repo-dash` already treats a dumped text frame as the checkable surface for a UI change.
