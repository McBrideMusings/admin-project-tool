# Dashboard key bindings are declared per row kind, and `enter` is an ordinary binding

`admin.toml` may carry a top-level `[keys.<row-kind>]` table per bindable **Row kind** (`bead`, `issue`, `branch`, `pr`, `commit`, `stash`), whose entries map a key to the name of a **Command** in the same manifest:

```toml
[keys.bead]
enter = "implement"
o     = "open-bead"
```

The dashboard consults `keys[rowKind][key]` before its own key switch. A bound key runs the named command for rows of that kind only; every unbound (key, row kind) pair falls through to the built-in behavior unchanged. `enter` gets no special treatment — the ability to override what Enter does on a bead is the same mechanism as the ability to add `o` on a bead.

**Why.** What Enter does on a row is a project fact, not a tool fact. `internal/tui/model.go`'s `activate()` hardcodes one answer — a bead or an issue builds a branch name and opens a worktree — and that is right for a repo where the next step is a human in a worktree and wrong for one where it is an agent dispatched over the bead. The manifest already declares the commands and `kind="workflow"` actions that would do the latter; the only thing missing was a way to say which key reaches them.

Scoping to the row kind rather than the tab or the whole app is what makes the feature safe to use on an already-full keymap. Nearly every letter is taken (`q h j k l a r R o i e d p s g w W x X`, plus `enter`, `space`, `tab`, `/`, `esc`, `1`–`4`, arrows), so any binding table lands on top of built-ins rather than in free space. Row-kind scoping turns a collision into a local one: `d` bound on an issue row takes nothing from `d` on a bead row, which opens the detail overlay.

**Considered and rejected.**

- **Override only the default action per row kind — no other key bindable.** Answers the immediate pain with no collision rule at all, since no new key exists. Rejected because the next ask ("also give me `i`") would need an entirely separate design for what is obviously the same feature.
- **A flat global keymap, key → command, with no row-kind concept.** Simplest to specify and validate. Rejected because a key would then mean one thing everywhere, so `enter` could not differ between a bead and a branch — the exact distinction `activate()` exists to draw.
- **Collisions are a `check` error; bindings only get unclaimed keys.** Leaves `b c f m n t u v y z` and the shifted letters. Rejected outright: it makes overriding Enter — the request — inexpressible.
- **Bindings always win, globally, with no reserved set.** Total control, config as truth. Rejected because binding `q` makes the dashboard unquittable, recoverable only by editing `admin.toml` from another terminal.
- **A `[[keys]]` array of tables** (`key`, `rows`, `run`), so one binding spanning three row kinds is written once. Rejected in favor of the nested table, where the file's structure *is* the lookup — one map access, duplicates caught by the TOML parser rather than by a rule someone has to write.
- **Holding the table in `<repo>/.repo/config.toml`** beside `[beads].columns` and `[identity]`, with the global `~/.config/repo/config.toml` tier for free. Rejected because a binding is a pointer into the manifest's own namespace: split across files, the file naming `implement` cannot be validated against the file defining it, and a typo surfaces as a dead keypress instead of a `admin check` error.

**Consequences.**

- **A reserved set is never bindable.** `q`, `ctrl+c`, `esc`, `tab`, `shift+tab`, the arrows, `j`, `k`, `1`–`4` and `/` are rejected by `admin check`. Navigation and escape cannot be taken away by a config file.
- **The Work tab's own rows are not bindable.** `command`, `commandTarget` and `worker` rows already have an Enter that means "run this command" / "attach to this worker"; a command row that does not run its command is not a configuration anyone wants.
- **Shadowing is reported, not blocked.** `admin check` prints `keys.bead.d shadows the bead detail overlay` and passes. The alternative — erroring — would forbid the deliberate case, and saying nothing would leave a key silently dead.
- **The built-in key labels move to `internal/keymap`, keyed by row kind.** They live today in `Model.keymap()` (`internal/tui/view.go:634`), keyed by tab and pane — which corresponds 1:1 to the six bindable row kinds (`tabBeads`→`bead`, `tabIssues`→`issue`, `tabPRs`→`pr`, and `tabLocal`'s default/commits/stashes panes→`branch`/`commit`/`stash`). Two things need that table: `admin check`'s shadow line, and the footer's binding merge. `internal/validate` imports no other internal package, and importing `internal/tui` — the heaviest package in the repo — to read a string table would invert the dependency direction. So the data moves to a leaf package both can import, re-keyed by row kind, and `Model.keymap()` reads it back. Duplicating the table into `internal/validate` was rejected: two tables with nothing checking that they agree means a relabelled footer key silently makes `check` report a shadow that no longer exists.
- **The footer keybar shows the effective binding, not the built-in.** `keymap()` merges the bindings for the current row kind, using the command's `desc` as the label. The footer is where every key in this TUI is learned, so a rebound `enter` still advertising "start work" is worse than no footer at all. The existing drop-from-the-middle rule (`internal/tui/view.go:680`) absorbs the extra width.
- **A bound key over a nonempty selection acts on the selection.** It falls back to the row under the cursor when nothing is selected — the rule `x` on the Branches pane already established, so the dashboard has one selection semantic rather than two.
- **A single cursor row dispatches without the confirm overlay; a multi-row selection keeps it.** The overlay exists because a *query* can match more than the user expects ([`internal/kinds/workflow.go`](../../internal/kinds/workflow.go)); the row under the cursor cannot. Misconfiguration that the overlay reports as a field — no agent configured — surfaces as a red status line on the immediate path.
- **A non-workflow command receives the row through the environment**, as `ADMIN_ROW_KIND`, `ADMIN_ROW_ID` and `ADMIN_ROW_URL`. Environment rather than arguments because it flows through every step of a multi-step command and every action kind untouched, and because argument forwarding is itself undecided (`admin-1qte`) and not ground to build on.
- **A keymap does not travel.** `admin.toml` is not committed, so bindings reach a new worktree only via `admin worktree`'s seeding and a new machine only by hand — the same consequence every other manifest key already carries.
