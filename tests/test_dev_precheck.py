"""Tests for the live-vs-orphan dev precheck (#427).

Folds muxarr's ~86-line bash `dev-precheck` heredoc into a portable
`kind = "dev-precheck"` backed by admin_lib.ports_nuke.dev_precheck. The core
distinction under test: reap a corpse from a dead dev session (no TTY in its
ancestry) but never kill the interactive session a human is sitting in (a TTY
somewhere up the chain).

Spawning a process with a real controlling terminal is not portable in a test
harness, so the *classification* is unit-tested against an injected tty/ppid
snapshot, and the orphan-reap path is exercised end-to-end with a real spawned
process (pytest runs detached, so its children have no TTY ancestor → orphans).
"""
from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import time
import tomllib
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib import ports_nuke  # noqa: E402
from admin_lib.manifest import ManifestError, parse  # noqa: E402
from admin_lib.ports_nuke import (  # noqa: E402
    _argv_in_project,
    dev_precheck,
    has_tty_ancestor,
)


def _parse(toml: str):
    return parse(tomllib.loads(toml), source="t")


def _spawn(cwd: Path, argv_path: str):
    """A long-lived `python <argv_path>`-shaped process running in `cwd`.

    argv0 basename is the runtime (python3/python) and argv carries argv_path so
    _argv_in_project can match it when argv_path is inside the project.
    """
    p = subprocess.Popen(
        [sys.executable, "-c", "import time; time.sleep(60)", argv_path],
        cwd=str(cwd),
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    time.sleep(0.3)  # let it appear in ps
    return p


def _runtime_of(pid: int):
    """The argv0 basename the process table reports for `pid`.

    Derived live rather than hardcoded because it differs by platform/venv
    (macOS reports the framework 'Python', Linux a venv 'python'/'python3.x').
    """
    for proc in ports_nuke.list_processes():
        if proc["pid"] == pid:
            return os.path.basename(proc.get("argv0") or "")
    raise AssertionError(f"pid {pid} not visible in the process table")


def _alive(p) -> bool:
    return p.poll() is None


def _reap(*procs):
    for p in procs:
        if _alive(p):
            p.kill()
        try:
            p.wait(timeout=5)
        except Exception:
            pass


# --- has_tty_ancestor: the classification core ---------------------------


def test_tty_on_the_process_itself_is_live():
    # 100 has a tty; walk starts at the pid and checks it first.
    tmap = {100: (1, True)}
    assert has_tty_ancestor(100, tmap) is True


def test_tty_on_an_ancestor_is_live():
    # 100 -> 50 -> 10, and 10 (a shell) owns the terminal.
    tmap = {100: (50, False), 50: (10, False), 10: (1, True)}
    assert has_tty_ancestor(100, tmap) is True


def test_chain_to_init_without_tty_is_orphan():
    tmap = {100: (50, False), 50: (1, False), 1: (0, False)}
    assert has_tty_ancestor(100, tmap) is False


def test_missing_ancestor_is_treated_as_orphan():
    # ppid 50 not in the snapshot (raced away) => reap, don't refuse dev.
    tmap = {100: (50, False)}
    assert has_tty_ancestor(100, tmap) is False


def test_cycle_in_snapshot_does_not_hang():
    # A bogus ps could report a cycle; must terminate and classify as orphan.
    tmap = {100: (50, False), 50: (100, False)}
    assert has_tty_ancestor(100, tmap) is False


# --- _argv_in_project narrowing ------------------------------------------


def test_argv_in_project_matches_a_path_inside_root():
    proc = {"argv": ["bun", "/root/projects/muxarr/worker.js"]}
    assert _argv_in_project(proc, "/root/projects/muxarr") is True


def test_argv_in_project_rejects_npx_cache_outside_root():
    proc = {"argv": ["node", "/root/.npm/_npx/abc/comfyui-mcp"]}
    assert _argv_in_project(proc, "/root/projects/muxarr") is False


def test_argv_in_project_matches_root_itself():
    proc = {"argv": ["node", "/root/projects/muxarr"]}
    assert _argv_in_project(proc, "/root/projects/muxarr") is True


# --- dev_precheck end-to-end ---------------------------------------------


def test_orphan_in_project_is_reaped_and_returns_zero(monkeypatch):
    with tempfile.TemporaryDirectory() as d:
        root = Path(d).resolve()
        marker = str(root / "worker.js")
        p = _spawn(root, marker)
        try:
            runtime = _runtime_of(p.pid)
            # Force the classification: the harness itself owns a TTY, so a live
            # spawned child would otherwise inherit that ancestry. Mark it an
            # orphan (no TTY up the chain) to exercise the reap path end-to-end —
            # candidate matching, cwd/argv scoping, and the real _kill_pids.
            monkeypatch.setattr(ports_nuke, "_proc_tty_map", lambda: {p.pid: (1, False)})
            rc = dev_precheck(str(root), runtimes=(runtime,))
            time.sleep(0.4)
            assert rc == 0, "reaping an orphan should let dev proceed"
            assert not _alive(p), "orphaned in-project process should be reaped"
        finally:
            _reap(p)


def test_process_outside_project_is_left_alone():
    with tempfile.TemporaryDirectory() as d_proj, tempfile.TemporaryDirectory() as d_other:
        root = Path(d_proj).resolve()
        other = Path(d_other).resolve()
        stranger = _spawn(other, str(other / "worker.js"))
        try:
            rc = dev_precheck(str(root), runtimes=(_runtime_of(stranger.pid),))
            time.sleep(0.4)
            assert rc == 0
            assert _alive(stranger), "a process in another checkout must survive"
        finally:
            _reap(stranger)


def test_runtime_without_project_argv_is_ignored():
    """cwd-under-project alone is too loose: an unrelated tool launched from the
    repo dir (no project path in argv) must not be reaped."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d).resolve()
        # argv path points OUTSIDE root, though cwd is inside it.
        bystander = _spawn(root, "/opt/some-mcp-server/index.js")
        try:
            rc = dev_precheck(str(root), runtimes=(_runtime_of(bystander.pid),))
            time.sleep(0.4)
            assert rc == 0
            assert _alive(bystander), "runtime with no in-project argv must survive"
        finally:
            _reap(bystander)


def test_live_session_aborts(monkeypatch):
    """A candidate whose ancestry crosses a TTY aborts (returns 1) and is NOT
    killed. Forced via an injected tty map so the test needs no real terminal."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d).resolve()
        marker = str(root / "worker.js")
        p = _spawn(root, marker)
        try:
            runtime = _runtime_of(p.pid)
            monkeypatch.setattr(
                ports_nuke, "_proc_tty_map", lambda: {p.pid: (1, True)}
            )
            rc = dev_precheck(str(root), runtimes=(runtime,))
            time.sleep(0.3)
            assert rc == 1, "a live session must abort the precheck"
            assert _alive(p), "a live session must never be killed"
        finally:
            _reap(p)


def test_running_container_aborts(monkeypatch):
    monkeypatch.setattr(ports_nuke, "_container_running", lambda name: name == "muxarr-dev")
    with tempfile.TemporaryDirectory() as d:
        rc = dev_precheck(str(Path(d).resolve()), containers=("muxarr-dev",))
        assert rc == 1, "a running container must abort the precheck"


def test_absent_container_proceeds(monkeypatch):
    monkeypatch.setattr(ports_nuke, "_container_running", lambda name: False)
    with tempfile.TemporaryDirectory() as d:
        rc = dev_precheck(str(Path(d).resolve()), containers=("muxarr-dev",))
        assert rc == 0


def test_reap_pidfiles_removed_on_orphan(monkeypatch):
    with tempfile.TemporaryDirectory() as d:
        root = Path(d).resolve()
        marker = str(root / "worker.js")
        pidfile = root / "worker.pid"
        pidfile.write_text("123")
        p = _spawn(root, marker)
        try:
            runtime = _runtime_of(p.pid)
            monkeypatch.setattr(ports_nuke, "_proc_tty_map", lambda: {p.pid: (1, False)})
            rc = dev_precheck(
                str(root),
                runtimes=(runtime,),
                reap_pidfiles=(str(pidfile),),
            )
            time.sleep(0.4)
            assert rc == 0
            assert not pidfile.exists(), "reap_pidfiles should be unlinked after reaping"
        finally:
            _reap(p)


def test_empty_precheck_is_a_no_op():
    with tempfile.TemporaryDirectory() as d:
        rc = dev_precheck(str(Path(d).resolve()))
        assert rc == 0


# --- config adapter + manifest validation --------------------------------


def test_config_resolves_env_in_containers(monkeypatch):
    seen = []
    monkeypatch.setattr(ports_nuke, "_container_running", lambda name: seen.append(name) or False)
    monkeypatch.setenv("MUX_CONTAINER", "muxarr-dev")
    rc = ports_nuke.dev_precheck_from_config({"containers": ["${MUX_CONTAINER}"]})
    assert rc == 0
    assert seen == ["muxarr-dev"], "container name should resolve ${VAR}"


def test_manifest_accepts_dev_precheck_table():
    m = _parse(
        'project_name = "X"\n'
        "[dev_precheck]\n"
        'containers = ["muxarr-dev"]\n'
        'runtimes = ["bun", "node"]\n'
        'accept_runtimes = ["ffmpeg"]\n'
        'reap_pidfiles = ["/tmp/muxarr-worker.pid"]\n'
    )
    assert m.dev_precheck["containers"] == ["muxarr-dev"]
    assert m.dev_precheck["accept_runtimes"] == ["ffmpeg"]


def test_manifest_rejects_unknown_key():
    try:
        _parse('project_name = "X"\n[dev_precheck]\nports = [3300]\n')
    except ManifestError as e:
        assert "unknown keys" in str(e) and "ports" in str(e)
    else:
        raise AssertionError("expected ManifestError")


def test_manifest_rejects_non_string_list():
    try:
        _parse('project_name = "X"\n[dev_precheck]\ncontainers = [3300]\n')
    except ManifestError as e:
        assert "must be a list of strings" in str(e)
    else:
        raise AssertionError("expected ManifestError")


def test_manifest_rejects_non_table():
    try:
        _parse('project_name = "X"\ndev_precheck = "nope"\n')
    except ManifestError as e:
        assert "'[dev_precheck]' must be a table" in str(e)
    else:
        raise AssertionError("expected ManifestError")
