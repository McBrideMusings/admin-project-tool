"""Tests for admin_lib.apple.install_app (#417).

Six projects hand-rolled "install the .app into /Applications" and only macterm
and term got it right. The three failure modes the others had — copying over a
running bundle, a non-atomic rm -rf + cp -R window, and no LaunchServices
re-registration — are what these tests pin.

Nothing here touches the real /Applications; dest_dir is a temp dir.
"""
from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.apple import _pids_running_from, install_app  # noqa: E402

pytestmark = pytest.mark.skipif(sys.platform != "darwin", reason="macOS app install")


def _make_bundle(root: Path, name: str, marker: str = "") -> Path:
    """A minimal .app whose executable is a real Mach-O binary.

    It has to be a real binary, not a shell script: `ps comm` reports the
    interpreter (/bin/sh) for a script, so a script-based bundle would never
    match the bundle path _pids_running_from looks for — which is exactly the
    thing under test.

    The ad-hoc codesign is not optional either: copying a signed system binary
    breaks its signature and AMFI kills the process on exec, so an unsigned copy
    shows up as <defunct> instead of running.
    """
    import shutil as _shutil

    app = root / f"{name}.app"
    macos = app / "Contents" / "MacOS"
    macos.mkdir(parents=True)
    exe = macos / name
    _shutil.copy("/bin/sleep", exe)
    exe.chmod(0o755)
    subprocess.run(["codesign", "--force", "--sign", "-", str(exe)], capture_output=True)
    (app / "Contents" / "Info.plist").write_text("<plist/>")
    if marker:
        (app / "Contents" / "marker.txt").write_text(marker)
    return app


def test_installs_into_dest_dir():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        src = _make_bundle(root / "build", "Demo")
        dest_dir = root / "Applications"
        dest_dir.mkdir()
        out = install_app(str(src), dest_dir=str(dest_dir), relaunch=False)
        assert Path(out) == dest_dir / "Demo.app"
        assert (dest_dir / "Demo.app" / "Contents" / "MacOS" / "Demo").exists()


def test_replaces_an_existing_install():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        dest_dir = root / "Applications"
        dest_dir.mkdir()
        old = _make_bundle(dest_dir, "Demo", marker="OLD")
        assert old.exists()
        src = _make_bundle(root / "build", "Demo", marker="NEW")
        install_app(str(src), dest_dir=str(dest_dir), relaunch=False)
        marker = (dest_dir / "Demo.app" / "Contents" / "marker.txt").read_text()
        assert marker == "NEW", "new bundle should have replaced the old"


def test_leaves_no_staging_directories_behind():
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        dest_dir = root / "Applications"
        dest_dir.mkdir()
        src = _make_bundle(root / "build", "Demo")
        install_app(str(src), dest_dir=str(dest_dir), relaunch=False)
        names = {p.name for p in dest_dir.iterdir()}
        assert names == {"Demo.app"}, f"staging leftovers: {names}"


