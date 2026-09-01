# ADR-0017: The orchestrator never lands work

**Status:** Accepted
**Date:** 2026-08-31
**Context:** scopes `admin-16ju` (orchestrator logic); reads the registry built by `admin-3rgp.28`

## Decision

**The orchestrator's responsibility ends when a live-work entry reaches `StateExited`. It
never merges, never pushes, never opens a pull request.** `internal/git` gains no `Merge`
and no `Push`; `internal/gh` stays read-only query code.

Landing stays where it already happens: inside the worker's own session, in its own
wrap-up, on the worktree it owns.

Because nothing here lands, repository ownership never enters this codebase. There is no
branch on `git.OriginNWO` (`internal/git/git.go:198`) deciding merge-to-default versus
open-a-PR, because there is no merge path to gate.

## Why

The worker holds the information the decision needs and the orchestrator does not. A
worker knows what it changed, whether its tests passed, and what it left undone. The
orchestrator sees a `State` enum and a one-line `Activity` string on `livework.Entry`.
Putting merge authority on the side with strictly less information is backwards.

Two landing paths would also disagree. The worker's wrap-up already commits, pushes, and
merges under the `SELF-LAND` convention. A second lander polling the registry would
race it, and the first symptom would be a double-merge or a branch deleted out from
under a session still writing to it.

## Alternatives considered

**Land, gated on repository owner.** Read `OriginNWO`: an owned repo fast-forwards into
the default branch, anyone else's stops at a pushed branch. This encodes a rule already
followed by hand, in the one place holding the owner string. Rejected because ownership
is the wrong axis. The risk is whether the work was verified — verified work is safe on
someone else's repo behind a pull request, unverified work is unsafe on your own. This
gate would merge a red branch into `main` on the grounds that you own the repo.

**Land only on a verification record.** The worker writes a completion marker carrying
exit status and test result; the orchestrator merges only on a pass. This gates on the
axis that matters. Rejected for now because the marker is written by the same process
whose work it certifies, and no such protocol exists.

## Consequences

- "May a swarm land work unattended?" is a question about the worker's wrap-up, not about
  this tool. Changing the answer means changing that, not this package.
- The orchestrator cannot consolidate several workers' branches into one landing. Each
  branch lands on its own or not at all.
- This is the reversible half of the scoping decision, and it was taken as "for now."
  Reversing it means adding a lander here *and* removing the worker's — never both at once.
