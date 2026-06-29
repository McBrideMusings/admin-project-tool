"""Guards — the single home for guard handling across the whole pipeline.

A guard is a named precondition referenced from a `requires` list on an action
or target. Before the action runs, each named guard is invoked in order
(fail-fast); on failure the runtime prints the guard's diagnostic and exits
non-zero. Two authoring paths share one name namespace:

  - Built-in guards (this module): Python functions registered at import time,
    free to produce dynamic diagnostics (e.g. the current branch in a hint).
  - User-defined guards (TOML): `[guards.<name>]` table with `run` (shell
    expression) and `fail` (static message).

This module owns every guard-specific step the pipeline needs — one hook per
phase, so guard logic lives in one place rather than smeared across the parser,
resolver, and interpreter:

  - parse_guard_tables(data, source) — validate `[guards.<name>]` definitions
    (manifest parse phase).
  - validate_requires(actions, guard_defs) — every `requires` name resolves to a
    known guard (resolve phase).
  - register(guard_defs) — wire user-defined shell guards into the registry
    (interpreter registration phase). Built-ins register at import.
  - run_requires(names) — run a `requires` list, fail-fast (dispatch phase).
  - run_global_preflight() — process-wide checks that run before any command
    regardless of `requires` (e.g. the `deps-fresh` node/bun workspace check).

parse_guard_tables and validate_requires take a caller-injected `exc` (the
phase's error class — ManifestError at parse, ArchetypeError at resolve, both
subclasses of admin_lib.validate.ValidationError) and raise it directly, the same
exc-injection pattern admin_lib/validate.py uses. This module imports no other
admin_lib module except core, so the parser/resolver can call its parse and
validate hooks without pulling in the runtime.
"""
from __future__ import annotations

import json
import subprocess
from pathlib import Path

from .core import err


GUARDS_BUILTIN = frozenset({"git-clean", "git-pushed", "deps-fresh"})

_guards: dict = {}


_GUARD_TABLE_KEYS = {"run", "fail"}


def parse_guard_tables(data, source, exc):
    """Validate the `[guards.<name>]` tables of a parsed manifest and return the
    guards dict. Each table needs a non-empty `run` + `fail`; a name may not
    collide with a built-in guard. Raises the caller-injected `exc` (the phase's
    ValidationError subclass) on any violation.
    """
    guards = data.get("guards", {}) or {}
    if not isinstance(guards, dict):
        raise exc(f"{source}: '[guards]' must be a table")
    for gname, gdef in guards.items():
        if not isinstance(gdef, dict):
            raise exc(
                f"{source}: '[guards.{gname}]' must be a table with 'run' and 'fail'"
            )
        if gname in GUARDS_BUILTIN:
            raise exc(
                f"{source}: '[guards.{gname}]' collides with a built-in guard name "
                f"({sorted(GUARDS_BUILTIN)}). Built-in guards are registered by "
                f"admin_lib/guards.py and cannot be redefined in TOML."
            )
        unknown = set(gdef) - _GUARD_TABLE_KEYS
        if unknown:
            raise exc(
                f"{source}: '[guards.{gname}]' unknown keys: {sorted(unknown)}, "
                f"valid: {sorted(_GUARD_TABLE_KEYS)}"
            )
        for required_key in ("run", "fail"):
            if required_key not in gdef:
                raise exc(
                    f"{source}: '[guards.{gname}]' missing required key '{required_key}'"
                )
            if not isinstance(gdef[required_key], str) or not gdef[required_key]:
                raise exc(
                    f"{source}: '[guards.{gname}].{required_key}' must be a non-empty string"
                )
    return guards


