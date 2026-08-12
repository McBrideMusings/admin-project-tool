"""Unit tests for the testable slices of admin_lib/apple.py.

Scope follows #120's recommendation: cover the helpers that are
pure-python or filesystem-only, skip the ones that shell out to
xcodebuild / plutil / codesign / xcrun. The module is loaded via the
same exec+regex workaround used by test_logtail (relative imports can't
resolve outside a package); #95 tracks replacing that workaround.
"""
from __future__ import annotations

import fcntl
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


def _load_apple(subprocess_run=None, subprocess_popen=None,
                 log_dir="/tmp", project_root="/tmp/project", warn=None):
    """Load apple.py into an isolated namespace with core/*and optionally
    subprocess.run / subprocess.Popen* stubbed.

    subprocess_run, if given, replaces subprocess.run in the loaded
    namespace so tests can assert what plutil/codesign would have been
    called with without actually invoking them. subprocess_popen does the
    same for the detached `log stream` child. log_dir/project_root back the
    get_log_dir()/get_project_root() stubs (admin-project-tool#447 — the raw
    xcodebuild log path is keyed on these). warn, if given, replaces the
    warn() stub so a test can capture what diagnose_build_failure() reports.
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
        "get_log_dir": lambda: log_dir,
        "get_project_root": lambda: project_root,
        "info": lambda *a, **k: None,
        "ok": lambda *a, **k: None,
        "warn": warn or (lambda *a, **k: None),
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
# target_build_dir — admin-project-tool#448
#
# `admin dev mac` used to resolve the just-built .app with find_app(), a glob
# across every `<scheme>-*` DerivedData directory the scheme has ever built
# into (one per git worktree it was ever built from, including deleted
# worktrees) picking the newest by mtime. That can return a stale sibling's
# app instead of the one the build just produced. target_build_dir() instead
# asks xcodebuild for TARGET_BUILD_DIR — the one directory the build for this
# exact scheme/config/destination actually used.
# ---------------------------------------------------------------------------

def _show_build_settings_stub(build_dir=None, returncode=0):
    """Fake subprocess.run answering `xcodebuild -showBuildSettings`.

    build_dir=None yields real-looking output with the TARGET_BUILD_DIR line
    left out, so the "setting missing" case is a scan over other settings
    rather than a trivially empty stdout.
    """
    lines = [
        'Build settings for action build and target "iptv-mac":',
        "    ACTION = build",
        "    CONFIGURATION = Debug",
    ]
    if build_dir is not None:
        lines.append(f"    TARGET_BUILD_DIR = {build_dir}")
    lines.append("    TARGET_NAME = iptv-mac")
    stdout = "\n".join(lines) + "\n"

    def _run(cmd, *a, **k):
        return _FakeCompleted(returncode=returncode, stdout=stdout, stderr="")
    return _run


def test_target_build_dir_parses_setting():
    ns = _load_apple(subprocess_run=_show_build_settings_stub("/tmp/fake-build-dir"))
    result = ns["target_build_dir"]("Foo.xcodeproj", "Foo", "Debug", "platform=macOS")
    assert result == "/tmp/fake-build-dir", result
    print("OK: target_build_dir parses TARGET_BUILD_DIR from -showBuildSettings output")


def test_target_build_dir_returns_none_when_setting_missing():
    ns = _load_apple(subprocess_run=_show_build_settings_stub(build_dir=None))
    result = ns["target_build_dir"]("Foo.xcodeproj", "Foo", "Debug", "platform=macOS")
    assert result is None, result
    print("OK: target_build_dir returns None when the setting isn't in the output")


def test_target_build_dir_returns_none_on_query_failure():
    ns = _load_apple(subprocess_run=_show_build_settings_stub("/tmp/x", returncode=1))
    result = ns["target_build_dir"]("Foo.xcodeproj", "Foo", "Debug", "platform=macOS")
    assert result is None, result
    print("OK: target_build_dir returns None when xcodebuild exits non-zero")


def test_target_build_dir_ignores_stale_sibling_that_find_app_would_pick():
    """The #448 regression, reproduced at the path-resolution level.

    Two DerivedData directories exist for the same scheme — one for the
    worktree that just built (newer TARGET_BUILD_DIR per xcodebuild), one
    stale sibling left behind by a deleted worktree whose .app happens to
    have a *newer* mtime (e.g. last opened, re-signed, or just an unlucky
    filesystem timestamp). find_app()'s mtime-max glob picks the stale one;
    target_build_dir() + a direct join does not, because it never globs.
    """
    with tempfile.TemporaryDirectory() as d:
        def _derived_data(slug, mtime):
            """One DerivedData products dir holding one built app."""
            products = os.path.join(d, f"iptv-mac-{slug}", "Build", "Products", "Debug")
            os.makedirs(products)
            app = os.path.join(products, "iptv-mac Dev.app")
            os.mkdir(app)
            os.utime(app, (mtime, mtime))
            return products, app

        just_built_dir, just_built_app = _derived_data("fgrnssly", 1000000)
        # Stale sibling has the newer mtime — the exact condition that made
        # find_app()'s glob pick the wrong app in the reported bug.
        _, stale_app = _derived_data("cclftyfv", 2000000)

        ns = _load_apple(subprocess_run=_show_build_settings_stub(just_built_dir))
        glob_pattern = os.path.join(
            d, "iptv-mac-*", "Build", "Products", "Debug", "iptv-mac Dev.app"
        )
        assert ns["find_app"](glob_pattern) == stale_app

        build_dir = ns["target_build_dir"](
            "Foo.xcodeproj", "iptv-mac", "Debug", "platform=macOS"
        )
        resolved = os.path.join(build_dir, os.path.basename(glob_pattern))
        assert resolved == just_built_app, resolved
    print("OK: target_build_dir-based resolution picks the just-built app, "
          "not the stale sibling find_app's mtime glob would return")


# ---------------------------------------------------------------------------
# _raw_build_log_path / diagnose_build_failure — admin-project-tool#447
#
# The raw xcodebuild log used to live at one fixed /tmp path shared by every
# checkout on the machine, so two builds running from different
# worktrees/clones of the same project overwrote each other's log with no
# way to tell after the fact. _raw_build_log_path() keys the path on the
# project's own tmp/ dir (get_log_dir()) instead.
# ---------------------------------------------------------------------------

def test_raw_build_log_path_is_under_the_projects_own_log_dir():
    with tempfile.TemporaryDirectory() as d:
        log_dir = os.path.join(d, "tmp")
        ns = _load_apple(log_dir=log_dir)
        result = ns["_raw_build_log_path"]()
        assert result == os.path.join(log_dir, "admin-xcode-build.log"), result
    print("OK: _raw_build_log_path is keyed on this project's own tmp/ dir, "
          "not a machine-wide /tmp path")


def test_diagnose_build_failure_reads_from_the_projects_own_log_dir():
    with tempfile.TemporaryDirectory() as d:
        log_dir = os.path.join(d, "tmp")
        os.makedirs(log_dir)
        log_path = os.path.join(log_dir, "admin-xcode-build.log")
        with open(log_path, "w") as f:
            f.write("# /some/project @ 2026-08-10 10:00:00\n")
            f.write("errSecInternalComponent\n")

        warnings = []
        ns = _load_apple(log_dir=log_dir, warn=lambda msg: warnings.append(msg))
        ns["diagnose_build_failure"]()
        assert any("keychain is locked" in w for w in warnings), warnings
    print("OK: diagnose_build_failure reads the log from this project's own "
          "tmp/ dir rather than the old fixed /tmp path")


def test_diagnose_build_failure_is_a_noop_when_no_log_written_yet():
    with tempfile.TemporaryDirectory() as d:
        log_dir = os.path.join(d, "tmp")  # deliberately not created
        warnings = []
        ns = _load_apple(log_dir=log_dir, warn=lambda msg: warnings.append(msg))
        ns["diagnose_build_failure"]()  # must not raise FileNotFoundError
        assert warnings == [], warnings
    print("OK: diagnose_build_failure is a no-op when no raw log exists yet")


# ---------------------------------------------------------------------------
# Xcode build lock — admin-project-tool#450
#
# Two admin build tasks (`admin test apple`, `admin dev mac`, `admin build`)
# against the same checkout could both invoke xcodebuild at once with
# nothing stopping them — the second just looked like a slow build.
# _acquire_xcode_lock()/_release_xcode_lock() wrap the build in a
# per-checkout flock(2); a second acquire while the first is held refuses
# outright rather than waiting or silently proceeding.
#
# flock(2) locks are tied to the *open file description*, not the pid, so a
# second open() of the same lock path conflicts exactly like a second OS
# process would — the tests below hold the lock on a real second fd rather
# than mocking the refusal.
# ---------------------------------------------------------------------------

def _hold_xcode_lock(ns, holder=b""):
    """Hold ns's Xcode build lock on a second fd, as a rival build would.

    Returns the fd; close it to release. holder, if given, is written as
    the description _acquire_xcode_lock() reads back when it refuses.
    """
    lock_path = ns["_xcode_lock_path"]()
    os.makedirs(os.path.dirname(lock_path), exist_ok=True)
    fd = os.open(lock_path, os.O_RDWR | os.O_CREAT)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    if holder:
        os.write(fd, holder)
    return fd


def test_acquire_xcode_lock_succeeds_when_uncontended():
    with tempfile.TemporaryDirectory() as d:
        ns = _load_apple(log_dir=d)
        lock = ns["_acquire_xcode_lock"]("xcodebuild build -scheme Foo")
        assert lock is not None
        ns["_release_xcode_lock"](lock)
    print("OK: _acquire_xcode_lock succeeds when nothing else holds it")


def test_xcode_lock_path_is_under_the_projects_own_log_dir():
    with tempfile.TemporaryDirectory() as d:
        ns = _load_apple(log_dir=d)
        result = ns["_xcode_lock_path"]()
        assert result == os.path.join(d, "admin-xcode-build.lock"), result
    print("OK: the Xcode build lock lives under this project's own tmp/ dir, "
          "not one fixed machine-wide path")


def test_acquire_xcode_lock_refuses_and_names_the_running_build():
    with tempfile.TemporaryDirectory() as d:
        ns = _load_apple(log_dir=d)
        holder_fd = _hold_xcode_lock(
            ns, b"xcodebuild test -scheme iptv-mac, started at 21:57:03 (pid 111)")

        messages = []
        ns["err"] = lambda *a, **k: messages.append(" ".join(str(x) for x in a))
        result = ns["_acquire_xcode_lock"]("xcodebuild build -scheme iptv-ios")
        assert result is None, result
        assert any("21:57:03" in m for m in messages), messages
        assert any("iptv-mac" in m for m in messages), messages

        os.close(holder_fd)
    print("OK: a second acquire refuses outright and names the build already "
          "running, including its start time")


def test_release_xcode_lock_lets_the_next_acquire_succeed():
    with tempfile.TemporaryDirectory() as d:
        ns = _load_apple(log_dir=d)
        first = ns["_acquire_xcode_lock"]("build 1")
        assert first is not None
        ns["_release_xcode_lock"](first)

        second = ns["_acquire_xcode_lock"]("build 2")
        assert second is not None
        ns["_release_xcode_lock"](second)
    print("OK: releasing the lock lets a later build acquire it")


def test_stale_lock_is_reclaimed_without_manual_cleanup():
    """A holder killed by kill -9 or the OOM killer never runs its own
    cleanup, so the lock must not depend on it doing so. Closing the
    holder's fd without going through _release_xcode_lock() is the same
    event the kernel generates when the holding process dies."""
    with tempfile.TemporaryDirectory() as d:
        ns = _load_apple(log_dir=d)
        holder_fd = _hold_xcode_lock(ns)
        os.close(holder_fd)  # simulates the holding process dying

        result = ns["_acquire_xcode_lock"]("xcodebuild build -scheme iptv-mac")
        assert result is not None, "a stale lock must not wedge later builds"
        ns["_release_xcode_lock"](result)
    print("OK: a lock abandoned by a dead process is reclaimed with no pid "
          "file and no manual deletion")


def test_two_different_checkouts_do_not_contend():
    """Two worktrees/clones of the same project get their own lock file (via
    get_log_dir()), so a build in one never refuses because of a build in
    the other — only two builds in the *same* checkout should contend."""
    with tempfile.TemporaryDirectory() as d1, tempfile.TemporaryDirectory() as d2:
        ns1 = _load_apple(log_dir=d1)
        ns2 = _load_apple(log_dir=d2)

        lock1 = ns1["_acquire_xcode_lock"]("build in checkout 1")
        assert lock1 is not None
        lock2 = ns2["_acquire_xcode_lock"]("build in checkout 2")
        assert lock2 is not None, \
            "a build in a different checkout must not be blocked by this one"

        ns1["_release_xcode_lock"](lock1)
        ns2["_release_xcode_lock"](lock2)
    print("OK: two different checkouts each get their own lock and never contend")


def test_run_locked_xcode_runs_cmd_when_uncontended():
    with tempfile.TemporaryDirectory() as d:
        ns, calls = _load_apple_with_run_cmd(log_dir=d)
        rc = ns["run_locked_xcode"]("xcodebuild test -scheme iptv-mac", "apple test_cmd")
        assert rc == 0, rc
        assert calls == ["xcodebuild test -scheme iptv-mac"], calls
    print("OK: run_locked_xcode runs the command when the lock is free")


def test_run_locked_xcode_refuses_without_running_cmd_when_locked():
    """`admin test apple`'s apple.test_cmd runs outside xcodebuild_filtered,
    so without this it would silently run alongside a build/dev/deploy
    xcodebuild instead of taking the same lock."""
    with tempfile.TemporaryDirectory() as d:
        ns, calls = _load_apple_with_run_cmd(log_dir=d)
        holder_fd = _hold_xcode_lock(ns)

        rc = ns["run_locked_xcode"]("xcodebuild test -scheme iptv-mac", "apple test_cmd")
        assert rc == 1, rc
        assert calls == [], calls

        os.close(holder_fd)
    print("OK: run_locked_xcode refuses and never runs the test command "
          "when another build already holds the lock")


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
    raise AssertionError("log_predicate did not raise SystemExit on an unknown level")


def test_log_predicate_unknown_level_names_accepted_set():
    # admin-project-tool#445 — the rejection used to say only "Unknown log
    # level: info", forcing a source read to learn the accepted set. It must
    # now name every accepted level, derived from the same dict log_predicate
    # dispatches on rather than typed out a second time.
    seen = []
    ns = _load_apple()
    ns["err"] = lambda msg: seen.append(msg)
    try:
        ns["log_predicate"]("MyApp", "com.example.app", level="info")
    except SystemExit:
        pass
    assert len(seen) == 1, seen
    assert seen[0] == (
        "Unknown log level: info (expected one of: app, debug, system, verbose)"
    ), seen[0]
    print("OK: log_predicate names the accepted set when rejecting an unknown level")


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
# _compose_dev_settings (#331 — must never push one bundle ID onto every
# target, since xcodebuild_filtered's extra_settings apply globally and that
# collides an app-extension's identity with its parent app's)
# ---------------------------------------------------------------------------

def test_compose_dev_settings_default_emits_suffix():
    ns = _load_apple_stubbed_xcode()
    result = ns["_compose_dev_settings"]({"mac_bundle": "com.example.app"}, "mac")
    assert result == "ADMIN_DEV_BUNDLE_SUFFIX=.dev", result
    print("OK: _compose_dev_settings emits ADMIN_DEV_BUNDLE_SUFFIX=.dev for a default config")


def test_compose_dev_settings_never_emits_product_bundle_identifier():
    ns = _load_apple_stubbed_xcode()
    cases = [
        ({"mac_bundle": "com.example.app"}, "mac"),
        ({"ios_bundle": "com.example.ios", "ios_dev_bundle": "com.example.ios.staging"}, "ios"),
        ({}, "mac"),
        ({"mac_bundle": "com.example.app", "dev_build_settings": "SWIFT_VERSION=5"}, "mac"),
    ]
    for cfg, platform in cases:
        result = ns["_compose_dev_settings"](cfg, platform)
        assert "PRODUCT_BUNDLE_IDENTIFIER" not in result, (cfg, result)
    print("OK: _compose_dev_settings never emits PRODUCT_BUNDLE_IDENTIFIER under any config")


def test_compose_dev_settings_no_bundle_resolvable_emits_nothing():
    ns = _load_apple_stubbed_xcode(xcode_return=None)
    result = ns["_compose_dev_settings"]({}, "mac")
    assert result == "", result
    print("OK: _compose_dev_settings emits nothing when no bundle can be resolved at all")


def test_compose_dev_settings_explicit_dev_bundle_prefixed_by_prod():
    ns = _load_apple_stubbed_xcode()
    cfg = {"mac_bundle": "com.example.app", "mac_dev_bundle": "com.example.app.staging"}
    result = ns["_compose_dev_settings"](cfg, "mac")
    assert result == "ADMIN_DEV_BUNDLE_SUFFIX=.staging", result
    print("OK: _compose_dev_settings derives the suffix from an explicit prod-plus-suffix dev bundle")


def test_compose_dev_settings_explicit_dev_bundle_not_prod_plus_suffix_errors():
    ns = _load_apple_stubbed_xcode()
    cfg = {"mac_bundle": "com.example.app", "mac_dev_bundle": "com.other.app.dev"}
    try:
        ns["_compose_dev_settings"](cfg, "mac")
    except SystemExit as e:
        assert e.code == 1, e.code
        print("OK: _compose_dev_settings errors when an explicit dev bundle isn't prod-plus-suffix")
        return
    raise AssertionError("expected SystemExit for a dev bundle that isn't prod plus a suffix")


def test_compose_dev_settings_bad_dev_bundle_error_names_both_values():
    seen = []
    ns = _load_apple_stubbed_xcode()
    ns["err"] = lambda msg: seen.append(msg)
    cfg = {"mac_bundle": "com.example.app", "mac_dev_bundle": "com.other.app.dev"}
    try:
        ns["_compose_dev_settings"](cfg, "mac")
    except SystemExit:
        pass
    assert len(seen) == 1, seen
    assert "com.other.app.dev" in seen[0], seen[0]
    assert "com.example.app" in seen[0], seen[0]
    print("OK: _compose_dev_settings names both the bad dev bundle and the prod bundle it should extend")


def test_compose_dev_settings_dev_build_settings_with_product_bundle_identifier_errors():
    ns = _load_apple_stubbed_xcode()
    cfg = {
        "mac_bundle": "com.example.app",
        "dev_build_settings": "PRODUCT_BUNDLE_IDENTIFIER=com.example.app.dev",
    }
    try:
        ns["_compose_dev_settings"](cfg, "mac")
    except SystemExit as e:
        assert e.code == 1, e.code
        print(
            "OK: _compose_dev_settings hard-errors when dev_build_settings "
            "names PRODUCT_BUNDLE_IDENTIFIER"
        )
        return
    raise AssertionError(
        "expected SystemExit when dev_build_settings contains PRODUCT_BUNDLE_IDENTIFIER"
    )


def test_compose_dev_settings_combines_extras_and_suffix_in_order():
    ns = _load_apple_stubbed_xcode()
    cfg = {"mac_bundle": "com.example.app", "dev_build_settings": "SWIFT_VERSION=5"}
    result = ns["_compose_dev_settings"](cfg, "mac")
    assert result == "SWIFT_VERSION=5 ADMIN_DEV_BUNDLE_SUFFIX=.dev", result
    print("OK: _compose_dev_settings appends the suffix after dev_build_settings extras")


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

# The runtime header is not decoration — `simctl list devices booted` groups its
# output by runtime exactly like the full listing, and that header is the only
# thing distinguishing a booted Apple TV from a booted iPhone. This fixture used
# to omit it, which made it the one arrangement the real command never produces.
_BOOTED_LIST = (
    "-- iOS 26.5 --\n"
    "    iPhone 17 (312D27AE-281C-4CD5-865E-45A30CEBCC60) (Booted)\n"
)
_BOOTED_LIST_TV = (
    "-- tvOS 26.5 --\n"
    "    Apple TV 4K (3rd generation) (99999999-1111-2222-3333-444444444444) (Booted)\n"
)
_BOOTED_LIST_MIXED = (
    "-- iOS 26.5 --\n"
    "    iPhone 17 (312D27AE-281C-4CD5-865E-45A30CEBCC60) (Booted)\n"
    "-- tvOS 26.5 --\n"
    "    Apple TV 4K (3rd generation) (99999999-1111-2222-3333-444444444444) (Booted)\n"
)
_AVAILABLE_LIST = (
    "-- iOS 25.0 --\n"
    "    iPhone 15 (AAAAAAAA-1111-2222-3333-444444444444) (Shutdown)\n"
    "-- iOS 26.5 --\n"
    "    iPhone 16 (BBBBBBBB-1111-2222-3333-444444444444) (Shutdown)\n"
    "    iPhone 17 (CCCCCCCC-1111-2222-3333-444444444444) (Shutdown)\n"
    "-- tvOS 26.5 --\n"
    "    Apple TV 4K (3rd generation) (DDDDDDDD-1111-2222-3333-444444444444) (Shutdown)\n"
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


def test_resolve_simulator_tv_picks_apple_tv_when_none_booted():
    ns = _load_apple(subprocess_run=_simctl_stub(available=_AVAILABLE_LIST))
    did, name = ns["resolve_simulator"]("tv")
    assert name == "Apple TV 4K (3rd generation)", name
    assert did == "DDDDDDDD-1111-2222-3333-444444444444", did
    print("OK: resolve_simulator('tv') picks the Apple TV, not the newest iPhone")


def test_resolve_simulator_tv_returns_booted_apple_tv():
    ns = _load_apple(subprocess_run=_simctl_stub(booted=_BOOTED_LIST_TV))
    did, name = ns["resolve_simulator"]("tv")
    assert did == "99999999-1111-2222-3333-444444444444", did
    assert name == "Apple TV 4K (3rd generation)", name
    print("OK: resolve_simulator('tv') returns the already-booted Apple TV")


def test_resolve_simulator_ignores_a_booted_device_of_another_platform():
    """The failure this prevents: a booted iPhone answering a request for an
    Apple TV. The app then installs onto the wrong simulator and fails several
    steps away from the wrong choice made here."""
    ns = _load_apple(subprocess_run=_simctl_stub(booted=_BOOTED_LIST,
                                                 available=_AVAILABLE_LIST))
    did, name = ns["resolve_simulator"]("tv")
    assert "Apple TV" in name, name
    assert did == "DDDDDDDD-1111-2222-3333-444444444444", did

    ns = _load_apple(subprocess_run=_simctl_stub(booted=_BOOTED_LIST_TV,
                                                 available=_AVAILABLE_LIST))
    did, name = ns["resolve_simulator"]("ios")
    assert "iPhone" in name, name
    print("OK: a booted simulator of the wrong platform is skipped, not returned")


def test_resolve_simulator_picks_its_own_platform_from_a_mixed_booted_list():
    ns = _load_apple(subprocess_run=_simctl_stub(booted=_BOOTED_LIST_MIXED))
    assert ns["resolve_simulator"]("ios")[1] == "iPhone 17"
    assert ns["resolve_simulator"]("tv")[1] == "Apple TV 4K (3rd generation)"
    print("OK: with both booted, each platform gets its own")


def test_resolve_simulator_rejects_an_unknown_platform():
    ns = _load_apple(subprocess_run=_simctl_stub(available=_AVAILABLE_LIST))
    try:
        ns["resolve_simulator"]("watch")
    except SystemExit as e:
        assert e.code == 1, e.code
        print("OK: resolve_simulator exits 1 on a platform with no SIM_PLATFORMS row")
        return
    raise AssertionError("expected SystemExit for an unknown platform")


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

def _load_apple_with_run_cmd(rc=0, log_dir="/tmp"):
    """apple.py plus a recording run_cmd, which the stripped .core import
    would otherwise leave undefined."""
    ns = _load_apple(log_dir=log_dir)
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


# ---------------------------------------------------------------------------
# Bootstrapping an appiconset that has no PNGs yet
# ---------------------------------------------------------------------------

def test_sizes_come_from_contents_json_when_no_pngs_exist():
    """A brand-new appiconset is a Contents.json and nothing else, and that is
    exactly when someone runs `admin icons regenerate` — to make the art. Sizing
    the render off the PNGs already there cannot serve that case, so the catalog
    is asked what it declares instead.
    """
    ns = _load_apple()
    with tempfile.TemporaryDirectory() as root:
        # A macOS catalog: `size` is in points and `scale` multiplies it, so
        # 16x16@2x is a 32-pixel file — and 32x32@1x names that same file again.
        with open(os.path.join(root, "Contents.json"), "w") as f:
            f.write("""{
              "images": [
                {"filename": "icon_16.png", "idiom": "mac", "scale": "1x", "size": "16x16"},
                {"filename": "icon_32.png", "idiom": "mac", "scale": "2x", "size": "16x16"},
                {"filename": "icon_32.png", "idiom": "mac", "scale": "1x", "size": "32x32"},
                {"filename": "icon_1024.png", "idiom": "mac", "scale": "2x", "size": "512x512"}
              ],
              "info": {"author": "xcode", "version": 1}
            }""")

        sizes = ns["_sizes_from_contents_json"](root)

        assert sizes == {16: "icon_16.png", 32: "icon_32.png", 1024: "icon_1024.png"}, sizes
    print("OK: mac Contents.json sizes resolve to pixel widths, scale included")


def test_ios_contents_json_entries_have_no_scale():
    """An iOS appiconset is one 1024px square with no `scale` key at all —
    absent must read as 1x rather than dropping the entry.
    """
    ns = _load_apple()
    with tempfile.TemporaryDirectory() as root:
        with open(os.path.join(root, "Contents.json"), "w") as f:
            f.write("""{
              "images": [
                {"filename": "AppIcon.png", "idiom": "universal", "platform": "ios", "size": "1024x1024"}
              ],
              "info": {"author": "xcode", "version": 1}
            }""")

        assert ns["_sizes_from_contents_json"](root) == {1024: "AppIcon.png"}
    print("OK: iOS Contents.json entry without a scale resolves to 1024")


def test_missing_or_unreadable_contents_json_yields_nothing():
    ns = _load_apple()
    with tempfile.TemporaryDirectory() as root:
        assert ns["_sizes_from_contents_json"](root) == {}, "no Contents.json → nothing"

        with open(os.path.join(root, "Contents.json"), "w") as f:
            f.write("{ this is not json")
        assert ns["_sizes_from_contents_json"](root) == {}, "unparseable → nothing, not a crash"
    print("OK: a missing or unparseable Contents.json yields no sizes")


# ---------------------------------------------------------------------------
# `admin icons swap-dev` must not undo itself
# ---------------------------------------------------------------------------

def test_swap_dev_can_skip_the_exit_restore():
    """A dev loop swaps, builds, and restores inside one process, so registering
    the restore as an exit cleanup is what makes it safe. The standalone
    `icons swap-dev` verb has no build in between: the same cleanup fires the
    moment the command finishes and silently puts the originals back, so it
    printed "dev icons swapped in" over a catalog it had already restored.
    """
    ns = _load_apple()
    registered = []
    ns["register_cleanup"] = registered.append

    with tempfile.TemporaryDirectory() as root:
        icon_dir = os.path.join(root, "AppIcon.appiconset")
        os.makedirs(icon_dir)
        with open(os.path.join(icon_dir, "icon_128.png"), "wb") as f:
            f.write(b"PROD")
        cfg = {"icons": {"mac_asset_dir": icon_dir}}

        # Nothing to overlay a banner onto that PIL would accept, so stub the
        # two mutations out: what is under test is whether the restore gets
        # armed, not what the banner looks like.
        ns["backup_icons"] = lambda *a, **k: None
        ns["swap_dev_icons"] = lambda *a, **k: None
        ns["restore_stranded_dev_icons"] = lambda *a, **k: None

        pre, _post = ns["build_icon_hooks"](cfg, restore_on_exit=False)
        pre()
        assert registered == [], "swap-dev must not arm the restore that undoes it"

        pre_loop, _ = ns["build_icon_hooks"](cfg)
        pre_loop()
        assert len(registered) == 1, "a dev loop still needs the restore armed"
    print("OK: swap-dev skips the exit restore; the dev loop keeps it")


# ---------------------------------------------------------------------------
# #449 — `icons restore` after two un-restored `dev mac` runs must land on
# the committed originals, not the bannered PNGs from the second run.
#
# `dev_loop_macos` calls post_build() (restore) right after every build, so
# under normal operation the catalog is already clean long before the process
# could exit. The bug only shows up when a run never reaches that restore —
# closed terminal, SIGKILL, Ctrl-C mid-build — which `_load_apple()` already
# models for every test here: it stubs `register_cleanup` to a no-op, so
# calling `pre_build()` without ever calling `post_build()` *is* "the process
# died before its cleanup ran." Two such runs in a row, then a call to
# `icons restore` (`post_build()` from a fresh `build_icon_hooks()`, exactly
# what `archetypes/apple.py`'s `icons restore` subcommand does), must land on
# the byte-identical committed PNG — never the second run's bannered one.
# ---------------------------------------------------------------------------

def test_two_unrestored_dev_runs_then_icons_restore_is_byte_identical():
    ns = _load_apple()
    with tempfile.TemporaryDirectory() as root:
        mac_dir = os.path.join(root, "AppIcon.appiconset")
        os.makedirs(mac_dir)
        committed = b"COMMITTED-ICON-BYTES"
        icon_png = os.path.join(mac_dir, "icon_128.png")
        with open(icon_png, "wb") as f:
            f.write(committed)
        cfg = {"icons": {"mac_asset_dir": mac_dir}}
        backup_dir = os.path.join(root, "tmp", "icon-backup")

        # Stand in for the real PIL banner overlay: what's under test is the
        # backup/self-heal bookkeeping around the mutation, not the pixels.
        def _fake_swap(icon_dirs, icon_config):
            d = list(icon_dirs.values())[0]
            for fname in os.listdir(d):
                if fname.endswith(".png"):
                    p = os.path.join(d, fname)
                    with open(p, "rb") as f:
                        data = f.read()
                    with open(p, "wb") as f:
                        f.write(data + b"-BANNER")
            return True
        ns["swap_dev_icons"] = _fake_swap

        # Run 1: pre_build swaps the banner in; the process "dies" before its
        # post_build restore ever runs (register_cleanup is a no-op here).
        pre1, _post1 = ns["build_icon_hooks"](cfg, platforms=("mac",), backup_dir=backup_dir)
        pre1()
        with open(icon_png, "rb") as f:
            assert f.read() == committed + b"-BANNER", "run 1 should apply the banner"

        # Run 2: a fresh dev-mac invocation. Its pre_build must self-heal from
        # the stranded backup (restore the true original) BEFORE re-backing-up,
        # so the backup it leaves behind is the pristine committed PNG — not
        # the bannered PNG this run is about to write over it.
        pre2, _post2 = ns["build_icon_hooks"](cfg, platforms=("mac",), backup_dir=backup_dir)
        pre2()
        with open(os.path.join(backup_dir, "mac", "icon_128.png"), "rb") as f:
            assert f.read() == committed, (
                "backup after the second un-restored run must still be the "
                "pristine committed PNG, not the bannered one from run 2"
            )

        # `admin icons restore` == a fresh build_icon_hooks()'s post_build().
        _pre3, post3 = ns["build_icon_hooks"](cfg, platforms=("mac",), backup_dir=backup_dir)
        post3()
        with open(icon_png, "rb") as f:
            assert f.read() == committed, (
                "icons restore must leave the icon byte-identical to committed, "
                "not put the banner back"
            )
    print("OK: two un-restored dev-mac runs + icons restore lands on the committed PNG")


def test_two_unrestored_icon_composer_runs_then_icons_restore_is_byte_identical():
    try:
        from PIL import Image
    except ImportError:
        import pytest
        pytest.skip("Pillow not installed")
    sys.path.insert(0, str(LIB_DIR.parent))
    try:
        ns = _load_apple()
        with tempfile.TemporaryDirectory() as root:
            icon = os.path.join(root, "AppIcon.icon")
            assets = os.path.join(icon, "Assets")
            os.makedirs(assets)
            pristine = json.dumps(
                {"groups": [{"layers": [{"name": "icon-raw", "image-name": "icon-raw.png"}]}]},
                indent=2,
            ) + "\n"
            json_path = os.path.join(icon, "icon.json")
            with open(json_path, "w") as f:
                f.write(pristine)
            Image.new("RGBA", (64, 64), (10, 20, 30, 255)).save(
                os.path.join(assets, "icon-raw.png"))

            cfg = {"icons": {"mac_icon": icon}}
            backup_dir = os.path.join(root, "tmp", "icon-backup")

            # Run 1: swap in the banner group, "die" before the restore.
            pre1, _post1 = ns["build_icon_hooks"](cfg, platforms=("mac",), backup_dir=backup_dir)
            pre1()
            with open(json_path) as f:
                assert "dev-banner" in f.read(), "run 1 should inject the banner group"

            # Run 2: self-heal must restore the pristine icon.json from run 1's
            # backup BEFORE re-backing-up, so the backup left behind is the
            # committed icon.json, not run 2's bannered one.
            pre2, _post2 = ns["build_icon_hooks"](cfg, platforms=("mac",), backup_dir=backup_dir)
            pre2()
            backup_json = os.path.join(backup_dir, "icon-composer", "icon.json")
            with open(backup_json) as f:
                assert f.read() == pristine, (
                    "backup after the second un-restored run must still be the "
                    "pristine committed icon.json"
                )

            # `admin icons restore`.
            _pre3, post3 = ns["build_icon_hooks"](cfg, platforms=("mac",), backup_dir=backup_dir)
            post3()
            with open(json_path) as f:
                assert f.read() == pristine, (
                    "icons restore must leave icon.json byte-identical to "
                    "committed, not put the banner back"
                )
        print("OK: two un-restored icon-composer runs + icons restore lands on the "
              "committed icon.json")
    finally:
        sys.path.remove(str(LIB_DIR.parent))


# ---------------------------------------------------------------------------
# launch_args — admin-project-tool#442
#
# `admin dev ios` can hand launch arguments to the app it installs
# (install_and_launch_sim / dev_simulator's launch_args). `admin dev mac` had
# no equivalent: the `open -n <app>` launch took no arguments at all. dev_mac
# and dev_loop_macos now take a launch_args list mirroring dev_ios's, default
# to a new [apple] `launch_args` key, and append it to the open invocation as
# `open -n <app> --args ...`.
#
# These can't drive a real GUI launch (no window, no process to observe) — the
# only thing provable here is the argv construction: given a manifest with and
# without apple.launch_args, what exact command gets handed to `open`.
# ---------------------------------------------------------------------------

def _dev_mac_cfg(**overrides):
    """Minimal [apple] cfg that resolves through dev_mac without shelling
    out — every resolver dev_mac calls before reaching dev_loop_macos is
    given an explicit value so none of them falls back to `xcodebuild
    -showBuildSettings` or a keychain lookup."""
    cfg = {
        "mac_scheme": "Foo",
        "project_path": "Foo.xcodeproj",
        "mac_process": "Foo",
        "mac_bundle": "com.example.foo",
    }
    cfg.update(overrides)
    return cfg


def test_dev_mac_forwards_launch_args_from_apple_config():
    """No launch_args passed explicitly — dev_mac must read the default off
    the [apple] table's launch_args key, the way dev_ios's caller already
    can, and forward it to dev_loop_macos."""
    ns = _load_apple()
    captured = {}
    ns["dev_loop_macos"] = lambda **kwargs: captured.update(kwargs)

    ns["dev_mac"](_dev_mac_cfg(launch_args=["--defaults-suite=throwaway"]))

    assert captured["launch_args"] == ["--defaults-suite=throwaway"], captured.get("launch_args")
    print("OK: dev_mac reads apple.launch_args and forwards it to dev_loop_macos")


def test_dev_mac_install_still_renames_the_dev_app():
    """`dev --install` used to turn the dev rename off.

    dev_loop_macos has one pre_launch slot, and the rename and the install
    were the two halves of an if/else fighting over it. With --install the
    project got a dev build installed under the production name, carrying the
    production name in the menu bar, and no message said the rename had been
    skipped. Both must happen now, rename first: what gets installed is the
    renamed bundle, and pre_launch returns the install path.
    """
    ns = _load_apple()
    captured = {}
    ns["dev_loop_macos"] = lambda **kwargs: captured.update(kwargs)

    order = []
    ns["dev_macos_postprocess"] = (
        lambda built, product, display, sign_identity="-":
            order.append("rename") or os.path.join(os.path.dirname(built), f"{product}.app")
    )

    with tempfile.TemporaryDirectory() as d:
        built = _make_fake_app(d, "Foo.app")
        renamed = os.path.join(d, "Foo DEV.app")
        os.makedirs(renamed, exist_ok=True)
        install_path = os.path.join(d, "installed", "Foo DEV.app")

        ns["dev_mac"](_dev_mac_cfg(
            mac_dev_product_name="Foo DEV",
            mac_install_path_dev=install_path,
        ), install=True)

        result = captured["pre_launch"](built)

    assert order == ["rename"], f"rename must run, got {order}"
    assert result == install_path, result
    print("OK: dev_mac --install renames the dev app before installing it")


def test_dev_mac_install_alone_when_nothing_to_rename():
    """mac_dev_rename = false leaves the installer as the only pre_launch step
    — composing must not invent a rename the project opted out of."""
    ns = _load_apple()
    captured = {}
    ns["dev_loop_macos"] = lambda **kwargs: captured.update(kwargs)
    def _no_rename(*a, **k):
        raise AssertionError("rename must not run when mac_dev_rename is false")

    ns["dev_macos_postprocess"] = _no_rename

    with tempfile.TemporaryDirectory() as d:
        built = _make_fake_app(d, "Foo.app")
        install_path = os.path.join(d, "installed", "Foo.app")

        ns["dev_mac"](_dev_mac_cfg(
            mac_dev_product_name="Foo DEV",
            mac_dev_rename=False,
            mac_install_path_dev=install_path,
        ), install=True)

        assert captured["pre_launch"](built) == install_path

    print("OK: mac_dev_rename = false keeps --install to the install alone")


def test_dev_mac_launch_args_absent_without_the_config_key():
    ns = _load_apple()
    captured = {}
    ns["dev_loop_macos"] = lambda **kwargs: captured.update(kwargs)

    ns["dev_mac"](_dev_mac_cfg())

    assert captured["launch_args"] is None, captured.get("launch_args")
    print("OK: dev_mac forwards no launch_args when apple.launch_args is unset")


def _run_dev_loop_macos_once(build_dir, launch_args):
    """Drive dev_loop_macos through exactly one build/launch iteration and
    return every argv handed to subprocess.run.

    Every dependency dev_loop_macos calls other than the `open` launch itself
    is stubbed to a deterministic no-op: xcodebuild/target-dir resolution
    (build "succeeds" into build_dir), the os_log stream, the reload signal
    file, and poll_for_reload (return False so the loop exits after one pass
    instead of waiting on stdin/SIGUSR1). is_running_fn always reports the
    app as up, so the loop never sleeps waiting for it to appear.
    """
    calls = []

    def _run(cmd, *a, **k):
        calls.append(cmd)
        return _FakeCompleted(returncode=0)

    ns = _load_apple(subprocess_run=_run)
    ns["get_log_file"] = lambda: os.path.join(build_dir, "fake.log")
    ns["setup_reload"] = lambda *a, **k: None
    ns["cleanup_pid_file"] = lambda *a, **k: None
    ns["xcodebuild_filtered"] = lambda *a, **k: 0
    ns["target_build_dir"] = lambda *a, **k: build_dir
    ns["stream_os_log"] = lambda *a, **k: (None, None, None)
    ns["stop_stream"] = lambda *a, **k: None
    ns["poll_for_reload"] = lambda *a, **k: False

    ns["dev_loop_macos"](
        project="Foo.xcodeproj", scheme="Foo", config="Debug",
        destination="platform=macOS", process="Foo", subsystem="",
        predicate='process == "Foo"', app_pattern="Foo.app",
        pid_file=os.path.join(build_dir, "test.pid"),
        is_running_fn=lambda: True, kill_fn=lambda: None,
        launch_args=launch_args,
    )
    return calls


def test_dev_loop_macos_open_call_carries_launch_args():
    with tempfile.TemporaryDirectory() as build_dir:
        os.mkdir(os.path.join(build_dir, "Foo.app"))
        calls = _run_dev_loop_macos_once(
            build_dir, launch_args=["--defaults-suite=throwaway"]
        )

    open_calls = [c for c in calls if c[:2] == ["open", "-n"]]
    assert len(open_calls) == 1, open_calls
    assert open_calls[0] == [
        "open", "-n", os.path.join(build_dir, "Foo.app"),
        "--args", "--defaults-suite=throwaway",
    ], open_calls[0]
    print("OK: dev_loop_macos appends --args <launch_args> to the open invocation")


def test_dev_loop_macos_open_call_omits_args_flag_without_launch_args():
    with tempfile.TemporaryDirectory() as build_dir:
        os.mkdir(os.path.join(build_dir, "Foo.app"))
        calls = _run_dev_loop_macos_once(build_dir, launch_args=None)

    open_calls = [c for c in calls if c[:2] == ["open", "-n"]]
    assert len(open_calls) == 1, open_calls
    assert open_calls[0] == ["open", "-n", os.path.join(build_dir, "Foo.app")], open_calls[0]
    print("OK: dev_loop_macos launches with a bare `open -n` when launch_args is unset")
