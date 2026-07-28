"""Unit tests for the testable slices of admin_lib/apple.py.

Scope follows #120's recommendation: cover the helpers that are
pure-python or filesystem-only, skip the ones that shell out to
xcodebuild / plutil / codesign / xcrun. The module is loaded via the
same exec+regex workaround used by test_logtail (relative imports can't
resolve outside a package); #95 tracks replacing that workaround.
"""
from __future__ import annotations

import json
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


def _load_apple(subprocess_run=None, subprocess_popen=None):
    """Load apple.py into an isolated namespace with core/*and optionally
    subprocess.run / subprocess.Popen* stubbed.

    subprocess_run, if given, replaces subprocess.run in the loaded
    namespace so tests can assert what plutil/codesign would have been
    called with without actually invoking them. subprocess_popen does the
    same for the detached `log stream` child.
    """
    source = (LIB_DIR / "apple.py").read_text()
    # Only .core is stripped, and only because ns below stubs its names to keep
    # tests from logging and shelling out. Every OTHER relative import resolves
    # for real, because __name__/__package__ are set in ns: exec'ing without
    # them is what makes `from .x import y` raise KeyError('__name__'), and
    # regex-stripping each new import as it appears just moves the breakage to
    # the next one added (it already did, twice). #95 tracks replacing this
    # loader with a plain import.
    source = _re.sub(r"from \.core import \([^)]*\)", "pass", source, count=1)
    ns: dict = {
        "__name__": "admin_lib.apple",
        "__package__": "admin_lib",
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
    }
    exec(compile(source, "apple.py", "exec"), ns)
    if subprocess_run is not None or subprocess_popen is not None:
        class _SubprocessStub:
            STDOUT = -2
            PIPE = -1
        stub = _SubprocessStub()
        stub.run = subprocess_run or (lambda *a, **k: _FakeCompleted())
        stub.Popen = subprocess_popen or (lambda *a, **k: None)
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


# ---------------------------------------------------------------------------
# sweep_orphan_log_streams (#353)
# ---------------------------------------------------------------------------

def test_sweep_orphan_log_streams_invokes_pkill_with_escaped_subsystem():
    calls = []
    def fake_run(*args, **kwargs):
        calls.append((args, kwargs))
        return _FakeCompleted()
    ns = _load_apple(subprocess_run=fake_run)
    ns["sweep_orphan_log_streams"]("com.example.app")
    assert len(calls) == 1, calls
    argv = calls[0][0][0]
    assert argv[0] == "pkill"
    assert argv[1] == "-f"
    # Dots must be escaped so the regex doesn't match unrelated argv text.
    assert "log stream.*com\\.example\\.app" in argv[2], argv[2]
    print("OK: sweep_orphan_log_streams runs pkill -f with regex-escaped subsystem")


def test_sweep_orphan_log_streams_noop_on_empty_subsystem():
    calls = []
    def fake_run(*args, **kwargs):
        calls.append((args, kwargs))
        return _FakeCompleted()
    ns = _load_apple(subprocess_run=fake_run)
    ns["sweep_orphan_log_streams"]("")
    ns["sweep_orphan_log_streams"](None)
    assert calls == [], calls
    print("OK: sweep_orphan_log_streams skips pkill when subsystem is falsy")


# ---------------------------------------------------------------------------
# resolve_signing_identity
# ---------------------------------------------------------------------------

_FIND_IDENTITY_ONE = (
    '  1) ' + 'A' * 40 + ' "Apple Development: Pierce McBride (TEAM123)"\n'
    '     1 valid identities found\n'
)
_FIND_IDENTITY_TWO = (
    '  1) ' + 'A' * 40 + ' "Apple Development: Pierce McBride (TEAM123)"\n'
    '  2) ' + 'B' * 40 + ' "Apple Development: Acme Inc (TEAM999)"\n'
    '     2 valid identities found\n'
)


def _identity_stub(stdout):
    def fake_run(argv, **kwargs):
        return _FakeCompleted(returncode=0, stdout=stdout)
    return fake_run


def test_resolve_signing_identity_auto_picks_single():
    ns = _load_apple(subprocess_run=_identity_stub(_FIND_IDENTITY_ONE))
    assert ns["resolve_signing_identity"]({}) == "A" * 40
    print("OK: resolve_signing_identity auto-picks the sole identity with no selector")