def validate_requires(actions, guard_defs, exc):
    """Audit that every name in any action's (or target's) `requires` list
    resolves to a known guard — a built-in or a user-defined `[guards.<name>]`.
    Catches typos at resolve time rather than at dispatch. Raises the
    caller-injected `exc` (the phase's ValidationError subclass).

    `actions` maps action name -> an object with a `.meta` dict (`requires`,
    `target_requires`); `guard_defs` is the manifest's user-defined guards dict.
    """
    known = set(GUARDS_BUILTIN) | set(guard_defs)
    for action_name, action in actions.items():
        for req in action.meta.get("requires", []):
            if req not in known:
                raise exc(
                    f"action '{action_name}': requires references unknown guard '{req}'. "
                    f"Known: {sorted(known)}"
                )
        for tname, treq in action.meta.get("target_requires", {}).items():
            if treq is None:
                continue
            for req in treq:
                if req not in known:
                    raise exc(
                        f"action '{action_name}': target '{tname}' requires references unknown "
                        f"guard '{req}'. Known: {sorted(known)}"
                    )


def register_guard(name, fn):
    """Register a Guard. `fn` is called with no args and returns (ok, message).

    On success: (True, None). On failure: (False, "<actionable message>").
    Messages are conventionally prefixed with the guard name.
    """
    _guards[name] = fn


def register_shell_guard(name, run, fail):
    """Register a Guard backed by a `/bin/sh -c <run>` invocation.

    cwd inherits from the process (the generated `./admin` cd's to project root
    at boot, so guards run there too). On non-zero exit, the captured output is
    prepended to the `fail` message — gives context (e.g. a `git status` dump)
    in front of the actionable instruction.
    """
    def _impl():
        proc = subprocess.run(
            ["/bin/sh", "-c", run],
            capture_output=True, text=True,
        )
        if proc.returncode == 0:
            return True, None
        out = (proc.stdout + proc.stderr).strip()
        message = f"{name}: {fail}"
        if out:
            message = f"{out}\n{message}"
        return False, message
    register_guard(name, _impl)


def register(guard_defs):
    """Register every user-defined shell guard from a manifest's guards dict into
    the runtime registry. The interpreter calls this once during setup; built-in
    guards are already registered at import.
    """
    for name, gdef in guard_defs.items():
        register_shell_guard(name, gdef["run"], gdef["fail"])


def run_requires(names):
    """Run a list of named Guards, fail-fast. Returns 0 on all-pass, 1 on first fail.

    On failure, the offending Guard's message is printed via `err()` before
    returning. Unknown names are themselves a failure — the audit pass in
    `admin_lib/archetypes.py` should have caught them upstream.
    """
    for name in names:
        fn = _guards.get(name)
        if fn is None:
            err(f"requires: unknown guard '{name}'")
            return 1
        passed, message = fn()
        if not passed:
            if message:
                err(message)
            return 1
    return 0


# ---------------------------------------------------------------------------
# deps-fresh — node/bun workspace integrity (a built-in guard, also run as a
# global preflight before every command). Catches the "declared a `workspace:*`
# dependency but never linked it into node_modules" class: a fresh git worktree
# that was never installed, or a workspace dep added without re-running install.
# The symptom is an opaque bundler "failed to resolve import" deep in a build;
# this turns it into "your install is stale — run <pm> install" up front.
# ---------------------------------------------------------------------------

_PM_LOCKFILES = (
    ("bun.lock", "bun install"),
    ("bun.lockb", "bun install"),
    ("pnpm-lock.yaml", "pnpm install"),
    ("yarn.lock", "yarn install"),
    ("package-lock.json", "npm install"),
)


def _pm_install_cmd(root):
    """Pick the install command from whichever lockfile the project ships."""
    for lock, cmd in _PM_LOCKFILES:
        if (root / lock).exists():
            return cmd
    return "bun install"


def _read_json(path):
    try:
        return json.loads(path.read_text())
    except (OSError, ValueError):
        return None


def _read_text(path):
    try:
        return path.read_text()
    except OSError:
        return None


def _workspace_globs(root_pkg):
    """Normalize the root package.json `workspaces` field to a list of globs.
    Supports both the array form and npm/bun's `{"packages": [...]}` form."""
    patterns = root_pkg.get("workspaces")
    if isinstance(patterns, dict):
        patterns = patterns.get("packages", [])
    if not isinstance(patterns, list):
        return []
    return [p for p in patterns if isinstance(p, str)]


