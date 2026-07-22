"""Bodies of the `admin branch` and `admin worktree` reserved verbs.

Orchestrate the resolver (admin_lib/resolve) and the git primitives
(admin_lib/gitwt): resolve an input to a branch name, then create/checkout it —
in the current tree (`branch`) or in a separate worktree with post-create hooks
and Herdr association (`worktree`). Modeled on wtree/repo-dash (reference only).
"""
from __future__ import annotations

import os
import shutil
import sys
from pathlib import Path

from admin_lib import ghwt, gitwt, herdr, resolve


def _c(code: str, msg: str) -> str:
    return f"\033[{code}m{msg}\033[0m" if sys.stderr.isatty() else msg


def info(msg: str) -> None:
    print(_c("34", msg), file=sys.stderr)


def ok(msg: str) -> None:
    print(_c("32", msg), file=sys.stderr)


def warn(msg: str) -> None:
    print(_c("33", msg), file=sys.stderr)


def err(msg: str) -> None:
    print(_c("31", f"error: {msg}"), file=sys.stderr)


def _sanitize_dir(branch: str) -> str:
    return branch.strip().replace("/", "-")


# --- admin branch <input> (P1) ----------------------------------------------

def cmd_branch(inp: str) -> int:
    """Resolve `inp` and create/checkout the branch in the current working tree."""
    try:
        gitwt.repo_root()
    except gitwt.GitError as e:
        err(str(e))
        return 1
    try:
        r = resolve.resolve(inp)
    except resolve.ResolveError as e:
        err(str(e))
        return 1
    if _fetch_pr_if_needed(r) != 0:
        return 1
    try:
        action = gitwt.checkout_in_current_tree(r.branch)
    except gitwt.GitError as e:
        err(str(e))
        return 1
    ok(f"{action} branch {r.branch}")
    return 0


def _fetch_pr_if_needed(r) -> int:
    """For a PR input, fetch pull/<n>/head into a local branch (fork-safe) so the
    normal branch-state attach then just checks it out. No-op for non-PR inputs
    or when the local branch already exists."""
    if not r.is_pr or gitwt.branch_exists_local(r.branch):
        return 0
    try:
        gitwt.fetch_pr(r.pr_number, r.branch)
        return 0
    except gitwt.GitError as e:
        err(f"failed to fetch PR #{r.pr_number} (pull/{r.pr_number}/head): {e}")
        return 1


# --- admin worktree <input> (P2) --------------------------------------------

def worktree_root(repo_root: str, cfg: dict) -> Path:
    """Base dir that holds this repo's worktrees: <root>/<repo-basename>.

    Default root ~/.worktrees (where `repo` looks); override via [worktree].root.
    """
    base = cfg.get("root") or "~/.worktrees"
    base = os.path.expanduser(os.path.expandvars(base))
    return Path(base) / Path(repo_root).name


def cmd_worktree(inp: str, cfg: dict | None = None, run_action=None) -> int:
    """Resolve `inp`, create a worktree, run post-create hooks, associate with Herdr.

    cfg: the project's [worktree] table (root/setup/copy). run_action(path, name)
    runs an admin action inside the new worktree; None skips the setup step.
    """
    cfg = cfg or {}
    try:
        root = gitwt.repo_root()
    except gitwt.GitError as e:
        err(str(e))
        return 1
    try:
        r = resolve.resolve(inp)
    except resolve.ResolveError as e:
        err(str(e))
        return 1

    branch = r.branch
    # git allows one worktree per branch — if it already has one, just open that.
    existing = gitwt.worktree_for_branch(branch)
    if existing:
        info(f"branch {branch} already has a worktree at {existing}")
        return _finish(root, existing, branch, cfg, run_action, created=False)

    if _fetch_pr_if_needed(r) != 0:
        return 1

    path = worktree_root(root, cfg) / _sanitize_dir(branch)
    if path.exists():
        err(f"path exists but is not a registered worktree: {path}")
        return 1
    path.parent.mkdir(parents=True, exist_ok=True)

    info(f"creating worktree for {branch} at {path}")
    try:
        gitwt.add_worktree(str(path), branch)
    except gitwt.GitError as e:
        err(str(e))
        return 1
    return _finish(root, str(path), branch, cfg, run_action, created=True)


def _link_manifest(repo_root: str, wt_path: str) -> None:
    """Symlink the main repo's admin.toml into the worktree (the documented
    convention) so admin commands — and the setup action — work there. admin.toml
    is gitignored, so a fresh worktree otherwise has none."""
    src = Path(repo_root) / "admin.toml"
    dst = Path(wt_path) / "admin.toml"
    if src.exists() and not dst.exists():
        try:
            dst.symlink_to(src)
        except OSError:
            pass


