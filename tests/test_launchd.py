"""Tests for [launchd] and admin_lib.launchd.

The behaviour worth pinning down is the one that motivated the module: macOS
Background Task Management posts an "App Background Activity" notification every
time a launchd plist is registered, and it keys off the plist install rather
than the process start. A deploy that rewrote an identical plist and did
bootout+bootstrap therefore produced a fresh notification on every deploy. So
`install` must reach for `kickstart -k` when the rendered plist matches what is
already on disk and the agent is loaded, and must only write+re-register when
something genuinely changed.
"""
from __future__ import annotations

import os
import plistlib
import subprocess
import sys
import tomllib
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib import launchd  # noqa: E402
from admin_lib.manifest import ManifestError, parse  # noqa: E402

BASE = """
project_name = "t"
archetypes = ["simple"]
modules = ["launchd"]
"""


def _parse(table: str):
    return parse(tomllib.loads(BASE + table), source="t")


def _stub_launchctl(tmp_path, loaded: bool):
    """Put a launchctl on PATH that records argv and reports load state."""
    bin_dir = tmp_path / "stub-bin"
    bin_dir.mkdir(exist_ok=True)
    calls = tmp_path / "launchctl-calls.txt"
    stub = bin_dir / "launchctl"
    stub.write_text(
        "#!/bin/sh\n"
        f'echo "$@" >> "{calls}"\n'
        'if [ "$1" = "print" ]; then\n'
        f"  exit {0 if loaded else 1}\n"
        "fi\n"
        "exit 0\n"
    )
    stub.chmod(0o755)
    os.environ["PATH"] = str(bin_dir) + os.pathsep + os.environ["PATH"]
    return calls


def _program(tmp_path):
    p = tmp_path / "bin" / "myserver"
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text("#!/bin/sh\nexit 0\n")
    p.chmod(0o755)
    return p


def _cfg(tmp_path):
    return {
        "label": "com.example.myserver",
        "program": str(_program(tmp_path)),
        "args": ["-wait-for-lock"],
        "stdout": str(tmp_path / "logs" / "server.log"),
        "throttle": 10,
        "inherit_path": False,
        "inherit_home": False,
    }


# ---------------------------------------------------------------------------
# Manifest validation
# ---------------------------------------------------------------------------

def test_launchd_table_parses():
    m = _parse('[launchd]\nlabel = "com.example.x"\nprogram = "/usr/bin/true"\n')
    assert m.launchd["label"] == "com.example.x"


def test_launchd_requires_label_and_program():
    with pytest.raises(ManifestError, match=r"\[launchd\].program"):
        _parse('[launchd]\nlabel = "com.example.x"\n')
    with pytest.raises(ManifestError, match=r"\[launchd\].label"):
        _parse('[launchd]\nprogram = "/usr/bin/true"\n')


def test_launchd_rejects_unknown_key():
    with pytest.raises(ManifestError, match="unknown keys"):
        _parse(
            '[launchd]\nlabel = "com.example.x"\nprogram = "/usr/bin/true"\n'
            'keepalive = true\n'
        )


def test_launchd_rejects_wrong_types():
    head = '[launchd]\nlabel = "com.example.x"\nprogram = "/usr/bin/true"\n'
    with pytest.raises(ManifestError, match=r"\[launchd\].args"):
        _parse(head + "args = 3\n")
    with pytest.raises(ManifestError, match=r"\[launchd\].throttle"):
        _parse(head + 'throttle = "ten"\n')
    with pytest.raises(ManifestError, match=r"\[launchd\].keep_alive"):
        _parse(head + 'keep_alive = "yes"\n')


def test_absent_launchd_table_is_empty():
    assert _parse("").launchd == {}


# ---------------------------------------------------------------------------
# Rendering
# ---------------------------------------------------------------------------

def test_render_carries_program_args_and_logs(tmp_path):
    c = launchd._read_config(_cfg(tmp_path))
    doc = plistlib.loads(launchd._render(c))
    assert doc["Label"] == "com.example.myserver"
    assert doc["ProgramArguments"][1] == "-wait-for-lock"
    assert doc["ThrottleInterval"] == 10
    assert doc["KeepAlive"] is True
    assert doc["StandardOutPath"].endswith("server.log")
    assert doc["ProcessType"] == "Background"