def test_resolve_signing_identity_substring_selector_picks_match():
    ns = _load_apple(subprocess_run=_identity_stub(_FIND_IDENTITY_TWO))
    assert ns["resolve_signing_identity"]({"sign_identity": "acme"}) == "B" * 40
    print("OK: resolve_signing_identity matches a case-insensitive substring selector")


def test_resolve_signing_identity_team_id_selector():
    ns = _load_apple(subprocess_run=_identity_stub(_FIND_IDENTITY_TWO))
    assert ns["resolve_signing_identity"]({"sign_identity": "TEAM123"}) == "A" * 40
    print("OK: resolve_signing_identity matches on a team-id substring")


def test_resolve_signing_identity_ambiguous_returns_none():
    ns = _load_apple(subprocess_run=_identity_stub(_FIND_IDENTITY_TWO))
    assert ns["resolve_signing_identity"]({}) is None
    print("OK: resolve_signing_identity returns None when many match and no selector")


def test_resolve_signing_identity_no_match_returns_none():
    ns = _load_apple(subprocess_run=_identity_stub(_FIND_IDENTITY_TWO))
    assert ns["resolve_signing_identity"]({"sign_identity": "nonesuch"}) is None
    print("OK: resolve_signing_identity returns None when the selector matches nothing")


def test_dev_macos_postprocess_signs_with_given_identity():
    calls = []

    def fake_run(argv, **kwargs):
        calls.append(argv)
        return _FakeCompleted(returncode=0)

    ns = _load_apple(subprocess_run=fake_run)
    with tempfile.TemporaryDirectory() as d:
        built = _make_fake_app(d, "Scratchpad.app")
        ns["dev_macos_postprocess"](built, "Scratchpad DEV", "Scratchpad DEV", sign_identity="DEADBEEF")
    codesign = next(c for c in calls if "codesign" in " ".join(str(x) for x in c))
    assert codesign[codesign.index("--sign") + 1] == "DEADBEEF", codesign
    print("OK: dev_macos_postprocess re-signs with the supplied identity, not ad-hoc")


# ---------------------------------------------------------------------------
# Icon Composer (.icon) dev-banner swap
# ---------------------------------------------------------------------------

def test_build_icon_hooks_works_without_glyph_when_asset_dir_set():
    # Real-art appiconset (macterm): mac_asset_dir set, no glyph_text/glyph_svg.
    # The swap overlays the banner on the existing PNGs, so hooks must be live.
    ns = _load_apple()
    with tempfile.TemporaryDirectory() as root:
        mac_dir = os.path.join(root, "AppIcon.appiconset")
        os.makedirs(mac_dir)
        cfg = {"icons": {"mac_asset_dir": mac_dir}}
        pre, post = ns["build_icon_hooks"](cfg, platforms=("mac",))
        assert pre is not None and post is not None, (
            "no-glyph appiconset must still get overlay hooks"
        )
    print("OK: build_icon_hooks returns overlay hooks for a glyphless asset dir")


def test_swap_dev_icons_overlays_banner_without_glyph():
    try:
        from PIL import Image
    except ImportError:
        import pytest
        pytest.skip("Pillow not installed")
    sys.path.insert(0, str(LIB_DIR.parent))
    try:
        ns = _load_apple()
        ns["__name__"] = "admin_lib.apple"
        ns["__package__"] = "admin_lib"
        with tempfile.TemporaryDirectory() as root:
            mac_dir = os.path.join(root, "AppIcon.appiconset")
            os.makedirs(mac_dir)
            png = os.path.join(mac_dir, "icon_512.png")
            Image.new("RGBA", (512, 512), (20, 20, 30, 255)).save(png)
            before = open(png, "rb").read()

            ok = ns["swap_dev_icons"]({"mac": mac_dir}, {})  # no glyph -> overlay
            assert ok is True
            after = open(png, "rb").read()
            assert after != before, "overlay must modify the existing PNG in place"
            # the art region (top-left) stays the base color; only the banner changes
            im = Image.open(png).convert("RGBA")
            assert im.size == (512, 512), "overlay preserves the canonical size"
        print("OK: swap_dev_icons overlays the banner on existing PNGs (no glyph)")
    finally:
        sys.path.remove(str(LIB_DIR.parent))


