"""Preconditions for actions and multi-target targets.

Generated `./admin` files reference Guards by name from a `requires` list on an
action or target. Before the action runs, each named Guard is invoked in order
(fail-fast); on failure, the runtime prints the Guard's diagnostic and exits
non-zero. Two authoring paths share one name namespace:

  - Built-in Guards (this module): Python functions, registered at import
    time. Free to produce dynamic diagnostics (e.g. interpolating the current
    branch name into a "push first" hint).
  - User-defined Guards (TOML): `[guards.<name>]` table with `run` (shell
    expression) and `fail` (static message). The renderer emits a
    `register_shell_guard(name, run, fail)` call into the bundled output.

Both kinds resolve through `_guards`; both are invoked via `run_requires`.
"""
from __future__ import annotations

import subprocess

from .core import err


GUARDS_BUILTIN = frozenset({"git-clean", "git-pushed"})

_guards: dict = {}


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
