"""Tests for [dev_kill].patterns — the argv-pattern pass in nuke_dev_processes (#416).

wedding, portfolio, and etv-next each hand-rolled `pkill -f '<thing>'` because
their dev process binds no port we listed, writes no pidfile, and is not a JS
runtime — so the first three passes cannot see it.

The pkill they were replacing is machine-wide: `pkill -f 'hugo server'` kills the
same dev server running from an unrelated checkout. These patterns are cwd-scoped
to the project by default, which is why the scoping tests below matter more than
the matching ones.
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

from admin_lib.manifest import ManifestError, parse  # noqa: E402
from admin_lib.ports_nuke import nuke_dev_processes  # noqa: E402


def _parse(toml: str):
    return parse(tomllib.loads(toml), source="t")


def _spawn(cwd: Path, tag: str):
    """A long-lived process with `tag` in its argv, running in `cwd`."""
    p = subprocess.Popen(
        [sys.executable, "-c", f"import time; time.sleep(60)  # {tag}"],
        cwd=str(cwd),
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    time.sleep(0.3)  # let it appear in ps
    return p


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


# --- config validation ---------------------------------------------------


def test_patterns_accepts_bare_strings():
    m = _parse('project_name = "X"\n[dev_kill]\npatterns = ["hugo server", "workerd"]\n')
    assert m.dev_kill["patterns"] == ["hugo server", "workerd"]


def test_patterns_accepts_tables():
    m = _parse(
        'project_name = "X"\n[[dev_kill.patterns]]\nmatch = "ersatztv"\nregex = true\n'
    )
    assert m.dev_kill["patterns"][0] == {"match": "ersatztv", "regex": True}


def test_patterns_rejects_entry_without_match():
    try:
        _parse('project_name = "X"\n[[dev_kill.patterns]]\nregex = true\n')
    except ManifestError as e:
        assert "needs a non-empty 'match' string" in str(e)
    else:
        raise AssertionError("expected ManifestError")


def test_patterns_rejects_unknown_key():
    try:
        _parse('project_name = "X"\n[[dev_kill.patterns]]\nmatch = "x"\nsignal = "KILL"\n')
    except ManifestError as e:
        assert "unknown keys" in str(e) and "signal" in str(e)
    else:
        raise AssertionError("expected ManifestError")


def test_patterns_rejects_non_bool_flag():
    try:
        _parse('project_name = "X"\n[[dev_kill.patterns]]\nmatch = "x"\nregex = "yes"\n')
    except ManifestError as e:
        assert "'regex' must be a boolean" in str(e)
    else:
        raise AssertionError("expected ManifestError")


# --- matching ------------------------------------------------------------


def test_pattern_kills_a_matching_process_in_the_project():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d).resolve()
        p = _spawn(root, "hugo-server-marker")
        try:
            nuke_dev_processes(str(root), patterns=["hugo-server-marker"])
            time.sleep(0.4)
            assert not _alive(p), "matching in-project process should have been killed"
        finally:
            _reap(p)


def test_pattern_leaves_non_matching_processes_alone():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d).resolve()
        keep = _spawn(root, "something-else-entirely")
        try:
            nuke_dev_processes(str(root), patterns=["hugo-server-marker"])
            time.sleep(0.4)
            assert _alive(keep), "non-matching process must survive"
        finally:
            _reap(keep)


def test_regex_pattern_matches():
    """etv-next matched target/[^/]*/ersatztv — a regex, not a substring."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d).resolve()
        p = _spawn(root, "target/debug/ersatztv-marker")
        try:
            nuke_dev_processes(
                str(root), patterns=[{"match": r"target/[^/]*/ersatztv-marker", "regex": True}]
            )
            time.sleep(0.4)
            assert not _alive(p)
        finally:
            _reap(p)


def test_bad_regex_is_reported_not_swallowed():
    with tempfile.TemporaryDirectory() as d:
        code = (
            "import sys; sys.path.insert(0, %r)\n"
            "from admin_lib.ports_nuke import nuke_dev_processes\n"
            "nuke_dev_processes(%r, patterns=[{'match': '[unclosed', 'regex': True}])\n"
            % (str(REPO), d)
        )
        r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=20)
        assert r.returncode != 0
        assert "bad regex" in (r.stdout + r.stderr)


