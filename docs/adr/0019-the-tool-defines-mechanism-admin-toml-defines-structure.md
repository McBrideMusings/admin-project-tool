# ADR-0019: The tool defines mechanism, admin.toml defines structure

**Status:** Accepted
**Date:** 2026-09-02
**Context:** first item under `admin-tlhw`; every other item in that epic depends on this
rule being written down before it is re-derived by accident.

## Decision

**The tool supplies mechanism; a project's `admin.toml` supplies structure.** Mechanism is
kinds, the interpreter, the dashboard, guards, worktrees, log framing, and the archetype
merge — the machinery that runs a manifest. Structure is which commands exist, what they
are named, what subcommands or targets they have, what each one runs, and what the
project ships to — decisions only the project can make about itself.

The tool never assumes a shipping target, never assumes a command name, and never
populates a menu a project did not ask for. `build`, `dev`, `deploy`, `test`, `clean`,
`logs`, `kill` are conventions for organizing a manifest — names that recur because they
are useful — and carry zero reserved semantics in the tool.

An archetype is a starting point a project can edit or discard, not a contract it
inherits. An archetype may ship actions as a library (`deploy-mac`, `dev-device`) and may
ship `[commands.*]` routing, but it must not ship a `multi-target` table enumerating
surfaces, because that enumeration is the project's statement about itself, not the
archetype's.

## Why

`admin deploy` in apple-notepad — a mac+iOS app whose manifest declares only
`mac_scheme` and `ios_scheme` — offered five targets: `bazzite`, `ios`, `mac`, `server`,
`tv`. `admin build` and `admin dev` were equally wrong, offering four and five targets
respectively. Nothing in that menu came from the project's own manifest; all of it came
from `archetypes/apple/archetype.toml`, whose `[actions.build]`, `[actions.dev]`, and
`[actions.deploy]` tables (`kind = "multi-target"` at lines 63, 102, and 149) enumerate
every surface the archetype has ever supported, and whose `desc` strings spell out the
menu verbatim (`build`: "mac | ios | tv | server"; `dev`: "mac|ios|tv|device|server";
`deploy`: "mac|ios|tv|server|bazzite"). A project that ships to one platform inherited a
menu built for every platform the archetype has ever seen, because the archetype baked
structure into what should have been a mechanism-only mixin.

The test for any future change: **could two projects using this archetype disagree about
it?** If yes, it belongs in `admin.toml`, not in the tool and not in the archetype. Two
apple projects can disagree about which platforms they ship to — that disagreement is
structure, so it must live in each project's own manifest, never in the shared archetype.

## Failure shapes

Four shapes of the same mistake, so each is recognisable on sight:

1. **An archetype enumerating targets.** `archetypes/apple/archetype.toml`'s
   `[actions.build]` (`kind = "multi-target"` at line 63), `[actions.dev]` (line 102), and
   `[actions.deploy]` (line 149) each hard-code the full set of platforms the archetype
   supports, rather than letting the project declare only the targets it ships.

2. **The tool reserving a name it does not implement.** `cmd/manifest.go:23` declares
   `reservedToolVerbs = []string{"new", "check", "compile", "branch", "worktree"}`, with
   the preceding comment already conceding that `new` and `compile` "live only in
   admin-py" — the Go tool reserves verbs in the manifest namespace for mechanism it does
   not itself provide.

3. **The tool invoking a project command by literal string, and routing on one.**
   `internal/kinds/docker.go:88` (`if _, ok := resolveCommand(m, "kill"); ok { ... }`),
   the identical block in `internal/kinds/cleandev.go:267` and
   `internal/kinds/vitedev.go:452`, and `internal/guards/preflight.go:29`
   (`preflightExempt = map[string]bool{"logs": true, "kill": true}`) all treat `"kill"`
   and `"logs"` as semantically special command names baked into Go, rather than as
   conventions a project happens to use.

4. **A closed list of settings-table names compiled into Go.**
   `internal/kinds/config.go:15`'s `archetypeConfigNames` is a fixed map of table names
   (`apple`, `electron`, `react_native`, `server`, `output`, `dev_kill`, `dev_precheck`,
   `clean_dev`, `vite_dev`, `docker_dev`, `docker_run`, `docker_build`, ...); `config.go`
   lines 48-51 return an empty map for any key not in that list, so a project cannot
   invent a settings table of its own and have its own actions read it back.

This ADR is a record of the rule and a catalogue of known violations at the time it was
written — it does not fix any of the four. The follow-on items under `admin-tlhw` are
where each gets fixed.

## Alternatives considered

**Leave the boundary implicit and rely on review to catch new violations.** This is what
produced the four failure shapes above: each one shipped past review because there was
no written rule to check a change against. Rejected — the whole purpose of this ADR is to
give reviewers and future contributors a rule to point at instead of re-deriving it (or
failing to) each time.

**Scope the rule narrowly to archetypes only (failure shape 1), since that is the shape
with concrete evidence.** Rejected — failure shapes 2 through 4 are the same mistake
(the tool deciding something only a project can decide) wearing different clothes, and a
rule that only names the archetype case would leave the other three unrecognisable as
instances of it.

## Consequences

- Any new archetype table gets checked against the "could two projects disagree?" test
  before it ships. A `multi-target` table enumerating surfaces fails that test and does
  not belong in an archetype.
- Any new tool verb, guard exemption, or literal-string command match gets checked the
  same way: if a project's own choice of command names could make it wrong, it is
  structure and cannot be compiled into Go or into `admin_lib`.
- **Resolved by `admin-tlhw.4` (commit `d9b31ec`):** `archetypeConfigNames` is gone.
  `Manifest.Settings` now holds every top-level table the parser doesn't already handle
  structurally, keyed by its own name, so a project can invent `[my_thing]` and read it
  back without a tool change.
- `reservedToolVerbs` (failure shape 2) continues to reserve `new` and `compile` in the Go
  binary's namespace even though only `admin-py` implements them, until that shape is
  fixed by a later item.