def _finish(repo_root: str, wt_path: str, branch: str, cfg: dict, run_action, *, created: bool) -> int:
    if created:
        _copy_files(repo_root, wt_path, cfg.get("copy") or [])
        _link_manifest(repo_root, wt_path)
        rc = _run_setup(wt_path, cfg.get("setup"), run_action)
        if rc != 0:
            return rc
        ok(f"worktree ready: {wt_path}")

    if herdr.open_in_herdr(repo_root, wt_path):
        info(f"opened Herdr workspace for {branch}")
    else:
        # No Herdr — tell the user where to go (admin can't cd the parent shell).
        print(f"cd {wt_path}")
    return 0


def _copy_files(repo_root: str, wt_path: str, files: list[str]) -> None:
    for rel in files:
        src = Path(repo_root) / rel
        if not src.exists():
            continue
        dst = Path(wt_path) / rel
        dst.parent.mkdir(parents=True, exist_ok=True)
        if src.is_dir():
            shutil.copytree(src, dst, dirs_exist_ok=True)
        else:
            shutil.copy2(src, dst)
        info(f"  copied {rel}")


def _run_setup(wt_path: str, action: str | None, run_action) -> int:
    if not action:
        return 0
    if run_action is None:
        warn(f"  [worktree].setup = {action!r} but no manifest to run it against — skipped")
        return 0
    info(f"  running setup action: {action}")
    rc = run_action(wt_path, action)
    if rc != 0:
        err(f"setup action {action!r} failed (exit {rc})")
    return rc


# --- cleanup (P3): admin branch --prune / admin worktree --prune ------------

def _confirm(prompt: str) -> bool:
    if not sys.stdin.isatty():
        return False
    try:
        ans = input(prompt).strip().lower()
    except (EOFError, KeyboardInterrupt):
        print()
        return False
    return ans in ("", "y", "yes")


def prune_branches(dry_run: bool = False, yes: bool = False) -> int:
    """Delete local branches that are provably safe to drop (merged / PR-merged /
    gone / fully pushed). Origin untouched; current/default/checked-out excluded."""
    try:
        gitwt.repo_root()
    except gitwt.GitError as e:
        err(str(e))
        return 1
    gitwt.fetch_prune()
    cands = gitwt.list_cleanup_candidates()
    dead, _ = gitwt.classify_dead(cands, ghwt.merged_pr_head_branches())
    if not dead:
        ok("no dead branches to prune")
        return 0
    info("dead local branches:")
    for d in dead:
        print(f"  {d.name}  ({d.reason})", file=sys.stderr)
    if dry_run:
        return 0
    if not yes and not _confirm(f"delete {len(dead)} branch(es)? [Y/n] "):
        warn("cancelled")
        return 0
    failed = 0
    for d in dead:
        try:
            gitwt.force_delete_branch(d.name)
            ok(f"deleted {d.name}")
        except gitwt.GitError as e:
            err(f"{d.name}: {e}")
            failed += 1
    return 1 if failed else 0


def prune_worktrees(dry_run: bool = False, yes: bool = False) -> int:
    """Remove worktrees whose branch is dead (merged / PR-merged / gone / pushed)
    and delete that branch. Skips the main tree and any worktree with local edits."""
    try:
        root = gitwt.repo_root()
    except gitwt.GitError as e:
        err(str(e))
        return 1
    gitwt.fetch_prune()
    merged_prs = ghwt.merged_pr_head_branches()
    victims = []  # (worktree, reason)
    skipped_dirty = []
    for w in gitwt.worktrees():
        if w.bare or w.path == root or not w.branch:
            continue
        dead, _ = gitwt.classify_dead([gitwt.branch_status(w.branch)], merged_prs)
        if not dead:
            continue
        if gitwt.worktree_dirty(w.path):
            skipped_dirty.append(w)
            continue
        victims.append((w, dead[0].reason))
    for w in skipped_dirty:
        warn(f"skipping {w.path} — uncommitted changes")
    if not victims:
        ok("no dead worktrees to prune")
        return 0
    info("dead worktrees:")
    for w, reason in victims:
        print(f"  {w.path}  [{w.branch}] ({reason})", file=sys.stderr)
    if dry_run:
        return 0
    if not yes and not _confirm(f"remove {len(victims)} worktree(s) and their branches? [Y/n] "):
        warn("cancelled")
        return 0
    failed = 0
    for w, _ in victims:
        try:
            gitwt.remove_worktree(w.path)
            gitwt.force_delete_branch(w.branch)
            ok(f"removed {w.path} [{w.branch}]")
        except gitwt.GitError as e:
            err(f"{w.path}: {e}")
            failed += 1
    return 1 if failed else 0


# --- TUI (P4): bare `admin branch` / `admin worktree` -----------------------

class _Entry:
    __slots__ = ("label", "status", "dead", "data")

    def __init__(self, label, status, dead, data):
        self.label = label
        self.status = status   # short status word shown in the list
        self.dead = dead       # deletable via [d]
        self.data = data       # gitwt.Worktree or branch name


