"""Layer 2 — Xcode/Swift/iOS helpers for Apple projects.

Provides: reload infrastructure, Xcode build helpers, simulator helpers,
log predicates, app finding.

Dev vs. prod side-by-side installs
----------------------------------
The convention for letting a Debug ("dev") build coexist with a Release
("prod") build on the same machine / simulator / device:

admin never passes a literal ``PRODUCT_BUNDLE_IDENTIFIER`` as
``extra_settings`` to ``xcodebuild_filtered`` — a setting passed that way
applies to *every* target in the project (see ``xcodebuild_filtered``'s
warning below), so naming one bundle ID there forces an app-extension
target (a widget, a share extension, ...) to claim its parent app's
identity, and the installer refuses it. Instead ``_compose_dev_settings``
emits ``ADMIN_DEV_BUNDLE_SUFFIX=<suffix>`` (e.g. ``.dev``, derived by
subtracting the prod bundle ID from the dev one), and each target's own
``PRODUCT_BUNDLE_IDENTIFIER`` setting in the pbxproj must reference it —
``com.foo.app$(ADMIN_DEV_BUNDLE_SUFFIX)`` for the app,
``com.foo.app$(ADMIN_DEV_BUNDLE_SUFFIX).widgets`` for its widget — so each
target derives its own suffixed identity and none can collide. Release
builds pass no suffix, so the variable expands to nothing and prod IDs are
unchanged. A project whose pbxproj hasn't been updated to reference the
variable silently builds dev on top of prod (same ID, no side-by-side
install) rather than failing loudly.

- macOS: after the build, call
  ``dev_macos_postprocess(built, MAC_DEV_PRODUCT_NAME, MAC_DEV_DISPLAY_NAME)``
  on the resulting ``.app`` to rename it, patch ``CFBundleDisplayName`` in
  Info.plist, and re-sign ad-hoc. The rename is what gives Finder / Launchpad
  / Dock a distinct entry; the ad-hoc re-sign is required because the rename
  + plist patch invalidates the original signature.

- iOS (simulator and device): the suffixed bundle ID is sufficient — iOS
  identifies installs by bundle ID, so a distinct dev bundle ID lets dev
  install alongside prod on the same simulator or device. The ``.app``
  filename itself stays the same; no rename is needed (and iOS wouldn't
  respect one anyway).

  Known gap: the home-screen *display name* on iOS cannot be differentiated
  via a global xcodebuild flag — ``INFOPLIST_KEY_CFBundleDisplayName`` is
  unsafe (see ``xcodebuild_filtered``'s warning about SwiftPM resource-bundle
  cascade). A distinct dev display name on iOS requires a per-target
  ``.xcconfig`` or Info.plist setup at the Xcode-project level, which is
  outside admin-project-tool's scope.

Defaults
--------
The dev wrappers (``dev_mac``, ``dev_ios``, ``dev_device``) auto-derive every
dev-side identifier from the production values so a manifest only needs the
prod keys (``mac_bundle``, ``ios_bundle``, ``mac_scheme``, ``ios_scheme``).
Explicit dev keys override the derived defaults.

  Default                                         | Source
  -------                                         | ------
  ``mac_dev_bundle = <mac_bundle>.dev``           | ``_resolve_mac_dev_bundle``
  ``ios_dev_bundle = <ios_bundle>.dev``           | ``_resolve_ios_dev_bundle``
  ``mac_dev_product_name = <process>Dev``         | ``_resolve_mac_dev_product_name``
  ``mac_dev_display_name = <process> Dev``        | ``_resolve_mac_dev_display_name``
  ``ios_dev_display_name = <process> Dev``        | ``_resolve_ios_dev_display_name``
  ``mac_dev_process = <ProductDev>.app/...``      | ``_resolve_mac_dev_process``

``<process>`` falls back to the scheme name when ``{platform}_process`` is
unset (i.e. PRODUCT_NAME == scheme, the Xcode default).
"""

import datetime
import fcntl
import glob
import json
import os
import shutil
import signal
import subprocess
import sys

from .core import (
    C,
    _children,
    command,
    get_log_dir,
    get_log_file,
    get_project_root,
    info,
    ok,
    warn,
    err,
    register_cleanup,
    run_cmd,
)

# The reload flag, its SIGUSR1 handler, and the pid-file plumbing live in
# admin_lib.reload — shared with run_with_reload rather than kept as a second
# private copy here. apple's dev loops own their own loop and poll the flag;
# see admin_lib/reload.py for why both shapes sit on one piece of state.
from .reload import (
    cleanup_pid_file,
    clear_rebuild_request,
    is_rebuild_requested,
    poll_for_reload,
    setup_reload,
)
from .streams import stop_stream, stream_subprocess


# ---------------------------------------------------------------------------
# Xcode build helpers
# ---------------------------------------------------------------------------

_RAW_BUILD_LOG_NAME = "admin-xcode-build.log"


def _raw_build_log_path():
    """Path to the raw xcodebuild log, under this project's own tmp/ dir.

    Keyed on the project (via get_log_dir(), which is <project_root>/tmp by
    default) rather than one fixed /tmp path shared by the whole machine —
    two builds running from different worktrees/clones of the same project
    no longer overwrite each other's raw log (admin-project-tool#447).
    """
    return os.path.join(get_log_dir(), _RAW_BUILD_LOG_NAME)


# ---------------------------------------------------------------------------
# Xcode build lock — admin-project-tool#450
# ---------------------------------------------------------------------------

_XCODE_LOCK_NAME = "admin-xcode-build.lock"


def _xcode_lock_path():
    """Path to the Xcode build lock, under this project's own tmp/ dir.

    Keyed on the project (via get_log_dir()) exactly like
    _raw_build_log_path() is (admin-project-tool#447), so two
    worktrees/clones building different branches never contend with each
    other. Only two builds in the *same* checkout do.
    """
    return os.path.join(get_log_dir(), _XCODE_LOCK_NAME)


