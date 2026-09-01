"""Tests for admin_lib.apple.sign (#425).

Harvested from nts-desktop's mac/codesign-local.sh, which admin_lib had no
equivalent of. Its header records the reason: `swift build` ad-hoc signs, so the
binary's designated requirement is its raw cdhash and changes every build —
macOS then treats each rebuild as a new app and re-prompts for access, and a
Keychain item bound to the old requirement is orphaned. A fixed --identifier
makes the requirement constant.

These run codesign for real against throwaway binaries.
"""
from __future__ import annotations

import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.apple import sign  # noqa: E402

pytestmark = pytest.mark.skipif(sys.platform != "darwin", reason="codesign is macOS-only")

_IDENTITY_LINE = re.compile(r'^\s*\d+\)\s+([0-9A-Fa-f]{40})\s+"(.+)"\s*$')


def _binary(root: Path, name: str = "demo") -> Path:
    exe = root / name
    shutil.copy("/bin/sleep", exe)
    exe.chmod(0o755)
    return exe


def _designated_requirement(path: Path) -> str:
    """Just the requirement expression.

    `codesign -d -r -` also prints an `Executable=<path>` line to stderr, which
    differs by definition between two files — comparing raw output would compare
    the paths rather than the requirement.
    """
    r = subprocess.run(
        ["codesign", "-d", "-r", "-", str(path)],
        capture_output=True, text=True,
    )
    for line in (r.stdout + r.stderr).splitlines():
        # The line is prefixed with '#': `# designated => ...`
        if "designated =>" in line:
            return line.split("=>", 1)[1].strip()
    return ""


def _resolve_test_identity() -> str | None:
    """Pick ONE unambiguous codesigning identity for the guarded tests to sign
    with — resolve_signing_identity (admin_lib/apple.py) refuses to guess
    when `security find-identity` lists more than one, which is correct
    behavior, not something these tests should be gated on. Preferring
    "Apple Development" matches nts-desktop's own auto-pick (see this file's
    module docstring); falling back to the sole identity keeps this working
    on a single-identity machine exactly as before.
    """
    r = subprocess.run(
        ["security", "find-identity", "-v", "-p", "codesigning"],
        capture_output=True, text=True,
    )
    hashes = []
    for line in r.stdout.splitlines():
        m = _IDENTITY_LINE.match(line)
        if m:
            hashes.append((m.group(1), m.group(2)))
    for h, desc in hashes:
        if "Apple Development" in desc:
            return h
    if len(hashes) == 1:
        return hashes[0][0]
    return None


_TEST_IDENTITY = _resolve_test_identity()


def _identifier_of(path: Path) -> str:
    r = subprocess.run(["codesign", "-dv", str(path)], capture_output=True, text=True)
    for line in (r.stdout + r.stderr).splitlines():
        if line.startswith("Identifier="):
            return line.split("=", 1)[1]
    return ""


def test_ad_hoc_sign_produces_a_loadable_binary():
    """A copied system binary has a broken signature and AMFI kills it on exec."""
    with tempfile.TemporaryDirectory() as d:
        exe = _binary(Path(d))
        sign(exe, identity="-")
        r = subprocess.run([str(exe), "0"], capture_output=True, timeout=10)
        assert r.returncode == 0, "ad-hoc signed binary should run"


def test_identifier_is_applied():
    with tempfile.TemporaryDirectory() as d:
        exe = _binary(Path(d))
        sign(exe, identity="-", identifier="live.nts.desktop")
        assert _identifier_of(exe) == "live.nts.desktop"


@pytest.mark.skipif(_TEST_IDENTITY is None, reason="no unambiguous codesigning identity to resolve")
def test_identifier_makes_the_designated_requirement_stable_across_rebuilds():
    """The whole reason this helper exists.

    Two *different* binaries (i.e. a rebuild) signed with the same identity and
    identifier must present the same designated requirement, so the keychain item
    bound to it stays reachable and macOS does not re-prompt.

    Only meaningful with a real identity: an ad-hoc signature has no authority to
    anchor to, so its requirement stays cdhash-based no matter what identifier is
    passed — which is itself why nts-desktop's script auto-picks Apple Development
    and only falls back to ad-hoc for CI and the public dmg.
    """
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        first = _binary(root, "build1")
        second = root / "build2"
        shutil.copy("/bin/echo", second)  # a different binary == a rebuild
        second.chmod(0o755)

        used_first = sign(first, identity=_TEST_IDENTITY, identifier="live.nts.desktop")
        used_second = sign(second, identity=_TEST_IDENTITY, identifier="live.nts.desktop")
        assert used_first != "-" and used_second != "-", (
            f"sign() fell back to ad-hoc (used_first={used_first!r}, "
            f"used_second={used_second!r}) instead of using the resolved identity"
        )
        req_first = _designated_requirement(first)
        assert "cdhash" not in req_first, (
            f"a real identity + identifier should anchor to the identifier, "
            f"not the cdhash; got {req_first!r}"
        )
        assert req_first == _designated_requirement(second), (
            "same identity + identifier must yield the same designated requirement"
        )


