# Running against another worktree (`-w`/`--worktree`)

By default `admin` operates on the checkout the shell's current directory is
in: it finds `admin.toml` by walking up from `$PWD`, and every step of a
command runs with that directory as its project root. `-w`/`--worktree`
re-targets that whole resolution at a different git worktree of the current
repo, without cd'ing there first.

```bash
admin -w redux-c38.5 dev      # run the "dev" command with that worktree as the project root
admin --worktree redux-c38.5 dev
admin -w redux-c38.5          # no further args: open the dashboard on that worktree instead
```

## Resolving `<target>`

`<target>` is matched against `git worktree list --porcelain`, run against
the repo containing `$PWD` (not the target itself — you haven't gone there
yet). Four tiers are tried in order, and resolution **stops at the first
tier that produces any match at all**:

1. **An existing directory** — absolute or relative to `$PWD`. Resolves
   directly; it doesn't need to be one of this repo's worktrees.
2. **A worktree whose directory basename equals `<target>` exactly.**
3. **A worktree whose branch name equals `<target>` exactly.**
4. **A worktree whose branch name *or* basename contains `<target>`**,
   case-insensitively — `c38.5` or `redux-c38.5` finds
   `~/.worktrees/term-redux/redux-c38.5` on branch `feat/redux-c38.5-sidebar`.

Two or more matches at the tier that decided it — including two worktrees
both matching at tier 4 — is never guessed between: `admin` exits non-zero
and lists every candidate (path + branch), one per line, to stderr. No match
at any tier is the same non-zero exit, naming what was checked.

## What re-targets

Once `<target>` resolves, everything downstream runs as though `admin` had
been invoked from inside that worktree:

- **`admin.toml` lookup.** The target's own `admin.toml` wins if it has one.
  A fresh worktree usually won't — `admin.toml` is never committed — so
  `admin` falls back to the **primary checkout's** `admin.toml` instead of
  failing. Either way, every step still runs *inside the target worktree*,
  never inside the primary checkout its manifest was borrowed from.
- **Archetype merge**, resolved against whichever `admin.toml` content was
  found above.
- **The working directory every manifest step runs in**, the **`tmp/` log
  directory**, and **`${VAR}` resolution** — all keyed off the target
  worktree, not wherever the manifest content came from.

## The dashboard's worktree picker

With `admin -w <target>` opening the dashboard, or from any already-running
dashboard, press **`T`** to open a picker listing the repo's worktrees (path
+ branch). `↑`/`↓` (or `j`/`k`) move, `enter` switches the dashboard's
target to the selected worktree — its `admin.toml` reloads from there,
following the same "own manifest, else fall back" rule as the CLI — and
`esc` (or any other key) cancels without changing anything. Once a target is
picked, the header shows it (`target: <name> (<branch>)`) until another pick
changes it or the dashboard restarts.
