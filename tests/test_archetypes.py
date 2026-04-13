"""Sanity tests for the archetype catalog and generator pipeline.

Asserts that every archetype in archetypes/ loads, has the required shape,
that detectors behave, and that a multi-archetype composition round-trips
through resolve+render without crashing.
"""
from __future__ import annotations

import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from gen.archetypes import load_archetype, resolve, ArchetypeError, _to_command  # noqa: E402
from gen.detectors import detect  # noqa: E402
from gen.fragments import collect_env_var_calls  # noqa: E402
from gen.manifest import Command, Manifest, ManifestError, parse  # noqa: E402
from gen.render import render  # noqa: E402

ARCH_DIR = REPO / "archetypes"
DET_DIR = REPO / "detectors"
LIB_DIR = REPO / "admin_lib"

EXPECTED = [
    "hugo", "simple", "go-cli", "firefox-extension",
    "stash-plugin", "unraid-plugin", "docker-unraid", "apple",
]


def test_all_archetypes_load_and_validate():
    for name in EXPECTED:
        arch = load_archetype(ARCH_DIR, name)
        assert arch.name, f"{name}: empty archetype name"
        assert arch.modules, f"{name}: empty modules list"
        assert arch.commands, f"{name}: no commands"
        for cmd_name, data in arch.commands.items():
            assert isinstance(data, dict), f"{name}.{cmd_name}: not a dict"
            has_run = bool(data.get("run"))
            has_targets = bool(data.get("targets"))
            assert has_run or has_targets, (
                f"{name}.{cmd_name}: needs 'run' or 'targets'"
            )
            assert data.get("desc"), f"{name}.{cmd_name}: missing desc"
    print(f"OK: {len(EXPECTED)} archetypes load and validate")


def test_composition_docker_unraid_plus_apple():
    """docker-unraid + apple is the canonical composition case from the design doc."""
    m = Manifest(
        project_name="Test Composition",
        python_min="3.11",
        archetypes=["docker-unraid", "apple"],
        urls={},
        overrides={},
        custom={},
        order=[],
    )
    resolved = resolve(m, ARCH_DIR)
    # Commands from both should be present; apple's `build` overrides docker-unraid's.
    for name in ("build", "dev", "install", "clean", "deploy", "logs", "reload", "open"):
        assert name in resolved.commands, f"missing command after merge: {name}"
    assert resolved.commands["build"].source == "apple"
    assert resolved.commands["deploy"].source == "docker-unraid"
    # Render must not crash.
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    assert "def cmd_build" in script
    assert "def cmd_deploy" in script
    assert "run_with_reload" in script  # docker-reload-loop fragment
    assert "pick_target" in script      # multi-target fragment
    print("OK: docker-unraid + apple composition renders")


def test_detector_composition():
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp)
        (d / "Dockerfile").write_text("FROM scratch\n")
        (d / "Foo.xcodeproj").mkdir()
        result = detect(d, DET_DIR)
        assert "docker-unraid" in result["archetypes"]
        assert "apple" in result["archetypes"]
    print("OK: detector returns unioned archetypes")


def test_hugo_roundtrip():
    m = Manifest(
        project_name="Hugo Site",
        python_min="3.11",
        archetypes=["hugo"],
        urls={},
        overrides={},
        custom={},
        order=[],
    )
    resolved = resolve(m, ARCH_DIR)
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    assert "hugo --minify" in script
    assert "http://localhost:1313" in script
    print("OK: hugo round-trips")


def test_unknown_command_key_rejected():
    try:
        _to_command("bad", {"run": "echo hi", "desc": "test", "desription": "typo"}, source="test")
        assert False, "should have raised ArchetypeError"
    except ArchetypeError as e:
        assert "desription" in str(e)
        assert "unknown keys" in str(e)
    print("OK: unknown command key rejected")


def test_invalid_kind_rejected():
    try:
        _to_command("bad", {"run": "echo hi", "desc": "test", "kind": "bogus"}, source="test")
        assert False, "should have raised ArchetypeError"
    except ArchetypeError as e:
        assert "bogus" in str(e)
        assert "unknown kind" in str(e)
    print("OK: invalid kind rejected")


def test_python_min_validation():
    for bad in ["3", "3.11.0", "abc", "3.11+", ""]:
        try:
            parse({"project_name": "T", "python_min": bad})
            assert False, f"should have rejected python_min={bad!r}"
        except ManifestError as e:
            assert "MAJOR.MINOR" in str(e)
    parse({"project_name": "T", "python_min": "3.11"})
    parse({"project_name": "T", "python_min": "3.12"})
    print("OK: python_min validation works")


def test_env_vars_from_urls():
    cmds = [Command(name="x", run="echo ${HOST}", desc="test")]
    urls = {"prod": "${APP_URL:-http://localhost}", "dev": "http://localhost:8080"}
    lines = collect_env_var_calls(cmds, urls)
    var_names = " ".join(lines)
    assert "HOST" in var_names, "should find HOST from command"
    assert "APP_URL" in var_names, "should find APP_URL from url"
    assert "http://localhost" in var_names, "should preserve default"
    lines_no_urls = collect_env_var_calls(cmds)
    assert not any("APP_URL" in l for l in lines_no_urls), "without urls param, APP_URL should not appear"
    print("OK: env vars from urls detected")


if __name__ == "__main__":
    test_all_archetypes_load_and_validate()
    test_composition_docker_unraid_plus_apple()
    test_detector_composition()
    test_hugo_roundtrip()
    test_unknown_command_key_rejected()
    test_invalid_kind_rejected()
    test_python_min_validation()
    test_env_vars_from_urls()
    print("\nAll tests passed.")
