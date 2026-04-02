---
name: audit-projects
description: Audit all sibling projects for admin script health — checks bundling, runtime admin_lib references, and patterns worth upstreaming. Run with /audit-projects.
---

# /audit-projects — Audit Admin Scripts Across All Projects

Scan all sibling project directories (../\*) for `admin` scripts and audit them for common problems.

## Instructions

### Phase 1: Discover

Find all `admin` scripts in sibling directories:

```
ls ../*/admin
```

For each one found, run three checks.

### Phase 2: Check Each Project

For each project with an `admin` script, report on three things:

#### Check 1: Bundled vs Runtime Reference

A properly bundled admin script has a header comment like:
```
# @bundled admin_lib=...
```

A broken one imports admin_lib at runtime:
```python
sys.path.insert(0, os.path.expanduser("~/.admin"))
from admin_lib.core import ...
```

**If it references admin_lib at runtime, flag it.** This means anyone cloning the repo without admin-project-tool installed gets an import error.

#### Check 2: Runtime admin_lib reference (same as unbundled)

This is the same root cause as Check 1 — any `sys.path.insert` pointing at `~/.admin` or direct `from admin_lib` imports without a bundled header. Flag these together.

#### Check 3: Patterns Worth Upstreaming

Read the full admin script. Look for:
- Custom helper functions that solve general problems (not project-specific)
- Workarounds for admin_lib limitations (e.g. the `_flatten_icons_alpha` pattern that led to adding `opaque` to `generate_icons`)
- Repeated patterns across multiple projects that could be a new admin_lib feature
- Custom command implementations that duplicate or extend admin_lib functionality

For each pattern found, briefly describe what it does and whether it's worth abstracting.

### Phase 3: Report

Print a summary table:

```
Project           Bundled?  Runtime Ref?  Patterns to Upstream
─────────────────────────────────────────────────────────────
project-a         YES       NO            none
project-b         NO        YES           custom deploy helper
open-assistant    NO        YES           opaque icon gen (already fixed)
```

### Phase 4: Offer Fixes

For any project that is NOT bundled or HAS runtime references:
1. Ask the user if they want to fix them
2. If yes, run `~/.admin/bundle <path-to-admin>` for each one
3. Verify the bundle worked by checking for the `@bundled` header

For patterns worth upstreaming:
1. List them with enough context for the user to decide
2. Do NOT automatically implement them — just flag for discussion

### Notes

- Only scan directories directly under `../` — don't recurse into nested projects
- Skip directories that don't have an `admin` file
- The bundle tool is at `~/.admin/bundle`
- After bundling, the admin script should be self-contained with no runtime admin_lib dependency