def test_inject_and_strip_banner_layer():
    ns = _load_apple()
    doc = {"groups": [{
        "layers": [{"name": "icon-raw", "image-name": "icon-raw.png"}],
        "translucency": {"enabled": True, "value": 0.5},
    }]}

    ns["_inject_banner_layer"](doc)
    assert len(doc["groups"]) == 2
    banner_group = doc["groups"][-1]
    assert ns["_is_banner_group"](banner_group), "banner group appended last (renders on top)"
    layer = banner_group["layers"][0]
    assert layer["glass"] is False, "banner layer must be opaque (no Liquid Glass)"
    assert layer["image-name"] == "dev-banner.png"
    assert "translucency" not in banner_group, "banner group must not inherit translucency"
    # Base glyph group is untouched.
    assert doc["groups"][0]["layers"][0]["name"] == "icon-raw"

    # Idempotent: re-injecting does not stack a second banner group.
    ns["_inject_banner_layer"](doc)
    assert sum(1 for g in doc["groups"] if ns["_is_banner_group"](g)) == 1

    ns["_strip_banner_layer"](doc)
    assert len(doc["groups"]) == 1
    assert not any(ns["_is_banner_group"](g) for g in doc["groups"])
    print("OK: _inject/_strip_banner_layer add/remove one opaque top group idempotently")


def test_restore_icon_composer_reverts_from_backup():
    ns = _load_apple()
    with tempfile.TemporaryDirectory() as root:
        icon = os.path.join(root, "AppIcon.icon")
        assets = os.path.join(icon, "Assets")
        os.makedirs(assets)
        pristine = '{\n  "groups" : [\n\n  ]\n}\n'
        json_path = os.path.join(icon, "icon.json")
        backup = os.path.join(root, "icon-backup", "icon-composer")
        os.makedirs(backup)

        # Simulate a swapped state: pristine json saved in backup, live json
        # mutated with a banner group, banner asset present.
        with open(os.path.join(backup, "icon.json"), "w") as f:
            f.write(pristine)
        with open(json_path, "w") as f:
            f.write('{"groups":[{"layers":[{"name":"dev-banner"}]}]}')
        with open(os.path.join(assets, "dev-banner.png"), "wb") as f:
            f.write(b"PNG")

        ns["restore_icon_composer"](icon, backup)

        with open(json_path) as f:
            assert f.read() == pristine, "icon.json restored byte-for-byte from backup"
        assert not os.path.exists(os.path.join(assets, "dev-banner.png")), "banner asset removed"
        assert not os.path.exists(backup), "backup dir cleaned up"
    print("OK: restore_icon_composer reverts icon.json from backup + removes banner asset")


def test_restore_icon_composer_strips_in_place_without_backup():
    ns = _load_apple()
    with tempfile.TemporaryDirectory() as root:
        icon = os.path.join(root, "AppIcon.icon")
        assets = os.path.join(icon, "Assets")
        os.makedirs(assets)
        json_path = os.path.join(icon, "icon.json")
        with open(json_path, "w") as f:
            json.dump({"groups": [
                {"layers": [{"name": "icon-raw"}]},
                {"layers": [{"name": "dev-banner", "image-name": "dev-banner.png"}]},
            ]}, f)
        with open(os.path.join(assets, "dev-banner.png"), "wb") as f:
            f.write(b"PNG")
        backup = os.path.join(root, "missing-backup")  # never created

        ns["restore_icon_composer"](icon, backup)

        with open(json_path) as f:
            doc = json.load(f)
        assert len(doc["groups"]) == 1, "banner group stripped in place"
        assert doc["groups"][0]["layers"][0]["name"] == "icon-raw"
        assert not os.path.exists(os.path.join(assets, "dev-banner.png"))
    print("OK: restore_icon_composer strips banner in place when backup is gone")


