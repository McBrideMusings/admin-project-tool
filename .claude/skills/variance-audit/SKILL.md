---
name: variance-audit
description: Audit every project using the admin task runner for duplicate, variant, or custom implementations worth migrating upstream into admin-project-tool. Fans out reader agents over all admin.toml manifests + their sidecar scripts, clusters findings, then grills each candidate one at a time — file an issue, record it as legitimate, fix it now, or skip. Triggers: 'variance audit', '/variance-audit', 'audit projects for variance', 'find duplicated admin implementations', 'what should be upstreamed'.
---

# /variance-audit — find variance across admin-runner projects, ticket the cleanup

Goal: collapse three recurring smells across projects that use the admin runner —

1. **Duplicate** — the same (or near-same) implementation appears in ≥2 projects' manifests/scripts.
2. **Variant** — the tool (a kind, built-in guard, archetype, or `admin_lib` helper) already provides the feature, but a project rolled its own or a hybrid.
3. **Custom** — a single-project implementation that lives in an escape valve and looks abstractable.

**Escape-valve lens (core judgment rule):** sidecar scripts and multiline inline code in `admin.toml` are guilty until proven necessary. Single-line inline shell/python is acceptable when needed. The moment code is being written inside the TOML, or a project grows `scripts/` helpers, ask: is this truly project-specific, or an abstraction the tool should own?

## Phase 0 — Ground truth

Run from the admin-project-tool repo. Load the capability catalog so "the tool already does this" judgments are grounded, not guessed:

- `admin_lib/kinds.py` — the `KINDS` catalog (every action kind and its config surface)
- `admin_lib/guards.py` — `GUARDS_BUILTIN` and the preflight checks
- `archetypes/*.py` — what each archetype already provides (they are Python modules exporting `ARCHETYPE`, not TOML)
- Skim `admin_lib/` module names for runtime helpers (ports, logging, apple, icons, …)

Load prior verdicts for dedup:

- Ledger: `~/.admin/variance-ledger.md` (may not exist yet — that's fine). Entries here are accepted-as-legitimate variances; never resurface them.
- Open issues: `gh issue list --repo McBrideMusings/admin-project-tool --label variance --state all --limit 200`. Anything already filed (open or closed) is not re-presented.

## Phase 1 — Discover

```
find ~/Projects ~/Work -maxdepth 3 -name admin.toml -not -path '*/node_modules/*'
```

Include admin-project-tool's own manifest (dogfooding counts). Each hit is one project to inventory.

## Phase 2 — Fan-out inventory

Spawn one read-only Explore agent per project, in parallel batches. Each agent returns a structured inventory — extraction only, no judgment. Agent prompt template:

> Read `<project>/admin.toml` and produce a structured inventory. Return exactly:
>
> 1. **Actions** — for each `[actions.*]`: name, kind, requires, and a one-line gist of what `run` does. For multi-target actions, one line per target.
> 2. **Inline code** — any `run` value that is multiline, or that embeds python (`python -c`, heredocs): quote it verbatim if ≤15 lines, otherwise gist + line count.
> 3. **Sidecar scripts** — every local file referenced from any `run`/`targets` value (e.g. `./scripts/x.sh`, `python tools/y.py`). Read each one: path, language, line count, what it does (2-3 sentences), whether it imports `admin_lib` or duplicates shell patterns like port-picking, log-tailing, process-killing, build-then-copy.
> 4. **Unrouted scripts** — filenames (names only, do not read) in `scripts/`, `tools/`, `bin/` that no manifest action references.
> 5. **Custom guards** — any `[guards.*]` tables: name, run, one-line gist.
>
> Return raw data only, no recommendations.

## Phase 3 — Synthesize (main context)

Cluster the inventories into candidate findings:

- Group near-identical `run` bodies / sidecar scripts across projects → **Duplicate** candidates.
- Compare each action/script/guard against the capability catalog from Phase 0 → **Variant** candidates (feature exists upstream; project diverged).
- Remaining escape valves (multiline inline code, sidecar scripts, unrouted script piles) → **Custom** candidates, each with an "is this truly project-specific?" assessment.

Drop any candidate matching a ledger entry or an existing `variance`-labeled issue. Order the survivors by blast radius (number of affected projects, then size of the escape valve).

## Phase 4 — Grill, one candidate at a time

Present candidates one per message, plain chat prose — never `AskUserQuestion`. Each presentation: what it is, where it lives (project paths), what the upstream shape would be (new kind? archetype addition? `admin_lib` helper? guard?), and a recommended verdict with reasoning. Verdicts:

- **File** — create the issue now (see Phase 5) and move on.
- **Legit** — append to the ledger with the user's one-line reason; never resurfaces.
- **Fix now** — implement it in this session: upstream change in admin-project-tool first, then update each affected project's manifest/scripts to adopt it. Run `./admin deploy` after tool changes. Normal commit-confirmation rules apply.
- **Skip** — no record; it resurfaces on the next run.

Wait for the user's verdict on each candidate before showing the next.

## Phase 5 — Verdict mechanics

**File.** Ensure the label exists (`gh label create variance --repo McBrideMusings/admin-project-tool --description "found by /variance-audit" 2>/dev/null || true`), then create the issue with a HEREDOC body (never inline multiline markdown in `--body`):

- Title: imperative, names the abstraction ("Add X kind", "Move Y into apple archetype").
- Body sections: **Problem** (the variance, quoting the divergent snippets), **Proposed abstraction** (where it lands: kind / archetype / admin_lib / guard), **Affected projects** (absolute paths), **Done when** — checklist: upstream implementation + one checkbox per affected project ("migrate <project> to the shared implementation").
- Label: `variance`. No AI attribution anywhere.

**Legit.** Append to `~/.admin/variance-ledger.md` (create with a one-line header if missing):

```markdown
## <stable-kebab-slug>
- date: <YYYY-MM-DD>
- projects: <paths>
- what: <one-line description of the variance>
- why legit: <user's reason>
```

The slug + description are what Phase 3 matches against on future runs. This file is machine-local state — never commit it, never copy it into a repo.

## Phase 6 — Wrap

Summary table: candidate → verdict → artifact (issue #, ledger slug, commit, or —). Do not auto-commit anything; do not chain into other skills.