# --- scoping (the reason this exists) ------------------------------------


def test_pattern_does_not_kill_a_match_outside_the_project():
    """The whole point: `pkill -f 'hugo server'` would kill this. We must not."""
    with tempfile.TemporaryDirectory() as d_proj, tempfile.TemporaryDirectory() as d_other:
        root = Path(d_proj).resolve()
        other = Path(d_other).resolve()
        stranger = _spawn(other, "hugo-server-marker")
        try:
            nuke_dev_processes(str(root), patterns=["hugo-server-marker"])
            time.sleep(0.4)
            assert _alive(stranger), (
                "a matching process in a different checkout must survive — "
                "cwd scoping is what makes this safer than pkill -f"
            )
        finally:
            _reap(stranger)


def test_global_pattern_opts_out_of_cwd_scoping():
    with tempfile.TemporaryDirectory() as d_proj, tempfile.TemporaryDirectory() as d_other:
        root = Path(d_proj).resolve()
        other = Path(d_other).resolve()
        stranger = _spawn(other, "workerd-global-marker")
        try:
            nuke_dev_processes(
                str(root), patterns=[{"match": "workerd-global-marker", "global": True}]
            )
            time.sleep(0.4)
            assert not _alive(stranger), "global=true should reach outside the project"
        finally:
            _reap(stranger)


def test_pidfile_kill_reaps_children_too():
    """A pidfile names a supervisor; SIGTERM to it does not take its children.

    term did `pkill -P "$p"; kill "$p"` by hand for exactly this — its server /
    renderer / mac pids are parents whose children would otherwise survive and
    keep holding ports.
    """
    with tempfile.TemporaryDirectory() as d:
        root = Path(d).resolve()
        parent = subprocess.Popen(
            [sys.executable, "-c",
             "import subprocess, sys, time; "
             "subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)']); "
             "time.sleep(60)"],
            cwd=str(root), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
        time.sleep(0.8)
        from admin_lib.ports_nuke import child_pids

        kids = child_pids(parent.pid)
        assert kids, "test setup: expected a child process"
        pidfile = root / "server.pid"
        pidfile.write_text(str(parent.pid))
        try:
            nuke_dev_processes(str(root), pidfiles=[{"path": str(pidfile), "format": "int"}])
            time.sleep(0.6)
            assert not _alive(parent), "supervisor should be dead"
            for k in kids:
                assert not _pid_alive(k), f"child {k} survived the pidfile kill"
            assert not pidfile.exists(), "stale pidfile should be unlinked"
        finally:
            _reap(parent)
            for k in kids:
                try:
                    os.kill(k, 9)
                except OSError:
                    pass


def _pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def test_port_entries_resolve_env_placeholders():
    """muxarr's dev port is ${PORT:-3300}; pidfile paths already resolved ${VAR}
    but ports did not, forcing the port to be hardcoded and drift."""
    import os as _os

    from admin_lib.ports_nuke import nuke_dev_from_config

    prev = _os.environ.pop("PORT", None)
    try:
        # No listener on it — this asserts the value parses, not that it kills.
        rc = nuke_dev_from_config({"ports": ["${PORT:-3300}", 3350]})
        assert rc == 0
        _os.environ["PORT"] = "3999"
        rc = nuke_dev_from_config({"ports": ["${PORT:-3300}"]})
        assert rc == 0
    finally:
        _os.environ.pop("PORT", None)
        if prev is not None:
            _os.environ["PORT"] = prev


def test_unresolvable_port_is_reported():
    code = (
        "import sys; sys.path.insert(0, %r)\n"
        "from admin_lib.ports_nuke import nuke_dev_from_config\n"
        "nuke_dev_from_config({'ports': ['${NOPE_NOT_SET:-abc}']})\n" % str(REPO)
    )
    r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=20)
    assert r.returncode != 0
    assert "not a port" in (r.stdout + r.stderr)


def test_no_patterns_configured_is_a_no_op():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d).resolve()
        keep = _spawn(root, "untouched-marker")
        try:
            nuke_dev_processes(str(root))
            time.sleep(0.3)
            assert _alive(keep)
        finally:
            _reap(keep)