def test_refresh_app_icon_cache_reregisters_bundle():
    calls = []

    def fake_run(argv, **kwargs):
        calls.append(argv)
        return _FakeCompleted(returncode=0)

    ns = _load_apple(subprocess_run=fake_run)
    with tempfile.TemporaryDirectory() as root:
        app = os.path.join(root, "Macterm.app")
        os.makedirs(app)
        ns["refresh_app_icon_cache"](app)

    if os.path.exists(ns["_LSREGISTER"]):
        assert calls, "refresh must invoke lsregister when it is present"
        ls = calls[-1]
        assert ls[0] == ns["_LSREGISTER"] and ls[1] == "-f" and ls[2] == app, ls
        print("OK: refresh_app_icon_cache touches + re-registers the bundle via lsregister -f")
    else:
        assert not calls, "refresh must be a no-op when lsregister is absent"
        print("OK: refresh_app_icon_cache is a no-op without lsregister (non-macOS)")


def test_swap_dev_icon_composer_roundtrip():
    try:
        import PIL  # noqa: F401
    except ImportError:
        import pytest
        pytest.skip("Pillow not installed")

    # swap does `from .icons import make_dev_banner`; give the exec-loaded
    # namespace a package context so that relative import resolves to the real
    # admin_lib.icons (repo root on sys.path makes admin_lib importable).
    sys.path.insert(0, str(LIB_DIR.parent))
    try:
        ns = _load_apple()
        ns["__name__"] = "admin_lib.apple"
        ns["__package__"] = "admin_lib"
        from PIL import Image
        with tempfile.TemporaryDirectory() as root:
            icon = os.path.join(root, "AppIcon.icon")
            assets = os.path.join(icon, "Assets")
            os.makedirs(assets)
            Image.new("RGBA", (256, 256), (10, 10, 10, 255)).save(
                os.path.join(assets, "icon-raw.png"))
            pristine = json.dumps({
                "fill": {"solid": "srgb:0,0,0,1"},
                "groups": [{
                    "layers": [{"glass": True, "image-name": "icon-raw.png", "name": "icon-raw"}],
                    "translucency": {"enabled": True, "value": 0.5},
                }],
            }, indent=2) + "\n"
            json_path = os.path.join(icon, "icon.json")
            with open(json_path, "w") as f:
                f.write(pristine)
            backup = os.path.join(root, "icon-backup", "icon-composer")

            assert ns["swap_dev_icon_composer"](icon, backup) is True
            assert os.path.isfile(os.path.join(assets, "dev-banner.png")), "banner asset generated"
            with open(json_path) as f:
                doc = json.load(f)
            assert any(ns["_is_banner_group"](g) for g in doc["groups"]), "banner group injected"
            assert os.path.isfile(os.path.join(backup, "icon.json")), "pristine json backed up"

            ns["restore_icon_composer"](icon, backup)
            with open(json_path) as f:
                assert f.read() == pristine, "icon.json restored byte-for-byte after dev run"
            assert not os.path.exists(os.path.join(assets, "dev-banner.png"))
            assert not os.path.exists(backup)
        print("OK: swap_dev_icon_composer → restore round-trips icon.json + banner asset")
    finally:
        sys.path.remove(str(LIB_DIR.parent))




# ---------------------------------------------------------------------------
# resolve_simulator / ensure_booted_simulator
# ---------------------------------------------------------------------------

_BOOTED_LIST = "    iPhone 17 (312D27AE-281C-4CD5-865E-45A30CEBCC60) (Booted)\n"
_AVAILABLE_LIST = (
    "-- iOS 25.0 --\n"
    "    iPhone 15 (AAAAAAAA-1111-2222-3333-444444444444) (Shutdown)\n"
    "-- iOS 26.5 --\n"
    "    iPhone 16 (BBBBBBBB-1111-2222-3333-444444444444) (Shutdown)\n"
    "    iPhone 17 (CCCCCCCC-1111-2222-3333-444444444444) (Shutdown)\n"
)


def _simctl_stub(booted="", available="", calls=None):
    """Fake subprocess.run answering `simctl list devices booted|available`."""
    def _run(cmd, *a, **k):
        if calls is not None:
            calls.append(cmd)
        if "list" in cmd and "booted" in cmd:
            return _FakeCompleted(stdout=booted)
        if "list" in cmd and "available" in cmd:
            return _FakeCompleted(stdout=available)
        return _FakeCompleted()
    return _run