@pytest.mark.skipif(_TEST_IDENTITY is None, reason="no unambiguous codesigning identity to resolve")
def test_without_an_identifier_the_requirement_is_cdhash_bound():
    """The failure mode being fixed: no identifier, requirement follows the build."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        first = _binary(root, "build1")
        second = root / "build2"
        shutil.copy("/bin/echo", second)
        second.chmod(0o755)

        used_first = sign(first, identity=_TEST_IDENTITY)
        used_second = sign(second, identity=_TEST_IDENTITY)
        assert used_first != "-" and used_second != "-", (
            f"sign() fell back to ad-hoc (used_first={used_first!r}, "
            f"used_second={used_second!r}) instead of using the resolved identity"
        )
        assert _designated_requirement(first) != _designated_requirement(second), (
            "no identifier means the requirement follows the binary — "
            "if this ever passes, the helper's reason to exist is gone"
        )


def test_ad_hoc_requirement_stays_cdhash_bound_even_with_an_identifier():
    """Records a real limit: ad-hoc cannot give a stable designated requirement.

    An ad-hoc signature has no signing authority to anchor the requirement to, so
    it falls back to cdhash regardless of identifier. Anything relying on stable
    grants must sign with a real identity.
    """
    with tempfile.TemporaryDirectory() as d:
        exe = _binary(Path(d))
        sign(exe, identity="-", identifier="live.nts.desktop")
        assert "cdhash" in _designated_requirement(exe)


def test_explicit_identity_wins_over_the_environment():
    with tempfile.TemporaryDirectory() as d:
        exe = _binary(Path(d))
        prev = os.environ.get("CODE_SIGN_IDENTITY")
        os.environ["CODE_SIGN_IDENTITY"] = "definitely-not-a-real-identity"
        try:
            used = sign(exe, identity="-", identifier="x.y")
            assert used == "-", "an explicit identity must not be overridden by env"
        finally:
            os.environ.pop("CODE_SIGN_IDENTITY", None)
            if prev is not None:
                os.environ["CODE_SIGN_IDENTITY"] = prev


def test_env_identity_is_used_when_none_is_passed():
    with tempfile.TemporaryDirectory() as d:
        exe = _binary(Path(d))
        prev = os.environ.get("CODE_SIGN_IDENTITY")
        os.environ["CODE_SIGN_IDENTITY"] = "-"
        try:
            assert sign(exe, identifier="x.y") == "-"
        finally:
            os.environ.pop("CODE_SIGN_IDENTITY", None)
            if prev is not None:
                os.environ["CODE_SIGN_IDENTITY"] = prev


def test_missing_path_errors_clearly():
    code = (
        "import sys; sys.path.insert(0, %r)\n"
        "from admin_lib.apple import sign\n"
        "sign('/nope/does/not/exist', identity='-')\n" % str(REPO)
    )
    r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=20)
    assert r.returncode != 0
    assert "no such path" in (r.stdout + r.stderr)


def test_signing_an_app_bundle_works():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        app = root / "Demo.app" / "Contents" / "MacOS"
        app.mkdir(parents=True)
        shutil.copy("/bin/sleep", app / "Demo")
        (app / "Demo").chmod(0o755)
        (root / "Demo.app" / "Contents" / "Info.plist").write_text(
            '<?xml version="1.0" encoding="UTF-8"?>\n'
            '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" '
            '"http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n'
            '<plist version="1.0"><dict>'
            "<key>CFBundleIdentifier</key><string>com.example.demo</string>"
            "<key>CFBundleExecutable</key><string>Demo</string>"
            "</dict></plist>\n"
        )
        sign(root / "Demo.app", identity="-", identifier="com.example.demo")
        assert _identifier_of(root / "Demo.app") == "com.example.demo"