def _pnpm_globs(root):
    """pnpm declares workspace members in `pnpm-workspace.yaml`, not package.json.
    Parse its top-level `packages:` block directly — it's a flat list of glob
    strings, not worth a YAML dependency for a tool that otherwise ships none.
    Only the standard block-list form is handled; anything else degrades to []."""
    text = _read_text(root / "pnpm-workspace.yaml")
    if text is None:
        return []
    globs, in_packages = [], False
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if not line[:1].isspace():  # a top-level key
            in_packages = stripped.startswith("packages:")
            continue
        if in_packages and stripped.startswith("- "):
            globs.append(stripped[2:].strip().strip("'\""))
    return globs


def _workspace_members(root, root_pkg):
    """Every workspace member dir holding a package.json (the root package
    included, since it can declare workspace deps too).

    Pulls globs from both package.json `workspaces` (npm/bun/yarn) and
    `pnpm-workspace.yaml` (pnpm). Honors npm/yarn `!`-prefixed exclusion
    patterns, and never descends into a `node_modules/` (a `packages/**`-style
    glob would otherwise pull installed deps in as if they were members)."""
    includes, excludes = [], []
    for pat in _workspace_globs(root_pkg) + _pnpm_globs(root):
        (excludes if pat.startswith("!") else includes).append(pat.lstrip("!"))
    excluded = set()
    for pat in excludes:
        excluded.update(root.glob(pat))
    members = [root]
    for pat in includes:
        for p in sorted(root.glob(pat)):
            if p in excluded or "node_modules" in p.relative_to(root).parts:
                continue
            if (p / "package.json").is_file():
                members.append(p)
    return members


def _link_present(consumer, root, dep):
    """A `workspace:*` dep is satisfied when it's linked into node_modules —
    either per-consumer or hoisted to the workspace root. We require an actual
    `package.json` at the link target (exists() follows the symlink), so a
    dangling link or a leftover same-named empty dir counts as missing, not
    satisfied."""
    for base in (consumer, root):
        if (base / "node_modules" / dep / "package.json").exists():
            return True
    return False


def _scan_missing(root, root_pkg):
    """Return [(consumer_rel, dep)] for every `workspace:*` dependency declared
    by a workspace member but not linked into node_modules."""
    missing = []
    for member in _workspace_members(root, root_pkg):
        pkg = _read_json(member / "package.json")
        if pkg is None:
            continue
        deps = {}
        for field in ("dependencies", "devDependencies"):
            d = pkg.get(field)
            if isinstance(d, dict):
                deps.update(d)
        for dep, spec in deps.items():
            if not (isinstance(spec, str) and spec.startswith("workspace:")):
                continue
            if _link_present(member, root, dep):
                continue
            rel = "." if member == root else str(member.relative_to(root))
            missing.append((rel, dep))
    return missing


def deps_fresh_check(root=None):
    """Core of the `deps-fresh` guard. Returns (ok, message).

    No-op pass when the cwd isn't a node project (no root package.json) — so it's
    safe to run globally across every archetype. For a node/bun workspace it
    enumerates each member's `workspace:*` dependencies and confirms each is
    linked; any that aren't mean the install is stale.
    """
    root = Path.cwd() if root is None else Path(root)
    root_pkg = _read_json(root / "package.json")
    if root_pkg is None:
        return True, None

    try:
        missing = _scan_missing(root, root_pkg)
    except Exception:
        # This runs as a global preflight before every command — a bug here, or
        # an unusual manifest (e.g. an absolute/`~` workspace glob, which
        # Path.glob rejects on 3.9–3.12), must never crash or block the tool.
        # Degrade to a no-op pass.
        return True, None

    if not missing:
        return True, None

    listing = "\n".join(f"  {dep}  (needed by {consumer})" for consumer, dep in missing)
    return False, (
        "deps-fresh: workspace package(s) declared but not linked in node_modules — "
        "your install is stale (a fresh worktree, or a workspace dep added without "
        f"reinstalling):\n{listing}\n"
        f"Fix:  {_pm_install_cmd(root)}"
    )


