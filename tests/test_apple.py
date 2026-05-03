"""Unit tests for the testable slices of admin_lib/apple.py.

Scope follows #120's recommendation: cover the helpers that are
pure-python or filesystem-only, skip the ones that shell out to
xcodebuild / plutil / codesign / xcrun. The module is loaded via the
same exec+regex workaround used by test_logtail (relative imports can't
resolve outside a package); #95 tracks replacing that workaround.
"""
from __future__ import annotations

import os
import re as _re
import shutil
import sys
import tempfile
from pathlib import Path

LIB_DIR = Path(__file__).resolve().parent.parent / "admin_lib"


class _FakeCompleted:
    def __init__(self, returncode=0, stdout=b"", stderr=b""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


def _load_apple(subprocess_run=None):
    """Load apple.py into an isolated namespace with core/*and optionally
    subprocess.run* stubbed.

    subprocess_run, if given, replaces subprocess.run in the loaded
    namespace so tests can assert what plutil/codesign would have been
    called with without actually invoking them.
    """
    source = (LIB_DIR / "apple.py").read_text()
    source = _re.sub(r"from \.core import \([^)]*\)", "pass", source, count=1)
    ns: dict = {
        "C": type("C", (), {"RED": "", "GREEN": "", "YELLOW": "", "CYAN": "", "RESET": ""}),
        "LOG_DIR": "/tmp",
        "LOG_FILE": "/tmp/apple-test.log",
        "_children": [],
        "command": lambda *a, **k: (lambda f: f),
        "info": lambda *a, **k: None,
        "ok": lambda *a, **k: None,
        "warn": lambda *a, **k: None,
        "err": lambda *a, **k: None,
        "register_cleanup": lambda *a, **k: None,
        "read_key": lambda *a, **k: None,
    }
    exec(compile(source, "apple.py", "exec"), ns)
    if subprocess_run is not None:
        class _SubprocessStub:
            pass
        stub = _SubprocessStub()
        stub.run = subprocess_run
        ns["subprocess"] = stub
    return ns


# ---------------------------------------------------------------------------
# find_app
# ---------------------------------------------------------------------------

def test_find_app_returns_match():
    ns = _load_apple()
    with tempfile.TemporaryDirectory() as d:
        app = os.path.join(d, "Foo.app")
        os.mkdir(app)
        result = ns["find_app"](os.path.join(d, "*.app"))
        assert result == app, result
    print("OK: find_app returns the newest glob match")


def test_find_app_returns_none_on_no_match():
    ns = _load_apple()
    with tempfile.TemporaryDirectory() as d:
        result = ns["find_app"](os.path.join(d, "*.app"))
        assert result is None, result
    print("OK: find_app returns None when glob has no matches")


def test_find_app_returns_newest_when_multiple():
    ns = _load_apple()
    with tempfile.TemporaryDirectory() as d:
        old_app = os.path.join(d, "Old.app")
        new_app = os.path.join(d, "New.app")
        os.mkdir(old_app)
        os.mkdir(new_app)
        os.utime(old_app, (1000000, 1000000))
        os.utime(new_app, (2000000, 2000000))
        result = ns["find_app"](os.path.join(d, "*.app"))
        assert result == new_app, result
    print("OK: find_app returns the newest app when multiple matches exist")


# ---------------------------------------------------------------------------
# log_predicate
# ---------------------------------------------------------------------------

def test_log_predicate_app():
    ns = _load_apple()
    p = ns["log_predicate"]("MyApp", "com.example.app", level="app")
    assert p == "subsystem == 'com.example.app'", p
    print("OK: log_predicate app-level filters on subsystem only")


def test_log_predicate_debug_includes_process_and_excludes_noise():
    ns = _load_apple()
    p = ns["log_predicate"]("MyApp", "com.example.app", level="debug")
    assert "subsystem == 'com.example.app'" in p
    assert "process == 'MyApp'" in p
    assert "com.apple.launchservices" in p
    assert "com.apple.runningboard" in p
    print("OK: log_predicate debug-level widens to process and excludes known noise")


def test_log_predicate_system_excludes_own_subsystem():
    ns = _load_apple()
    p = ns["log_predicate"]("MyApp", "com.example.app", level="system")
    assert p == "process == 'MyApp' AND subsystem != 'com.example.app'", p
    print("OK: log_predicate system-level shows process minus own subsystem")


def test_log_predicate_verbose_is_process_only():
    ns = _load_apple()
    p = ns["log_predicate"]("MyApp", "com.example.app", level="verbose")
    assert p == "process == 'MyApp'", p
    print("OK: log_predicate verbose-level is process-only")


def test_log_predicate_unknown_level_exits():
    ns = _load_apple()
    try:
        ns["log_predicate"]("MyApp", "com.example.app", level="bogus")
    except SystemExit as e:
        assert e.code == 1, e.code
        print("OK: log_predicate exits non-zero on unknown level")
        return
    raise AssertionError("expected SystemExit on unknown level")


# ---------------------------------------------------------------------------
# dev_macos_postprocess
# ---------------------------------------------------------------------------

def _make_fake_app(root: str, name: str = "Scratchpad.app") -> str:
    """Create <root>/<name>/Contents/Info.plist — enough structure for
    dev_macos_postprocess to consider the path valid."""
    app = os.path.join(root, name)
    os.makedirs(os.path.join(app, "Contents"))
    with open(os.path.join(app, "Contents", "Info.plist"), "w") as f:
        f.write("<?xml version=\"1.0\"?><plist><dict/></plist>")
    return app


def test_dev_macos_postprocess_renames_and_returns_dest():
    calls = []

    def fake_run(argv, **kwargs):
        calls.append(argv)
        return _FakeCompleted(returncode=0)

    ns = _load_apple(subprocess_run=fake_run)
    with tempfile.TemporaryDirectory() as d:
        built = _make_fake_app(d, "Scratchpad.app")
        result = ns["dev_macos_postprocess"](built, "Scratchpad DEV", "Scratchpad DEV")
        expected = os.path.join(d, "Scratchpad DEV.app")
        assert result == expected, result
        assert os.path.isdir(expected), "renamed app dir must exist"
        assert not os.path.exists(built), "original path must no longer exist"
        assert os.path.isfile(os.path.join(expected, "Contents", "Info.plist"))
    assert any("plutil" in " ".join(str(x) for x in c) for c in calls), calls
    assert any("codesign" in " ".join(str(x) for x in c) for c in calls), calls
    print("OK: dev_macos_postprocess renames .app, calls plutil and codesign")


def test_dev_macos_postprocess_replaces_existing_dest():
    def fake_run(argv, **kwargs):
        return _FakeCompleted(returncode=0)

    ns = _load_apple(subprocess_run=fake_run)
    with tempfile.TemporaryDirectory() as d:
        built = _make_fake_app(d, "Scratchpad.app")
        stale = os.path.join(d, "Scratchpad DEV.app")
        os.makedirs(stale)
        with open(os.path.join(stale, "stale-marker"), "w") as f:
            f.write("x")
        result = ns["dev_macos_postprocess"](built, "Scratchpad DEV", "Scratchpad DEV")
        assert not os.path.exists(os.path.join(result, "stale-marker")), (
            "stale dest contents must be replaced"
        )
    print("OK: dev_macos_postprocess removes a stale dest bundle before rename")


def test_dev_macos_postprocess_rejects_non_app_path():
    ns = _load_apple(subprocess_run=lambda *a, **k: _FakeCompleted())
    with tempfile.TemporaryDirectory() as d:
        not_an_app = os.path.join(d, "just-a-dir")
        os.mkdir(not_an_app)
        try:
            ns["dev_macos_postprocess"](not_an_app, "Whatever DEV", "Whatever DEV")
        except SystemExit as e:
            assert e.code == 1, e.code
            assert os.path.isdir(not_an_app), "non-app path must not be renamed"
            print("OK: dev_macos_postprocess rejects paths not ending in .app")
            return
    raise AssertionError("expected SystemExit on non-.app path")


def test_dev_macos_postprocess_rejects_empty_path():
    ns = _load_apple(subprocess_run=lambda *a, **k: _FakeCompleted())
    try:
        ns["dev_macos_postprocess"]("", "Whatever DEV", "Whatever DEV")
    except SystemExit as e:
        assert e.code == 1, e.code
        print("OK: dev_macos_postprocess rejects empty path (find_app-None case)")
        return
    raise AssertionError("expected SystemExit on empty path")


def test_dev_macos_postprocess_rejects_app_without_info_plist():
    ns = _load_apple(subprocess_run=lambda *a, **k: _FakeCompleted())
    with tempfile.TemporaryDirectory() as d:
        bogus = os.path.join(d, "Bogus.app")
        os.makedirs(os.path.join(bogus, "Contents"))
        try:
            ns["dev_macos_postprocess"](bogus, "Bogus DEV", "Bogus DEV")
        except SystemExit as e:
            assert e.code == 1, e.code
            assert os.path.isdir(bogus), "bundle missing Info.plist must not be renamed"
            print("OK: dev_macos_postprocess rejects bundle missing Info.plist")
            return
    raise AssertionError("expected SystemExit on bundle missing Info.plist")


# ---------------------------------------------------------------------------
# backup_icons / restore_icons round-trip
# ---------------------------------------------------------------------------

def test_backup_and_restore_icons_roundtrip():
    ns = _load_apple()
    with tempfile.TemporaryDirectory() as root:
        mac_dir = os.path.join(root, "AppIcon-mac")
        ios_dir = os.path.join(root, "AppIcon-ios")
        os.makedirs(mac_dir)
        os.makedirs(ios_dir)
        for d, fname, content in [
            (mac_dir, "icon_128.png", b"macPROD128"),
            (mac_dir, "icon_256.png", b"macPROD256"),
            (mac_dir, "Contents.json", b'{"ignore": true}'),
            (ios_dir, "icon_60.png", b"iosPROD60"),
        ]:
            with open(os.path.join(d, fname), "wb") as f:
                f.write(content)

        icon_dirs = {"mac": mac_dir, "ios": ios_dir}
        backup_dir = os.path.join(root, "icon-backup")

        ns["backup_icons"](icon_dirs, backup_dir)

        assert os.path.isfile(os.path.join(backup_dir, "mac", "icon_128.png"))
        assert os.path.isfile(os.path.join(backup_dir, "mac", "icon_256.png"))
        assert os.path.isfile(os.path.join(backup_dir, "ios", "icon_60.png"))
        assert not os.path.exists(os.path.join(backup_dir, "mac", "Contents.json")), (
            "backup_icons must only copy PNGs"
        )

        for fname in ("icon_128.png", "icon_256.png"):
            with open(os.path.join(mac_dir, fname), "wb") as f:
                f.write(b"DEVoverwrite")
        with open(os.path.join(ios_dir, "icon_60.png"), "wb") as f:
            f.write(b"DEVoverwrite")

        ns["restore_icons"](icon_dirs, backup_dir)

        with open(os.path.join(mac_dir, "icon_128.png"), "rb") as f:
            assert f.read() == b"macPROD128"
        with open(os.path.join(mac_dir, "icon_256.png"), "rb") as f:
            assert f.read() == b"macPROD256"
        with open(os.path.join(ios_dir, "icon_60.png"), "rb") as f:
            assert f.read() == b"iosPROD60"
        assert not os.path.exists(backup_dir), (
            "restore_icons must clean up the backup directory"
        )
    print("OK: backup_icons + restore_icons round-trip restores original PNGs")


# ---------------------------------------------------------------------------
# _resolve_bundle
# ---------------------------------------------------------------------------

def _load_apple_stubbed_xcode(xcode_return=None):
    """Load apple.py with _xcode_setting stubbed to return *xcode_return*."""
    ns = _load_apple()
    ns["_xcode_setting"] = lambda cfg, scheme, key: xcode_return
    return ns


def test_resolve_bundle_manifest_override_prod():
    ns = _load_apple_stubbed_xcode()
    result = ns["_resolve_bundle"]({"mac_bundle": "com.example.app"}, "mac")
    assert result == "com.example.app", result
    print("OK: _resolve_bundle returns manifest mac_bundle for prod mac")


def test_resolve_bundle_manifest_override_ios():
    ns = _load_apple_stubbed_xcode()
    result = ns["_resolve_bundle"]({"ios_bundle": "com.example.ios"}, "ios")
    assert result == "com.example.ios", result
    print("OK: _resolve_bundle returns manifest ios_bundle for prod ios")


def test_resolve_bundle_from_pbxproj():
    ns = _load_apple_stubbed_xcode(xcode_return="com.example.fromxcode")
    result = ns["_resolve_bundle"]({"mac_scheme": "MyApp"}, "mac")
    assert result == "com.example.fromxcode", result
    print("OK: _resolve_bundle falls through to _xcode_setting when no manifest override")


def test_resolve_bundle_manifest_wins_over_pbxproj():
    ns = _load_apple_stubbed_xcode(xcode_return="com.example.fromxcode")
    result = ns["_resolve_bundle"]({"mac_bundle": "com.example.manifest"}, "mac")
    assert result == "com.example.manifest", result
    print("OK: _resolve_bundle manifest override wins over pbxproj value")


def test_resolve_bundle_dev_auto_derive():
    ns = _load_apple_stubbed_xcode()
    result = ns["_resolve_bundle"]({"mac_bundle": "com.example.app"}, "mac", dev=True)
    assert result == "com.example.app.dev", result
    print("OK: _resolve_bundle dev=True appends .dev when no explicit dev bundle")


def test_resolve_bundle_dev_explicit_override():
    ns = _load_apple_stubbed_xcode()
    cfg = {"mac_bundle": "com.example.app", "mac_dev_bundle": "com.example.app-debug"}
    result = ns["_resolve_bundle"](cfg, "mac", dev=True)
    assert result == "com.example.app-debug", result
    print("OK: _resolve_bundle dev=True uses explicit mac_dev_bundle when set")


def test_resolve_bundle_returns_none_when_nothing_configured():
    ns = _load_apple_stubbed_xcode(xcode_return=None)
    result = ns["_resolve_bundle"]({}, "mac")
    assert result is None, result
    print("OK: _resolve_bundle returns None when neither manifest nor pbxproj has a value")


def test_resolve_bundle_dev_returns_none_when_no_prod():
    ns = _load_apple_stubbed_xcode(xcode_return=None)
    result = ns["_resolve_bundle"]({}, "mac", dev=True)
    assert result is None, result
    print("OK: _resolve_bundle dev=True returns None when prod bundle is also unresolvable")


def test_resolve_bundle_unknown_platform_raises():
    ns = _load_apple_stubbed_xcode()
    try:
        ns["_resolve_bundle"]({}, "osx")
    except ValueError as e:
        assert "osx" in str(e), e
        print("OK: _resolve_bundle raises ValueError on unknown platform")
        return
    raise AssertionError("expected ValueError on unknown platform 'osx'")


if __name__ == "__main__":
    test_find_app_returns_match()
    test_find_app_returns_none_on_no_match()
    test_find_app_returns_newest_when_multiple()
    test_log_predicate_app()
    test_log_predicate_debug_includes_process_and_excludes_noise()
    test_log_predicate_system_excludes_own_subsystem()
    test_log_predicate_verbose_is_process_only()
    test_log_predicate_unknown_level_exits()
    test_dev_macos_postprocess_renames_and_returns_dest()
    test_dev_macos_postprocess_replaces_existing_dest()
    test_dev_macos_postprocess_rejects_non_app_path()
    test_dev_macos_postprocess_rejects_empty_path()
    test_dev_macos_postprocess_rejects_app_without_info_plist()
    test_backup_and_restore_icons_roundtrip()
    test_resolve_bundle_manifest_override_prod()
    test_resolve_bundle_manifest_override_ios()
    test_resolve_bundle_from_pbxproj()
    test_resolve_bundle_manifest_wins_over_pbxproj()
    test_resolve_bundle_dev_auto_derive()
    test_resolve_bundle_dev_explicit_override()
    test_resolve_bundle_returns_none_when_nothing_configured()
    test_resolve_bundle_dev_returns_none_when_no_prod()
    test_resolve_bundle_unknown_platform_raises()
    print("\nAll tests passed.")
