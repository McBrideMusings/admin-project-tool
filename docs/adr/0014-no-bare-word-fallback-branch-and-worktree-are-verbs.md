# ADR-0014: No bare-word fallback — `branch` and `worktree` are the verbs

**Status:** Accepted
**Date:** 2026-08-31
**Context:** amends [ADR-0011](0011-the-binary-is-admin-and-manifest-commands-are-bare-verbs.md) §2 (admin-oeqo)

## Decision

**Step 3 of ADR-0011 §2 — the branch shorthand — is deleted.** A bare first argument that
is neither a tool/`cmd/` verb nor a command in the project's `admin.toml` is an error. The
resolution order is now two steps and ends:

1. A tool verb or an existing `cmd/` verb (`branch`, `worktree`, `prs`, `populate`,
   `archetypes`, plus cobra's `help` and `completion`).
2. A command declared in the project's `admin.toml`.
3. — *(removed)* —

Creating a git ref from an issue, a PR, a bead id, a GitHub URL or a plain name is what
`admin branch <input>` and `admin worktree <input>` do. Both names stay reserved
(`reservedToolVerbs`, `cmd/manifest.go`), so no manifest can shadow them and the two verbs
are always reachable.

## Why the fallback had to go

ADR-0011 §2 argued the shorthand was safe because a shadowed manifest command is silent
while a shadowed branch name is loud, and kept it as the last step. That reasoning covered
the *collision* case and missed the *empty* case, which is the common one.

`admin.toml` is never committed. Every fresh clone therefore has no manifest, step 2 is
skipped entirely, and **every word typed at `admin` in a fresh clone was branch input.** Not
a rare collision — the default state of every new checkout:

```
$ git clone …/sideshow && cd sideshow
$ admin dev
branch 'dev' set up to track 'origin/main'.
Switched to a new branch 'dev'
created branch dev
```

The user typed a task-runner command and got a git checkout, with no confirmation
(`dispatchArgs` appended `--yes` specifically to skip `branch`'s prompt) and no way to tell
that the word had been reinterpreted. admin-3rgp.52 already patched the same shape once for
a *broken* manifest; that fix guarded one arm of the condition instead of removing the
condition, which is why the missing-manifest arm went on doing it.

The cost of removal is real and small: `admin my-new-feature` no longer creates a branch out
of a bare word. `admin branch my-new-feature` does, and the unknown-command error names both
verbs with the typed word already substituted in, so the remedy is one line above the cursor.

## Consequences

- `dispatchArgs` registers a hidden command under the unrecognized name whose `RunE` is the
  unknown-command error, rather than letting cobra emit its generic one. That is what lets
  the message carry the project's real command list (`kinds.CommandNames`) or the fact that
  the directory has no `admin.toml` at all.
- A broken-but-present manifest still reports the manifest problem, not "unknown command"
  (ADR-0011 §2's third case, unchanged) — a typo report would send the user hunting for a
  name their manifest does declare.
- `branch --yes` is now only reachable as typed; `--yes` is documented as `--prune`-only.
- Any muscle memory or script relying on `admin <new-branch-name>` breaks loudly with an
  error naming its replacement. That is the intended trade.

## Alternatives rejected

**Keep the shorthand but drop `--yes` so it confirms first.** One line, keeps the
convenience. Rejected: it is a guard, not a fix — the tool still guesses that an unknown
word means "mutate git", and one habitual `y` restores the original failure exactly.

**Restrict the shorthand to input that resolves to a real reference** (a number, `#N`, a
bead id, a GitHub URL, an existing branch), erroring on anything else.
`internal/resolve.BranchInput` already classifies all of those, so this was cheap and it
does remove the failing case. Rejected as still two rules where one will do: a bare word
would mean "run a command" in a project with a manifest and "check out an existing branch"
in one without, and the user has to know which repo they are in to predict what `admin 42`
does. One rule — a bare word is a command or an error — is the thing worth having.