# The always-on checks, run once before every command. This tuple is the single
# source of truth — the preflight analogue of GUARDS_BUILTIN; add a check by
# appending its `() -> (ok, message)` function here. Commands named in
# _PREFLIGHT_EXEMPT skip the whole pass: they're the control/diagnostic verbs you
# need in order to recover from (or look into) the very state a check might block
# on — blocking `logs`/`kill` on a stale install would lock you out of fixing it.
_PREFLIGHT_CHECKS = (deps_fresh_check,)
_PREFLIGHT_EXEMPT = frozenset({"logs", "kill"})

_preflight_result = None


def reset_preflight():
    """Clear the memoized preflight verdict. For tests and any long-lived host
    that dispatches more than once per process."""
    global _preflight_result
    _preflight_result = None


def run_global_preflight(command=None):
    """Run the always-on preflight checks once per invocation, before a command
    dispatches (CLI or TUI). Returns 0 on pass, 1 on first failure; the failing
    check's message is printed via err().

    `command` is the name of the command about to run; the control/diagnostic
    verbs in _PREFLIGHT_EXEMPT skip the pass entirely. The verdict (not merely
    the fact that it ran) is memoized via `_preflight_result`, so a repeat call
    in the same process returns the same answer rather than silently passing."""
    if command in _PREFLIGHT_EXEMPT:
        return 0
    global _preflight_result
    if _preflight_result is not None:
        return _preflight_result
    rc = 0
    for check in _PREFLIGHT_CHECKS:
        passed, message = check()
        if not passed:
            if message:
                err(message)
            rc = 1
            break
    _preflight_result = rc
    return rc


def _git_capture(*args):
    return subprocess.run(["git", *args], capture_output=True, text=True)


def _check_git_repo(guard_name):
    """Shared preflight: confirm we're inside a git work tree. Returns None on success,
    (False, msg) on failure — caller propagates."""
    proc = _git_capture("rev-parse", "--is-inside-work-tree")
    if proc.returncode != 0 or proc.stdout.strip() != "true":
        return (False, f"{guard_name}: not a git repository (or git is unavailable).")
    return None


def _git_clean_guard():
    bad = _check_git_repo("git-clean")
    if bad is not None:
        return bad
    st = _git_capture("status", "--porcelain")
    if st.returncode != 0:
        return False, "git-clean: could not read git status — aborting to be safe."
    if st.stdout.strip():
        return False, (
            "git-clean: working tree has uncommitted or untracked changes — "
            "commit or stash them before deploying (run: git status)."
        )
    return True, None


def _git_pushed_guard():
    bad = _check_git_repo("git-pushed")
    if bad is not None:
        return bad
    branch = _git_capture("rev-parse", "--abbrev-ref", "HEAD").stdout.strip()
    if not branch or branch == "HEAD":
        return False, (
            "git-pushed: detached HEAD — check out a branch and push it before deploying."
        )
    if _git_capture("rev-parse", "--verify", "--quiet", "origin/" + branch).returncode != 0:
        return False, (
            f"git-pushed: branch '{branch}' is not on origin — "
            f"push it first:  git push -u origin {branch}"
        )
    ahead = _git_capture("rev-list", "--count", f"origin/{branch}..HEAD")
    if ahead.returncode != 0 or ahead.stdout.strip() != "0":
        return False, (
            f"git-pushed: '{branch}' has commits not pushed to origin (or the "
            f"comparison failed) — push first:  git push origin {branch}"
        )
    return True, None


register_guard("git-clean", _git_clean_guard)
register_guard("git-pushed", _git_pushed_guard)
register_guard("deps-fresh", deps_fresh_check)