def test_resolve_simulator_prefers_booted_device():
    ns = _load_apple(subprocess_run=_simctl_stub(booted=_BOOTED_LIST))
    did, name = ns["resolve_simulator"]()
    assert did == "312D27AE-281C-4CD5-865E-45A30CEBCC60", did
    assert name == "iPhone 17", name
    print("OK: resolve_simulator returns the already-booted device")


def test_resolve_simulator_picks_newest_iphone_when_none_booted():
    ns = _load_apple(subprocess_run=_simctl_stub(available=_AVAILABLE_LIST))
    did, name = ns["resolve_simulator"]()
    assert name == "iPhone 17", name
    assert did == "CCCCCCCC-1111-2222-3333-444444444444", did
    print("OK: resolve_simulator picks newest runtime + newest model when none booted")


def test_resolve_simulator_exits_when_no_iphones():
    ns = _load_apple(subprocess_run=_simctl_stub(available="-- iOS 26.5 --\n"))
    try:
        ns["resolve_simulator"]()
    except SystemExit as e:
        assert e.code == 1, e.code
        print("OK: resolve_simulator exits 1 when no iPhone simulators exist")
        return
    raise AssertionError("expected SystemExit when no iPhone simulators exist")


def test_ensure_booted_simulator_waits_for_boot_to_complete():
    calls = []
    ns = _load_apple(subprocess_run=_simctl_stub(available=_AVAILABLE_LIST, calls=calls))
    did, name = ns["ensure_booted_simulator"]()
    assert did == "CCCCCCCC-1111-2222-3333-444444444444", did

    bootstatus = [c for c in calls if "bootstatus" in c]
    assert len(bootstatus) == 1, calls
    # -b boots the device if needed AND blocks until boot finishes; without it
    # callers race an unbooted device into install/launch.
    assert bootstatus[0] == ["xcrun", "simctl", "bootstatus", did, "-b"], bootstatus[0]
    assert ["open", "-a", "Simulator"] in calls, calls
    print("OK: ensure_booted_simulator blocks on `simctl bootstatus -b` for the resolved device")


# ---------------------------------------------------------------------------
# stream_sim_log_detached
# ---------------------------------------------------------------------------

class _FakePopen:
    def __init__(self, pid=4242):
        self.pid = pid


def _popen_stub(calls, pid=4242):
    def _popen(cmd, **kwargs):
        calls.append((cmd, kwargs))
        return _FakePopen(pid)
    return _popen


def test_stream_sim_log_detached_spawns_detached_and_writes_pid_file():
    calls = []
    ns = _load_apple(subprocess_popen=_popen_stub(calls))
    with tempfile.TemporaryDirectory() as d:
        log_file = os.path.join(d, "nested", "rn-sim.log")
        pid_file = os.path.join(d, "nested", "rn-sim.pid")
        pid = ns["stream_sim_log_detached"]("SIM-ID", 'subsystem == "x"', log_file, pid_file=pid_file)

        assert pid == 4242, pid
        cmd, kwargs = calls[0]
        assert cmd == ["xcrun", "simctl", "spawn", "SIM-ID",
                       "log", "stream", "--level", "debug",
                       "--predicate", 'subsystem == "x"'], cmd
        # start_new_session is what lets the stream outlive a caller that
        # os.execvp's into a supervisor.
        assert kwargs["start_new_session"] is True, kwargs
        assert os.path.isdir(os.path.dirname(log_file)), "log dir created"
        with open(pid_file) as f:
            assert f.read().strip() == "4242"
    print("OK: stream_sim_log_detached spawns a detached simctl log stream and records its pid")


def test_stream_sim_log_detached_stays_out_of_children():
    # _children is reaped by an atexit handler; a detached stream must not be
    # registered there or it dies with the launching process.
    ns = _load_apple(subprocess_popen=_popen_stub([]))
    with tempfile.TemporaryDirectory() as d:
        ns["stream_sim_log_detached"]("SIM-ID", "p", os.path.join(d, "s.log"))
        assert ns["_children"] == [], ns["_children"]
    print("OK: stream_sim_log_detached does not register the stream in _children")


