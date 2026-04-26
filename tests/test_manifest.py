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
    """A command with targets = {mac = "...", ios = "..."} must survive dump+parse."""
    m = _base_manifest(custom={"build": {
        "run": "",
        "desc": "Build",
        "kind": "multi-target",
        "targets": {"mac": "xcodebuild mac", "ios": "xcodebuild ios"},
    }})
    toml = dump(m)
    m2 = parse(tomllib.loads(toml))
    targets = m2.custom["build"].get("targets")
    assert targets == {"mac": "xcodebuild mac", "ios": "xcodebuild ios"}, (
        f"nested targets dict not preserved after round-trip; got {targets!r}"
    )


def test_inline_table_list_value_round_trips():
    """A command with a list-valued field (e.g. steps) must survive dump+parse."""
    m = _base_manifest(custom={"deploy": {
        "run": "",
        "desc": "Deploy",
        "kind": "sequence",
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
            print(f"FAIL {fn.__name__}: {e}")
            failed += 1
    if failed:
        sys.exit(failed)
    else:
        print(f"\nAll {len(tests)} tests passed.")
