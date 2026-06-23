"""Tests for the universal logging system in admin_lib/core.py and the generator pipeline.

Covers: _rotate_log, set_log_context, _prepare_log_file, configure_logging,
and render/parse assertions for [logging] manifest config.
"""
from __future__ import annotations

import glob
import os
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

import admin_lib.core as core  # noqa: E402
from admin_lib import interp  # noqa: E402
from admin_lib.archetypes import resolve  # noqa: E402
from admin_lib.manifest import Manifest, parse  # noqa: E402
from admin_lib.archetypes import _to_command  # noqa: E402


def _clear_registry():
    core._commands.clear()
    core._actions.clear()
    core._referenced_env_vars.clear()

ARCH_DIR = REPO / "archetypes"
LIB_DIR = REPO / "admin_lib"


# ---------------------------------------------------------------------------
# _prune_logs
# ---------------------------------------------------------------------------

def test_prune_logs_retain_zero_keeps_all():
    with tempfile.TemporaryDirectory() as d:
        core.configure_logging({})
        core._init("test", d)
        os.makedirs(os.path.join(d, "tmp"), exist_ok=True)
        for ts in ["2026-01-01_100000", "2026-01-01_110000"]:
            Path(os.path.join(d, "tmp", f"dev.{ts}.log")).write_text("x")
        core._prune_logs("dev", retain=0)
        assert len(glob.glob(os.path.join(d, "tmp", "dev.*.log"))) == 2, "retain=0 disables pruning"


def test_prune_logs_keeps_n_most_recent():
    with tempfile.TemporaryDirectory() as d:
        core.configure_logging({})
        core._init("test", d)
        os.makedirs(os.path.join(d, "tmp"), exist_ok=True)
        timestamps = ["2026-01-01_100000", "2026-01-01_110000", "2026-01-01_120000"]
        for ts in timestamps:
            Path(os.path.join(d, "tmp", f"dev.{ts}.log")).write_text("x")
        core._prune_logs("dev", retain=2)
        remaining = sorted(glob.glob(os.path.join(d, "tmp", "dev.*.log")))
        assert len(remaining) == 2, f"should keep 2, got {remaining}"
        assert "2026-01-01_110000" in remaining[0]
        assert "2026-01-01_120000" in remaining[1]


def test_prune_logs_noop_when_under_limit():
    with tempfile.TemporaryDirectory() as d:
        core.configure_logging({})
        core._init("test", d)
        os.makedirs(os.path.join(d, "tmp"), exist_ok=True)
        Path(os.path.join(d, "tmp", "dev.2026-01-01_100000.log")).write_text("x")
        core._prune_logs("dev", retain=5)
        assert len(glob.glob(os.path.join(d, "tmp", "dev.*.log"))) == 1, "should not delete"


# ---------------------------------------------------------------------------
# _rotate_log (still used by set_log_context_path)
# ---------------------------------------------------------------------------

def test_rotate_log_retain_zero_skips():
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "run.log")
        Path(p).write_text("data")
        core._rotate_log(p, retain=0)
        assert os.path.exists(p), "retain=0 should not delete or rotate"
        assert not os.path.exists(p + ".1"), "retain=0 should not create .1"


def test_rotate_log_retain_one():
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "run.log")
        Path(p).write_text("original")
        core._rotate_log(p, retain=1)
        assert os.path.exists(p + ".1"), ".1 should be created"
        assert Path(p + ".1").read_text() == "original"
        assert not os.path.exists(p), "original removed after rotation"


def test_rotate_log_nonexistent_file_noop():
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "missing.log")
        core._rotate_log(p, retain=3)  # must not raise


# ---------------------------------------------------------------------------
# configure_logging + set_log_context
# ---------------------------------------------------------------------------

def test_set_log_context_disabled_when_logging_off():
    with tempfile.TemporaryDirectory() as d:
        core.configure_logging({"enabled": False})
        core._init("test", d)
        original_log_file = core.LOG_FILE
        core.set_log_context("cmd", "sub")
        assert core.LOG_FILE == original_log_file, (
            "set_log_context should not change LOG_FILE when logging is disabled"
        )
    core.configure_logging({})


def test_set_log_context_creates_timestamped_file():
    with tempfile.TemporaryDirectory() as d:
        core.configure_logging({})
        core._init("test", d)
        core.set_log_context("dev", "macos")
        assert os.path.exists(core.LOG_FILE), "set_log_context must create the log file"
        import re
        assert re.search(r"dev-macos\.\d{4}-\d{2}-\d{2}_\d{6}\.log$", core.LOG_FILE), (
            f"expected timestamped filename, got {core.LOG_FILE}"
        )


