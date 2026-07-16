"""Tests for lan_ip() and the ${LAN_IP} substitution (#426).

reddit-poker computed its LAN address twice — inline in dev-cf, and again in
apps/mobile/tools/build.ts to bake the API origin into an iOS build — with no
shared source. When they disagree the phone points at the wrong host.

Both used `ipconfig getifaddr en0 || ipconfig getifaddr en1`, which is macOS-only
and wrong whenever the live interface is neither of those two.
"""
from __future__ import annotations

import ipaddress
import os
import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.core import COMPUTED_VARS, lan_ip, resolve_env  # noqa: E402
from admin_lib.kinds import scan_env_vars  # noqa: E402
from admin_lib.manifest import Command  # noqa: E402


def test_lan_ip_returns_a_usable_address():
    ip = lan_ip()
    parsed = ipaddress.ip_address(ip)
    assert parsed.version == 4, f"expected IPv4, got {ip}"


def test_lan_ip_is_not_loopback_when_networked():
    """The whole point is an address another device can reach."""
    ip = lan_ip()
    if ip == "127.0.0.1":
        import pytest

        pytest.skip("host appears to be offline")
    assert not ipaddress.ip_address(ip).is_loopback


def test_lan_ip_is_stable_across_calls():
    assert lan_ip() == lan_ip()


def test_lan_ip_substitution_expands():
    out = resolve_env("http://${LAN_IP}:8787")
    assert "${LAN_IP}" not in out
    assert out == f"http://{lan_ip()}:8787"


def test_an_explicit_env_value_wins():
    """A project must be able to pin it when the automatic answer is wrong."""
    prev = os.environ.get("LAN_IP")
    os.environ["LAN_IP"] = "10.9.9.9"
    try:
        assert resolve_env("http://${LAN_IP}:1") == "http://10.9.9.9:1"
    finally:
        os.environ.pop("LAN_IP", None)
        if prev is not None:
            os.environ["LAN_IP"] = prev


def test_lan_ip_is_not_registered_as_a_required_env_var():
    """Otherwise `admin env` demands a value and aborts a runnable command —
    the failure muxarr's manifest has a comment complaining about."""
    cmd = Command(name="dev", run="echo http://${LAN_IP}:8787", desc="dev", kind="shell")
    seen = scan_env_vars([cmd])
    assert "LAN_IP" not in seen, f"LAN_IP should not be a required var; got {seen}"


def test_other_vars_still_scan_normally():
    cmd = Command(name="dev", run="echo ${REAL_VAR} ${LAN_IP}", desc="dev", kind="shell")
    seen = scan_env_vars([cmd])
    assert "REAL_VAR" in seen
    assert "LAN_IP" not in seen


def test_lan_ip_is_declared_computed():
    assert "LAN_IP" in COMPUTED_VARS


def test_lan_ip_resolves_in_a_real_dispatch():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / "admin.toml").write_text(
            'project_name = "X"\n\n'
            '[commands.show]\ndesc = "show"\nsteps = ["show"]\n\n'
            '[actions.show]\nkind = "shell"\ndesc = "show"\n'
            'run  = "echo url=http://${LAN_IP}:8787"\n'
        )
        r = subprocess.run(
            [sys.executable, str(REPO / "admin-run"), "--project-dir", str(root), "show"],
            capture_output=True, text=True, timeout=20,
        )
        assert r.returncode == 0, r.stderr
        assert f"url=http://{lan_ip()}:8787" in r.stdout, r.stdout