def test_stops_a_copy_running_out_of_the_destination():
    """cp -R over a running bundle corrupts it — the running copy must die first."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        dest_dir = root / "Applications"
        dest_dir.mkdir()
        installed = _make_bundle(dest_dir, "Demo")
        exe = installed / "Contents" / "MacOS" / "Demo"
        proc = subprocess.Popen([str(exe), "60"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        time.sleep(0.5)
        try:
            assert _pids_running_from(str(installed)), "test setup: app should be running"
            src = _make_bundle(root / "build", "Demo")
            install_app(str(src), dest_dir=str(dest_dir), relaunch=False)
            time.sleep(0.3)
            assert proc.poll() is not None, "running copy should have been stopped"
        finally:
            if proc.poll() is None:
                proc.kill()
            proc.wait(timeout=5)


def test_pids_running_from_ignores_a_same_named_app_elsewhere():
    """Matching by bundle path, not process name: another Demo must be untouched."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        ours = _make_bundle(root / "Applications", "Demo")
        theirs = _make_bundle(root / "Elsewhere", "Demo")
        proc = subprocess.Popen(
            [str(theirs / "Contents" / "MacOS" / "Demo"), "60"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
        time.sleep(0.5)
        try:
            assert _pids_running_from(str(theirs)), "test setup"
            assert not _pids_running_from(str(ours)), (
                "a same-named app running from a different bundle must not match"
            )
        finally:
            if proc.poll() is None:
                proc.kill()
            proc.wait(timeout=5)


def test_glob_source_resolves_to_the_one_match():
    """electron-forge emits out/<Name>-darwin-<arch>/<Name>.app — rpg-toolkit globs it."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        dest_dir = root / "Applications"
        dest_dir.mkdir()
        _make_bundle(root / "out" / "Demo-darwin-arm64", "Demo", marker="ARM")
        out = install_app(
            str(root / "out" / "Demo-darwin-*" / "Demo.app"),
            dest_dir=str(dest_dir), relaunch=False,
        )
        assert Path(out) == dest_dir / "Demo.app"
        assert (dest_dir / "Demo.app" / "Contents" / "marker.txt").read_text() == "ARM"


def test_ambiguous_glob_is_an_error_not_a_coin_flip():
    """Two matches means a stale build — installing either silently is worse."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        dest_dir = root / "Applications"
        dest_dir.mkdir()
        _make_bundle(root / "out" / "Demo-darwin-arm64", "Demo")
        _make_bundle(root / "out" / "Demo-darwin-x64", "Demo")
        code = (
            "import sys; sys.path.insert(0, %r)\n"
            "from admin_lib.apple import install_app\n"
            "install_app(%r, dest_dir=%r, relaunch=False)\n"
            % (str(REPO), str(root / "out" / "Demo-darwin-*" / "Demo.app"), str(dest_dir))
        )
        r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=20)
        assert r.returncode != 0
        assert "matched several bundles" in (r.stdout + r.stderr)


def test_glob_matching_nothing_is_an_error():
    with tempfile.TemporaryDirectory() as d:
        code = (
            "import sys; sys.path.insert(0, %r)\n"
            "from admin_lib.apple import install_app\n"
            "install_app(%r, dest_dir=%r, relaunch=False)\n"
            % (str(REPO), str(Path(d) / "out" / "*" / "Demo.app"), d)
        )
        r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=20)
        assert r.returncode != 0
        assert "no app bundle matched" in (r.stdout + r.stderr)


def test_missing_bundle_exits_with_a_clear_error():
    with tempfile.TemporaryDirectory() as d:
        code = (
            "import sys; sys.path.insert(0, %r)\n"
            "from admin_lib.apple import install_app\n"
            "install_app(%r, dest_dir=%r)\n" % (str(REPO), str(Path(d) / "nope.app"), d)
        )
        r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=20)
        assert r.returncode != 0
        assert "no such app bundle" in (r.stdout + r.stderr)


def test_a_failed_copy_leaves_the_previous_install_in_place():
    """The atomicity claim: a failed install must not destroy what was there.

    `rm -rf dest && cp -R src dest` — what charcoal and rpg-toolkit do — has a
    window where the old app is gone and the new one is not yet there. Staging
    beside the destination and renaming closes it.
    """
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        dest_dir = root / "Applications"
        dest_dir.mkdir()
        _make_bundle(dest_dir, "Demo", marker="ORIGINAL")
        # Unreadable source: ditto cannot copy it, so the install must bail out
        # having touched nothing.
        src = _make_bundle(root / "build", "Demo", marker="NEW")
        os.chmod(src / "Contents", 0o000)
        try:
            code = (
                "import sys; sys.path.insert(0, %r)\n"
                "from admin_lib.apple import install_app\n"
                "install_app(%r, dest_dir=%r, relaunch=False)\n"
                % (str(REPO), str(src), str(dest_dir))
            )
            r = subprocess.run([sys.executable, "-c", code], capture_output=True,
                               text=True, timeout=20)
            assert r.returncode != 0, "a failed copy should not report success"
            survivor = dest_dir / "Demo.app" / "Contents" / "marker.txt"
            assert survivor.exists(), "previous install was destroyed by a failed copy"
            assert survivor.read_text() == "ORIGINAL"
            assert not (dest_dir / "Demo.app.installing").exists()
        finally:
            os.chmod(src / "Contents", 0o755)
