"""Tests for manifest parse/dump round-trips and edge cases."""
from __future__ import annotations

import io
import sys
import tomllib
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from gen.manifest import Manifest, ManifestError, dump, parse  # noqa: E402


def _base_data(**overrides) -> dict:
    data = {"project_name": "test-proj", "python_min": "3.11"}
    data.update(overrides)
    return data


def _base_manifest(**overrides) -> Manifest:
    m = Manifest(
        project_name="test-proj",
        python_min="3.11",
        archetypes=[],
        modules=[],
        urls={},
        overrides={},
        custom={},
        order=[],
    )
    for k, v in overrides.items():
        object.__setattr__(m, k, v)
    return m


def _capture_stderr(fn) -> str:
    """Execute function and capture stderr output."""
    buf = io.StringIO()
    old_stderr = sys.stderr
    sys.stderr = buf
    try:
        fn()
    finally:
        sys.stderr = old_stderr
    return buf.getvalue()


# ---------------------------------------------------------------------------
# exclude_modules round-trip
# ---------------------------------------------------------------------------

def test_exclude_modules_survives_dump():
    m = _base_manifest(exclude_modules=["log_columns", "logtail"])
    toml = dump(m)
    assert 'exclude_modules = ["log_columns", "logtail"]' in toml, (
        f"exclude_modules not emitted in dump output:\n{toml}"
    )


def test_exclude_modules_round_trip():
    m = _base_manifest(exclude_modules=["log_columns"])
    toml = dump(m)
    m2 = parse(tomllib.loads(toml))
    assert m2.exclude_modules == ["log_columns"], (
        f"exclude_modules lost after round-trip; got {m2.exclude_modules!r}"
    )


def test_exclude_modules_empty_not_emitted():
    m = _base_manifest(exclude_modules=[])
    toml = dump(m)
    assert "exclude_modules" not in toml, "empty exclude_modules should not be emitted"


# ---------------------------------------------------------------------------
# _inline_table / _inline_list — nested types
# ---------------------------------------------------------------------------

def test_inline_table_nested_dict_round_trips():
    """A command override with a steps list must survive dump+parse."""
    m = _base_manifest(overrides={"build": {
        "desc": "Build all",
        "steps": ["build-mac", "build-ios"],
    }})
    toml = dump(m)
    m2 = parse(tomllib.loads(toml))
    steps = m2.overrides["build"].get("steps")
    assert steps == ["build-mac", "build-ios"], (
        f"steps list not preserved after round-trip; got {steps!r}"
    )


def test_inline_table_list_value_round_trips():
    """A command with a list-valued field (steps) must survive dump+parse."""
    m = _base_manifest(custom={"deploy": {
        "desc": "Deploy",
        "steps": ["build", "push"],
    }})
    toml = dump(m)
    m2 = parse(tomllib.loads(toml))
    steps = m2.custom["deploy"].get("steps")
    assert steps == ["build", "push"], (
        f"list-valued field not preserved after round-trip; got {steps!r}"
    )


# ---------------------------------------------------------------------------
# Unknown-key warning
# ---------------------------------------------------------------------------

def test_unknown_key_prints_warning():
    data = _base_data(exlude_modules=["typo"])  # intentional typo
    warning = _capture_stderr(lambda: parse(data))
    assert "exlude_modules" in warning, (
        f"Expected warning mentioning 'exlude_modules', got: {warning!r}"
    )
    assert "warning" in warning.lower(), "Warning message should say 'warning'"


def test_known_keys_produce_no_warning():
    data = _base_data(archetypes=["docker-unraid"], exclude_modules=["log_columns"])
    warning = _capture_stderr(lambda: parse(data))
    assert warning == "", f"Unexpected warning for known keys: {warning!r}"


# ---------------------------------------------------------------------------
# [logging] round-trip
# ---------------------------------------------------------------------------

def test_logging_section_survives_dump():
    m = _base_manifest(logging={"enabled": True, "dir": "logs", "retain": 5})
    toml = dump(m)
    assert "[logging]" in toml, f"[logging] section not emitted:\n{toml}"
    assert "enabled = true" in toml
    assert 'dir = "logs"' in toml
    assert "retain = 5" in toml


def test_logging_section_round_trips():
    m = _base_manifest(logging={"enabled": False, "retain": 0})
    toml = dump(m)
    m2 = parse(tomllib.loads(toml))
    assert m2.logging["enabled"] is False
    assert m2.logging["retain"] == 0


def test_empty_logging_section_not_emitted():
    m = _base_manifest(logging={})
    toml = dump(m)
    assert "[logging]" not in toml, "empty [logging] should not be emitted"


# ---------------------------------------------------------------------------
# [logs] key order preservation
# ---------------------------------------------------------------------------

def test_logs_dump_preserves_raw_key_order():
    """dump() should write [logs] env entries in the original TOML order, not alphabetically."""
    toml_src = "\n".join([
        'project_name = "Ordering Test"',
        'python_min = "3.11"',
        "[logs.app]",
        'prod = "/var/log/app.log"',  # prod before default_env
        'default_env = "prod"',
        'dev = "/tmp/app.log"',       # dev after default_env
    ])
    m = parse(tomllib.loads(toml_src))
    m.raw = tomllib.loads(toml_src)
    out = dump(m)
    prod_pos = out.index("prod")
    default_pos = out.index("default_env")
    dev_pos = out.index("dev =")
    assert prod_pos < default_pos < dev_pos, (
        f"key order not preserved: prod={prod_pos}, default_env={default_pos}, dev={dev_pos}"
    )


# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------