def _acquire_xcode_lock(description):
    """Take the exclusive per-checkout Xcode build lock, or refuse.

    Two admin tasks against one checkout (`admin test apple` started while
    `admin dev mac` is building, say) can both invoke xcodebuild; the
    second silently queues behind Xcode's own build lock and just looks
    like a slow build. Refusing outright beats a multi-minute wait that
    nothing announces.

    Returns an open file handle holding the lock — pass it to
    _release_xcode_lock() when the build is done — or None, after printing
    a refusal via err() naming the build already running.

    Stale locks need no cleanup: flock(2) ties the lock to this open file
    description, which the kernel drops once every fd referencing it is
    gone, including when the holder is killed. There is no pid file to go
    stale and nothing to delete by hand.
    """
    lock_path = _xcode_lock_path()
    os.makedirs(os.path.dirname(lock_path), exist_ok=True)
    fh = open(lock_path, "a+")
    try:
        fcntl.flock(fh.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        # LOCK_NB's contended case — another holder has it. A genuine I/O
        # failure (permission denied, a flaky mount) is a different OSError
        # subclass and should propagate rather than be misreported as
        # "another build is running".
        fh.seek(0)
        holder = fh.read().strip()
        fh.close()
        err(f"Refusing to start — {holder or 'another Xcode build'} is "
            "already running in this checkout.")
        return None
    except OSError:
        fh.close()
        raise
    fh.seek(0)
    fh.truncate()
    started = datetime.datetime.now().strftime("%H:%M:%S")
    fh.write(f"{description}, started at {started} (pid {os.getpid()})")
    fh.flush()
    return fh


def _release_xcode_lock(fh):
    """Release and close a lock handle from _acquire_xcode_lock(). No-op on None."""
    if fh is None:
        return
    try:
        fcntl.flock(fh.fileno(), fcntl.LOCK_UN)
    finally:
        fh.close()


def run_locked_xcode(cmd, description):
    """Run *cmd* — a shell command that itself invokes xcodebuild, e.g. a
    project's ``[apple] test_cmd`` — under the same per-checkout lock
    xcodebuild_filtered() takes, so a raw command configured in the
    manifest can't run alongside a build/dev/deploy invocation.

    Returns run_cmd(cmd)'s rc, or 1 without running cmd if another build
    already holds the lock (its refusal has already been printed).
    """
    # Exports XCODE_SPM_CLONE_DIR into this process's environment, which the
    # raw command inherits — see shared_spm_clone_dir() for the `${VAR:+...}`
    # snippet a test_cmd uses to pick it up.
    shared_spm_clone_dir()
    lock = _acquire_xcode_lock(description)
    if lock is None:
        return 1
    try:
        return run_cmd(cmd)
    finally:
        _release_xcode_lock(lock)


_SPM_CLONE_DIR = None


def shared_spm_clone_dir():
    """One SwiftPM clone directory per repository, shared by every git
    worktree of it. Returns the path, or None when sharing is off.

    Why this exists: `orchestrate` gives each swarm worker its own git
    worktree, and Xcode keys DerivedData by the project's absolute path, so
    every worktree builds a full tree of its own. On iptv-mac each tree is
    6.2-6.9 GB and 2.1 GB of that is SourcePackages — the same VLCKit
    checkout, cloned again per worktree. Thirteen of them filled a 926 GB
    disk to 6.1 GiB free on 2026-08-20 and killed an orchestration round
    with ENOSPC.

    Only the clone directory is shared, never DerivedData itself. Measured
    2026-08-20, two worktrees building the same scheme at the same moment:

      - one shared `-derivedDataPath`: the loser dies with `unable to attach
        DB: ... build.db: database is locked`, rc 65, and both write the same
        `Build/Products/<Config>/<app>` so the winner's binary silently
        replaces the loser's while the loser prints BUILD SUCCEEDED.
      - one shared `-clonedSourcePackagesDirPath`, DerivedData per worktree:
        both rc 0, each tree kept its own product. 2.1 GB once instead of
        per worktree, and the second worktree's cold build fell 55s -> 19s
        because the clone was already there.

    Per repository, not per machine: two projects pinning different versions
    of the same package sharing one clone directory was not measured, so it
    is not done. Repo identity comes from `git rev-parse --git-common-dir`,
    which every worktree of a repo answers identically.

    `XCODE_SPM_CLONE_DIR` in the environment overrides the path, and is also
    exported so a project's raw `test_cmd` can pass the flag itself:

        ${XCODE_SPM_CLONE_DIR:+-clonedSourcePackagesDirPath "$XCODE_SPM_CLONE_DIR"}

    `ADMIN_SHARED_SPM=0` turns the whole thing off.
    """
    global _SPM_CLONE_DIR
    if _SPM_CLONE_DIR is not None:
        return _SPM_CLONE_DIR or None

    if os.environ.get("ADMIN_SHARED_SPM") == "0":
        _SPM_CLONE_DIR = ""
        return None

    path = os.environ.get("XCODE_SPM_CLONE_DIR")
    if not path:
        root = get_project_root() or os.getcwd()
        result = subprocess.run(["git", "rev-parse", "--git-common-dir"],
                                cwd=root, capture_output=True, text=True)
        if result.returncode != 0:
            _SPM_CLONE_DIR = ""
            return None
        common = os.path.abspath(os.path.join(root, result.stdout.strip()))
        repo = os.path.basename(os.path.dirname(common))
        if not repo:
            _SPM_CLONE_DIR = ""
            return None
        path = os.path.expanduser(
            f"~/Library/Developer/Xcode/SharedSourcePackages/{repo}")

    os.makedirs(path, exist_ok=True)
    _SPM_CLONE_DIR = path
    os.environ["XCODE_SPM_CLONE_DIR"] = path
    return path


def _spm_clone_flag():
    """The `-clonedSourcePackagesDirPath` flag for xcodebuild, or None."""
    path = shared_spm_clone_dir()
    return f'-clonedSourcePackagesDirPath "{path}"' if path else None


def find_app(pattern):
    """Glob for a built .app bundle and return the newest match, or None."""
    matches = glob.glob(os.path.expanduser(pattern))
    return max(matches, key=os.path.getmtime) if matches else None


def target_build_dir(project, scheme, config, destination, extra_settings="",
                      allow_provisioning=True):
    """Ask xcodebuild for TARGET_BUILD_DIR: the exact directory the build for
    this scheme/config/destination just wrote into. Returns None if the
    query fails or the setting isn't in its output.

    Not a DerivedData glob: Xcode keys each DerivedData directory by the
    project's absolute path, so a scheme built from N different git
    worktrees accumulates N DerivedData directories under the same
    `<scheme>-*` prefix — including ones for worktrees that have since been
    deleted. Globbing across all of them and picking the newest by mtime can
    return a stale sibling's app instead of the one this build just produced
    (admin-project-tool#448). Asking xcodebuild directly for the setting it
    used names the one true directory, no search required.
    """
    parts = ["xcodebuild"]
    if project:
        flag = "-workspace" if project.endswith(".xcworkspace") else "-project"
        parts.append(f'{flag} "{project}"')
    parts.append(f'-scheme "{scheme}"')
    parts.append(f'-configuration {config}')
    parts.append(f'-destination "{destination}"')
    parts.append("-showBuildSettings")
    # Same flag the build used, or this query resolves against a different
    # package graph than the one that produced the app.
    spm = _spm_clone_flag()
    if spm:
        parts.append(spm)
    if extra_settings:
        parts.append(extra_settings)
    if allow_provisioning:
        parts.append("-allowProvisioningUpdates")
        parts.extend(app_store_connect_auth_flags())

    result = subprocess.run(" ".join(parts), shell=True, capture_output=True, text=True)
    if result.returncode != 0:
        return None
    for line in result.stdout.splitlines():
        key, sep, value = line.strip().partition(" = ")
        if sep and key == "TARGET_BUILD_DIR":
            return value.strip() or None
    return None


def xcode_is_signed_out(build_log_text):
    """True when this build failed because Xcode has no Apple ID signed in.

    Detected from the build log rather than by inspecting Xcode's own state,
    because its preferences lie about this: the account row survives in
    `com.apple.dt.Xcode`'s DVTDeveloperAccountManagerAppleIDLists while the
    credential it points at is gone from the keychain, so "an account is
    listed" and "an account is usable" are different facts and only the second
    one matters. xcodebuild says so plainly when it matters; nothing else does.

    The signing-out itself is not rare — on this setup it has happened several
    times in a month — and what it costs is the confusing part: the build does
    not say "signed out", it says the *profile* is wrong, because xcodebuild
    falls back to the cached wildcard team profile and then reports whatever
    capability that profile happens to lack. Chasing the capability is chasing
    a symptom.

    One honest limit: "requires a provisioning profile with the <X> feature"
    has two causes, and the log cannot tell them apart — nobody signed in, or
    the App ID genuinely lacks that capability. So this returns True for both
    and the message names both, in the order they are worth checking. Being
    told to look at Accounts when the real problem is a capability costs a
    glance; not being told anything costs an afternoon, which is what it cost
    the day this was written.
    """
    if "No Accounts: Add a new account" in build_log_text:
        return True
    # What a signed-out machine actually prints, most of the time. xcodebuild
    # only says "No Accounts" when it gets as far as trying to make a profile;
    # short of that it reports the shortfall in whatever profile it fell back
    # to. Both shapes were produced by the same machine, minutes apart, for the
    # same cause.
    if "requires a provisioning profile with the" in build_log_text:
        return True
    return (
        'Provisioning profile "iOS Team Provisioning Profile: *"' in build_log_text
        or 'Provisioning profile "Mac Team Provisioning Profile: *"' in build_log_text
    ) and "doesn't include" in build_log_text


def app_store_connect_key_rejected(build_log_text):
    """True when xcodebuild refused the API key itself.

    Distinct from xcode_is_signed_out(), and the two look identical downstream:
    both end at the wildcard team profile reporting a missing capability. The
    difference is upstream and this one line is the only place it shows:

        error: Authentication failed: Make sure a bearer token was provided,
        it is properly configured and signed, and it has not expired.

    Worth separating because the remedies are opposites. Signed-out means add
    an account. This means the account (if any) is being *ignored*, because
    -authenticationKey* takes precedence over it — so passing the key turns a
    machine that could have signed into one that cannot.

    Observed 2026-08-21 on Xcode 26.6: a key that creates provisioning profiles
    over the App Store Connect API without complaint is rejected here. Same key,
    same issuer, 18ms of clock skew, Team key not Individual. Whatever the cause,
    it is not something a caller can fix by configuring differently, so the only
    useful response is to stop passing the key and let the account work.
    """
    return "Authentication failed: Make sure a bearer token was provided" in build_log_text


_signed_out_reported = False


def _report_signed_out():
    """Say what happened and what to click. Kept separate from the retry so a
    non-interactive run still gets the explanation.

    Said once per run. The retry path reports before it offers, and the
    caller's diagnose_build_failure() reports again on the way out — twelve
    lines of identical advice twice over reads like two different problems.
    """
    global _signed_out_reported
    if _signed_out_reported:
        return
    _signed_out_reported = True
    warn("This build could not get a provisioning profile carrying the capability it")
    warn("asked for. Two things cause that, and the log cannot tell them apart:")
    warn("")
    warn("  1. Xcode has no Apple ID signed in — by far the more common one, and it")
    warn("     does not say so: it falls back to the cached wildcard team profile and")
    warn("     then reports whatever that profile lacks.")
    warn("       Xcode ▸ Settings (⌘,) ▸ Accounts ▸ + ▸ Apple ID ▸ sign in")
    warn("")
    warn("  2. The App ID really is missing the capability.")
    warn("       developer.apple.com/account ▸ Identifiers ▸ <your bundle id>")
    warn("")
    warn("Check 1 first. Neither needs a project setting changed or a profile")
    warn("downloaded by hand.")


def diagnose_build_failure():
    """After a failed xcodebuild, check for known fixable errors and print remediation."""
    try:
        with open(_raw_build_log_path(), "r", errors="replace") as f:
            content = f.read()
    except FileNotFoundError:
        return

    if "errSecInternalComponent" in content:
        warn("Codesign failed: login keychain is locked.")
        warn("Unlock it with:")
        warn("  security unlock-keychain ~/Library/Keychains/login.keychain-db")
        warn("  security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k YOUR_PASSWORD ~/Library/Keychains/login.keychain-db")

    if xcode_is_signed_out(content):
        _report_signed_out()


_asc_key_rejected = False


def app_store_connect_auth_flags():
    """The -authenticationKey* flags when an App Store Connect API key is
    configured, or [] when one is not — and [] for the rest of the run once a
    build has shown xcodebuild rejecting the key (see
    app_store_connect_key_rejected).

    Why this exists: without it, every signed build depends on an Apple ID
    being signed into Xcode's Accounts pane, and that login does not survive.
    On this setup it vanished several times in a month, each time surfacing as
    a provisioning-profile error that names a capability rather than an
    account. With a key on disk, xcodebuild authenticates against the Apple
    Developer website itself and the Xcode account stops mattering — including
    for creating profiles under -allowProvisioningUpdates.

    Read from the environment rather than a config table, because the key is a
    credential: the path can sit in a machine's own .env while every project's
    committed admin.toml stays free of it.

        APP_STORE_CONNECT_KEY_PATH    path to AuthKey_<KEYID>.p8
        APP_STORE_CONNECT_KEY_ID      the <KEYID> from that filename
        APP_STORE_CONNECT_ISSUER_ID   the UUID from App Store Connect ▸
                                      Users and Access ▸ Integrations

    All three or nothing: xcodebuild rejects a partial set, and a build that
    fails on a half-configured credential is worse than one that quietly does
    what it did before.
    """
    if _asc_key_rejected:
        return []

    path = os.environ.get("APP_STORE_CONNECT_KEY_PATH", "").strip()
    key_id = os.environ.get("APP_STORE_CONNECT_KEY_ID", "").strip()
    issuer = os.environ.get("APP_STORE_CONNECT_ISSUER_ID", "").strip()
    if not (path and key_id and issuer):
        return []

    expanded = os.path.expanduser(os.path.expandvars(path))
    if not os.path.isfile(expanded):
        warn(f"APP_STORE_CONNECT_KEY_PATH points at {expanded}, which is not there —")
        warn("falling back to whatever account Xcode has.")
        return []

    return [
        f'-authenticationKeyPath "{expanded}"',
        f'-authenticationKeyID "{key_id}"',
        f'-authenticationKeyIssuerID "{issuer}"',
    ]


def xcodebuild_filtered(project, scheme, config, destination, action="build",
                         extra_settings="", allow_provisioning=True):
    """Run xcodebuild with grep-filtered output. Raw log tee'd to this
    project's own tmp/admin-xcode-build.log (see _raw_build_log_path()),
    prefixed with the project root and a timestamp so a log that somehow
    still ends up stale or foreign identifies itself. Returns rc.

    Holds the per-checkout Xcode build lock for the duration of the build,
    and refuses outright — naming the build already running — if another
    one in this checkout holds it (see _acquire_xcode_lock()).

    Warning: extra_settings applies globally (incl. SwiftPM bundles). Only
    PRODUCT_BUNDLE_IDENTIFIER is SwiftPM-safe; PRODUCT_NAME and
    INFOPLIST_KEY_CFBundleDisplayName cause "Multiple commands produce"
    failures. Use dev_macos_postprocess() for product/display-name diffs.
    """
    from .core import run_cmd

    parts = [f'xcodebuild']
    if project:
        flag = "-workspace" if project.endswith(".xcworkspace") else "-project"
        parts.append(f'{flag} "{project}"')
    parts.append(f'-scheme "{scheme}"')
    parts.append(f'-configuration {config}')
    parts.append(f'-destination "{destination}"')
    spm = _spm_clone_flag()
    if spm:
        parts.append(spm)
    parts.append(action)
    if extra_settings:
        parts.append(extra_settings)

    def _compose():
        """The command line as it stands now. Re-composed between attempts
        because app_store_connect_auth_flags() can go empty mid-run once the
        key has been rejected."""
        tail = list(parts)
        if allow_provisioning:
            tail.append("-allowProvisioningUpdates")
            tail.extend(app_store_connect_auth_flags())
        tail.append("2>&1")
        return " ".join(tail)

    build_cmd = _compose()
    description = f'xcodebuild {action} -scheme "{scheme}" -destination "{destination}"'
    lock = _acquire_xcode_lock(description)
    if lock is None:
        return 1
    try:
        log_path = _raw_build_log_path()
        os.makedirs(os.path.dirname(log_path), exist_ok=True)
        def _piped(cmd):
            return f'{cmd} | tee -a "{log_path}" | grep -E "BUILD|error:|warning:" ; exit ${{PIPESTATUS[0]}}'

        # Two attempts at most. The second happens for one of two reasons: a
        # person signed in between them, or the first attempt showed xcodebuild
        # refusing the API key and the retry drops it. The key case needs nobody
        # present, so it is checked first — an unattended build fixes itself.
        global _asc_key_rejected
        for attempt in (1, 2):
            now = datetime.datetime.now().strftime("%Y-%m-%d %H:%M:%S")
            with open(log_path, "w") as f:
                f.write(f"# {get_project_root()} @ {now}\n")
            rc = run_cmd(_piped(build_cmd))
            if rc == 0 or attempt == 2:
                return rc

            try:
                with open(log_path, "r", errors="replace") as f:
                    log_text = f.read()
            except FileNotFoundError:
                log_text = ""

            if app_store_connect_key_rejected(log_text) and not _asc_key_rejected:
                warn("xcodebuild rejected the App Store Connect API key:")
                warn("  Authentication failed: Make sure a bearer token was provided …")
                warn("The key is not the only way in, and passing it overrides the Apple ID")
                warn("signed into Xcode. Retrying without it, on the account instead.")
                _asc_key_rejected = True
                build_cmd = _compose()
                continue

            if not _offer_xcode_sign_in(log_path):
                return rc
    finally:
        _release_xcode_lock(lock)


def _offer_xcode_sign_in(log_path):
    """After a signed-out failure, stop and wait for the person to sign in.
    Returns True when they say they have and the build should be retried.

    **Only when someone is there.** A cron run, an agent's build, anything with
    no terminal on stdin gets the explanation and the failure — a prompt nobody
    can answer is a hang, and this is exactly the kind of guard that would
    otherwise turn an unattended nightly into a machine sitting on a question
    until morning.
    """
    try:
        with open(log_path, "r", errors="replace") as f:
            content = f.read()
    except FileNotFoundError:
        return False

    if not xcode_is_signed_out(content):
        return False

    _report_signed_out()

    if not sys.stdin.isatty():
        warn("Not a terminal, so not waiting — sign in and run this again.")
        return False

    try:
        answer = input("Signed in? Press Return to retry, or Ctrl-C to stop: ")
    except (EOFError, KeyboardInterrupt):
        print()
        return False
    return answer.strip().lower() not in {"n", "no", "q", "quit"}


def dev_macos_postprocess(built_app_path, dev_product_name, dev_display_name, sign_identity="-"):
    """Rename a Debug build .app → <dev_product_name>.app, patch
    CFBundleName and CFBundleDisplayName, re-sign. Returns the renamed path.

    WHY RENAME THE .app AT ALL (the Dock-name gotcha) — ADR-0007:
    The macOS Dock's hover tooltip and Finder show the app's **.app filename**,
    NOT CFBundleName or CFBundleDisplayName. So a dev build at `Macterm.app`
    shows "Macterm" in the Dock tooltip *even if* CFBundleName /
    CFBundleDisplayName are "Macterm Dev" (those only drive the menu-bar app
    menu and some in-app surfaces). The ONLY way to make the Dock say
    "Macterm Dev" is to rename the bundle file to `Macterm Dev.app`. That's what
    this does. Enable it per project with `[apple] mac_dev_product_name`
    (mac_dev_rename defaults to True). Keep PRODUCT_NAME stable ("Macterm") so
    the .app the build produces — and anything that references it by name, e.g.
    a test target's TEST_HOST — is unaffected; the rename happens post-build,
    just before launch.

    The full name picture for a dev variant:
      • Dock hover tooltip / Finder  → the .app filename   (THIS rename)
      • Menu-bar app menu            → CFBundleName         (patched here)
      • Some Finder/system surfaces  → CFBundleDisplayName  (patched here)
      • In-app strings (window title, welcome screen, etc.) → hardcoded in the
        app unless they read Bundle.main's CFBundleDisplayName; make them read it
        so the dev variant labels itself everywhere.

    Use this instead of PRODUCT_NAME / INFOPLIST_KEY_CFBundleDisplayName
    xcodebuild flags (those break SwiftPM bundles). Pair with a distinct
    PRODUCT_BUNDLE_IDENTIFIER passed via xcodebuild_filtered.extra_settings.

    sign_identity defaults to "-" (ad-hoc). Pass a real identity (hash or name)
    so the rename re-signs with the *same* stable identity the build used —
    otherwise the ad-hoc re-sign throws away the stable signature and macOS
    re-prompts for TCC permissions on every rebuild.
    """
    if not built_app_path.endswith(".app") or not os.path.isdir(built_app_path):
        err(f"dev_macos_postprocess: not an .app bundle: {built_app_path!r}")
        sys.exit(1)
    if not os.path.isfile(os.path.join(built_app_path, "Contents", "Info.plist")):
        err(f"dev_macos_postprocess: missing Info.plist in {built_app_path!r}")
        sys.exit(1)

    dest = os.path.join(
        os.path.dirname(built_app_path), f"{dev_product_name}.app"
    )
    if os.path.exists(dest):
        shutil.rmtree(dest)
    os.rename(built_app_path, dest)

    plist_path = os.path.join(dest, "Contents", "Info.plist")
    r = subprocess.run(
        ["plutil", "-replace", "CFBundleDisplayName",
         "-string", dev_display_name, plist_path],
        capture_output=True,
    )
    if r.returncode != 0:
        warn(f"plutil failed to patch CFBundleDisplayName: "
             f"{r.stderr.decode('utf-8', 'replace').strip()}")

    # CFBundleName is what the menu bar at the top of the screen reads, and it
    # is patched here rather than left to project.yml because a project using
    # GENERATE_INFOPLIST_FILE = YES cannot set it there at all: Xcode's
    # generated Info.plist derives CFBundleName from PRODUCT_NAME, and there is
    # no INFOPLIST_KEY_CFBundleName to override it with. PRODUCT_NAME has to
    # stay put (TEST_HOST and the built .app name depend on it), so the dev app
    # ended up announcing itself as the production app in the one surface you
    # look at while driving it. Patching it post-build, next to the display
    # name, is the only place both spellings can be made to agree.
    r = subprocess.run(
        ["plutil", "-replace", "CFBundleName",
         "-string", dev_product_name, plist_path],
        capture_output=True,
    )
    if r.returncode != 0:
        warn(f"plutil failed to patch CFBundleName: "
             f"{r.stderr.decode('utf-8', 'replace').strip()}")

    r = subprocess.run(
        ["codesign", "--force", "--sign", sign_identity, dest],
        capture_output=True,
    )
    if r.returncode != 0:
        label = "ad-hoc" if sign_identity == "-" else f"identity {sign_identity}"
        warn(f"codesign ({label}) failed: "
             f"{r.stderr.decode('utf-8', 'replace').strip()}")

    return dest


def clean_derived_data(project_name):
    """Remove DerivedData for a specific project."""
    pattern = os.path.expanduser(f"~/Library/Developer/Xcode/DerivedData/{project_name}-*")
    for d in glob.glob(pattern):
        subprocess.run(["rm", "-rf", d])


def clean_ios(cfg):
    """Remove Xcode DerivedData for the project in cfg.

    DerivedData is per-project, not per-target, so any configured scheme names
    the same tree — the fallback chain only exists to find one that is set.
    """
    scheme = cfg.get("ios_scheme") or cfg.get("mac_scheme") or cfg.get("tv_scheme")
    if not scheme:
        err("apple.ios_scheme, apple.mac_scheme or apple.tv_scheme required for clean")
        sys.exit(1)
    clean_derived_data(_resolve_derived_data_name(cfg) or scheme)


# ---------------------------------------------------------------------------
# Log predicates
# ---------------------------------------------------------------------------

_LOG_LEVEL_PREDICATES = {
    "app": lambda process, subsystem: f"subsystem == '{subsystem}'",
    "debug": lambda process, subsystem: (
        f"(subsystem == '{subsystem}' OR process == '{process}') "
        f"AND NOT (subsystem BEGINSWITH 'com.apple.launchservices') "
        f"AND NOT (subsystem BEGINSWITH 'com.apple.runningboard')"
    ),
    "system": lambda process, subsystem: f"process == '{process}' AND subsystem != '{subsystem}'",
    "verbose": lambda process, subsystem: f"process == '{process}'",
}


def log_predicate(process, subsystem, level="app"):
    """Return a macOS `log stream` predicate string for the given level.

    `level` must be a key of _LOG_LEVEL_PREDICATES (app, debug, system,
    verbose) — that dict is the single source of truth for the accepted
    set, so the rejection message below can never drift from it.
    """
    build = _LOG_LEVEL_PREDICATES.get(level)
    if build is None:
        expected = ", ".join(_LOG_LEVEL_PREDICATES)
        err(f"Unknown log level: {level} (expected one of: {expected})")
        sys.exit(1)
    return build(process, subsystem)


# ---------------------------------------------------------------------------
# Simulator helpers (iOS and tvOS)
# ---------------------------------------------------------------------------

# Everything that differs between one simulator platform and another, in one
# place. Before this table each value was a string literal buried in a different
# function — the device filter in resolve_simulator, the destination in the dev
# loop, the SDK suffix in the DerivedData glob, the key prefix in every
# _resolve_* helper — so adding a platform meant finding four unrelated lines
# and hoping there was not a fifth. Adding visionOS or watchOS is now a row.
#
# device_match is a substring of the simulator's name as `simctl list devices`
# prints it ("iPhone 17 Pro", "Apple TV 4K (3rd generation)"), and runtime_match
# a substring of the runtime header above it ("-- iOS 26.5 --",
# "-- tvOS 26.5 --"). Both are needed: the runtime header is what separates an
# Apple TV simulator from an iPhone one when only the booted list is being read,
# where there are no headers to group by.
SIM_PLATFORMS = {
    "ios": {
        "label":         "iOS",
        "destination":   "iOS Simulator",
        "sdk_suffix":    "-iphonesimulator",
        "device_match":  "iPhone",
        "device_env":    "IOS_DEVICE",
        "device_key":    "ios_device",
    },
    "tv": {
        "label":         "tvOS",
        "destination":   "tvOS Simulator",
        "sdk_suffix":    "-appletvsimulator",
        "device_match":  "Apple TV",
        "device_env":    "TV_DEVICE",
        "device_key":    "tv_device",
    },
}


# The physical-device counterpart of SIM_PLATFORMS: everything that differs
# between installing on an iPhone and installing on an Apple TV. `platform` is
# the value `xcrun devicectl list devices` reports under
# hardwareProperties.platform, and is what keeps a paired Apple TV from being
# picked as the target of `deploy ios` (both are CoreDevices in the same list).
DEVICE_PLATFORMS = {
    "ios": {
        "label":       "iOS",
        "destination": "generic/platform=iOS",
        "sdk_suffix":  "-iphoneos",
        "platform":    "iOS",
        # An iPhone and an iPad both report platform "iOS", so the platform
        # filter alone still leaves `deploy ios` taking whichever devicectl
        # happens to list first. `device_types` is the tie-break, and
        # `ios_device_type` in [apple] (or IOS_DEVICE_TYPE) picks one.
        "device_types": ("iPhone", "iPad"),
        "connect_hint": (
            "Connect via USB, or enable wireless debugging:",
            "  Xcode > Window > Devices and Simulators > select device > Connect via network",
        ),
    },
    "tv": {
        "label":       "tvOS",
        "destination": "generic/platform=tvOS",
        "sdk_suffix":  "-appletvos",
        "platform":    "tvOS",
        "device_types": ("appleTV",),
        # An Apple TV has no port to plug into — pairing is network-only and
        # one-time, and until it is done devicectl does not list the device at
        # all, which reads as "deploy is broken" rather than "not paired yet".
        "connect_hint": (
            "Pair the Apple TV once, over the network:",
            "  Apple TV: Settings > Remotes and Devices > Remote App and Devices (leave it open)",
            "  Mac: Xcode > Window > Devices and Simulators > select the Apple TV > enter the PIN",
        ),
    },
}


def _device_platform(platform):
    """Look up a DEVICE_PLATFORMS row, failing loudly on an unknown key."""
    try:
        return DEVICE_PLATFORMS[platform]
    except KeyError:
        err(f"unknown device platform {platform!r} — expected one of "
            + ", ".join(sorted(DEVICE_PLATFORMS)))
        sys.exit(1)


# Every Apple platform this archetype can drive, in menu order, each keyed by
# the [apple] scheme key a project must set for that platform to exist at all.
#
# A project declares the platforms it has; the archetype never assumes one. An
# iOS-only manifest offering a `mac` target is how `admin deploy` came to fail
# with "apple.mac_scheme required" on a project that has no macOS target and
# never claimed to (admin-project-tool: mac/ios were hardcoded into the build,
# dev and deploy pickers while `tv` was correctly gated on tv_scheme).
APPLE_PLATFORMS = {
    "mac": {"scheme_key": "mac_scheme", "aliases": ("mac", "macos")},
    "ios": {"scheme_key": "ios_scheme", "aliases": ("ios", "iphone", "simulator")},
    "tv":  {"scheme_key": "tv_scheme",  "aliases": ("tv", "tvos", "appletv")},
}


def apple_platforms(cfg, only=None):
    """Return the platform keys this project declares a scheme for, menu-ordered.

    `only` restricts the answer to platforms a given command can actually
    handle, so a command never offers — or accepts — a target it cannot run.
    """
    cfg = cfg or {}
    return [p for p, meta in APPLE_PLATFORMS.items()
            if cfg.get(meta["scheme_key"]) and (only is None or p in only)]


def only_target(choices, cmd_name):
    """Return the sole target when a project has exactly one, else None.

    A one-row picker asks a question with a single possible answer. `test`
    already skipped it; build/dev/deploy do the same through this. It says which
    target it took, so a run with no prompt is never mistaken for a picker that
    failed to appear.
    """
    if len(choices) != 1:
        return None
    target = next(iter(choices))
    info(cmd_name + ": " + target + " is this project's only target")
    return target


def resolve_apple_platform(arg):
    """Map a CLI alias ('macos', 'iphone', 'appletv', …) to a platform key, or None."""
    a = str(arg).lower()
    for p, meta in APPLE_PLATFORMS.items():
        if a in meta["aliases"]:
            return p
    return None


def _sim_platform(platform):
    """Look up a SIM_PLATFORMS row, failing loudly on an unknown key."""
    try:
        return SIM_PLATFORMS[platform]
    except KeyError:
        err(f"unknown simulator platform {platform!r} — expected one of "
            + ", ".join(sorted(SIM_PLATFORMS)))
        sys.exit(1)


_GLOBAL_ADMIN_CONFIG_CACHE = None
_GLOBAL_ADMIN_CONFIG_LOADED = False


def _global_admin_config():
    """Load ~/.admin/admin.toml once and return it as a dict. Returns {} on any error."""
    global _GLOBAL_ADMIN_CONFIG_CACHE, _GLOBAL_ADMIN_CONFIG_LOADED
    if _GLOBAL_ADMIN_CONFIG_LOADED:
        return _GLOBAL_ADMIN_CONFIG_CACHE or {}
    _GLOBAL_ADMIN_CONFIG_LOADED = True
    try:
        import tomllib
    except ImportError:
        try:
            import tomli as tomllib  # type: ignore[no-redef]
        except ImportError:
            _GLOBAL_ADMIN_CONFIG_CACHE = {}
            return {}
    path = os.path.expanduser("~/.admin/admin.toml")
    if not os.path.isfile(path):
        _GLOBAL_ADMIN_CONFIG_CACHE = {}
        return {}
    try:
        with open(path, "rb") as f:
            _GLOBAL_ADMIN_CONFIG_CACHE = tomllib.load(f)
    except Exception:
        _GLOBAL_ADMIN_CONFIG_CACHE = {}
    return _GLOBAL_ADMIN_CONFIG_CACHE


def _with_global_defaults(cfg, section):
    """Return cfg merged with ~/.admin/admin.toml [section] as defaults.

    Project values win; the global file fills in any missing keys.
    """
    global_section = _global_admin_config().get(section) or {}
    if not global_section:
        return cfg
    return {**global_section, **cfg}


def find_simulator(device_name):
    """Find a simulator device ID by exact name. Prefers booted. Returns device_id or None."""
    import re
    result = subprocess.run(
        ["xcrun", "simctl", "list", "devices", "available"],
        capture_output=True, text=True,
    )
    pattern = re.compile(r'^\s+' + re.escape(device_name) + r'\s+\(([A-F0-9-]{36})\)')
    first_match = None
    for line in result.stdout.splitlines():
        m = pattern.match(line)
        if m:
            did = m.group(1)
            if "Booted" in line:
                return did          # booted instance wins immediately
            if first_match is None:
                first_match = did
    return first_match


def resolve_simulator(platform="ios"):
    """Pick the best simulator for a platform: already-booted first, then the
    newest available device of that kind.

    Returns (device_id, device_name) or calls sys.exit(1).

    The booted pass filters by platform too, which it did not have to when iOS
    was the only platform. A booted iPhone is not a usable answer to "give me an
    Apple TV" — installing a tvOS app on it fails with a message about the
    wrong architecture, several steps away from the wrong choice made here.
    """
    import re

    plat = _sim_platform(platform)
    runtime_match = plat["label"]
    device_match = plat["device_match"]

    # 1. An already-booted simulator of this platform. `simctl list devices
    #    booted` groups by runtime the same way the full listing does, so the
    #    header is what says which platform a booted device belongs to.
    result = subprocess.run(
        ["xcrun", "simctl", "list", "devices", "booted"],
        capture_output=True, text=True,
    )
    current_runtime = None
    for line in result.stdout.splitlines():
        runtime_m = re.match(r'^-- (.+) --$', line)
        if runtime_m:
            current_runtime = runtime_m.group(1)
            continue
        m = re.search(r'^\s+(.+?)\s+\(([A-F0-9-]{36})\)\s+\(Booted\)', line)
        if m and current_runtime and current_runtime.startswith(runtime_match):
            name, did = m.group(1), m.group(2)
            info(f"Using booted simulator: {name}")
            return did, name

    # 2. None booted — parse all available devices of this platform, pick
    #    newest OS + newest model.
    result = subprocess.run(
        ["xcrun", "simctl", "list", "devices", "available"],
        capture_output=True, text=True,
    )
    current_runtime = None
    candidates = []
    for line in result.stdout.splitlines():
        runtime_m = re.match(r'^-- (.+) --$', line)
        if runtime_m:
            current_runtime = runtime_m.group(1)
            continue
        device_m = re.search(r'^\s+(.+?)\s+\(([A-F0-9-]{36})\)\s+\(Shutdown\)', line)
        if device_m and current_runtime and current_runtime.startswith(runtime_match):
            name = device_m.group(1)
            did = device_m.group(2)
            if device_match in name:
                candidates.append((current_runtime, name, did))

    if not candidates:
        err(f"No {device_match} simulators found.")
        if platform != "ios":
            err(f"Install the runtime with: xcodebuild -downloadPlatform {plat['label']}")
        sys.exit(1)

    # candidates are already in order from simctl output: grouped by runtime
    # (oldest first), devices within each runtime in Apple's default order.
    # Take the last one = newest runtime, last device in that group.
    best = candidates[-1]
    name = best[1]
    did = best[2]
    info(f"Using simulator: {name} ({best[0]})")
    return did, name


def boot_simulator(device_id):
    """Boot a simulator and open Simulator.app.

    Returns once `simctl boot` has been issued — the device is still booting.
    Use ensure_booted_simulator() when the caller needs a usable device.
    """
    subprocess.run(["xcrun", "simctl", "boot", device_id], capture_output=True)
    subprocess.run(["open", "-a", "Simulator"])


def ensure_booted_simulator(platform="ios"):
    """Resolve a simulator, boot it if needed, and wait until it finishes booting.

    Returns (device_id, device_name). Callers that install/launch on the returned
    id can do so immediately — `simctl bootstatus -b` boots the device when it
    isn't already and blocks until boot completes, so there is no race.
    """
    device_id, name = resolve_simulator(platform)
    subprocess.run(["open", "-a", "Simulator"])
    subprocess.run(["xcrun", "simctl", "bootstatus", device_id, "-b"])
    return device_id, name


def app_bundle_identifier(app_path):
    """Read CFBundleIdentifier from a built .app, or return None."""
    info_plist = os.path.join(app_path, "Info.plist")
    if not os.path.isfile(info_plist):
        return None

    r = subprocess.run(
        ["/usr/libexec/PlistBuddy", "-c", "Print :CFBundleIdentifier", info_plist],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        return None

    bundle_id = r.stdout.strip()
    return bundle_id or None


def install_and_launch_sim(device_id, app_path, bundle_id, launch_args=None):
    """Install and launch an app on a simulator.

    launch_args: optional list of strings appended to `xcrun simctl launch`,
    delivered to the app as UIApplication launch arguments.
    """
    subprocess.run(["xcrun", "simctl", "install", device_id, app_path])
    cmd = ["xcrun", "simctl", "launch", device_id, bundle_id]
    if launch_args:
        cmd.extend(launch_args)
    subprocess.run(cmd)


def terminate_sim_app(device_id, bundle_id):
    """Terminate an app on a simulator."""
    subprocess.run(["xcrun", "simctl", "terminate", device_id, bundle_id],
                    capture_output=True)


def is_sim_app_running(device_id, bundle_id):
    """Check if an app is running in a simulator."""
    r = subprocess.run(
        ["xcrun", "simctl", "spawn", device_id, "launchctl", "list"],
        capture_output=True, text=True,
    )
    return bundle_id in r.stdout


# ---------------------------------------------------------------------------
# Physical device helpers
# ---------------------------------------------------------------------------

_DEVICE_LOG_ENV = {"OS_ACTIVITY_DT_MODE": "YES"}

def _list_devices():
    """Query xcrun devicectl for connected devices.

    Returns list of device dicts from the JSON output, or empty list.
    """
    import json

    log_dir = get_log_dir()
    json_path = os.path.join(log_dir, "devices.json")
    os.makedirs(log_dir, exist_ok=True)
    result = subprocess.run(
        ["xcrun", "devicectl", "list", "devices", "--json-output", json_path],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        return []

    try:
        with open(json_path) as f:
            data = json.load(f)
        return [
            d for d in data.get("result", {}).get("devices", [])
            if d.get("connectionProperties", {}).get("transportType") in ("wired", "localNetwork")
        ]
    except (json.JSONDecodeError, KeyError, FileNotFoundError):
        return []


def _extract_device_info(device):
    """Extract (device_id, device_name, udid, transport) from a device dict."""
    device_id = device.get("identifier", "")
    device_name = device.get("deviceProperties", {}).get("name", "Unknown")
    udid = device.get("hardwareProperties", {}).get("udid", "")
    transport = device.get("connectionProperties", {}).get("transportType", "")
    return device_id, device_name, udid, transport


def find_connected_device(platform="ios", cfg=None):
    """Find a connected device of the given platform via xcrun devicectl.

    Returns (device_id, device_name, udid) or calls sys.exit(1).
    device_id is the CoreDevice identifier (for devicectl commands).
    udid is the hardware UDID.
    Accepts any connection type (USB or network).

    devicectl lists every paired CoreDevice together, so an Apple TV and an
    iPhone show up in the same array — the platform filter is what stops
    `deploy ios` handing an iPhone build to the Apple TV that happened to be
    listed first.

    An iPhone and an iPad are *both* platform "iOS", so with both connected the
    platform filter still leaves a coin toss. `[apple] ios_device_type` (or the
    IOS_DEVICE_TYPE env var) names one — "iPhone" or "iPad" — and with more than
    one candidate left the choice is printed rather than made silently.
    """
    plat = _device_platform(platform)
    cfg = cfg or {}
    info(f"Finding connected {plat['label']} device...")
    devices = [d for d in _list_devices()
               if d.get("hardwareProperties", {}).get("platform") == plat["platform"]]

    wanted = (os.environ.get(f"{platform.upper()}_DEVICE_TYPE")
              or cfg.get(f"{platform}_device_type"))
    if wanted and devices:
        matching = [d for d in devices
                    if d.get("hardwareProperties", {}).get("deviceType", "").lower() == wanted.lower()]
        if matching:
            devices = matching
        else:
            warn(f"no connected {wanted} — falling back to any {plat['label']} device")

    if not devices:
        err(f"No connected {plat['label']} device found.")
        for line in plat["connect_hint"]:
            err(line)
        sys.exit(1)

    if len(devices) > 1:
        names = ", ".join(
            f"{d.get('deviceProperties', {}).get('name', '?')} "
            f"({d.get('hardwareProperties', {}).get('deviceType', '?')})"
            for d in devices
        )
        warn(f"{len(devices)} {plat['label']} devices connected: {names}")
        warn(f"Using the first. Pin one with {platform}_device_type in [apple] "
             f"or {platform.upper()}_DEVICE_TYPE.")

    device_id, device_name, udid, _ = _extract_device_info(devices[0])
    info(f"Target device: {device_name} ({device_id})")
    return device_id, device_name, udid


def require_usb_device():
    """Find a connected iOS device that is visible over USB.

    Returns (device_id, device_name, udid) or calls sys.exit(1).
    Uses idevice_id (libimobiledevice) to verify USB visibility, since
    devicectl's transportType can report 'localNetwork' even when USB
    is physically connected.
    """
    import shutil

    info("Finding USB-connected iOS device...")

    if not shutil.which("idevice_id"):
        err("idevice_id not found — install with: brew install libimobiledevice")
        sys.exit(1)

    result = subprocess.run(["idevice_id", "-l"], capture_output=True, text=True)
    usb_udids = [u.strip() for u in result.stdout.strip().splitlines() if u.strip()]

    if not usb_udids:
        devices = _list_devices()
        if devices:
            device_name = devices[0].get("deviceProperties", {}).get("name", "Unknown")
            err(f"{device_name} is visible to Xcode but not over USB.")
        else:
            err("No iOS device found.")
        err("Device not visible over USB.")
        err("Plug in your device via USB cable and try again.")
        sys.exit(1)

    target_udid = usb_udids[0]

    devices = _list_devices()
    for d in devices:
        _id, _name, _udid, _transport = _extract_device_info(d)
        if _udid == target_udid:
            info(f"Target device: {_name} (USB)")
            return _id, _name, _udid

    info(f"Target device: UDID {target_udid} (USB)")
    return "", "Unknown", target_udid


def install_on_device(device_id, app_path):
    """Install an app on a physical iOS device. Returns exit code."""
    from .core import run_cmd
    return run_cmd(
        f'xcrun devicectl device install app --device "{device_id}" "{app_path}" 2>&1'
    )


def launch_on_device(device_id, bundle_id, console=False, env=None):
    """Launch an app on a physical iOS device.

    console: if True, uses --console to attach stdout/stderr and blocks until
             the app exits. Returns the Popen process instead of an exit code.
    env:     dict of environment variables to pass to the app (via -e JSON).
    Returns exit code (console=False) or Popen process (console=True).
    """
    import json as _json

    cmd = ["xcrun", "devicectl", "device", "process", "launch",
           "--terminate-existing", "--device", device_id]
    if console:
        cmd.append("--console")
    if env:
        cmd.extend(["-e", _json.dumps(env)])
    cmd.append(bundle_id)

    if console:
        proc = subprocess.Popen(
            cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            bufsize=1, universal_newlines=True,
        )
        _children.append(proc)
        return proc

    result = subprocess.run(cmd, capture_output=True, text=True)
    return result.returncode


def _get_device_process_for_bundle(device_id, bundle_id):
    """Query running processes on a device and return the process dict for bundle_id, or None.

    Silently returns None if devicectl fails or bundle is not found.
    """
    import json

    log_dir = get_log_dir()
    json_path = os.path.join(log_dir, "processes.json")
    os.makedirs(log_dir, exist_ok=True)
    result = subprocess.run(
        ["xcrun", "devicectl", "device", "info", "processes",
         "--device", device_id, "--json-output", json_path],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        return None

    try:
        with open(json_path) as f:
            data = json.load(f)
        for proc in data.get("result", {}).get("runningProcesses", []):
            if proc.get("bundleIdentifier") == bundle_id:
                return proc
    except (json.JSONDecodeError, KeyError, FileNotFoundError):
        pass
    return None


def terminate_device_app(device_id, bundle_id):
    """Terminate an app on a physical iOS device by bundle ID.

    Silently succeeds if the app isn't running.
    """
    proc = _get_device_process_for_bundle(device_id, bundle_id)
    if proc:
        pid = proc.get("processIdentifier")
        if pid:
            subprocess.run(
                ["xcrun", "devicectl", "device", "process", "terminate",
                 "--device", device_id, "--pid", str(pid)],
                capture_output=True, text=True,
            )


def is_device_app_running(device_id, bundle_id):
    """Check if an app is running on a physical iOS device."""
    return _get_device_process_for_bundle(device_id, bundle_id) is not None


def device_log_attach(bundle_id, log_file=None, output_config=None):
    """Stream logs from a connected iOS device, blocking until Ctrl+C.

    Launches the app via devicectl --console with OS_ACTIVITY_DT_MODE to get
    non-redacted os_log output. Works with any build (dev or production).
    """
    device_id, device_name, _ = find_connected_device()

    info(f"Streaming logs from {device_name} (bundle: {bundle_id})")
    info("Press Ctrl+C to stop")
    print()

    console_proc = launch_on_device(
        device_id, bundle_id, console=True,
        env=_DEVICE_LOG_ENV)
    from .log_columns import make_column_formatter
    fmt = make_column_formatter("generic", output_config)
    if log_file is None:
        _log_dir = get_log_dir() or "tmp"
        os.makedirs(_log_dir, exist_ok=True)
        log_file = os.path.join(_log_dir, "console.log")
    _, log_fh = stream_subprocess(console_proc, log_file=log_file, formatter=fmt.format_line)

    try:
        console_proc.wait()
    except KeyboardInterrupt:
        pass
    finally:
        stop_stream(console_proc, log_fh)
        print()
        info("Log streaming stopped.")


# ---------------------------------------------------------------------------
# Reload command auto-registration
# ---------------------------------------------------------------------------

def stream_os_log(predicate, log_file=None, sim_id=None, formatter=None):
    """Start streaming macOS/simulator logs in a background thread.

    predicate:  log stream --predicate string
    log_file:   path to tee output to (defaults to tmp/os_log.log)
    sim_id:     if set, uses xcrun simctl spawn for simulator logs
    formatter:  optional callable(line) -> line for column formatting
    Returns (proc, thread, log_fh) — caller should call stop_stream().
    """
    if sim_id:
        cmd = ["xcrun", "simctl", "spawn", sim_id,
               "log", "stream", "--predicate", predicate,
               "--level", "debug", "--color", "always"]
    else:
        cmd = ["log", "stream", "--predicate", predicate,
               "--level", "debug", "--color", "always"]

    proc = subprocess.Popen(
        cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        bufsize=1, universal_newlines=True,
    )
    _children.append(proc)

    if log_file is None:
        _log_dir = get_log_dir() or "tmp"
        os.makedirs(_log_dir, exist_ok=True)
        log_file = os.path.join(_log_dir, "os_log.log")

    thread, log_fh = stream_subprocess(proc, log_file=log_file, formatter=formatter)
    return proc, thread, log_fh


def sweep_orphan_log_streams(subsystem):
    # If a prior admin dev session was hard-killed (terminal closed, SIGKILL,
    # Ctrl+\, OOM), its `log stream` child survives as an orphan parented to
    # launchd. _children + atexit can't clean those up; sweep by argv before
    # starting a fresh loop. See issue #353.
    if not subsystem:
        return
    import re
    pattern = f"log stream.*{re.escape(subsystem)}"
    subprocess.run(["pkill", "-f", pattern], capture_output=True)


def stream_sim_log_detached(sim_id, predicate, log_file, pid_file=None):
    """Stream a simulator's os_log to log_file from a detached process.

    Unlike stream_os_log(), nothing Python-side stays in the data path: the
    `log stream` child is a new session leader writing straight to the file, so
    it survives the caller exiting or os.execvp'ing into a supervisor. It is
    deliberately kept out of _children — the atexit handler must not reap it.

    Callers own the lifetime: pass pid_file and kill that pid on teardown, or
    sweep by predicate with sweep_orphan_log_streams(). If a live stream is
    already recorded in pid_file, returns its pid instead of starting a second.

    Returns the pid of the streaming process.
    """
    if pid_file and os.path.isfile(pid_file):
        try:
            with open(pid_file) as pf:
                existing = int(pf.read().strip())
            os.kill(existing, 0)
        except (ValueError, OSError):
            pass          # stale or unreadable pid file — fall through and restart
        else:
            return existing

    log_dir = os.path.dirname(log_file)
    if log_dir:
        os.makedirs(log_dir, exist_ok=True)

    fh = open(log_file, "a")
    try:
        proc = subprocess.Popen(
            ["xcrun", "simctl", "spawn", sim_id,
             "log", "stream", "--level", "debug", "--predicate", predicate],
            stdout=fh, stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    finally:
        fh.close()        # the child holds its own dup of the fd

    if pid_file:
        pid_dir = os.path.dirname(pid_file)
        if pid_dir:
            os.makedirs(pid_dir, exist_ok=True)
        with open(pid_file, "w") as pf:
            pf.write(str(proc.pid))
    return proc.pid


# ---------------------------------------------------------------------------
# Dev icon management
# ---------------------------------------------------------------------------

def backup_icons(icon_dirs, backup_dir):
    """Back up icon PNGs from asset catalog dirs to backup_dir.

    icon_dirs: dict mapping label → appiconset directory path
               e.g. {"mac": "/path/to/AppIcon.appiconset", "ios": "..."}
    backup_dir: base directory for backups (subdirs per label created automatically)
    """
    import shutil
    for label, src_dir in icon_dirs.items():
        dest = os.path.join(backup_dir, label)
        os.makedirs(dest, exist_ok=True)
        if not os.path.isdir(src_dir):
            continue
        for fname in os.listdir(src_dir):
            if fname.endswith(".png"):
                shutil.copy2(os.path.join(src_dir, fname), os.path.join(dest, fname))


def swap_dev_icons(icon_dirs, icon_config):
    """Generate dev icons and copy them into asset catalog directories.

    icon_dirs:    dict mapping label → appiconset directory path
    icon_config:  dict of kwargs for generate_icons() (bg_color,
                  glyph_svg, glyph_text, glyph_color, glyph_scale —
                  sizes/output_dir/dev are handled automatically)
    Returns True on success, False if generation was skipped.
    """
    import shutil
    import tempfile

    from .icons import generate_icons

    info("Generating dev icons...")

    try:
        from PIL import Image as _Img
    except ImportError:
        warn("Pillow not installed — skipping dev icon generation")
        return False

    tmp_base = tempfile.mkdtemp(prefix="dev-icons-")
    success = True

    has_glyph = bool(icon_config.get("glyph_text") or icon_config.get("glyph_svg"))

    try:
        for label, src_dir in icon_dirs.items():
            if not os.path.isdir(src_dir):
                continue

            if not has_glyph:
                # No glyph: overlay the banner on the existing canonical PNGs so
                # the real icon art shows through (the originals are already
                # backed up by build_icon_hooks._pre, so editing in place is
                # safe). Same _apply_dev_banner as the glyph path.
                from .icons import _apply_dev_banner
                for fname in os.listdir(src_dir):
                    if not fname.lower().endswith(".png"):
                        continue
                    fpath = os.path.join(src_dir, fname)
                    try:
                        img = _Img.open(fpath).convert("RGBA")
                    except Exception:
                        continue
                    _apply_dev_banner(img)
                    # Drop the alpha channel (opaque RGB) so macOS renders the
                    # icon full-bleed instead of insetting it into a padded tile.
                    img.convert("RGB").save(fpath, "PNG")
                continue

            # Detect sizes from existing PNGs in the asset catalog.
            # Assumes unique pixel widths per file (standard for Apple asset catalogs).
            size_to_file = {}
            for fname in os.listdir(src_dir):
                if not fname.lower().endswith(".png"):
                    continue
                try:
                    with _Img.open(os.path.join(src_dir, fname)) as im:
                        size_to_file[im.width] = fname
                except Exception:
                    continue

            if not size_to_file:
                warn(f"No PNGs found in {src_dir} — skipping {label}")
                continue

            sizes = sorted(size_to_file.keys())
            tmp_out = os.path.join(tmp_base, label)

            config = dict(icon_config)
            config["sizes"] = sizes
            config["output_dir"] = tmp_out
            config["dev"] = True

            if not generate_icons(**config):
                warn(f"Dev icon generation failed for {label}")
                success = False
                continue

            # Copy generated icons back, matching original filenames
            for size, orig_fname in size_to_file.items():
                gen_fname = f"{size}.png"
                gen_path = os.path.join(tmp_out, gen_fname)
                if os.path.isfile(gen_path):
                    shutil.copy2(gen_path, os.path.join(src_dir, orig_fname))
    finally:
        if os.path.isdir(tmp_base):
            shutil.rmtree(tmp_base)

    return success


def restore_icons(icon_dirs, backup_dir):
    """Restore icons from backup and clean up backup dir.

    icon_dirs: same dict as backup_icons
    backup_dir: same base directory passed to backup_icons
    """
    import shutil
    for label, dest_dir in icon_dirs.items():
        src = os.path.join(backup_dir, label)
        if not os.path.isdir(src):
            continue
        for fname in os.listdir(src):
            if fname.endswith(".png"):
                shutil.copy2(os.path.join(src, fname), os.path.join(dest_dir, fname))
    if os.path.isdir(backup_dir):
        shutil.rmtree(backup_dir)


# ---------------------------------------------------------------------------
# tvOS `.brandassets` dev-banner swap
#
# The tvOS layered icon stack (see admin_lib/icons.py's tvOS section) is a
# directory tree, not a flat set of PNGs, so it can't go through
# backup_icons/swap_dev_icons/restore_icons above. Same backup -> swap ->
# restore -> self-heal shape, generalized from "copy files by name" to "copy
# the whole tree".
# ---------------------------------------------------------------------------

_TV_BACKUP_LABEL = "tv-brandassets"


def backup_tv_icons(brandassets_dir, backup_dir):
    """Back up a tvOS `.brandassets` catalog to backup_dir before mutating it."""
    import shutil
    dest = os.path.join(backup_dir, _TV_BACKUP_LABEL)
    if os.path.isdir(dest):
        shutil.rmtree(dest)
    if os.path.isdir(brandassets_dir):
        shutil.copytree(brandassets_dir, dest)


def restore_tv_icons(brandassets_dir, backup_dir):
    """Restore a tvOS `.brandassets` catalog from backup and clean up."""
    import shutil
    src = os.path.join(backup_dir, _TV_BACKUP_LABEL)
    if not os.path.isdir(src):
        return
    if os.path.isdir(brandassets_dir):
        shutil.rmtree(brandassets_dir)
    shutil.copytree(src, brandassets_dir)
    if os.path.isdir(backup_dir):
        shutil.rmtree(backup_dir)


# ---------------------------------------------------------------------------
# Icon Composer (.icon) dev-banner swap
#
# Xcode 26 projects use a single layered `.icon` bundle (icon.json + Assets/)
# that actool compiles into the Liquid Glass .icns, not an appiconset of flat
# PNGs — so the appiconset swap above can't touch it. These mirror the same
# backup → swap → restore → self-heal shape for the `.icon` format: inject an
# opaque "DEV" banner layer for dev builds, restore the pristine icon.json on
# exit. The banner rides its own top group with glass/translucency disabled so
# the stripe stays crisp over the Liquid Glass glyph instead of being faded by
# the base group's translucency.
# ---------------------------------------------------------------------------

_DEV_BANNER_ASSET = "dev-banner.png"
_DEV_BANNER_LAYER = "dev-banner"


def _icon_composer_json(icon_path):
    return os.path.join(icon_path, "icon.json")


def _icon_composer_assets(icon_path):
    return os.path.join(icon_path, "Assets")


def _is_banner_group(group):
    """True if a parsed icon.json group is the injected DEV-banner group."""
    return any(layer.get("name") == _DEV_BANNER_LAYER
               for layer in (group.get("layers") or []))


def _inject_banner_layer(doc):
    """Append the opaque DEV-banner group to a parsed icon.json dict.

    Idempotent: a stale banner group from a re-swap is dropped first. Pure
    dict transform (no filesystem / Pillow) so it's unit-testable.
    """
    groups = [g for g in (doc.get("groups") or []) if not _is_banner_group(g)]
    groups.append({
        "layers": [{
            "glass": False,
            "image-name": _DEV_BANNER_ASSET,
            "name": _DEV_BANNER_LAYER,
            "position": {"scale": 1, "translation-in-points": [0, 0]},
        }],
    })
    doc["groups"] = groups
    return doc


def _strip_banner_layer(doc):
    """Remove the injected DEV-banner group from a parsed icon.json dict."""
    doc["groups"] = [g for g in (doc.get("groups") or [])
                     if not _is_banner_group(g)]
    return doc


def swap_dev_icon_composer(icon_path, backup_dir):
    """Inject an opaque 'DEV' banner layer into an Icon Composer ``.icon``.

    Backs up the pristine ``icon.json`` to ``backup_dir``, renders a
    transparent DEV-banner overlay into ``Assets/dev-banner.png`` sized to the
    base art, and appends a top group referencing it (glass/translucency off).
    Returns True on success, False if skipped (no icon.json / no Pillow).
    """
    import json
    import shutil

    json_path = _icon_composer_json(icon_path)
    if not os.path.isfile(json_path):
        warn(f"{json_path} not found — skipping dev icon")
        return False

    try:
        from PIL import Image as _Img
        from .icons import make_dev_banner
    except ImportError:
        warn("Pillow not installed — skipping dev icon generation")
        return False

    info("Injecting DEV banner into Icon Composer icon...")

    os.makedirs(backup_dir, exist_ok=True)
    shutil.copy2(json_path, os.path.join(backup_dir, "icon.json"))

    # Size the banner to the base art so layer coordinates line up.
    assets_dir = _icon_composer_assets(icon_path)
    os.makedirs(assets_dir, exist_ok=True)
    base_size = 1024
    raw = os.path.join(assets_dir, "icon-raw.png")
    if os.path.isfile(raw):
        try:
            with _Img.open(raw) as im:
                base_size = max(im.width, im.height)
        except Exception:
            pass

    make_dev_banner(base_size).save(
        os.path.join(assets_dir, _DEV_BANNER_ASSET), "PNG")

    with open(json_path) as f:
        doc = json.load(f)
    _inject_banner_layer(doc)
    with open(json_path, "w") as f:
        json.dump(doc, f, indent=2)
        f.write("\n")

    return True


def restore_icon_composer(icon_path, backup_dir):
    """Restore a ``.icon`` to its pristine state after a dev build.

    Prefers the byte-exact pristine ``icon.json`` from ``backup_dir`` (keeps
    the user's formatting); if it's missing — a corrupted/partial state —
    strips the banner group in place. Removes the generated banner asset and
    the backup dir either way.
    """
    import json
    import shutil

    json_path = _icon_composer_json(icon_path)
    backup_json = os.path.join(backup_dir, "icon.json")
    if os.path.isfile(backup_json):
        shutil.copy2(backup_json, json_path)
    elif os.path.isfile(json_path):
        with open(json_path) as f:
            doc = json.load(f)
        _strip_banner_layer(doc)
        with open(json_path, "w") as f:
            json.dump(doc, f, indent=2)
            f.write("\n")

    banner = os.path.join(_icon_composer_assets(icon_path), _DEV_BANNER_ASSET)
    if os.path.isfile(banner):
        os.remove(banner)
    if os.path.isdir(backup_dir):
        shutil.rmtree(backup_dir)


_LSREGISTER = ("/System/Library/Frameworks/CoreServices.framework/Versions/A/"
               "Frameworks/LaunchServices.framework/Versions/A/Support/lsregister")


def sign(path, identity=None, identifier=None, deep=True, cfg=None):
    """Code-sign a binary or .app, keeping macOS keychain/TCC grants across rebuilds.

    The identifier is the point. An ad-hoc-signed binary's designated
    requirement is its raw cdhash, which changes on every build — so macOS sees
    each rebuild as a different app and re-prompts for camera/Documents/keychain
    access, and any Keychain item bound to the old requirement is unreachable.
    Signing with a *fixed* identifier makes the designated requirement constant
    (identifier + team), and the grants persist.

    identity: an explicit identity or hash; "-" forces ad-hoc. None (default)
        takes $CODE_SIGN_IDENTITY, else resolves one from the keychain, else
        falls back to ad-hoc — so a machine with no cert (CI) still produces a
        loadable binary rather than failing.
    identifier: the stable bundle identifier to bind the signature to. Omit and
        codesign derives one from the binary, which is the behavior this exists
        to avoid.

    Returns the identity used.
    """
    path = str(path)
    if not os.path.exists(path):
        err("sign: no such path: " + path)
        sys.exit(1)

    if identity is None:
        identity = os.environ.get("CODE_SIGN_IDENTITY") or ""
    if not identity:
        identity = resolve_signing_identity(cfg or {}) or "-"

    base = ["codesign", "--force"]
    if deep:
        base.append("--deep")

    attempts = []
    if identifier:
        attempts.append(base + ["--identifier", identifier, "--sign", identity, path])
    attempts.append(base + ["--sign", identity, path])

    for i, cmd in enumerate(attempts):
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode == 0:
            label = "ad-hoc" if identity == "-" else identity[:14]
            if identifier and i == 0:
                info(f"signed {os.path.basename(path)} ({label}, identifier {identifier})")
            else:
                # No identifier means the designated requirement is the cdhash
                # again, so grants will not survive the next build.
                warn(f"signed {os.path.basename(path)} ({label}) without a stable "
                     f"identifier — keychain/TCC grants will not persist")
            return identity

    err(f"codesign failed for {path}: " + (r.stderr or "").strip())
    sys.exit(1)


def _pids_running_from(app_path):
    """Pids whose executable lives inside `app_path`.

    Matching on the bundle path rather than the process name is deliberate: a
    name match ("Term", "Stash") can hit an unrelated app, and the thing we
    actually need to stop is whatever is running *out of the bundle we are about
    to replace*.
    """
    app_path = os.path.abspath(app_path).rstrip("/")
    try:
        r = subprocess.run(["ps", "-Ao", "pid=,comm="],
                           capture_output=True, text=True, timeout=5)
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return []
    pids = []
    for line in r.stdout.splitlines():
        parts = line.strip().split(None, 1)
        if len(parts) < 2:
            continue
        try:
            pid = int(parts[0])
        except ValueError:
            continue
        if parts[1].startswith(app_path + "/"):
            pids.append(pid)
    return pids


def install_app(bundle_path, dest_dir="/Applications", relaunch=None, timeout=5.0):
    """Install a built .app into `dest_dir`, replacing any copy already there.

    Six projects hand-rolled this and only two got it right. The three things
    that are easy to get wrong and are handled here:

    * The running copy is stopped first, gracefully (SIGTERM, then SIGKILL), and
      we wait for it to actually exit — `cp -r` over a running bundle corrupts
      the copy on disk and leaves the old binary mapped.
    * The swap is atomic: the new bundle is staged beside the destination and
      renamed into place, so an interrupted install can never leave a
      half-copied .app where a working one used to be. `rm -rf` + `cp -R` (what
      most of them did) has exactly that window.
    * LaunchServices is told the bundle changed, otherwise the Dock and `open`
      keep using the previous registration.

    relaunch: True/False to force; None (default) relaunches only if a copy was
    running when we started — which is what you want for `admin deploy` while
    using the app.

    Returns the installed path.
    """
    import time

    # A glob is the normal case for electron-forge, which names its output dir
    # with an arch suffix (out/<Name>-darwin-arm64/<Name>.app). Ambiguity is an
    # error rather than a coin flip — two matches means a stale build is lying
    # around and picking either one silently installs the wrong binary.
    if glob.has_magic(bundle_path):
        matches = sorted(glob.glob(bundle_path))
        if not matches:
            err("install_app: no app bundle matched: " + bundle_path)
            sys.exit(1)
        if len(matches) > 1:
            err("install_app: " + bundle_path + " matched several bundles:")
            for m in matches:
                info("  " + m)
            info("remove the stale build(s), or pass an exact path")
            sys.exit(1)
        bundle_path = matches[0]

    bundle_path = os.path.abspath(bundle_path)
    if not os.path.isdir(bundle_path):
        err("install_app: no such app bundle: " + bundle_path)
        sys.exit(1)
    dest = os.path.join(dest_dir, os.path.basename(bundle_path))

    was_running = bool(_pids_running_from(dest))
    if was_running:
        pids = _pids_running_from(dest)
        info(f"stopping running {os.path.basename(dest)} (pids: {pids})")
        for pid in pids:
            try:
                os.kill(pid, signal.SIGTERM)
            except (ProcessLookupError, PermissionError):
                pass
        deadline = time.time() + timeout
        while time.time() < deadline and _pids_running_from(dest):
            time.sleep(0.1)
        stubborn = _pids_running_from(dest)
        if stubborn:
            warn(f"{os.path.basename(dest)} ignored SIGTERM, sending SIGKILL")
            for pid in stubborn:
                try:
                    os.kill(pid, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    pass
            deadline = time.time() + 2.0
            while time.time() < deadline and _pids_running_from(dest):
                time.sleep(0.1)

    staged = dest + ".installing"
    previous = dest + ".previous"
    for leftover in (staged, previous):
        if os.path.exists(leftover):
            shutil.rmtree(leftover, ignore_errors=True)

    # ditto rather than cp -R: it preserves resource forks, symlinks, and the
    # bundle's signature, which a naive copy can strip. Called directly rather
    # than through run_cmd so this stays usable as a plain library function,
    # with no dependency on a configured log file.
    r = subprocess.run(["ditto", bundle_path, staged], capture_output=True, text=True)
    if r.returncode != 0:
        shutil.rmtree(staged, ignore_errors=True)
        err("install_app: copy failed: " + (r.stderr or "").strip())
        sys.exit(r.returncode)

    try:
        if os.path.exists(dest):
            os.rename(dest, previous)
        os.rename(staged, dest)
    except OSError as e:
        # Put the old one back rather than leaving nothing installed.
        if os.path.exists(previous) and not os.path.exists(dest):
            os.rename(previous, dest)
        shutil.rmtree(staged, ignore_errors=True)
        err("install_app: could not swap in the new bundle: " + str(e))
        sys.exit(1)
    shutil.rmtree(previous, ignore_errors=True)

    refresh_app_icon_cache(dest)
    ok("installed " + dest)

    should_relaunch = was_running if relaunch is None else relaunch
    if should_relaunch:
        info("relaunching " + os.path.basename(dest))
        subprocess.run(["open", dest], capture_output=True)
    return dest



def refresh_app_icon_cache(app_path):
    """Make LaunchServices/Dock re-read a freshly built app's icon.

    A dev app reuses one bundle id at one path, so when its icon *content*
    changes (e.g. a DEV banner is compiled in for the first time) macOS keeps
    serving the previously cached icon. Bump the bundle's mtime and re-register
    it with LaunchServices so the next ``open`` shows the new icon. Best-effort
    — a cache refresh is never worth failing a dev launch over.

    Note: this fixes future launches. An icon already cached *without* the
    banner from earlier builds is sticky; clearing that one-time poisoned cache
    needs a `killall Dock`.
    """
    try:
        os.utime(app_path, None)
    except OSError:
        pass
    if os.path.exists(_LSREGISTER):
        try:
            subprocess.run([_LSREGISTER, "-f", app_path],
                           capture_output=True, check=False)
        except Exception:
            pass


# ---------------------------------------------------------------------------
# Dev loops
# ---------------------------------------------------------------------------

def dev_loop_macos(project, scheme, config, destination, process, subsystem,
                   predicate, app_pattern, pid_file="/tmp/admin-run.pid",
                   extra_settings="", build_action="build",
                   pre_build=None, post_build=None, pre_launch=None,
                   is_running_fn=None, kill_fn=None, output_config=None,
                   refresh_icon=False, launch_args=None):
    """Full macOS dev loop: build, launch, stream logs, reload on 'r' or SIGUSR1.

    Hook callbacks: pre_build/post_build wrap each build; pre_launch(app_path)
    runs before launch and may return a new app_path. is_running_fn / kill_fn
    override the default pgrep/pkill -x process check. refresh_icon re-reads the
    built app's icon into LaunchServices before launch (set when a dev-icon swap
    is active, so a just-compiled banner isn't masked by a stale icon cache).
    launch_args: optional list of strings appended to `open -n <app> --args ...`,
    delivered to the app as its process arguments (mirrors install_and_launch_sim's
    launch_args on the simulator path).
    """
    import time
    from .log_columns import make_column_formatter

    setup_reload(pid_file)
    sweep_orphan_log_streams(subsystem)

    def _default_is_running():
        r = subprocess.run(["pgrep", "-x", process], capture_output=True)
        return r.returncode == 0

    def _default_kill():
        if subprocess.run(["pgrep", "-x", process], capture_output=True).returncode != 0:
            return
        subprocess.run(["pkill", "-x", process], capture_output=True)
        for _ in range(15):
            if subprocess.run(["pgrep", "-x", process], capture_output=True).returncode != 0:
                return
            time.sleep(0.1)
        warn(f"{process} ignored SIGTERM, sending SIGKILL")
        subprocess.run(["pkill", "-9", "-x", process], capture_output=True)

    check_running = is_running_fn or _default_is_running
    kill_app = kill_fn or _default_kill

    first_launch = True
    while True:
        clear_rebuild_request()
        kill_app()

        if pre_build:
            pre_build()

        info("Building...")
        rc = xcodebuild_filtered(project, scheme, config, destination,
                                 action=build_action,
                                 extra_settings=extra_settings)
        print()

        if post_build:
            post_build()

        if rc != 0:
            err("Build failed")
            if is_rebuild_requested():
                warn("Rebuild requested during failed build, retrying...")
                continue
            cleanup_pid_file(pid_file)
            sys.exit(1)

        # Ask xcodebuild where this build wrote, never glob DerivedData for it
        # (find_app) — see target_build_dir's docstring and #448.
        build_dir = target_build_dir(project, scheme, config, destination, extra_settings)
        if not build_dir:
            err("BUILD SUCCEEDED but xcodebuild reported no TARGET_BUILD_DIR for "
                f"{scheme} / {config} — can't tell which directory it built into.")
            cleanup_pid_file(pid_file)
            sys.exit(1)

        app_path = os.path.join(build_dir, os.path.basename(app_pattern))
        if not os.path.isdir(app_path):
            err(f"BUILD SUCCEEDED but no app at {app_path}")
            err("Not falling back to a DerivedData search — that's how a stale build from")
            err("another worktree gets launched instead of this one.")
            cleanup_pid_file(pid_file)
            sys.exit(1)

        if pre_launch:
            result = pre_launch(app_path)
            if result:
                app_path = result

        # The swapped-in banner is identical content every rebuild, so the
        # cache only needs refreshing once — skip the blocking lsregister scan
        # on subsequent hot-reloads.
        if refresh_icon and first_launch:
            refresh_app_icon_cache(app_path)
        first_launch = False

        info(f"Launching {app_path}")
        open_cmd = ["open", "-n", app_path]
        if launch_args:
            open_cmd.extend(["--args", *launch_args])
        subprocess.run(open_cmd, check=True)

        appeared = False
        for _ in range(33):
            if check_running():
                appeared = True
                break
            time.sleep(0.3)

        if not appeared:
            warn("App did not appear in process list within 10s")
            warn("It may have crashed on launch, or detection failed.")
            warn("Check Console.app for crash reports — logs below may be empty.")

        fmt = make_column_formatter("apple_os_log", output_config)
        log_proc, log_thread, log_fh = stream_os_log(
            predicate, log_file=get_log_file(), formatter=fmt.format_line)

        info("Press r to rebuild, Ctrl+C to quit")
        print()

        got_reload = poll_for_reload(check_running if appeared else lambda: True)

        stop_stream(log_proc, log_fh)
        print()

        if got_reload:
            info("Reload signal received — rebuilding...")
        elif appeared:
            info(f"Stopping {process}...")
        else:
            info("Exiting (app never appeared).")
        kill_app()

        if not got_reload:
            cleanup_pid_file(pid_file)
            return


def dev_loop_simulator(project, scheme, config, subsystem, process, predicate,
                       app_pattern, pid_file="/tmp/admin-run.pid",
                       extra_settings="", build_action="build",
                       bundle_id=None, sim_id=None,
                       pre_build=None, post_build=None, display_name=None,
                       output_config=None, launch_args=None, platform="ios"):
    """Full simulator dev loop: build, install, stream logs, reload on 'r'/SIGUSR1.

    Drives an iOS or a tvOS simulator — everything below is identical between
    them except the xcodebuild destination, which comes from SIM_PLATFORMS.

    bundle_id:   bundle ID for launch/terminate (defaults to subsystem)
    sim_id:      simulator device ID (if None, auto-resolves via resolve_simulator)
    pre_build:   callable() run before each build (e.g. swap dev icons)
    post_build:  callable() run after each build completes (e.g. restore icons)
    launch_args: optional list of strings passed to the app on each launch
    platform:    a SIM_PLATFORMS key — "ios" or "tv"
    """
    import time
    from .log_columns import make_column_formatter

    plat = _sim_platform(platform)

    setup_reload(pid_file)
    sweep_orphan_log_streams(subsystem)

    launch_bundle = bundle_id or subsystem

    if sim_id is None:
        sim_id, sim_name = ensure_booted_simulator(platform)
    else:
        boot_simulator(sim_id)
        subprocess.run(["xcrun", "simctl", "bootstatus", sim_id, "-b"])
    destination = f"platform={plat['destination']},id={sim_id}"

    while True:
        clear_rebuild_request()
        existing_app_path = find_app(app_pattern)
        existing_bundle = (
            app_bundle_identifier(existing_app_path) if existing_app_path else None
        )
        launch_bundle = existing_bundle or bundle_id or subsystem
        terminate_sim_app(sim_id, launch_bundle)

        if pre_build:
            pre_build()

        info("Building for simulator...")
        rc = xcodebuild_filtered(project, scheme, config, destination,
                                 action=build_action,
                                 extra_settings=extra_settings)
        print()

        if post_build:
            post_build()

        if rc != 0:
            err("Build failed")
            if is_rebuild_requested():
                warn("Rebuild requested during failed build, retrying...")
                continue
            cleanup_pid_file(pid_file)
            sys.exit(1)

        app_path = find_app(app_pattern)
        if not app_path:
            err("Could not find built app")
            cleanup_pid_file(pid_file)
            sys.exit(1)

        launch_bundle = app_bundle_identifier(app_path) or bundle_id or subsystem
        expected = bundle_id or subsystem
        if expected and launch_bundle and launch_bundle != expected:
            warn(f"Built app has bundle ID {launch_bundle!r}, expected {expected!r}")
            warn(f"Path: {app_path}")
            warn("Stale DerivedData from another project may be interfering.")
            warn("Run: ./admin clean  — then retry")
            cleanup_pid_file(pid_file)
            sys.exit(1)

        if display_name:
            patch_ios_display_name(app_path, display_name)

        info("Installing on simulator...")
        install_and_launch_sim(sim_id, app_path, launch_bundle, launch_args=launch_args)

        def _is_running():
            return is_sim_app_running(sim_id, launch_bundle)

        for _ in range(10):
            if _is_running():
                break
            time.sleep(0.3)

        fmt = make_column_formatter("apple_os_log", output_config)
        log_proc, log_thread, log_fh = stream_os_log(
            predicate, log_file=get_log_file(), sim_id=sim_id, formatter=fmt.format_line)

        info("Press r to rebuild, Ctrl+C to quit")
        print()

        got_reload = poll_for_reload(_is_running)

        stop_stream(log_proc, log_fh)
        print()

        if got_reload:
            info("Reload signal received — rebuilding...")
        else:
            info(f"Stopping {process}...")
        terminate_sim_app(sim_id, launch_bundle)

        if not got_reload:
            cleanup_pid_file(pid_file)
            return


def dev_loop_device(project, scheme, config, subsystem, process,
                    app_pattern, pid_file="/tmp/admin-run.pid",
                    extra_settings="", build_action="build",
                    bundle_id=None,
                    pre_build=None, post_build=None, display_name=None,
                    skip_build=False, output_config=None):
    """Build, install, and run on a physical iOS device with log streaming.

    Launches via `devicectl --console` with OS_ACTIVITY_DT_MODE=YES (non-redacted
    os_log). bundle_id defaults to subsystem. pre_build/post_build wrap each
    build cycle.

    The device is always resolved here. It used to be passable as `device_id`,
    with a branch that took the *identifier* as the display name and skipped
    registering the device with the team — a path nothing has ever called, so
    the only thing it could do was give a future caller a half-working deploy.

    Loops on 'r'/SIGUSR1 like dev_loop_simulator. The device log stream is only
    obtainable by owning the launch — `devicectl device process launch --console`
    attaches and waits, and there is no attach-to-running-process subcommand nor
    a `log stream --device` — so a rebuild has to tear the launched app down and
    relaunch it, rather than restarting a child beside an independent stream the
    way the simulator can.
    """
    from .log_columns import make_column_formatter

    if output_config is None:
        output_config = globals().get("_OUTPUT_CONFIG")
    launch_bundle = bundle_id or subsystem

    device_id, device_name, udid = find_connected_device()
    ensure_device_registered(udid, device_name)

    destination = "generic/platform=iOS"

    setup_reload(pid_file)

    while True:
        clear_rebuild_request()
        terminate_device_app(device_id, launch_bundle)

        if skip_build:
            app_path = find_app(app_pattern)
            if not app_path:
                err("Could not find previously built app — run without cache to rebuild")
                cleanup_pid_file(pid_file)
                sys.exit(1)
            launch_bundle = app_bundle_identifier(app_path) or bundle_id or subsystem
        else:
            if pre_build:
                pre_build()

            info(f"Building for device ({device_name})...")
            rc = xcodebuild_filtered(project, scheme, config, destination,
                                     action=build_action,
                                     extra_settings=extra_settings)
            print()

            if post_build:
                post_build()

            if rc != 0:
                err("Build failed")
                if is_rebuild_requested():
                    warn("Rebuild requested during failed build, retrying...")
                    continue
                cleanup_pid_file(pid_file)
                sys.exit(1)

            app_path = find_app(app_pattern)
            if not app_path:
                err("Could not find built app")
                cleanup_pid_file(pid_file)
                sys.exit(1)

            launch_bundle = app_bundle_identifier(app_path) or bundle_id or subsystem

            if display_name:
                patch_ios_display_name(app_path, display_name)

            info(f"Installing on {device_name}...")
            rc = install_on_device(device_id, app_path)
            if rc != 0:
                err("Install failed")
                cleanup_pid_file(pid_file)
                sys.exit(1)

        info(f"Launching on {device_name}...")
        console_proc = launch_on_device(
            device_id, launch_bundle, console=True,
            env=_DEVICE_LOG_ENV)

        # "generic", not "apple_os_log" as the simulator loop uses: that
        # formatter waits for `log stream`'s native "Timestamp..." header and
        # then parses fixed-width os_log columns. devicectl --console emits the
        # app's raw stdout/stderr with no header and no columns, so the Apple
        # formatter would wait forever and pass every line through unformatted.
        fmt = make_column_formatter("generic", output_config)
        _, log_fh = stream_subprocess(
            console_proc, log_file=get_log_file(), formatter=fmt.format_line)

        info("Press r to rebuild, Ctrl+C to quit")
        print()

        got_reload = poll_for_reload(lambda: console_proc.poll() is None)

        stop_stream(console_proc, log_fh)
        print()

        if got_reload:
            info("Reload signal received — rebuilding...")
        else:
            info(f"Stopping {process}...")
        terminate_device_app(device_id, launch_bundle)

        if not got_reload:
            cleanup_pid_file(pid_file)
            info("Done.")
            return


# ---------------------------------------------------------------------------
# Reload command auto-registration
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# High-level archetype wrappers — driven by the [apple] table in admin.toml.
# The apple archetype's kind:"python" commands call these with _APPLE_CONFIG.
# ---------------------------------------------------------------------------


_PREBUILD_RAN = set()


def _run_prebuild_cmd(cfg):
    """Run ``[apple] prebuild_cmd`` once per invocation, at the moment the
    project path is resolved.

    This lives here, rather than at the top of each command, because every
    apple action funnels through ``_detect_project_path`` -- so a command
    added later cannot forget to call it. It previously sat inline in
    ``dev_mac``/``dev_ios``/``dev_device``/``deploy_mac``/``deploy_ios``, and
    ``build_mac``/``build_ios`` were duly forgotten: ``admin build`` skipped
    the prereq the docs promised runs before every xcodebuild.

    The motivating case is a generated ``.xcodeproj`` (XcodeGen, Tuist) kept
    out of version control, where the project is absent on a fresh clone and
    stale after a spec edit.

    Runs at most once per process: a single ``admin`` invocation resolves the
    project path many times, and re-running the prereq each time would be
    wasted work.
    """
    cmd = (cfg or {}).get("prebuild_cmd")
    if not cmd or cmd in _PREBUILD_RAN:
        return
    _PREBUILD_RAN.add(cmd)
    info("prereqs: " + cmd)
    if run_cmd(cmd) != 0:
        err("prereq command failed"); sys.exit(1)


def _detect_project_path(cfg):
    """Return cfg['project_path'], or the first .xcworkspace/.xcodeproj in cwd."""
    _run_prebuild_cmd(cfg)
    p = cfg.get("project_path")
    if p:
        return p
    for pat in ("*.xcworkspace", "*.xcodeproj"):
        matches = glob.glob(pat)
        if matches:
            return matches[0]
    err("apple.project_path not set and no .xcodeproj/.xcworkspace found in cwd")
    sys.exit(1)


_PBXPROJ_CACHE = {}


def _read_pbxproj(project_path):
    """Parse the Xcode project's ``project.pbxproj`` (an OpenStep ASCII
    plist) into a dict via ``plutil -convert json``. Cached per project_path
    so a single admin invocation parses at most once. Returns ``{}`` on any
    failure so callers fall back to manifest-supplied values.
    """
    if project_path in _PBXPROJ_CACHE:
        return _PBXPROJ_CACHE[project_path]

    actual = project_path
    if project_path.endswith(".xcworkspace"):
        # Workspaces aren't pbxproj-shaped; resolve to a sibling .xcodeproj
        # if there's an obvious one. (Covers the common SPM/CocoaPods case
        # where the workspace wraps a single .xcodeproj of the same name.)
        base = os.path.basename(project_path).removesuffix(".xcworkspace")
        sibling = os.path.join(os.path.dirname(project_path) or ".",
                               base + ".xcodeproj")
        if os.path.isdir(sibling):
            actual = sibling
        else:
            _PBXPROJ_CACHE[project_path] = {}
            return {}

    pbxproj = os.path.join(actual, "project.pbxproj")
    if not os.path.isfile(pbxproj):
        _PBXPROJ_CACHE[project_path] = {}
        return {}

    try:
        rc = subprocess.run(
            ["plutil", "-convert", "json", "-o", "-", pbxproj],
            capture_output=True, text=True, check=True,
        )
        data = json.loads(rc.stdout)
    except (subprocess.CalledProcessError, json.JSONDecodeError, FileNotFoundError):
        data = {}

    _PBXPROJ_CACHE[project_path] = data
    return data


def _xcode_target_settings(cfg, scheme_name, *, configuration="Debug"):
    """Return the buildSettings dict for the PBXNativeTarget backing `scheme_name`
    at `configuration` (default Debug). Returns {} on any resolution failure;
    callers must tolerate that and fall back to manifest defaults.

    Only sees settings written into pbxproj — .xcconfig values and `$(VAR)`
    substitutions come back as literals.
    """
    if not scheme_name:
        return {}
    project_path = _detect_project_path(cfg)
    proj = _read_pbxproj(project_path)
    objects = proj.get("objects") or {}
    if not objects:
        return {}

    target = next(
        (obj for obj in objects.values()
         if obj.get("isa") == "PBXNativeTarget" and obj.get("name") == scheme_name),
        None,
    )
    if not target:
        return {}

    cfg_list = objects.get(target.get("buildConfigurationList"), {}) or {}
    cfg_ids = cfg_list.get("buildConfigurations") or []

    chosen = next(
        (objects[cid] for cid in cfg_ids
         if (objects.get(cid) or {}).get("name") == configuration),
        None,
    )
    if chosen is None and cfg_ids:
        chosen = objects.get(cfg_ids[0]) or {}
    if chosen is None:
        return {}

    return chosen.get("buildSettings") or {}


def _xcode_setting(cfg, scheme_name, key):
    """Return ``buildSettings[key]`` for the given scheme's target, or None
    if missing or if the value is a Xcode variable substitution like
    ``$(TARGET_NAME)`` (which the caller can't act on without a proper
    expansion pass).
    """
    val = _xcode_target_settings(cfg, scheme_name).get(key)
    if not val or not isinstance(val, str):
        return None
    if val.startswith("$"):
        return None
    return val


def _resolve_bundle(cfg, platform, *, dev=False):
    """Single authoritative bundle ID resolver. The Xcode project is the
    source of truth; the manifest can override. ``dev=True`` appends ``.dev``
    (or uses the explicit ``{platform}_dev_bundle`` key).
    """
    if platform not in ("ios", "mac", "tv"):
        raise ValueError(f"_resolve_bundle: unknown platform {platform!r} — expected 'ios', 'mac' or 'tv'")
    key = platform
    prod = cfg.get(f"{key}_bundle") or _xcode_setting(
        cfg, cfg.get(f"{key}_scheme"), "PRODUCT_BUNDLE_IDENTIFIER"
    )
    if not dev:
        return prod
    return cfg.get(f"{key}_dev_bundle") or (f"{prod}.dev" if prod else None)


def _resolve_mac_bundle(cfg):
    return _resolve_bundle(cfg, "mac")


def _resolve_ios_bundle(cfg):
    return _resolve_bundle(cfg, "ios")


def _resolve_mac_process(cfg):
    """macOS PRODUCT_NAME (== ``.app`` filename and ``CFBundleName``).
    Falls back to scheme name when the pbxproj uses a variable substitution
    or the manifest doesn't override.
    """
    return (cfg.get("mac_process")
            or _xcode_setting(cfg, cfg.get("mac_scheme"), "PRODUCT_NAME")
            or cfg.get("mac_scheme"))


def _resolve_sim_process(cfg, platform):
    """PRODUCT_NAME for a simulator platform. Same fallback chain as
    :func:`_resolve_mac_process`, with the config keys taken from the platform
    prefix: ``ios_process``/``ios_scheme`` or ``tv_process``/``tv_scheme``.
    """
    return (cfg.get(f"{platform}_process")
            or _xcode_setting(cfg, cfg.get(f"{platform}_scheme"), "PRODUCT_NAME")
            or cfg.get(f"{platform}_scheme"))


def _resolve_ios_process(cfg):
    """iOS PRODUCT_NAME. Same fallback chain as :func:`_resolve_mac_process`."""
    return _resolve_sim_process(cfg, "ios")


def _resolve_derived_data_name(cfg):
    """Project-name prefix Xcode uses for the DerivedData folder. Defaults
    to the basename of the .xcodeproj/.xcworkspace.
    """
    explicit = cfg.get("derived_data_name")
    if explicit:
        return explicit
    project_path = _detect_project_path(cfg)
    base = os.path.basename(project_path)
    return base.removesuffix(".xcworkspace").removesuffix(".xcodeproj")


def _resolve_mac_install_path_prod(cfg):
    """Filesystem path the production macOS app installs to. Defaults to
    ``/Applications/<process>.app``.
    """
    explicit = cfg.get("mac_install_path_prod")
    if explicit:
        return explicit
    process = _resolve_mac_process(cfg)
    return f"/Applications/{process}.app" if process else None


def _resolve_subsystem(cfg, platform="mac", *, dev=False):
    """os_log subsystem — the bundle ID the running app logs under.

    Matches Bundle.main.bundleIdentifier at runtime (dev vs prod bundle by
    `dev` flag). Returns "" instead of None so callers don't need a None check.
    No cross-platform fallback: missing iOS config returns "" — set ios_bundle /
    ios_dev_bundle in [apple] if needed.
    """
    explicit = cfg.get("subsystem")
    if explicit:
        return explicit
    return _resolve_bundle(cfg, platform, dev=dev) or ""


def _resolve_icon_dirs(cfg, platforms=("mac", "ios")):
    """Return {platform: abs_path} for platforms that have an asset dir
    configured. "tv" resolves to the `.brandassets` folder path, not a flat
    appiconset — callers that iterate PNGs in the returned dir (backup_icons,
    swap_dev_icons, restore_icons, the size-sniffing loop in
    regenerate_icons) only ever get "mac"/"ios" via their default
    `platforms`; tv is resolved through this same key_map but consumed
    separately by the tv-specific generate_tv_icons()/apply_tv_dev_banner()
    functions, which understand its nested imagestack/imageset layout.

    "mac-dev"/"ios-dev" resolve a SEPARATE, permanently-committed appiconset
    carrying the DEV banner baked in (as opposed to build_icon_hooks' runtime
    swap-and-restore of a single catalog) — for a project that ships two
    static asset catalogs and switches between them via
    ASSETCATALOG_COMPILER_APPICON_NAME per build configuration.
    """
    icons_cfg = cfg.get("icons") or {}
    key_map = {
        "mac": "mac_asset_dir", "ios": "ios_asset_dir", "tv": "tv_asset_dir",
        "mac-dev": "mac_dev_asset_dir", "ios-dev": "ios_dev_asset_dir",
    }
    result = {}
    for plat in platforms:
        rel = icons_cfg.get(key_map[plat])
        if rel:
            result[plat] = os.path.abspath(rel)
    return result


def _sizes_from_contents_json(src_dir):
    """Return {pixel_width: filename} from an appiconset's ``Contents.json``.

    Used to bootstrap an appiconset that has no PNGs yet — the case where
    reading the sizes off the existing files cannot work, because making those
    files is the point.

    The pixel width is ``size`` times ``scale``, not ``size``: a macOS catalog
    asks for ``16x16`` at ``2x`` and means a 32-pixel PNG, and lists the same
    filename again as ``32x32`` at ``1x``. Collapsing on pixel width is
    therefore correct and is what the PNG-reading path arrives at too. iOS
    entries carry no ``scale`` at all, which reads as 1x.
    """
    contents = os.path.join(src_dir, "Contents.json")
    if not os.path.isfile(contents):
        return {}
    try:
        with open(contents) as fh:
            data = json.load(fh)
    except Exception:
        warn(f"{contents} is not readable JSON — cannot take icon sizes from it")
        return {}

    result = {}
    for image in data.get("images") or []:
        fname = image.get("filename")
        size = image.get("size") or ""
        if not fname or "x" not in size:
            continue
        try:
            points = float(size.split("x")[0])
            scale = float((image.get("scale") or "1x").rstrip("x"))
        except ValueError:
            continue
        width = int(round(points * scale))
        if width > 0:
            result[width] = fname
    return result


def _resolve_icon_composer(cfg):
    """Abs path to the Icon Composer ``.icon`` bundle from
    ``[apple.icons] mac_icon``, or None. Its presence routes the dev-icon
    swap through the ``.icon`` path instead of the appiconset PNG path.
    """
    icons_cfg = cfg.get("icons") or {}
    rel = icons_cfg.get("mac_icon")
    return os.path.abspath(rel) if rel else None


def _icon_generation_kwargs(cfg):
    """Return kwargs dict forwarded to generate_icons() from [apple.icons]."""
    i = cfg.get("icons") or {}
    out = {}
    for k in ("bg_color", "glyph_text", "glyph_svg", "glyph_color", "glyph_scale"):
        if k in i:
            out[k] = i[k]
    return out


def _restore_stranded_icon_composer(icon_path, backup_dir):
    """Composer analog of restore_stranded_dev_icons: if a prior dev run was
    SIGKILL'd before its post-build restore, the injected banner is still in
    the ``.icon`` (its pristine icon.json is in ``backup_dir``). Revert before
    a fresh build so a DEV banner can't be baked into a release.
    """
    banner = os.path.join(_icon_composer_assets(icon_path), _DEV_BANNER_ASSET)
    if not (os.path.isdir(backup_dir) or os.path.isfile(banner)):
        return
    warn(f"Stranded dev icon found for {icon_path} (interrupted dev run?) — restoring original")
    restore_icon_composer(icon_path, backup_dir)


def _build_icon_composer_hooks(cfg, icon_path, backup_dir, restore_on_exit=True):
    """(pre_build, post_build) for the Icon Composer ``.icon`` dev-banner swap.

    Mirrors the appiconset hooks: self-heal a stranded prior run, register the
    restore as a cleanup hook before mutating, then inject the banner —
    including ``restore_on_exit``, so `admin icons swap-dev` leaves the banner
    in place on this path too.
    """
    composer_backup = os.path.join(backup_dir, "icon-composer")

    def _post():
        restore_icon_composer(icon_path, composer_backup)

    cleanup_registered = [False]

    def _pre():
        _restore_stranded_icon_composer(icon_path, composer_backup)
        if restore_on_exit and not cleanup_registered[0]:
            register_cleanup(_post)
            cleanup_registered[0] = True
        swap_dev_icon_composer(icon_path, composer_backup)

    return _pre, _post


def _build_appiconset_hooks(cfg, icon_dirs, platforms, backup_dir, restore_on_exit=True):
    """(pre_build, post_build) for the flat mac/iOS appiconset PNG dev-banner
    swap. Extracted out of build_icon_hooks so it can be composed alongside
    the tv brandassets hooks below.
    """
    icon_kwargs = _icon_generation_kwargs(cfg)
    # With glyph config, swap_dev_icons regenerates the icon from the glyph and
    # adds the banner (apple-coding-tool, apple-notepad). Without it — a real-art
    # canonical appiconset, e.g. macterm — it overlays the banner on the existing
    # committed PNGs. Either path uses the same _apply_dev_banner, so all projects
    # share one dev-banner implementation.

    def _post():
        restore_icons(icon_dirs, backup_dir)

    cleanup_registered = [False]

    def _pre():
        # Self-heal first: a prior dev run killed via SIGKILL leaves the
        # catalog in DEV-overlay state with the originals still in
        # backup_dir. If we ran backup_icons now we'd overwrite those
        # originals with the current DEV PNGs and lose the only remaining
        # copy. Restore first so the catalog is back to a known-clean
        # state, then back up that.
        _restore_stranded_appiconset_dev_icons(cfg, platforms, backup_dir)
        # Register cleanup BEFORE we mutate anything. If SIGINT lands during
        # backup_icons or swap_dev_icons (PIL ops + filesystem copies), the
        # restore still runs. Once-only — register_cleanup has no dedup, so
        # repeated dev iterations would otherwise stack copies of _post.
        if restore_on_exit and not cleanup_registered[0]:
            register_cleanup(_post)
            cleanup_registered[0] = True
        backup_icons(icon_dirs, backup_dir)
        swap_dev_icons(icon_dirs, icon_kwargs)

    return _pre, _post


def _tv_icon_backup_dir(backup_dir):
    """tv's own backup root — a sibling of, not nested under, the shared
    mac/iOS ``backup_dir``. ``restore_icons()`` (the flat appiconset path)
    wholesale-``rmtree``s the directory it's handed once it's done, so if tv
    nested its backup inside that same directory, composing tv with an
    appiconset in one ``build_icon_hooks`` call would let either system's
    restore delete the other's still-pending backup out from under it —
    whichever runs last (or self-heals first) wins and the other's dev icons
    never come back. A distinct root removes the shared-directory hazard
    instead of trying to order around it.
    """
    return f"{backup_dir}-tv"


def _restore_stranded_tv_icons(brandassets_dir, backup_dir):
    """tv analog of _restore_stranded_appiconset_dev_icons: if a prior dev run
    was SIGKILL'd before its post-build restore, the pristine `.brandassets`
    tree backed up by backup_tv_icons is still sitting in backup_dir. Restore
    it before a fresh build so a DEV banner can't be baked into a release.
    """
    if not os.path.isdir(os.path.join(backup_dir, _TV_BACKUP_LABEL)):
        return
    warn(f"Stranded dev tv icons found for {brandassets_dir} (interrupted dev run?) — restoring original")
    restore_tv_icons(brandassets_dir, backup_dir)


def _build_tv_icon_hooks(cfg, brandassets_dir, backup_dir, restore_on_exit=True):
    """(pre_build, post_build) for the tvOS `.brandassets` dev-banner swap.
    Mirrors _build_appiconset_hooks: self-heal a stranded prior run, back up
    the pristine catalog, then either regenerate it from glyph_* with the
    banner baked in, or overlay the banner on the committed art in place —
    same has_glyph branch swap_dev_icons uses for mac/iOS.
    """
    from .icons import generate_tv_icons, apply_tv_dev_banner

    icon_kwargs = _icon_generation_kwargs(cfg)
    has_glyph = bool(icon_kwargs.get("glyph_text") or icon_kwargs.get("glyph_svg"))

    def _post():
        restore_tv_icons(brandassets_dir, backup_dir)

    cleanup_registered = [False]

    def _pre():
        _restore_stranded_tv_icons(brandassets_dir, backup_dir)
        if restore_on_exit and not cleanup_registered[0]:
            register_cleanup(_post)
            cleanup_registered[0] = True
        backup_tv_icons(brandassets_dir, backup_dir)
        if has_glyph:
            info("Generating dev tv icons...")
            generate_tv_icons(brandassets_dir, dev=True, **icon_kwargs)
        else:
            info("Generating dev tv icons...")
            apply_tv_dev_banner(brandassets_dir)

    return _pre, _post


def build_icon_hooks(
    cfg, platforms=("mac", "ios"), backup_dir="tmp/icon-backup", restore_on_exit=True
):
    """Return (pre_build, post_build) for dev-icon swap. (None, None) if
    [apple.icons] has no mac/iOS/tv asset dirs configured, so callers can pass
    directly to dev_loop_*.

    pre_build also registers post_build as a cleanup hook — required so a
    Ctrl-C'd dev run doesn't leak dev-overlay icons into the next deploy build.
    An ``[apple.icons] mac_icon`` (Xcode 26 Icon Composer) routes the mac/iOS
    side through the ``.icon`` banner swap; otherwise the appiconset PNG swap
    is used. ``tv_asset_dir`` is independent of that choice and always goes
    through the tvOS `.brandassets` layered-stack swap when configured, so a
    project can mix Icon Composer for mac with a tvOS brandassets catalog.

    ``restore_on_exit=False`` skips that registration, and exists for exactly
    one caller: ``admin icons swap-dev``, where the swap IS the whole command.
    A dev loop swaps, builds, and restores inside one process, so the exit
    cleanup is what makes it safe; the standalone verb has no build in between,
    so the same cleanup fires the instant it finishes and silently undoes what
    the user just asked for — it printed "dev icons swapped in" over a catalog
    it had already put back.
    """
    hook_pairs = []

    icon_composer = _resolve_icon_composer(cfg)
    if icon_composer:
        hook_pairs.append(_build_icon_composer_hooks(
            cfg, icon_composer, backup_dir, restore_on_exit=restore_on_exit
        ))
    else:
        icon_dirs = _resolve_icon_dirs(cfg, platforms)
        if icon_dirs:
            hook_pairs.append(_build_appiconset_hooks(
                cfg, icon_dirs, platforms, backup_dir, restore_on_exit=restore_on_exit
            ))

    tv_dir = _resolve_icon_dirs(cfg, ("tv",)).get("tv")
    if tv_dir:
        hook_pairs.append(_build_tv_icon_hooks(
            cfg, tv_dir, _tv_icon_backup_dir(backup_dir), restore_on_exit=restore_on_exit
        ))

    if not hook_pairs:
        return (None, None)

    def _pre():
        for pre, _post in hook_pairs:
            pre()

    def _post():
        for _pre, post in hook_pairs:
            post()

    return _pre, _post


def patch_ios_display_name(app_path, display_name):
    """Patch CFBundleDisplayName in a built iOS .app and re-sign with the
    original identity. Used by iOS dev loops to give the dev install a distinct
    home-screen name without using the unsafe INFOPLIST_KEY_CFBundleDisplayName
    xcodebuild flag (which corrupts SwiftPM bundles). No-op when display_name is falsy.
    """
    if not display_name:
        return
    info_plist = os.path.join(app_path, "Info.plist")
    if not os.path.isfile(info_plist):
        err(f"Info.plist not found at {info_plist} — cannot patch display name")
        sys.exit(1)

    rc = subprocess.run(
        ["plutil", "-replace", "CFBundleDisplayName", "-string", display_name, info_plist],
        capture_output=True, text=True,
    )
    if rc.returncode != 0:
        err(f"plutil patch failed: {rc.stderr.strip()}")
        sys.exit(1)

    # Recover the original signing identity so the re-signed app still
    # installs on device. codesign -dvvv writes to stderr; "Authority="
    # lines are listed leaf → root, so the first one is the leaf identity
    # we want. Fall back to ad-hoc ("-") if codesign reports none.
    sig = subprocess.run(
        ["codesign", "-dvvv", app_path], capture_output=True, text=True,
    )
    identity = "-"
    for line in sig.stderr.splitlines():
        if line.startswith("Authority="):
            identity = line.split("=", 1)[1].strip()
            break

    rc = subprocess.run(
        ["codesign", "--force", "--sign", identity,
         "--preserve-metadata=entitlements,requirements", app_path],
        capture_output=True, text=True,
    )
    if rc.returncode != 0:
        err(f"resign after display-name patch failed: {rc.stderr.strip()}")
        err("App will not install on device with broken signature; aborting")
        sys.exit(1)
    info(f'Display name → "{display_name}"')


def _restore_stranded_appiconset_dev_icons(cfg, platforms, backup_dir):
    """mac/iOS appiconset half of restore_stranded_dev_icons — see there."""
    if not os.path.isdir(backup_dir):
        return
    icon_dirs = _resolve_icon_dirs(cfg, platforms)
    if not icon_dirs:
        return
    warn(f"Stranded dev icons found in {backup_dir} (interrupted dev run?) — restoring originals")
    restore_icons(icon_dirs, backup_dir)


def restore_stranded_dev_icons(cfg, platforms=("mac", "ios"), backup_dir="tmp/icon-backup"):
    """If a previous dev run was interrupted before its post-build restore,
    its dev-overlay PNGs are still sitting in the asset catalog(s). Recover
    the originals from ``backup_dir`` (left behind by the interrupted run)
    before a fresh build so we don't bake DEV-banner icons into a production
    deploy. The mac/iOS side (Icon Composer or flat appiconset) and the tv
    `.brandassets` side are independent — a project can have either, both,
    or neither configured, so both are checked unconditionally.
    """
    icon_composer = _resolve_icon_composer(cfg)
    if icon_composer:
        _restore_stranded_icon_composer(icon_composer, os.path.join(backup_dir, "icon-composer"))
    else:
        _restore_stranded_appiconset_dev_icons(cfg, platforms, backup_dir)

    tv_dir = _resolve_icon_dirs(cfg, ("tv",)).get("tv")
    if tv_dir:
        _restore_stranded_tv_icons(tv_dir, _tv_icon_backup_dir(backup_dir))


def _resolve_mac_dev_bundle(cfg):
    return _resolve_bundle(cfg, "mac", dev=True)


def _resolve_ios_dev_bundle(cfg):
    return _resolve_bundle(cfg, "ios", dev=True)


def _resolve_mac_dev_product_name(cfg):
    """Renamed dev ``.app`` filename. Default: ``<process>Dev`` (no space —
    must be a valid filename and bundle path component)."""
    base = _resolve_mac_process(cfg)
    return cfg.get("mac_dev_product_name") or (f"{base}Dev" if base else None)


def _resolve_mac_dev_display_name(cfg):
    """Dock / Launchpad label for the dev install. Default: ``<process> Dev``."""
    base = _resolve_mac_process(cfg)
    return cfg.get("mac_dev_display_name") or (f"{base} Dev" if base else None)


def _resolve_sim_dev_display_name(cfg, platform):
    """Home-screen label for a simulator platform's dev install.
    Default: ``<process> Dev``.
    """
    base = _resolve_sim_process(cfg, platform)
    return cfg.get(f"{platform}_dev_display_name") or (f"{base} Dev" if base else None)


def _resolve_ios_dev_display_name(cfg):
    """Home-screen label for the iOS dev install. Default: ``<process> Dev``."""
    return _resolve_sim_dev_display_name(cfg, "ios")


def _resolve_mac_dev_process(cfg, install=False):
    """``pgrep -f`` pattern that uniquely identifies the running dev
    instance (so it isn't confused with prod). Default:
    ``<DevProductName>.app/Contents/MacOS/<process>``.

    The pattern has to name the bundle the loop actually launches, or it matches
    nothing: the loop never finds its own app to kill and reports that it never
    launched, while ``open -n`` stacks a new window every rebuild. Two ways to
    end up there, both handled here:

    * ``mac_dev_rename = false`` — no ``<name>Dev.app`` is ever produced, so the
      app runs straight out of the build-products directory.
    * ``install=True`` (``dev --install``) — the app is copied to
      ``mac_install_path_dev`` first and launched from there.

    Neither pattern can collide with a prod copy under ``/Applications``.
    """
    explicit = cfg.get("mac_dev_process")
    if explicit:
        return explicit
    binary = _resolve_mac_process(cfg)
    if not binary:
        return None
    if install:
        dev_install_path = cfg.get("mac_install_path_dev")
        if dev_install_path:
            return f"{dev_install_path.rstrip('/')}/Contents/MacOS/{binary}"
    if not cfg.get("mac_dev_rename", True):
        config = cfg.get("mac_build_config", "Debug")
        return f"/Build/Products/{config}/{binary}.app/Contents/MacOS/{binary}"
    product = _resolve_mac_dev_product_name(cfg)
    return f"{product}.app/Contents/MacOS/{binary}" if product else None


def _make_mac_dev_running_check(cfg, install=False):
    """Return a callable that reports whether the dev mac instance is running.

    Uses ``pgrep -f <dev_name>`` where dev_name (e.g.
    ``CCToolDev.app/Contents/MacOS/CCTool``) is itself the disambiguator —
    the prod build lives under ``/Applications/CCTool.app/...`` so the
    pattern won't match it. Falls back to ``pgrep -x mac_process`` if no
    dev product name is configured.
    """
    dev_name = _resolve_mac_dev_process(cfg, install=install)
    prod_name = _resolve_mac_process(cfg)

    if dev_name:
        def _check():
            r = subprocess.run(["pgrep", "-f", dev_name], capture_output=True)
            return r.returncode == 0
        return _check

    if prod_name:
        def _check():
            r = subprocess.run(["pgrep", "-x", prod_name], capture_output=True)
            return r.returncode == 0
        return _check

    return lambda: False


def _make_mac_dev_kill(cfg, install=False):
    """Return a callable that kills only the dev instance (not prod), then waits
    for it to actually exit. SIGTERM first; escalates to SIGKILL if still alive
    after ~1.5s. Returning before the process is gone causes ``open -n`` to race
    with LaunchServices and silently activate the dying instance.
    """
    import time
    dev_name = _resolve_mac_dev_process(cfg, install=install)
    prod_name = _resolve_mac_process(cfg)
    check_running = _make_mac_dev_running_check(cfg, install=install)

    if dev_name:
        def _send(sig):
            r = subprocess.run(["pgrep", "-f", dev_name],
                               capture_output=True, text=True)
            for pid_str in r.stdout.split():
                try:
                    os.kill(int(pid_str), sig)
                except (ProcessLookupError, ValueError):
                    pass

        def _kill():
            if not check_running():
                return
            _send(signal.SIGTERM)
            for _ in range(15):
                if not check_running():
                    return
                time.sleep(0.1)
            warn(f"{dev_name} ignored SIGTERM, sending SIGKILL")
            _send(signal.SIGKILL)
            for _ in range(10):
                if not check_running():
                    return
                time.sleep(0.1)
            warn(f"{dev_name} still alive after SIGKILL — proceeding anyway")
        return _kill

    if prod_name:
        def _kill():
            if subprocess.run(["pgrep", "-x", prod_name], capture_output=True).returncode != 0:
                return
            subprocess.run(["pkill", "-x", prod_name], capture_output=True)
            for _ in range(15):
                if subprocess.run(["pgrep", "-x", prod_name], capture_output=True).returncode != 0:
                    return
                time.sleep(0.1)
            warn(f"{prod_name} ignored SIGTERM, sending SIGKILL")
            subprocess.run(["pkill", "-9", "-x", prod_name], capture_output=True)
        return _kill

    return lambda: None


def resolve_signing_identity(cfg):
    """Resolve a concrete code-signing identity (SHA-1 hash) by querying the
    keychain with ``security find-identity``, so callers never hardcode a hash.

    The optional ``[apple].sign_identity`` is a case-insensitive *substring*
    matched against the identity descriptions (e.g. a developer/team name, a
    team ID, or ``"Apple Development"``). With no selector — or an empty one —
    and exactly one valid identity, that identity is auto-picked. Returns the
    hash, or ``None`` (caller falls back to ad-hoc) when nothing matches or the
    match is ambiguous, warning loudly so a silent ad-hoc downgrade is visible.

    Signing with a stable identity instead of ad-hoc is what lets macOS keep
    TCC privacy grants (camera, Documents, …) across rebuilds rather than
    re-prompting — ad-hoc gives the binary a fresh code hash every build.
    """
    import re as _re
    selector = cfg.get("sign_identity")
    try:
        r = subprocess.run(
            ["security", "find-identity", "-v", "-p", "codesigning"],
            capture_output=True, text=True,
        )
    except (OSError, ValueError):
        warn("could not run `security find-identity` — using ad-hoc signing")
        return None

    ids = []
    for line in r.stdout.splitlines():
        m = _re.match(r'^\s*\d+\)\s+([0-9A-Fa-f]{40})\s+"(.+)"\s*$', line)
        if m:
            ids.append((m.group(1), m.group(2)))

    matched = ids
    if selector:
        sel = str(selector).lower()
        matched = [(h, d) for (h, d) in ids if sel in d.lower()]

    if len(matched) == 1:
        return matched[0][0]
    if not matched:
        hint = f" matching {selector!r}" if selector else ""
        warn(f"no code-signing identity{hint} found — using ad-hoc signing "
             "(macOS will re-prompt for permissions on each rebuild)")
        return None
    names = ", ".join(d for _, d in matched)
    warn("multiple code-signing identities match — set [apple].sign_identity to "
         f"a unique substring to disambiguate: {names}. Using ad-hoc.")
    return None


def _compose_sign_settings(cfg, platform=None):
    """Build xcodebuild build-setting overrides for code signing from [apple] config.

    Reads code_sign_identity, development_team, code_sign_style. Resolves
    ${VAR} / ${VAR:-default} from the environment. Additionally, when
    ``sign_identity`` is present (and no explicit ``code_sign_identity``), the
    identity is resolved dynamically from the keychain (see
    :func:`resolve_signing_identity`) and signed manually — no hash in config.
    Returns empty string when none of the keys are present.

    ``platform``: "ios" | "tv" | "mac" | None. Device platforms always need a
    provisioning profile (unlike mac, which can run manually-signed with no
    profile at all) — see :func:`_target_needs_provisioning_profile`.
    """
    from .core import resolve_env
    parts = []
    for manifest_key, xcode_key in (
        ("code_sign_identity", "CODE_SIGN_IDENTITY"),
        ("development_team",   "DEVELOPMENT_TEAM"),
        ("code_sign_style",    "CODE_SIGN_STYLE"),
    ):
        val = cfg.get(manifest_key)
        if val is None:
            continue
        val = resolve_env(str(val))
        if val:
            parts.append(f'{xcode_key}="{val}"')

    # Dynamic identity: resolve [apple].sign_identity against the keychain to a
    # concrete hash and sign manually, so dev builds get a stable signature
    # (TCC grants persist) without a hardcoded hash. Skipped if the manifest
    # already pinned an explicit literal CODE_SIGN_IDENTITY above.
    if "sign_identity" in cfg and cfg.get("code_sign_identity") is None:
        ident = resolve_signing_identity(cfg)
        if ident and _target_needs_provisioning_profile(cfg, platform):
            # Automatic signing refuses a hash: "iptv-mac is automatically
            # signed, but code signing identity <hash> has been manually
            # specified". It accepts the identity by *name*, which is what
            # sign_identity holds anyway ("Apple Development"), and which still
            # pins the certificate family the TCC grants are keyed to.
            parts.append(f'CODE_SIGN_IDENTITY="{resolve_env(str(cfg["sign_identity"]))}"')
        elif ident:
            parts.append(f'CODE_SIGN_IDENTITY="{ident}"')
            # Manual signing ONLY when the app needs no provisioning profile.
            #
            # `CODE_SIGN_STYLE=Manual` with an empty PROVISIONING_PROFILE_SPECIFIER
            # tells xcodebuild to sign with an existing profile and never to make
            # one — so `-allowProvisioningUpdates` and an App Store Connect API key
            # are both ignored, and a target that declares any capability fails
            # with a profile error naming that capability. Which reads exactly like
            # the capability is missing from the App ID, and sends you to the
            # developer portal to check something that was already right.
            #
            # An app with entitlements beyond the sandbox — iCloud, push, App
            # Groups — must therefore keep automatic signing. The stable identity
            # this block exists for (so macOS TCC grants survive a rebuild) is
            # still pinned above; only the style is left alone.
            parts.append("CODE_SIGN_STYLE=Manual")
            parts.append("PROVISIONING_PROFILE_SPECIFIER=")

    return " ".join(parts)


def _target_needs_provisioning_profile(cfg, platform=None):
    """True when this target cannot be signed and installed without a real,
    creatable-or-refreshable provisioning profile.

    Every iOS/tvOS device install needs one unconditionally — there is no
    "no profile at all" path on those platforms, unlike a locally-run mac
    build. So platform in ("ios", "tv") short-circuits to True here; forcing
    manual signing with an empty PROVISIONING_PROFILE_SPECIFIER on a device
    build fails every time with "requires a provisioning profile", which
    reads exactly like a missing capability rather than a signing-style bug.

    For mac (or when platform is unspecified, for backward compatibility),
    read from the entitlements files the manifest points at, because that is
    where the requirement actually lives there. The sandbox and its network
    client are not in this class — they are granted by the entitlement alone
    — so a plain sandboxed Mac app keeps the stable manual identity it had
    before.
    """
    if platform in ("ios", "tv"):
        return True

    keys_needing_a_profile = (
        "com.apple.developer.",       # iCloud, App Groups, push, HealthKit, …
        "aps-environment",
    )
    for path in _entitlements_paths(cfg):
        try:
            with open(path, "r", errors="replace") as f:
                text = f.read()
        except OSError:
            # Say so. An unreadable entitlements file makes this function
            # answer False, which forces manual signing and fails the build
            # with a message naming a capability — pointing at the developer
            # portal instead of at the path that could not be read.
            warn(f"entitlements file not readable, assuming no profile needed: {path}")
            continue
        if any(k in text for k in keys_needing_a_profile):
            return True
    return False


def _entitlements_paths(cfg):
    """Every entitlements file this project might sign with, as absolute paths.

    Sourced from the Xcode project rather than the manifest: admin.toml has no
    entitlements key, and adding one would be a second place to keep the answer
    correct. The pbxproj already knows.

    CODE_SIGN_ENTITLEMENTS is relative to SRCROOT — the directory holding the
    .xcodeproj — NOT to the repo root. Those are the same directory only when
    the project sits at the top of the repo. Resolving against the repo root
    instead silently produced a nonexistent path for any project nested one
    level down (open-assistant keeps its .xcodeproj in hermes-companion/), the
    caller's `except OSError: continue` swallowed the miss, and
    _target_needs_provisioning_profile then answered False for an app that
    declares iCloud. The visible result was `admin deploy mac` failing with
    "requires a provisioning profile with the iCloud feature" while the App ID
    had the capability all along — the exact wrong-thing-to-check this
    function exists to prevent.

    SRCROOT is taken from _detect_project_path rather than a build setting:
    _xcode_target_settings reads the pbxproj directly, and SRCROOT is supplied
    by xcodebuild at build time, so it never appears there.
    """
    srcroot = os.path.dirname(_detect_project_path(cfg)) or get_project_root()
    seen = []
    for scheme_key in ("mac_scheme", "ios_scheme", "tv_scheme"):
        scheme = cfg.get(scheme_key)
        if not scheme:
            continue
        value = _xcode_setting(cfg, scheme, "CODE_SIGN_ENTITLEMENTS")
        if not value:
            continue
        path = value if os.path.isabs(value) else os.path.join(srcroot, value)
        if path not in seen:
            seen.append(path)
    return seen


def _compose_dev_settings(cfg, platform):
    """Combine cross-platform `dev_build_settings` with ADMIN_DEV_BUNDLE_SUFFIX
    (appended last so it wins). Never emits PRODUCT_BUNDLE_IDENTIFIER: a
    setting passed through xcodebuild_filtered's extra_settings applies to
    every target in the project (see its docstring above, xcodebuild_filtered),
    so naming one bundle ID there forces an app-extension target to claim
    its parent app's identity and the simulator refuses to install it.

    Instead each target's own PRODUCT_BUNDLE_IDENTIFIER in the pbxproj must
    reference ``$(ADMIN_DEV_BUNDLE_SUFFIX)`` (e.g.
    ``com.foo.app$(ADMIN_DEV_BUNDLE_SUFFIX)`` for the app,
    ``com.foo.app$(ADMIN_DEV_BUNDLE_SUFFIX).widgets`` for its widget) so each
    target derives its own suffixed identity and none can collide. The
    suffix is derived by subtracting the resolved prod bundle ID from the
    resolved dev one — normally ``.dev`` from the ``<prod>.dev`` default.

    Hard errors (exits) rather than warning, because under this scheme a bad
    value doesn't just misconfigure a nice-to-have — it silently glues the
    old broken behavior back on:
    - ``dev_build_settings`` containing PRODUCT_BUNDLE_IDENTIFIER — exactly
      the string that breaks extensions.
    - an explicit ``{platform}_dev_bundle`` that isn't the prod bundle ID
      plus a suffix — unexpressible as a suffix, so falling back to the old
      global override would reintroduce the bug silently.
    """
    extras = (cfg.get("dev_build_settings") or "").strip()
    if "PRODUCT_BUNDLE_IDENTIFIER" in extras:
        err(
            "apple.dev_build_settings contains PRODUCT_BUNDLE_IDENTIFIER — a "
            "setting passed to xcodebuild this way applies to every target "
            "in the project, so it forces an app extension's bundle ID to "
            "match its parent app's and the simulator refuses to install "
            "it. Remove it — admin derives ADMIN_DEV_BUNDLE_SUFFIX from "
            f"{platform}_dev_bundle instead; see the apple archetype docs."
        )
        sys.exit(1)

    prod = _resolve_bundle(cfg, platform)
    dev_bundle_id = _resolve_bundle(cfg, platform, dev=True)
    suffix = ""
    if dev_bundle_id:
        if not prod or not dev_bundle_id.startswith(prod):
            err(
                f"apple.{platform}_dev_bundle ({dev_bundle_id!r}) is not the "
                f"prod {platform}_bundle ({prod!r}) plus a suffix — "
                "ADMIN_DEV_BUNDLE_SUFFIX can only express <prod bundle ID> + "
                f"<suffix>. Drop the explicit {platform}_dev_bundle key to "
                "default to <prod>.dev, or set it to the prod bundle ID "
                "with a suffix appended."
            )
            sys.exit(1)
        suffix = dev_bundle_id[len(prod):]

    override = f"ADMIN_DEV_BUNDLE_SUFFIX={suffix}" if suffix else ""
    result = f"{extras} {override}".strip() if extras else override
    sign = _compose_sign_settings(cfg, platform)
    parts = [p for p in [result, sign] if p]
    return " ".join(parts)


def _derived_glob(scheme, config, platform_subdir, product_name):
    """Glob pattern under ~/Library/Developer/Xcode/DerivedData for a built .app."""
    return os.path.expanduser(
        f"~/Library/Developer/Xcode/DerivedData/{scheme}-*"
        f"/Build/Products/{config}{platform_subdir}/{product_name}.app"
    )


def dev_mac(cfg, *, log_level="app", clean=False, install=False, output_config=None,
           launch_args=None):
    """macOS dev loop driven by [apple] config. Swaps dev icons, builds, launches.

    launch_args: optional list of strings passed to the app on each launch
    (as `open -n <app> --args ...`). Falls back to apple.launch_args in
    admin.toml when not given explicitly.
    """
    if output_config is None:
        output_config = globals().get("_OUTPUT_CONFIG")
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    cfg = _with_global_defaults(cfg, "apple")
    if launch_args is None:
        launch_args = cfg.get("launch_args")
    scheme = cfg.get("mac_scheme")
    if not scheme:
        err("apple.mac_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("mac_build_config", "Debug")
    process = _resolve_mac_process(cfg)
    dev_process = _resolve_mac_dev_process(cfg, install=install) or process
    bundle_id = _resolve_bundle(cfg, "mac", dev=True)
    subsystem = _resolve_subsystem(cfg, "mac", dev=True)
    extra_settings = _compose_dev_settings(cfg, "mac")
    build_action = "clean build" if clean else "build"
    # os_log's `process` field is the executable's name, never a path, so the
    # pgrep pattern in dev_process can't be handed to it — a bundle-path pattern
    # matches no log line and the predicate silently narrows to its subsystem
    # half. The dev rename leaves the executable name alone, so prod and dev
    # both log under `process`.
    predicate = log_predicate(process, subsystem, level=log_level)
    app_pattern = _derived_glob(_resolve_derived_data_name(cfg), config, "", process)

    pre_build, post_build = build_icon_hooks(cfg)

    # Two independent things can happen to the built app before it is launched:
    # it can be renamed to the dev name, and it can be installed to a fixed
    # path. dev_loop_macos has one pre_launch slot, and these two used to sit in
    # the two halves of an if/else competing for it — so `dev --install`
    # silently turned the dev rename OFF. The dev build installed under the
    # production name, with the production name in the menu bar, and nothing
    # said so. They are composed now: rename first, install what came out.
    _dev_product = _resolve_mac_dev_product_name(cfg)
    _dev_display = _resolve_mac_dev_display_name(cfg)
    _rename = None
    # Opt out of the dev rename/display-patch with mac_dev_rename = false — for
    # projects that already differentiate their debug build at the Xcode level
    # (distinct bundle ID, CFBundleDisplayName) and don't want a renamed
    # "<name>Dev.app". Stable signing still applies via the build's CODE_SIGN
    # flags; the rename is purely a Finder/Dock cosmetic.
    if cfg.get("mac_dev_rename", True) and (_dev_product or _dev_display):
        # Re-sign the renamed .app with the same stable identity the build
        # used (not ad-hoc), so TCC grants survive the rename. "-" → ad-hoc
        # when no sign_identity is configured (unchanged default).
        _resign = (resolve_signing_identity(cfg) if "sign_identity" in cfg else None) or "-"

        def _rename(built_app):
            return dev_macos_postprocess(built_app, _dev_product, _dev_display, sign_identity=_resign)

    _installer = None
    if install:
        dev_install_path = cfg.get("mac_install_path_dev")
        if not dev_install_path:
            err("--install requires apple.mac_install_path_dev in admin.toml")
            sys.exit(1)

        def _installer(app_path):
            info(f"Installing to {dev_install_path}")
            if os.path.exists(dev_install_path):
                shutil.rmtree(dev_install_path)
            shutil.copytree(app_path, dev_install_path)
            return dev_install_path

    if _rename and _installer:
        def pre_launch(built_app):
            return _installer(_rename(built_app))
    else:
        pre_launch = _rename or _installer

    dev_loop_macos(
        project=project, scheme=scheme, config=config,
        destination="platform=macOS",
        process=dev_process, subsystem=subsystem,
        predicate=predicate, app_pattern=app_pattern,
        extra_settings=extra_settings, build_action=build_action,
        pre_build=pre_build, post_build=post_build, pre_launch=pre_launch,
        is_running_fn=_make_mac_dev_running_check(cfg, install=install),
        kill_fn=_make_mac_dev_kill(cfg, install=install),
        output_config=output_config,
        # An icon swap is active iff build_icon_hooks returned hooks; refresh
        # the built app's icon cache so the swapped-in banner actually shows.
        refresh_icon=pre_build is not None,
        launch_args=launch_args,
    )


def dev_simulator(cfg, platform="ios", *, log_level="app", clean=False,
                  output_config=None, launch_args=None):
    """Simulator dev loop driven by [apple] config, for iOS or tvOS.

    Every config key is read through the platform prefix — ``ios_scheme`` or
    ``tv_scheme``, ``ios_device`` or ``tv_device`` — and the three values that
    genuinely differ between the platforms come from SIM_PLATFORMS.

    launch_args: optional list of strings forwarded to xcrun simctl launch
    so the running app receives them as launch arguments.
    """
    plat = _sim_platform(platform)
    if output_config is None:
        output_config = globals().get("_OUTPUT_CONFIG")
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    cfg = _with_global_defaults(cfg, "apple")
    scheme = cfg.get(f"{platform}_scheme")
    if not scheme:
        err(f"apple.{platform}_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get(f"{platform}_build_config", "Debug")
    process = _resolve_sim_process(cfg, platform)
    bundle_id = _resolve_bundle(cfg, platform, dev=True)
    subsystem = _resolve_subsystem(cfg, platform, dev=True)
    extra_settings = _compose_dev_settings(cfg, platform)
    build_action = "clean build" if clean else "build"
    predicate = log_predicate(process, subsystem, level=log_level)
    app_pattern = _derived_glob(_resolve_derived_data_name(cfg), config,
                                plat["sdk_suffix"], process)

    sim_id = None
    device_name = os.environ.get(plat["device_env"]) or cfg.get(plat["device_key"])
    if device_name:
        sim_id = find_simulator(device_name)
        if not sim_id:
            warn(f"simulator '{device_name}' not found, falling back to auto-resolve")
    if sim_id is None:
        if not device_name:
            info("No simulator pinned — auto-resolving.")
        info(f"Pin one: set {plat['device_env']} env var, or set {plat['device_key']} "
             "in [apple] of your project or ~/.admin/admin.toml")

    pre_build, post_build = build_icon_hooks(cfg)

    dev_loop_simulator(
        project=project, scheme=scheme, config=config,
        subsystem=subsystem, process=process, predicate=predicate,
        app_pattern=app_pattern, extra_settings=extra_settings,
        build_action=build_action, bundle_id=bundle_id, sim_id=sim_id,
        pre_build=pre_build, post_build=post_build,
        display_name=_resolve_sim_dev_display_name(cfg, platform),
        output_config=output_config,
        launch_args=launch_args,
        platform=platform,
    )


def dev_ios(cfg, *, log_level="app", clean=False, output_config=None, launch_args=None):
    """iOS simulator dev loop driven by [apple] config."""
    dev_simulator(cfg, "ios", log_level=log_level, clean=clean,
                  output_config=output_config, launch_args=launch_args)


def dev_tv(cfg, *, log_level="app", clean=False, output_config=None, launch_args=None):
    """tvOS simulator dev loop driven by [apple] config."""
    dev_simulator(cfg, "tv", log_level=log_level, clean=clean,
                  output_config=output_config, launch_args=launch_args)


def deploy_mac(cfg, *, clean=False):
    """Build and install a production macOS app to /Applications. No icon swap."""
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    cfg = _with_global_defaults(cfg, "apple")
    scheme = cfg.get("mac_scheme")
    if not scheme:
        err("apple.mac_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("mac_deploy_config", "Release")
    process = _resolve_mac_process(cfg)
    install_path = _resolve_mac_install_path_prod(cfg)
    build_action = "clean build" if clean else "build"
    derived_name = _resolve_derived_data_name(cfg)

    restore_stranded_dev_icons(cfg)

    info(f"Killing any running {process}...")
    subprocess.run(["pkill", "-x", process], capture_output=True)

    info(f"Building {scheme} ({config})...")
    rc = xcodebuild_filtered(project, scheme, config, "platform=macOS",
                             action=build_action,
                             extra_settings=_compose_sign_settings(cfg, "mac"))
    if rc != 0:
        err("Build failed"); sys.exit(rc)

    app_path = find_app(_derived_glob(derived_name, config, "", process))
    if not app_path:
        err("Could not find built app"); sys.exit(1)

    info(f"Installing to {install_path}")
    if os.path.exists(install_path):
        shutil.rmtree(install_path)
    shutil.copytree(app_path, install_path)

    # `open` pulls the app to the front and takes focus off whatever the
    # developer was doing. That is what you want when deploying is the last step
    # of a session, and not what you want when it is a step in the middle of one
    # — a project whose deploy also installs binaries, or which is deployed on a
    # loop, sets mac_deploy_launch = false in [apple] and gets its focus back.
    if cfg.get("mac_deploy_launch", True):
        info(f"Launching {install_path}")
        subprocess.run(["open", install_path], check=True)
    ok(f"deploy mac → {install_path}")


def ensure_device_registered(udid, name):
    """Make sure the team knows this device before a signed build asks Apple
    for a profile that has to include it.

    A device Apple has never seen fails the build with "Your team has no
    devices from which to generate a provisioning profile" — a message about
    the team, not about the device sitting on the desk, which is why plugging
    in a second phone or pairing an Apple TV used to mean a trip to the
    developer portal. `-allowProvisioningUpdates` creates profiles but never
    registers hardware, so the API key has to do it explicitly.

    Best-effort: with no API key, or on any API failure, this warns and returns
    so the build still runs and reports Apple's own error.
    """
    from .appstoreconnect import (
        AppStoreConnectError, credentials, enable_device, find_device,
        missing_credential_reason, register_device,
    )

    if not udid:
        return
    if credentials() is None:
        warn(f"Skipping device registration — App Store Connect key {missing_credential_reason()}")
        return
    try:
        existing = find_device(udid)
        if existing is None:
            info(f"Registering {name} ({udid}) with the team...")
            register_device(udid, name)
            ok(f"{name} registered")
            return
        if existing.get("attributes", {}).get("status") == "DISABLED":
            info(f"{name} is registered but disabled — re-enabling...")
            enable_device(existing["id"])
            ok(f"{name} enabled")
    except AppStoreConnectError as e:
        warn(f"Could not register {name} with the team: {e}")


def deploy_device(cfg, platform="ios", *, clean=False):
    """Build and install a production app on a connected iOS or tvOS device.

    Every value that differs between an iPhone and an Apple TV comes from the
    platform prefix (``ios_scheme`` / ``tv_scheme``, ``ios_deploy_config`` /
    ``tv_deploy_config``) or the DEVICE_PLATFORMS row. No icon swap.
    """
    plat = _device_platform(platform)
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    cfg = _with_global_defaults(cfg, "apple")
    scheme = cfg.get(f"{platform}_scheme")
    if not scheme:
        err(f"apple.{platform}_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get(f"{platform}_deploy_config", "Release")
    process = _resolve_sim_process(cfg, platform)
    bundle_id = _resolve_bundle(cfg, platform)
    build_action = "clean build" if clean else "build"
    derived_name = _resolve_derived_data_name(cfg)

    restore_stranded_dev_icons(cfg)

    device_id, device_name, udid = find_connected_device(platform, cfg)
    info(f"Deploying to {device_name}")
    ensure_device_registered(udid, device_name)

    info(f"Building {scheme} for device ({config})...")
    rc = xcodebuild_filtered(project, scheme, config, plat["destination"],
                             action=build_action,
                             extra_settings=_compose_sign_settings(cfg, platform))
    if rc != 0:
        diagnose_build_failure()
        err("Build failed"); sys.exit(rc)

    app_path = find_app(_derived_glob(derived_name, config, plat["sdk_suffix"], process))
    if not app_path:
        err("Could not find built app"); sys.exit(1)

    info(f"Installing to device {device_name}...")
    rc = install_on_device(device_id, app_path)
    if rc != 0:
        warn("Install failed — retrying once...")
        rc = install_on_device(device_id, app_path)
        if rc != 0:
            err("Install failed after retry.")
            if platform == "tv":
                err("Keep the Apple TV awake and on the same network as this Mac.")
            else:
                err("Try plugging in via USB cable for a more reliable connection.")
            sys.exit(rc)

    if bundle_id:
        info(f"Launching {bundle_id}")
        launch_on_device(device_id, bundle_id)
    ok(f"deploy {platform} → {device_name}")


def deploy_ios(cfg, *, clean=False):
    """Build and install a production iOS app on a connected device (USB or wireless)."""
    deploy_device(cfg, "ios", clean=clean)


def deploy_tv(cfg, *, clean=False):
    """Build and install a production tvOS app on a paired Apple TV (network)."""
    deploy_device(cfg, "tv", clean=clean)


def dev_device(cfg, *, log_level="app", clean=False, output_config=None):
    """iOS physical-device dev loop driven by [apple] config.

    Builds for iphoneos, installs via devicectl, launches with --console so
    os_log output is non-redacted. Blocks until the app exits or Ctrl+C.
    """
    if output_config is None:
        output_config = globals().get("_OUTPUT_CONFIG")
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    cfg = _with_global_defaults(cfg, "apple")
    scheme = cfg.get("ios_scheme")
    if not scheme:
        err("apple.ios_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("ios_build_config", "Debug")
    process = _resolve_ios_process(cfg)
    bundle_id = _resolve_bundle(cfg, "ios", dev=True)
    subsystem = _resolve_subsystem(cfg, "ios", dev=True)
    extra_settings = _compose_dev_settings(cfg, "ios")
    build_action = "clean build" if clean else "build"
    app_pattern = _derived_glob(_resolve_derived_data_name(cfg), config, "-iphoneos", process)

    pre_build, post_build = build_icon_hooks(cfg)

    dev_loop_device(
        project=project, scheme=scheme, config=config,
        subsystem=subsystem, process=process,
        app_pattern=app_pattern, extra_settings=extra_settings,
        build_action=build_action, bundle_id=bundle_id,
        pre_build=pre_build, post_build=post_build,
        display_name=_resolve_ios_dev_display_name(cfg),
        output_config=output_config,
    )


def build_mac(cfg, *, force=False):
    """Build macOS client from [apple] config (no run, no install)."""
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    cfg = _with_global_defaults(cfg, "apple")
    scheme = cfg.get("mac_scheme")
    if not scheme:
        err("apple.mac_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("mac_build_config", "Debug")
    build_action = "clean build" if force else "build"
    restore_stranded_dev_icons(cfg)
    info(f"Building {scheme} ({config}) for macOS...")
    rc = xcodebuild_filtered(project, scheme, config, "platform=macOS",
                             action=build_action,
                             extra_settings=_compose_sign_settings(cfg, "mac"))
    if rc != 0:
        diagnose_build_failure()
        err("Build failed"); sys.exit(rc)
    ok("macOS build succeeded")


def build_simulator(cfg, platform="ios", *, force=False):
    """Build an iOS or tvOS client from [apple] config (no run, no install)."""
    plat = _sim_platform(platform)
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    cfg = _with_global_defaults(cfg, "apple")
    scheme = cfg.get(f"{platform}_scheme")
    if not scheme:
        err(f"apple.{platform}_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get(f"{platform}_build_config", "Debug")
    sim = os.environ.get(plat["device_env"]) or cfg.get(plat["device_key"]) or ""
    if sim:
        dest = f"platform={plat['destination']},name=" + sim
    else:
        # No simulator pinned — auto-resolve (booted first, then newest device).
        # The bare "platform=<X> Simulator" destination is ambiguous on machines
        # with multiple runtimes and causes xcodebuild to fail before compiling.
        sim_id, _ = resolve_simulator(platform)
        dest = f"platform={plat['destination']},id={sim_id}"
    build_action = "clean build" if force else "build"
    restore_stranded_dev_icons(cfg)
    info(f"Building {scheme} ({config}) for {plat['label']}...")
    rc = xcodebuild_filtered(project, scheme, config, dest, action=build_action,
                             extra_settings=_compose_sign_settings(cfg, platform))
    if rc != 0:
        diagnose_build_failure()
        err("Build failed"); sys.exit(rc)
    ok(f"{plat['label']} build succeeded")


def build_ios(cfg, *, force=False):
    """Build iOS client from [apple] config (no run, no install)."""
    build_simulator(cfg, "ios", force=force)


def build_tv(cfg, *, force=False):
    """Build tvOS client from [apple] config (no run, no install)."""
    build_simulator(cfg, "tv", force=force)


def get_ios_log_bundle(cfg, *, prod=False):
    """Return the iOS bundle ID for log streaming from [apple] config.

    prod=False → ios_dev_bundle, prod=True → ios_bundle.
    Calls sys.exit(1) if neither is configured.
    """
    bundle = _resolve_bundle(cfg, "ios") if prod else _resolve_bundle(cfg, "ios", dev=True)
    if not bundle:
        key = "ios_bundle" if prod else "ios_dev_bundle"
        err(f"apple.{key} not set in admin.toml and could not derive from project")
        sys.exit(1)
    return bundle


def clean_generated_icons(cfg, backup_dir="tmp/icon-backup"):
    """Reset asset catalog icons to a freshly-generated state from
    ``[apple.icons]``. Removes any stranded ``tmp/icon-backup/`` and
    regenerates the production PNGs from ``glyph_svg``/``glyph_text``,
    overwriting whatever's currently committed. The asset catalog is the
    source of truth, so this is the recovery path when the committed PNGs
    have drifted (e.g. an interrupted dev run got committed with the
    DEV-overlay icons baked in).
    """
    if os.path.isdir(backup_dir):
        info(f"removing stranded icon backup: {backup_dir}")
        shutil.rmtree(backup_dir)
    tv_backup_dir = _tv_icon_backup_dir(backup_dir)
    if os.path.isdir(tv_backup_dir):
        info(f"removing stranded tv icon backup: {tv_backup_dir}")
        shutil.rmtree(tv_backup_dir)
    regenerate_icons(cfg)


def _regenerate_appiconset_dir(label, src_dir, kwargs, dev):
    """Regenerate one flat appiconset directory in place, sizes taken from its
    own committed PNGs (or Contents.json for a not-yet-populated catalog).
    Returns True on success, False if skipped/failed. Shared by the prod and
    permanently-committed-dev paths in regenerate_icons.
    """
    from .icons import generate_icons

    try:
        from PIL import Image as _Img
    except ImportError:
        err("Pillow not installed — pip install pillow"); sys.exit(1)

    import tempfile

    if not os.path.isdir(src_dir):
        warn(f"{label}: {src_dir} not a directory — skipping")
        return False
    size_to_file = {}
    for fname in os.listdir(src_dir):
        if not fname.lower().endswith(".png"):
            continue
        try:
            with _Img.open(os.path.join(src_dir, fname)) as im:
                size_to_file[im.width] = fname
        except Exception:
            continue
    if not size_to_file:
        # A brand-new appiconset has a Contents.json and no PNGs yet, and
        # that is exactly when someone runs this: it is how the first set of
        # art gets made. Reading the sizes off existing PNGs cannot serve
        # that case, so fall back to what the catalog *declares* it wants.
        size_to_file = _sizes_from_contents_json(src_dir)
        if size_to_file:
            info(f"{label}: no PNGs yet — taking sizes from Contents.json")

    if not size_to_file:
        warn(
            f"{label}: {src_dir} declares no icons — it needs PNGs or a "
            "Contents.json listing filenames and sizes"
        )
        return False

    sizes = sorted(size_to_file.keys())
    tmp_out = tempfile.mkdtemp(prefix=f"prod-icons-{label}-")
    try:
        gen_kwargs = dict(kwargs)
        gen_kwargs["sizes"] = sizes
        gen_kwargs["output_dir"] = tmp_out
        gen_kwargs["dev"] = dev
        if not generate_icons(**gen_kwargs):
            err(f"{label}: icon generation failed")
            return False
        for size, orig_fname in size_to_file.items():
            gen_path = os.path.join(tmp_out, f"{size}.png")
            if os.path.isfile(gen_path):
                shutil.copy2(gen_path, os.path.join(src_dir, orig_fname))
        ok(f"{label}: regenerated {len(sizes)} icons in {src_dir}")
        return True
    finally:
        shutil.rmtree(tmp_out, ignore_errors=True)


def regenerate_icons(cfg):
    """Regenerate icons from [apple.icons] config into the asset catalogs.

    Detects sizes from the existing PNGs in each appiconset and overwrites
    them with freshly generated ones. The asset catalog is the source of
    truth; running this makes committed PNGs match the glyph config. No
    backup is kept.

    mac_asset_dir/ios_asset_dir regenerate with no DEV overlay. A companion
    mac_dev_asset_dir/ios_dev_asset_dir (a SEPARATE, permanently-committed
    catalog — see _resolve_icon_dirs) regenerates with the DEV banner baked
    in, for a project that switches catalogs via
    ASSETCATALOG_COMPILER_APPICON_NAME per build configuration instead of
    swapping one catalog at build time (build_icon_hooks).
    """
    from .icons import generate_tv_icons

    icon_dirs = _resolve_icon_dirs(cfg)
    dev_icon_dirs = _resolve_icon_dirs(cfg, ("mac-dev", "ios-dev"))
    tv_dir = _resolve_icon_dirs(cfg, ("tv",)).get("tv")
    if not icon_dirs and not dev_icon_dirs and not tv_dir:
        err("no icon asset dirs configured in [apple.icons]"); sys.exit(1)
    kwargs = _icon_generation_kwargs(cfg)
    if not (kwargs.get("glyph_text") or kwargs.get("glyph_svg")):
        err("[apple.icons] needs glyph_text or glyph_svg"); sys.exit(1)

    any_success = False
    for label, src_dir in icon_dirs.items():
        if _regenerate_appiconset_dir(label, src_dir, kwargs, dev=False):
            any_success = True
        dev_dir = dev_icon_dirs.get(f"{label}-dev")
        if dev_dir and _regenerate_appiconset_dir(f"{label}-dev", dev_dir, kwargs, dev=True):
            any_success = True

    if tv_dir:
        if generate_tv_icons(tv_dir, dev=False, **kwargs):
            ok(f"tv: regenerated brandassets in {tv_dir}")
            any_success = True
        else:
            err("tv: icon generation failed")

    if not any_success:
        sys.exit(1)


def register_reload_command(pid_file="/tmp/admin-run.pid"):
    """Register a standard reload command that sends SIGUSR1."""
    @command("reload", "Signal a running './admin dev' to rebuild and relaunch")
    def cmd_reload(args):
        if not os.path.exists(pid_file):
            err("No admin dev process found (no PID file at %s)" % pid_file)
            sys.exit(1)
        with open(pid_file) as f:
            pid = int(f.read().strip())
        try:
            os.kill(pid, signal.SIGUSR1)
        except ProcessLookupError:
            err("PID %d is not running (stale PID file)" % pid)
            os.remove(pid_file)
            sys.exit(1)
        ok("Reload signal sent to PID %d" % pid)

def appid(cfg, args):
    """`admin appid [<bundle-id>] [--capability NAME ...] [--name "Display"]`

    Registers an App ID and turns capabilities on, so the developer portal
    stops being a step a person has to take between "the code is written" and
    "the build signs". With no bundle id it reports on the ones this project
    already builds, which is the question worth asking first — an identifier
    that exists but lacks a capability fails a build by naming the capability,
    not by naming itself.
    """
    from .appstoreconnect import (
        AppStoreConnectError, CAPABILITY_ALIASES, capabilities, create_bundle_id,
        credentials, enable_capability, find_bundle_id,
    )

    if credentials() is None:
        err("No App Store Connect API key configured.")
        err("Set APP_STORE_CONNECT_KEY_PATH, APP_STORE_CONNECT_KEY_ID and")
        err("APP_STORE_CONNECT_ISSUER_ID — the key comes from App Store Connect ▸")
        err("Users and Access ▸ Integrations, and is downloadable exactly once.")
        return 1

    wanted = []
    display_name = None
    identifiers = []
    index = 0
    while index < len(args):
        arg = args[index]
        if arg == "--capability" and index + 1 < len(args):
            raw = args[index + 1]
            wanted.append(CAPABILITY_ALIASES.get(raw.lower(), raw.upper()))
            index += 2
        elif arg == "--name" and index + 1 < len(args):
            display_name = args[index + 1]
            index += 2
        else:
            identifiers.append(arg)
            index += 1

    if not identifiers:
        # Whatever this project builds, both halves of the dev/release pair.
        for platform in ("mac", "ios", "tv"):
            for dev in (False, True):
                found = _resolve_bundle(cfg, platform, dev=dev)
                if found and found not in identifiers:
                    identifiers.append(found)
        if not identifiers:
            err("no bundle identifiers found in this project, and none given")
            return 1

    failures = 0
    for identifier in identifiers:
        try:
            resource = find_bundle_id(identifier)
            if resource is None:
                if not wanted and not display_name:
                    warn(f"{identifier}: not registered")
                    continue
                info(f"{identifier}: registering")
                resource = create_bundle_id(identifier, display_name or identifier)
            already = capabilities(resource["id"])
            for capability in wanted:
                if capability in already:
                    info(f"{identifier}: {capability} already on")
                    continue
                enable_capability(resource["id"], capability)
                ok(f"{identifier}: {capability} enabled")
                already.append(capability)
            info(f"{identifier}: {', '.join(sorted(already)) or 'no capabilities'}")
        except AppStoreConnectError as error:
            err(f"{identifier}: {error}")
            failures += 1
    return 1 if failures else 0