def test_stream_sim_log_detached_reuses_a_live_stream():
    calls = []
    ns = _load_apple(subprocess_popen=_popen_stub(calls))
    with tempfile.TemporaryDirectory() as d:
        pid_file = os.path.join(d, "rn-sim.pid")
        with open(pid_file, "w") as f:
            f.write(str(os.getpid()))       # a pid that is definitely alive

        pid = ns["stream_sim_log_detached"]("SIM-ID", "p", os.path.join(d, "s.log"), pid_file=pid_file)
        assert pid == os.getpid(), pid
        assert calls == [], "must not spawn a second stream while one is live"
    print("OK: stream_sim_log_detached reuses a live stream instead of stacking a second")


def test_stream_sim_log_detached_restarts_on_stale_pid_file():
    calls = []
    ns = _load_apple(subprocess_popen=_popen_stub(calls))
    with tempfile.TemporaryDirectory() as d:
        pid_file = os.path.join(d, "rn-sim.pid")
        with open(pid_file, "w") as f:
            f.write("999999")               # above macOS max pid — never live

        pid = ns["stream_sim_log_detached"]("SIM-ID", "p", os.path.join(d, "s.log"), pid_file=pid_file)
        assert pid == 4242, pid
        assert len(calls) == 1, calls
        with open(pid_file) as f:
            assert f.read().strip() == "4242", "pid file rewritten to the new stream"
    print("OK: stream_sim_log_detached restarts when the recorded pid is dead")


def test_stream_sim_log_detached_restarts_on_unreadable_pid_file():
    calls = []
    ns = _load_apple(subprocess_popen=_popen_stub(calls))
    with tempfile.TemporaryDirectory() as d:
        pid_file = os.path.join(d, "rn-sim.pid")
        with open(pid_file, "w") as f:
            f.write("not-a-pid")

        pid = ns["stream_sim_log_detached"]("SIM-ID", "p", os.path.join(d, "s.log"), pid_file=pid_file)
        assert pid == 4242, pid
        assert len(calls) == 1, calls
    print("OK: stream_sim_log_detached restarts when the pid file is garbage")


def test_resolve_mac_dev_process_defaults_to_the_dev_bundle():
    ns = _load_apple()
    cfg = {"mac_scheme": "CCTool", "mac_process": "CCTool"}
    assert ns["_resolve_mac_dev_process"](cfg) == "CCToolDev.app/Contents/MacOS/CCTool"
    print("OK: _resolve_mac_dev_process matches the renamed dev bundle by default")


def test_resolve_mac_dev_process_without_rename_matches_build_products():
    """mac_dev_rename = false produces no <name>Dev.app, so a pattern naming one
    matches nothing — the dev loop then never kills its own app and reports it
    never launched."""
    ns = _load_apple()
    cfg = {"mac_scheme": "CCTool", "mac_process": "CCTool", "mac_dev_rename": False}
    pattern = ns["_resolve_mac_dev_process"](cfg)
    assert pattern == "/Build/Products/Debug/CCTool.app/Contents/MacOS/CCTool", pattern

    running = ("/Users/x/Library/Developer/Xcode/DerivedData/CCTool-abc"
               "/Build/Products/Debug/CCTool.app/Contents/MacOS/CCTool")
    assert pattern in running, "pattern must match the app the dev loop just built"
    assert pattern not in "/Applications/CCTool.app/Contents/MacOS/CCTool", \
        "pattern must not match the installed prod copy"
    print("OK: _resolve_mac_dev_process falls back to the build-products path when rename is off")


def test_resolve_mac_dev_process_honours_build_config_without_rename():
    ns = _load_apple()
    cfg = {"mac_scheme": "CCTool", "mac_process": "CCTool",
           "mac_dev_rename": False, "mac_build_config": "Staging"}
    assert ns["_resolve_mac_dev_process"](cfg) == \
        "/Build/Products/Staging/CCTool.app/Contents/MacOS/CCTool"
    print("OK: _resolve_mac_dev_process uses mac_build_config in the no-rename pattern")


def test_resolve_mac_dev_process_explicit_wins_over_rename_flag():
    ns = _load_apple()
    cfg = {"mac_scheme": "CCTool", "mac_process": "CCTool",
           "mac_dev_rename": False, "mac_dev_process": "custom-pattern"}
    assert ns["_resolve_mac_dev_process"](cfg) == "custom-pattern"
    print("OK: mac_dev_process overrides the no-rename fallback")