def _tui(title, build_entries, on_delete, on_enter, enter_hint):
    """Full-screen list over build_entries(); [d] deletes dead rows, enter acts.

    build_entries() is re-run after each delete so the list stays current.
    Reuses core's raw-mode key reader + home-cursor helper (stdlib, no curses).
    """
    if not sys.stdout.isatty():
        err("no TTY for the interactive list — use --prune for headless cleanup")
        return 1
    from admin_lib.core import read_key, _make_home, _fit_display
    import termios

    fd = sys.stdin.fileno()
    old = termios.tcgetattr(fd)
    try:
        while True:
            entries = build_entries()
            if not entries:
                ok(f"{title}: nothing to show")
                return 0
            sel = 0
            total = len(entries) + 4  # title, blank, rows, blank, hint
            home = _make_home(fd, total)

            def draw():
                import shutil
                cols = shutil.get_terminal_size((80, 24)).columns
                home()
                sys.stdout.write(_c("1", title) + "\n\n")
                for i, e in enumerate(entries):
                    tag = _c("31" if e.dead else "33", f"[{e.status}]")
                    label = _fit_display(e.label, max(10, cols - 24))
                    if i == sel:
                        sys.stdout.write(_c("36;1", f"  > {label}") + f"  {tag}\n")
                    else:
                        sys.stdout.write(f"    {label}  {tag}\n")
                sys.stdout.write("\n" + _c("2", f"  [↑/↓ j/k] move  [d] delete dead  [enter] {enter_hint}  [q] quit") + "\n")
                sys.stdout.flush()

            rebuild = False
            while not rebuild:
                draw()
                key = read_key()
                if key in ("q", "Q", "esc"):
                    home()
                    return 0
                if key in ("up", "k"):
                    sel = (sel - 1) % len(entries)
                elif key in ("down", "j"):
                    sel = (sel + 1) % len(entries)
                elif key == "d":
                    e = entries[sel]
                    if not e.dead:
                        continue
                    termios.tcsetattr(fd, termios.TCSADRAIN, old)
                    home()
                    if _confirm(f"delete {e.label}? [Y/n] "):
                        rc = on_delete(e)
                        if rc == 0:
                            ok(f"deleted {e.label}")
                        else:
                            err(f"failed to delete {e.label}")
                    rebuild = True
                elif key in ("\r", "\n"):
                    e = entries[sel]
                    termios.tcsetattr(fd, termios.TCSADRAIN, old)
                    home()
                    return on_enter(e)
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old)


def branch_entries() -> list:
    """Rows for the branches TUI: dead cleanup candidates first, then survivors."""
    gitwt.fetch_prune()
    cands = gitwt.list_cleanup_candidates()
    dead, survivors = gitwt.classify_dead(cands, ghwt.merged_pr_head_branches())
    entries = [_Entry(d.name, d.reason, True, d.name) for d in dead]
    for s in survivors:
        status = "stale" if s.is_stale else ("ahead" if s.ahead else "unmerged")
        entries.append(_Entry(s.name, status, False, s.name))
    return entries


def worktree_entries(root: str) -> list:
    """Rows for the worktrees TUI: each linked worktree tagged dirty/dead/active."""
    merged_prs = ghwt.merged_pr_head_branches()
    entries = []
    for w in gitwt.worktrees():
        if w.bare or w.path == root or not w.branch:
            continue
        if gitwt.worktree_dirty(w.path):
            entries.append(_Entry(f"{w.branch}  {w.path}", "dirty", False, w))
            continue
        dead, _ = gitwt.classify_dead([gitwt.branch_status(w.branch)], merged_prs)
        if dead:
            entries.append(_Entry(f"{w.branch}  {w.path}", dead[0].reason, True, w))
        else:
            entries.append(_Entry(f"{w.branch}  {w.path}", "active", False, w))
    return entries


def tui_branches() -> int:
    try:
        gitwt.repo_root()
    except gitwt.GitError as e:
        err(str(e))
        return 1

    def on_delete(e):
        try:
            gitwt.force_delete_branch(e.data)
            return 0
        except gitwt.GitError:
            return 1

    def on_enter(e):
        try:
            action = gitwt.checkout_in_current_tree(e.data)
            ok(f"{action} branch {e.data}")
            return 0
        except gitwt.GitError as ex:
            err(str(ex))
            return 1

    return _tui("branches", branch_entries, on_delete, on_enter, "checkout")


def tui_worktrees() -> int:
    try:
        root = gitwt.repo_root()
    except gitwt.GitError as e:
        err(str(e))
        return 1

    def on_delete(e):
        w = e.data
        try:
            gitwt.remove_worktree(w.path)
            gitwt.force_delete_branch(w.branch)
            return 0
        except gitwt.GitError:
            return 1

    def on_enter(e):
        w = e.data
        if herdr.open_in_herdr(root, w.path):
            info(f"opened Herdr workspace for {w.branch}")
        else:
            print(f"cd {w.path}")
        return 0

    return _tui("worktrees", lambda: worktree_entries(root), on_delete, on_enter, "open")
