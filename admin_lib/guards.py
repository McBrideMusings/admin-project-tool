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

parse_guard_tables and validate_requires take a caller-injected `exc` (the
phase's error class — ManifestError at parse, ArchetypeError at resolve, both
subclasses of admin_lib.validate.ValidationError) and raise it directly, the same
exc-injection pattern admin_lib/validate.py uses. This module imports no other
admin_lib module except core, so the parser/resolver can call its parse and
validate hooks without pulling in the runtime.
"""
from __future__ import annotations

import subprocess

from .core import err


GUARDS_BUILTIN = frozenset({"git-clean", "git-pushed"})

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