def test_resolve_mac_dev_process_install_matches_the_install_path():
    """`dev --install` launches from mac_install_path_dev, not the build products
    dir — a pattern naming either default bundle would match nothing there."""
    ns = _load_apple()
    cfg = {"mac_scheme": "CCTool", "mac_process": "CCTool",
           "mac_install_path_dev": "/Applications/CCToolDev.app"}
    assert ns["_resolve_mac_dev_process"](cfg, install=True) == \
        "/Applications/CCToolDev.app/Contents/MacOS/CCTool"


def test_resolve_mac_dev_process_install_wins_over_the_rename_fallback():
    ns = _load_apple()
    cfg = {"mac_scheme": "CCTool", "mac_process": "CCTool", "mac_dev_rename": False,
           "mac_install_path_dev": "/Applications/CCTool.app/"}
    assert ns["_resolve_mac_dev_process"](cfg, install=True) == \
        "/Applications/CCTool.app/Contents/MacOS/CCTool"


def test_resolve_mac_dev_process_install_without_a_path_keeps_the_normal_pattern():
    """--install errors out later when mac_install_path_dev is unset; until then the
    pattern must stay the one the loop would otherwise launch."""
    ns = _load_apple()
    cfg = {"mac_scheme": "CCTool", "mac_process": "CCTool"}
    assert ns["_resolve_mac_dev_process"](cfg, install=True) == \
        "CCToolDev.app/Contents/MacOS/CCTool"


def test_resolve_mac_dev_process_ignores_install_when_not_installing():
    ns = _load_apple()
    cfg = {"mac_scheme": "CCTool", "mac_process": "CCTool",
           "mac_install_path_dev": "/Applications/CCToolDev.app"}
    assert ns["_resolve_mac_dev_process"](cfg) == "CCToolDev.app/Contents/MacOS/CCTool"


# ---------------------------------------------------------------------------
# prebuild_cmd
# ---------------------------------------------------------------------------

def _load_apple_with_run_cmd(rc=0):
    """apple.py plus a recording run_cmd, which the stripped .core import
    would otherwise leave undefined."""
    ns = _load_apple()
    calls = []

    def _run_cmd(cmd, *a, **k):
        calls.append(cmd)
        return rc

    ns["run_cmd"] = _run_cmd
    return ns, calls


def test_prebuild_cmd_runs_before_the_project_path_resolves():
    ns, calls = _load_apple_with_run_cmd()
    cfg = {"project_path": "App.xcodeproj", "prebuild_cmd": "xcodegen generate"}
    assert ns["_detect_project_path"](cfg) == "App.xcodeproj"
    assert calls == ["xcodegen generate"], calls
    print("OK: prebuild_cmd runs before the project path resolves")


def test_prebuild_cmd_runs_once_per_invocation():
    """A single admin run resolves the project path many times; regenerating
    on each would be wasted work."""
    ns, calls = _load_apple_with_run_cmd()
    cfg = {"project_path": "App.xcodeproj", "prebuild_cmd": "xcodegen generate"}
    for _ in range(4):
        ns["_detect_project_path"](cfg)
    assert calls == ["xcodegen generate"], calls
    print("OK: prebuild_cmd runs once per invocation")


def test_no_prebuild_cmd_key_runs_nothing():
    ns, calls = _load_apple_with_run_cmd()
    assert ns["_detect_project_path"]({"project_path": "App.xcodeproj"}) == "App.xcodeproj"
    assert calls == [], calls
    print("OK: projects without prebuild_cmd are unaffected")


def test_prebuild_cmd_failure_aborts():
    """A stale or missing project must not be built against, so a failed
    generate stops the command rather than falling through."""
    ns, _ = _load_apple_with_run_cmd(rc=3)
    cfg = {"project_path": "App.xcodeproj", "prebuild_cmd": "xcodegen generate"}
    try:
        ns["_detect_project_path"](cfg)
    except SystemExit:
        pass
    else:
        raise AssertionError("expected SystemExit when prebuild_cmd fails")
    print("OK: prebuild_cmd failure aborts the command")