def test_set_log_context_prunes_old_files():
    with tempfile.TemporaryDirectory() as d:
        core.configure_logging({})
        core._init("test", d)
        os.makedirs(os.path.join(d, "tmp"), exist_ok=True)
        import time
        for i in range(4):
            core.set_log_context("dev", retain=3)
            time.sleep(0.01)
        remaining = glob.glob(os.path.join(d, "tmp", "dev.*.log"))
        assert len(remaining) <= 3, f"should keep at most 3, got {len(remaining)}: {remaining}"


# ---------------------------------------------------------------------------
# _prepare_log_file
# ---------------------------------------------------------------------------

def test_prepare_log_file_disabled_skips():
    with tempfile.TemporaryDirectory() as d:
        core.configure_logging({"enabled": False})
        core._init("test", d)
        core._prepare_log_file()
        assert not os.path.exists(core.LOG_FILE), "disabled logging must not create files"
    core.configure_logging({})


def test_prepare_log_file_creates_and_truncates():
    with tempfile.TemporaryDirectory() as d:
        core.configure_logging({})
        core._init("test", d)
        Path(core.LOG_FILE).parent.mkdir(parents=True, exist_ok=True)
        Path(core.LOG_FILE).write_text("old content")
        core._prepare_log_file()
        assert Path(core.LOG_FILE).read_text() == "", "log must be truncated"
        assert os.path.exists(core.LOG_FILE + ".1"), "old log must be rotated"


# ---------------------------------------------------------------------------
# [logging] manifest parsing
# ---------------------------------------------------------------------------

def test_logging_section_parsed():
    data = {
        "project_name": "t",
        "python_min": "3.11",
        "logging": {"enabled": True, "dir": "logs", "retain": 5},
    }
    m = parse(data)
    assert m.logging["enabled"] is True
    assert m.logging["dir"] == "logs"
    assert m.logging["retain"] == 5


def test_logging_section_empty_ok():
    data = {"project_name": "t", "python_min": "3.11"}
    m = parse(data)
    assert m.logging == {}


def test_logging_unknown_key_rejected():
    from admin_lib.manifest import ManifestError
    data = {"project_name": "t", "python_min": "3.11", "logging": {"unknown_key": True}}
    try:
        parse(data)
        assert False, "should raise ManifestError"
    except ManifestError as e:
        assert "unknown_key" in str(e)


# ---------------------------------------------------------------------------
# [logging] config applied by the interpreter
# ---------------------------------------------------------------------------

def test_configure_logging_applied_by_interpreter():
    m = Manifest(
        project_name="Log Test",
        python_min="3.11",
        archetypes=["simple"],
        modules=[],
        urls={},
        overrides={},
        custom={},
        order=[],
        logging={"enabled": True, "retain": 5},
    )
    resolved = resolve(m, ARCH_DIR)
    _clear_registry()
    core.LOG_CONFIG = {}
    interp.register_all(resolved, "Log Test")
    assert core.LOG_CONFIG == {"enabled": True, "retain": 5}


def test_no_logging_config_leaves_log_config_untouched():
    m = Manifest(
        project_name="No Log",
        python_min="3.11",
        archetypes=["simple"],
        modules=[],
        urls={},
        overrides={},
        custom={},
        order=[],
    )
    resolved = resolve(m, ARCH_DIR)
    _clear_registry()
    core.LOG_CONFIG = {}
    interp.register_all(resolved, "No Log")
    assert core.LOG_CONFIG == {}


# ---------------------------------------------------------------------------
# per-command log overrides drive the step-runner's set_log_context calls
# ---------------------------------------------------------------------------

def _capture_log_calls(monkeypatch):
    calls = []
    monkeypatch.setattr(interp, "set_log_context", lambda *a, **k: calls.append(("ctx", a, k)))
    monkeypatch.setattr(interp, "set_log_context_path", lambda *a, **k: calls.append(("path", a, k)))
    monkeypatch.setattr(interp, "run_command", lambda *a, **k: None)
    return calls


def test_log_false_suppresses_set_log_context(monkeypatch):
    calls = _capture_log_calls(monkeypatch)
    cmd = _to_command("build", {"desc": "Build", "steps": ["build"], "log": False},
                      source="test", is_action=False)
    interp._make_step_runner(cmd)([])
    assert not any(c[0] in ("ctx", "path") for c in calls), "log=false must skip set_log_context"


def test_log_retain_passed_to_set_log_context(monkeypatch):
    calls = _capture_log_calls(monkeypatch)
    cmd = _to_command("build", {"desc": "Build", "steps": ["build"], "log_retain": 7},
                      source="test", is_action=False)
    interp._make_step_runner(cmd)([])
    assert ("ctx", ("build",), {"retain": 7}) in calls


def test_log_file_uses_set_log_context_path(monkeypatch):
    calls = _capture_log_calls(monkeypatch)
    cmd = _to_command("build", {"desc": "Build", "steps": ["build"], "log_file": "custom/build.log"},
                      source="test", is_action=False)
    interp._make_step_runner(cmd)([])
    assert ("path", ("custom/build.log",), {}) in calls


# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------

