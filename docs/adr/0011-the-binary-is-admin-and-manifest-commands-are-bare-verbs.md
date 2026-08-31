# ADR-0011: The binary is `admin`, and manifest commands are bare verbs

**Status:** Accepted
**Date:** 2026-08-31
**Context:** [ADR-0006](0006-interpreter-mode.md), the one-binary epic (admin-3rgp)

## Decision

Three separate questions, decided together because the answers constrain each other.

### 1. The root command is `admin`

`rootCmd.Use` becomes `"admin"`. `repo` survives as a **cobra alias**, not as a second name
with a life of its own.

The alias exists for one concrete reason: `repo completion zsh` is wired into the user's
`.zshrc`, and `cmd/root.go` force-registers cobra's help and completion commands during
`dispatchArgs` precisely so that line keeps working. Renaming with no alias breaks the
shell's completion silently — it fails at shell-startup time, far from the change that
caused it. The alias is a migration affordance and may be deleted once nothing invokes it.

### 2. Manifest commands are reached bare, and they beat the branch shorthand

`admin <command>` runs the manifest command named `<command>`. There is no `admin run`
subcommand wrapper.

`dispatchArgs` currently claims **any** unrecognized bare first argument as branch input:
`repo <name>` becomes `repo branch --yes <name>`. That fallback stays, but it moves to last
place. The resolution order for a bare first argument is:

1. A tool verb or an existing `cmd/` verb (`new`, `check`, `compile`, `branch`, `worktree`,
   `prs`, `populate`, plus cobra's `help` and `completion`).
2. A command declared in the project's `admin.toml`.
3. Otherwise — branch shorthand, exactly as today.

Bare is the right shape because it is already the shape: every existing `admin.toml`, this
repo's own CLAUDE.md, and the muscle memory behind `admin dev` and `admin test` all assume
it. A `run` wrapper would buy zero collisions at the cost of invalidating all of it, and the
collision problem is solved by an ordering rule instead.

The branch shorthand loses because of what each side costs when it is wrong. A manifest
command shadowed by the shorthand is *silent*: `admin deploy` quietly creates a branch
called `deploy`. A branch name shadowed by a manifest command is *loud*: the command runs
and the user sees it run. Prefer the failure the user can see.

**An admin.toml that exists but fails to parse or resolve is a hard error, never a
fall-through.** Step 2 needs a resolved manifest to see any command names in it, so a
project whose admin.toml is malformed has none to offer — every bare argument used to fall
straight through to step 3, silently running `admin branch --yes <name>` against a project
whose manifest was broken and never even validated. Step 2 is now three-way, not two: no
admin.toml at all still falls through to step 3 exactly as before; a manifest that resolves
but doesn't declare the given name falls through too (that name may legitimately be a
branch); a manifest that is *present but fails to resolve* blocks the fall-through and
reports the manifest problem instead, whatever the bare argument was. Only the first case
is silent by design — the other two both surface something before touching git.

### 3. Tool verbs and `cmd/` verbs win collisions, and `admin check` reports them

A manifest may not declare a command whose name is a tool verb or an existing `cmd/` verb.
`admin check` rejects it with an error naming the rule and the colliding name.

This extends the rule the project already states — "tool verbs are reserved" — to `cmd/`'s
verbs, rather than inventing a second concept. The alternative, moving tool verbs under
`admin tool <verb>`, would re-teach five verbs and every document naming them, to protect
manifest authors from a name collision a validator can simply report.

The check belongs in `check` rather than at dispatch because a collision is a property of
the manifest, not of one invocation. A manifest is either well-formed or it is not, and the
user should learn which before the command they typed does something unexpected.

## Consequences

- `cmd/root.go`'s `dispatchArgs` grows a manifest-command lookup between the subcommand scan
  and the branch fallback. It must resolve the manifest to do this, so a project with no
  `admin.toml` keeps exactly today's behaviour — every unrecognized bare argument is branch
  input. A project whose `admin.toml` exists but fails to resolve does not: it gets a hard
  error naming the manifest problem instead.
- A project that today has a branch it reaches as `repo <name>` and a manifest command of the
  same name changes behaviour: the command wins. This is the intended trade from §2.
- `admin check` gains a failure mode that no existing manifest has been tested against.
  Adding a reserved verb later is therefore a breaking change for any manifest already using
  that name, and wants the same treatment as any other reserved-word addition.
- The alias in §1 means two spellings appear in help output until it is removed.

## Alternatives rejected

**`admin run <command>`.** Zero collision surface, and that is its whole case. It breaks
every existing invocation and every document, to avoid a problem an ordering rule solves.

**Manifest commands win collisions; tool verbs move to `admin tool <verb>`.** Consistent with
"the manifest is truth" (design principle 1), and the honest argument for it is that a
project's own vocabulary should outrank the tool's. Rejected because the tool's verbs are how
a user recovers when a manifest is broken — `admin check` in particular. A manifest that can
shadow `check` can hide its own breakage.

**No bare CLI path; commands run only from the dashboard TUI.** This is the most faithful
reading of the spec's user stories, which run commands from the TUI and mention the CLI only
as a test seam. Rejected because `admin test` and `admin build` are invoked by scripts, hooks
and CI, none of which can drive a TUI.