def test_render_is_byte_stable(tmp_path):
    c = launchd._read_config(_cfg(tmp_path))
    assert launchd._render(c) == launchd._render(c)


def test_inherit_path_writes_the_invoking_shells_path(tmp_path):
    cfg = _cfg(tmp_path)
    cfg["inherit_path"] = True
    doc = plistlib.loads(launchd._render(launchd._read_config(cfg)))
    assert doc["EnvironmentVariables"]["PATH"] == os.environ["PATH"]


def test_explicit_env_wins_over_inherited_path(tmp_path):
    cfg = _cfg(tmp_path)
    cfg["inherit_path"] = True
    cfg["env"] = {"PATH": "/only/this"}
    doc = plistlib.loads(launchd._render(launchd._read_config(cfg)))
    assert doc["EnvironmentVariables"]["PATH"] == "/only/this"


def test_relative_program_is_rejected(tmp_path):
    cfg = _cfg(tmp_path)
    cfg["program"] = "bin/myserver"
    with pytest.raises(SystemExit):
        launchd._read_config(cfg)


# ---------------------------------------------------------------------------
# The Background-Task-Management fix
# ---------------------------------------------------------------------------

def test_install_writes_and_bootstraps_when_absent(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    calls = _stub_launchctl(tmp_path, loaded=False)
    launchd._install(launchd._read_config(_cfg(tmp_path)))
    text = calls.read_text()
    assert "bootstrap" in text
    assert "kickstart" not in text
    plist = tmp_path / "home" / "Library" / "LaunchAgents" / "com.example.myserver.plist"
    assert plist.exists()


def test_install_kickstarts_when_plist_unchanged_and_loaded(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    c = launchd._read_config(_cfg(tmp_path))
    plist = tmp_path / "home" / "Library" / "LaunchAgents" / "com.example.myserver.plist"
    plist.parent.mkdir(parents=True)
    plist.write_bytes(launchd._render(c))
    before = plist.stat().st_mtime_ns

    calls = _stub_launchctl(tmp_path, loaded=True)
    launchd._install(c)

    text = calls.read_text()
    assert "kickstart -k" in text
    assert "bootstrap" not in text, "an unchanged plist must not be re-registered"
    assert plist.stat().st_mtime_ns == before, "an unchanged plist must not be rewritten"


def test_install_rewrites_when_the_plist_changed(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    c = launchd._read_config(_cfg(tmp_path))
    plist = tmp_path / "home" / "Library" / "LaunchAgents" / "com.example.myserver.plist"
    plist.parent.mkdir(parents=True)
    plist.write_bytes(b"<plist>stale</plist>")

    calls = _stub_launchctl(tmp_path, loaded=True)
    launchd._install(c)

    assert "bootstrap" in calls.read_text()
    assert plist.read_bytes() == launchd._render(c)


def test_install_bootstraps_when_plist_matches_but_agent_is_not_loaded(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    c = launchd._read_config(_cfg(tmp_path))
    plist = tmp_path / "home" / "Library" / "LaunchAgents" / "com.example.myserver.plist"
    plist.parent.mkdir(parents=True)
    plist.write_bytes(launchd._render(c))

    calls = _stub_launchctl(tmp_path, loaded=False)
    launchd._install(c)

    assert "bootstrap" in calls.read_text()


def test_install_refuses_a_missing_binary(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    _stub_launchctl(tmp_path, loaded=False)
    cfg = _cfg(tmp_path)
    cfg["program"] = str(tmp_path / "bin" / "nope")
    with pytest.raises(SystemExit):
        launchd._install(launchd._read_config(cfg))


def test_unknown_verb_exits_nonzero(tmp_path):
    with pytest.raises(SystemExit) as e:
        launchd.launchd_service(_cfg(tmp_path), ["frobnicate"])
    assert e.value.code == 1
