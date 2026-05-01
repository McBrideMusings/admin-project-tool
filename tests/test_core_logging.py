"""Tests for the universal logging system in admin_lib/core.py and the generator pipeline.

Covers: _rotate_log, set_log_context, _prepare_log_file, configure_logging,
and render/parse assertions for [logging] manifest config.
"""
from __future__ import annotations

import os
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

import admin_lib.core as core  # noqa: E402
from gen.archetypes import resolve  # noqa: E402
from gen.manifest import Manifest, parse  # noqa: E402
from gen.render import render  # noqa: E402
from gen.fragments import render_command  # noqa: E402
from gen.archetypes import _to_command  # noqa: E402

ARCH_DIR = REPO / "archetypes"
LIB_DIR = REPO / "admin_lib"


# ---------------------------------------------------------------------------
# _rotate_log
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


def test_rotate_log_retain_three_shifts():
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "run.log")
        Path(p + ".1").write_text("old1")
        Path(p + ".2").write_text("old2")
        Path(p).write_text("current")
        core._rotate_log(p, retain=3)
        assert Path(p + ".1").read_text() == "current"
        assert Path(p + ".2").read_text() == "old1"
        assert Path(p + ".3").read_text() == "old2"
        assert not os.path.exists(p)


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


def test_set_log_context_creates_file():
    with tempfile.TemporaryDirectory() as d:
        core.configure_logging({})
        core._init("test", d)
        core.set_log_context("dev", "macos")
        assert os.path.exists(core.LOG_FILE), "set_log_context must create the log file"
        assert core.LOG_FILE.endswith("dev-macos.log")


def test_set_log_context_with_retain_rotates():
    with tempfile.TemporaryDirectory() as d:
        core.configure_logging({})
        core._init("test", d)
        core.set_log_context("dev")
        log_path = core.LOG_FILE
        Path(log_path).write_text("first run")
        core.set_log_context("dev", retain=1)
        assert os.path.exists(log_path + ".1"), "previous log should be rotated to .1"


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
    from gen.manifest import ManifestError
    data = {"project_name": "t", "python_min": "3.11", "logging": {"unknown_key": True}}
    try:
        parse(data)
        assert False, "should raise ManifestError"
    except ManifestError as e:
        assert "unknown_key" in str(e)


# ---------------------------------------------------------------------------
# configure_logging emitted in render output
# ---------------------------------------------------------------------------

def test_configure_logging_emitted_in_render():
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
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    assert "configure_logging(" in script
    assert "'enabled': True" in script or '"enabled": True' in script
    assert "5" in script


def test_no_logging_config_no_configure_call():
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
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    # configure_logging appears as a function definition in core.py bundle,
    # but must NOT appear as a top-level call when no [logging] is configured.
    import re
    calls = re.findall(r"^configure_logging\(", script, re.MULTILINE)
    assert not calls, f"unexpected configure_logging() call without [logging] config"


# ---------------------------------------------------------------------------
# per-command log = false suppresses set_log_context
# ---------------------------------------------------------------------------

def test_log_false_suppresses_set_log_context():
    cmd = _to_command("build", {
        "run": "xcodebuild",
        "desc": "Build",
        "kind": "python",
        "log": False,
    }, source="test")
    out = render_command(cmd)
    assert "set_log_context" not in out, "log=false must suppress set_log_context emission"


def test_log_retain_emitted_in_set_log_context():
    cmd = _to_command("build", {
        "run": "xcodebuild",
        "desc": "Build",
        "kind": "python",
        "log_retain": 7,
    }, source="test")
    out = render_command(cmd)
    assert "retain=7" in out, "log_retain must appear as retain= kwarg in set_log_context"


def test_log_file_emits_set_log_context_path():
    cmd = _to_command("build", {
        "run": "xcodebuild",
        "desc": "Build",
        "kind": "python",
        "log_file": "custom/build.log",
    }, source="test")
    out = render_command(cmd)
    assert "set_log_context_path" in out
    assert "custom/build.log" in out


# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    failed = 0
    for fn in tests:
        try:
            fn()
            print(f"OK  {fn.__name__}")
        except Exception as e:
            import traceback
            print(f"FAIL {fn.__name__}: {e}")
            traceback.print_exc()
            failed += 1
    if failed:
        sys.exit(failed)
    else:
        print(f"\nAll {len(tests)} tests passed.")
