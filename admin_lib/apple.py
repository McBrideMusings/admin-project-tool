"""Layer 2 — Xcode/Swift/iOS helpers for Apple projects.

Provides: reload infrastructure, Xcode build helpers, simulator helpers,
log predicates, app finding.

Dev vs. prod side-by-side installs
----------------------------------
The convention for letting a Debug ("dev") build coexist with a Release
("prod") build on the same machine / simulator / device:

- macOS: pass ``PRODUCT_BUNDLE_IDENTIFIER=<mac-dev-bundle>`` as
  ``extra_settings`` to ``xcodebuild_filtered`` during the Debug build, then
  call ``dev_macos_postprocess(built, MAC_DEV_PRODUCT_NAME, MAC_DEV_DISPLAY_NAME)``
  on the resulting ``.app`` to rename it, patch ``CFBundleDisplayName`` in
  Info.plist, and re-sign ad-hoc. Do **not** put ``PRODUCT_BUNDLE_IDENTIFIER=``
  into the manifest's ``dev_build_settings`` — that string is
  platform-agnostic and would force one platform's bundle ID onto the other. The rename is what gives Finder / Launchpad
  / Dock a distinct entry; the ad-hoc re-sign is required because the rename
  + plist patch invalidates the original signature.

- iOS (simulator and device): pass
  ``PRODUCT_BUNDLE_IDENTIFIER=<ios-dev-bundle>`` as ``extra_settings``. iOS
  identifies installs by bundle ID, so a distinct dev bundle ID is sufficient
  to install dev alongside prod on the same simulator or device. The ``.app``
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

_RAW_BUILD_LOG = "/tmp/admin-xcode-build.log"


def find_app(pattern):
    """Glob for a built .app bundle and return the newest match, or None."""
    matches = glob.glob(os.path.expanduser(pattern))
    return max(matches, key=os.path.getmtime) if matches else None


def diagnose_build_failure():
    """After a failed xcodebuild, check for known fixable errors and print remediation."""
    try:
        with open(_RAW_BUILD_LOG, "r", errors="replace") as f:
            content = f.read()
    except FileNotFoundError:
        return

    if "errSecInternalComponent" in content:
        warn("Codesign failed: login keychain is locked.")
        warn("Unlock it with:")
        warn("  security unlock-keychain ~/Library/Keychains/login.keychain-db")
        warn("  security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k YOUR_PASSWORD ~/Library/Keychains/login.keychain-db")


def xcodebuild_filtered(project, scheme, config, destination, action="build",
                         extra_settings="", allow_provisioning=True):
    """Run xcodebuild with grep-filtered output. Raw log tee'd to
    /tmp/admin-xcode-build.log. Returns rc.

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
    parts.append(action)
    if extra_settings:
        parts.append(extra_settings)
    if allow_provisioning:
        parts.append("-allowProvisioningUpdates")
    parts.append("2>&1")

    build_cmd = " ".join(parts)
    return run_cmd(
        f'{build_cmd} | tee {_RAW_BUILD_LOG} | grep -E "BUILD|error:|warning:" ; exit ${{PIPESTATUS[0]}}'
    )


def dev_macos_postprocess(built_app_path, dev_product_name, dev_display_name, sign_identity="-"):
    """Rename a Debug build .app → <dev_product_name>.app, patch
    CFBundleDisplayName, re-sign. Returns the renamed path.

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
      • Menu-bar app menu            → CFBundleName         (project.yml)
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
    """Remove Xcode DerivedData for the iOS scheme in cfg."""
    scheme = cfg.get("ios_scheme") or cfg.get("mac_scheme")
    if not scheme:
        err("apple.ios_scheme or apple.mac_scheme required for clean")
        sys.exit(1)
    clean_derived_data(_resolve_derived_data_name(cfg) or scheme)


# ---------------------------------------------------------------------------
# Log predicates
# ---------------------------------------------------------------------------

def log_predicate(process, subsystem, level="app"):
    """Return a macOS `log stream` predicate string for the given level.

    Levels: app, debug, system, verbose.
    """
    if level == "app":
        return f"subsystem == '{subsystem}'"
    elif level == "debug":
        return (
            f"(subsystem == '{subsystem}' OR process == '{process}') "
            f"AND NOT (subsystem BEGINSWITH 'com.apple.launchservices') "
            f"AND NOT (subsystem BEGINSWITH 'com.apple.runningboard')"
        )
    elif level == "system":
        return f"process == '{process}' AND subsystem != '{subsystem}'"
    elif level == "verbose":
        return f"process == '{process}'"
    else:
        err(f"Unknown log level: {level}")
        err("Valid levels: app (default), debug, system, verbose")
        sys.exit(1)


# ---------------------------------------------------------------------------
# iOS simulator helpers
# ---------------------------------------------------------------------------

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


def resolve_simulator():
    """Pick the best simulator: already-booted first, then newest available iPhone.

    Returns (device_id, device_name) or calls sys.exit(1).
    """
    import re

    # 1. Check for already-booted simulators
    result = subprocess.run(
        ["xcrun", "simctl", "list", "devices", "booted"],
        capture_output=True, text=True,
    )
    for line in result.stdout.splitlines():
        m = re.search(r'^\s+(.+?)\s+\(([A-F0-9-]{36})\)\s+\(Booted\)', line)
        if m:
            name, did = m.group(1), m.group(2)
            info(f"Using booted simulator: {name}")
            return did, name

    # 2. No booted sim — parse all available iPhones, pick newest OS + newest model
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
        if device_m and current_runtime:
            name = device_m.group(1)
            did = device_m.group(2)
            if "iPhone" in name:
                candidates.append((current_runtime, name, did))

    if not candidates:
        err("No iPhone simulators found.")
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


def ensure_booted_simulator():
    """Resolve a simulator, boot it if needed, and wait until it finishes booting.

    Returns (device_id, device_name). Callers that install/launch on the returned
    id can do so immediately — `simctl bootstatus -b` boots the device when it
    isn't already and blocks until boot completes, so there is no race.
    """
    device_id, name = resolve_simulator()
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


def find_connected_device():
    """Find a connected iOS device via xcrun devicectl.

    Returns (device_id, device_name, udid) or calls sys.exit(1).
    device_id is the CoreDevice identifier (for devicectl commands).
    udid is the hardware UDID.
    Accepts any connection type (USB or network).
    """
    info("Finding connected iOS device...")
    devices = _list_devices()

    if not devices:
        err("No connected iOS device found.")
        err("Connect via USB, or enable wireless debugging:")
        err("  Xcode > Window > Devices and Simulators > select device > Connect via network")
        sys.exit(1)

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
                   refresh_icon=False):
    """Full macOS dev loop: build, launch, stream logs, reload on 'r' or SIGUSR1.

    Hook callbacks: pre_build/post_build wrap each build; pre_launch(app_path)
    runs before launch and may return a new app_path. is_running_fn / kill_fn
    override the default pgrep/pkill -x process check. refresh_icon re-reads the
    built app's icon into LaunchServices before launch (set when a dev-icon swap
    is active, so a just-compiled banner isn't masked by a stale icon cache).
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

        app_path = find_app(app_pattern)
        if not app_path:
            err("Could not find built app")
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
        subprocess.run(["open", "-n", app_path], check=True)

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


def dev_loop_ios(project, scheme, config, subsystem, process, predicate,
                 app_pattern, pid_file="/tmp/admin-run.pid",
                 extra_settings="", build_action="build",
                 bundle_id=None, sim_id=None,
                 pre_build=None, post_build=None, display_name=None,
                 output_config=None, launch_args=None):
    """Full iOS simulator dev loop: build, install, stream logs, reload on 'r'/SIGUSR1.

    bundle_id:   bundle ID for launch/terminate (defaults to subsystem)
    sim_id:      simulator device ID (if None, auto-resolves via resolve_simulator)
    pre_build:   callable() run before each build (e.g. swap dev icons)
    post_build:  callable() run after each build completes (e.g. restore icons)
    launch_args: optional list of strings passed to the app on each launch
    """
    import time
    from .log_columns import make_column_formatter

    setup_reload(pid_file)
    sweep_orphan_log_streams(subsystem)

    launch_bundle = bundle_id or subsystem

    if sim_id is None:
        sim_id, sim_name = ensure_booted_simulator()
    else:
        boot_simulator(sim_id)
        subprocess.run(["xcrun", "simctl", "bootstatus", sim_id, "-b"])
    destination = f"platform=iOS Simulator,id={sim_id}"

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
                    bundle_id=None, device_id=None, udid=None,
                    pre_build=None, post_build=None, display_name=None,
                    skip_build=False, output_config=None):
    """Build, install, and run on a physical iOS device with log streaming.

    Launches via `devicectl --console` with OS_ACTIVITY_DT_MODE=YES (non-redacted
    os_log). bundle_id defaults to subsystem; device_id auto-resolves via
    find_connected_device when None. pre_build/post_build wrap each build cycle.

    Loops on 'r'/SIGUSR1 like dev_loop_ios. The device log stream is only
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

    if device_id is None:
        device_id, device_name, _udid = find_connected_device()
    else:
        device_name = device_id

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


def _detect_project_path(cfg):
    """Return cfg['project_path'], or the first .xcworkspace/.xcodeproj in cwd."""
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
    if platform not in ("ios", "mac"):
        raise ValueError(f"_resolve_bundle: unknown platform {platform!r} — expected 'ios' or 'mac'")
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


def _resolve_ios_process(cfg):
    """iOS PRODUCT_NAME. Same fallback chain as :func:`_resolve_mac_process`."""
    return (cfg.get("ios_process")
            or _xcode_setting(cfg, cfg.get("ios_scheme"), "PRODUCT_NAME")
            or cfg.get("ios_scheme"))


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
    """Return {platform: abs_path} for platforms that have an asset dir configured."""
    icons_cfg = cfg.get("icons") or {}
    key_map = {"mac": "mac_asset_dir", "ios": "ios_asset_dir"}
    result = {}
    for plat in platforms:
        rel = icons_cfg.get(key_map[plat])
        if rel:
            result[plat] = os.path.abspath(rel)
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


def _build_icon_composer_hooks(cfg, icon_path, backup_dir):
    """(pre_build, post_build) for the Icon Composer ``.icon`` dev-banner swap.

    Mirrors the appiconset hooks: self-heal a stranded prior run, register the
    restore as a cleanup hook before mutating, then inject the banner.
    """
    composer_backup = os.path.join(backup_dir, "icon-composer")

    def _post():
        restore_icon_composer(icon_path, composer_backup)

    cleanup_registered = [False]

    def _pre():
        _restore_stranded_icon_composer(icon_path, composer_backup)
        if not cleanup_registered[0]:
            register_cleanup(_post)
            cleanup_registered[0] = True
        swap_dev_icon_composer(icon_path, composer_backup)

    return _pre, _post


def build_icon_hooks(cfg, platforms=("mac", "ios"), backup_dir="tmp/icon-backup"):
    """Return (pre_build, post_build) for dev-icon swap. (None, None) if
    [apple.icons] missing/empty so callers can pass directly to dev_loop_*.

    pre_build also registers post_build as a cleanup hook — required so a
    Ctrl-C'd dev run doesn't leak dev-overlay icons into the next deploy build.
    An ``[apple.icons] mac_icon`` (Xcode 26 Icon Composer) routes through the
    ``.icon`` banner swap; otherwise the appiconset PNG swap is used.
    """
    icon_composer = _resolve_icon_composer(cfg)
    if icon_composer:
        return _build_icon_composer_hooks(cfg, icon_composer, backup_dir)

    icon_dirs = _resolve_icon_dirs(cfg, platforms)
    if not icon_dirs:
        return (None, None)
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
        restore_stranded_dev_icons(cfg, platforms, backup_dir)
        # Register cleanup BEFORE we mutate anything. If SIGINT lands during
        # backup_icons or swap_dev_icons (PIL ops + filesystem copies), the
        # restore still runs. Once-only — register_cleanup has no dedup, so
        # repeated dev iterations would otherwise stack copies of _post.
        if not cleanup_registered[0]:
            register_cleanup(_post)
            cleanup_registered[0] = True
        backup_icons(icon_dirs, backup_dir)
        swap_dev_icons(icon_dirs, icon_kwargs)

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


def restore_stranded_dev_icons(cfg, platforms=("mac", "ios"), backup_dir="tmp/icon-backup"):
    """If a previous dev run was interrupted before its post-build restore,
    its dev-overlay PNGs are still sitting in the asset catalog. Recover the
    originals from ``backup_dir`` (left behind by the interrupted run) before
    a fresh build so we don't bake DEV-banner icons into a production deploy.
    """
    icon_composer = _resolve_icon_composer(cfg)
    if icon_composer:
        _restore_stranded_icon_composer(icon_composer, os.path.join(backup_dir, "icon-composer"))
        return
    if not os.path.isdir(backup_dir):
        return
    icon_dirs = _resolve_icon_dirs(cfg, platforms)
    if not icon_dirs:
        return
    warn(f"Stranded dev icons found in {backup_dir} (interrupted dev run?) — restoring originals")
    restore_icons(icon_dirs, backup_dir)


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


def _resolve_ios_dev_display_name(cfg):
    """Home-screen label for the iOS dev install. Default: ``<process> Dev``."""
    base = _resolve_ios_process(cfg)
    return cfg.get("ios_dev_display_name") or (f"{base} Dev" if base else None)


def _resolve_mac_dev_process(cfg):
    """``pgrep -f`` pattern that uniquely identifies the running dev
    instance (so it isn't confused with prod). Default:
    ``<DevProductName>.app/Contents/MacOS/<process>``.
    """
    explicit = cfg.get("mac_dev_process")
    if explicit:
        return explicit
    product = _resolve_mac_dev_product_name(cfg)
    binary = _resolve_mac_process(cfg)
    return f"{product}.app/Contents/MacOS/{binary}" if (product and binary) else None


def _make_mac_dev_running_check(cfg):
    """Return a callable that reports whether the dev mac instance is running.

    Uses ``pgrep -f <dev_name>`` where dev_name (e.g.
    ``CCToolDev.app/Contents/MacOS/CCTool``) is itself the disambiguator —
    the prod build lives under ``/Applications/CCTool.app/...`` so the
    pattern won't match it. Falls back to ``pgrep -x mac_process`` if no
    dev product name is configured.
    """
    dev_name = _resolve_mac_dev_process(cfg)
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


def _make_mac_dev_kill(cfg):
    """Return a callable that kills only the dev instance (not prod), then waits
    for it to actually exit. SIGTERM first; escalates to SIGKILL if still alive
    after ~1.5s. Returning before the process is gone causes ``open -n`` to race
    with LaunchServices and silently activate the dying instance.
    """
    import time
    dev_name = _resolve_mac_dev_process(cfg)
    prod_name = _resolve_mac_process(cfg)
    check_running = _make_mac_dev_running_check(cfg)

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


def _compose_sign_settings(cfg):
    """Build xcodebuild build-setting overrides for code signing from [apple] config.

    Reads code_sign_identity, development_team, code_sign_style. Resolves
    ${VAR} / ${VAR:-default} from the environment. Additionally, when
    ``sign_identity`` is present (and no explicit ``code_sign_identity``), the
    identity is resolved dynamically from the keychain (see
    :func:`resolve_signing_identity`) and signed manually — no hash in config.
    Returns empty string when none of the keys are present.
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
        if ident:
            parts.append(f'CODE_SIGN_IDENTITY="{ident}"')
            parts.append("CODE_SIGN_STYLE=Manual")
            parts.append("PROVISIONING_PROFILE_SPECIFIER=")
    return " ".join(parts)


def _compose_dev_settings(cfg, dev_bundle_id):
    """Combine cross-platform `dev_build_settings` with a platform-specific
    PRODUCT_BUNDLE_IDENTIFIER (appended last so it wins). Warns if the manifest's
    dev_build_settings already contains a PRODUCT_BUNDLE_IDENTIFIER (legacy).
    """
    extras = (cfg.get("dev_build_settings") or "").strip()
    if "PRODUCT_BUNDLE_IDENTIFIER" in extras:
        warn(
            "dev_build_settings contains PRODUCT_BUNDLE_IDENTIFIER — prefer "
            "mac_dev_bundle / ios_dev_bundle keys; the platform-specific "
            "value will override."
        )
    if not dev_bundle_id:
        result = extras
    else:
        override = f"PRODUCT_BUNDLE_IDENTIFIER={dev_bundle_id}"
        result = f"{extras} {override}".strip() if extras else override
    sign = _compose_sign_settings(cfg)
    parts = [p for p in [result, sign] if p]
    return " ".join(parts)


def _derived_glob(scheme, config, platform_subdir, product_name):
    """Glob pattern under ~/Library/Developer/Xcode/DerivedData for a built .app."""
    return os.path.expanduser(
        f"~/Library/Developer/Xcode/DerivedData/{scheme}-*"
        f"/Build/Products/{config}{platform_subdir}/{product_name}.app"
    )


def dev_mac(cfg, *, log_level="app", clean=False, install=False, output_config=None):
    """macOS dev loop driven by [apple] config. Swaps dev icons, builds, launches."""
    if output_config is None:
        output_config = globals().get("_OUTPUT_CONFIG")
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    cfg = _with_global_defaults(cfg, "apple")
    scheme = cfg.get("mac_scheme")
    if not scheme:
        err("apple.mac_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("mac_build_config", "Debug")
    process = _resolve_mac_process(cfg)
    dev_process = _resolve_mac_dev_process(cfg) or process
    bundle_id = _resolve_bundle(cfg, "mac", dev=True)
    subsystem = _resolve_subsystem(cfg, "mac", dev=True)
    extra_settings = _compose_dev_settings(cfg, bundle_id)
    build_action = "clean build" if clean else "build"
    predicate = log_predicate(dev_process, subsystem, level=log_level)
    app_pattern = _derived_glob(_resolve_derived_data_name(cfg), config, "", process)

    prebuild_cmd = cfg.get("prebuild_cmd")
    if prebuild_cmd:
        info("prereqs: " + prebuild_cmd)
        if run_cmd(prebuild_cmd) != 0:
            err("prereq command failed"); sys.exit(1)

    pre_build, post_build = build_icon_hooks(cfg)

    pre_launch = None
    if install:
        dev_install_path = cfg.get("mac_install_path_dev")
        if not dev_install_path:
            err("--install requires apple.mac_install_path_dev in admin.toml")
            sys.exit(1)

        def pre_launch(built_app):
            info(f"Installing to {dev_install_path}")
            if os.path.exists(dev_install_path):
                shutil.rmtree(dev_install_path)
            shutil.copytree(built_app, dev_install_path)
            return dev_install_path
    else:
        _dev_product = _resolve_mac_dev_product_name(cfg)
        _dev_display = _resolve_mac_dev_display_name(cfg)
        # Opt out of the dev rename/display-patch with mac_dev_rename = false —
        # for projects that already differentiate their debug build at the Xcode
        # level (distinct bundle ID, CFBundleDisplayName) and don't want a
        # renamed "<name>Dev.app". Stable signing still applies via the build's
        # CODE_SIGN flags; the rename is purely a Finder/Dock cosmetic.
        if cfg.get("mac_dev_rename", True) and (_dev_product or _dev_display):
            # Re-sign the renamed .app with the same stable identity the build
            # used (not ad-hoc), so TCC grants survive the rename. "-" → ad-hoc
            # when no sign_identity is configured (unchanged default).
            _resign = (resolve_signing_identity(cfg) if "sign_identity" in cfg else None) or "-"

            def pre_launch(built_app):
                return dev_macos_postprocess(built_app, _dev_product, _dev_display, sign_identity=_resign)

    dev_loop_macos(
        project=project, scheme=scheme, config=config,
        destination="platform=macOS",
        process=dev_process, subsystem=subsystem,
        predicate=predicate, app_pattern=app_pattern,
        extra_settings=extra_settings, build_action=build_action,
        pre_build=pre_build, post_build=post_build, pre_launch=pre_launch,
        is_running_fn=_make_mac_dev_running_check(cfg),
        kill_fn=_make_mac_dev_kill(cfg),
        output_config=output_config,
        # An icon swap is active iff build_icon_hooks returned hooks; refresh
        # the built app's icon cache so the swapped-in banner actually shows.
        refresh_icon=pre_build is not None,
    )


def dev_ios(cfg, *, log_level="app", clean=False, output_config=None, launch_args=None):
    """iOS simulator dev loop driven by [apple] config.

    launch_args: optional list of strings forwarded to xcrun simctl launch
    so the running app receives them as UIApplication launch arguments.
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
    extra_settings = _compose_dev_settings(cfg, bundle_id)
    build_action = "clean build" if clean else "build"
    predicate = log_predicate(process, subsystem, level=log_level)
    app_pattern = _derived_glob(_resolve_derived_data_name(cfg), config, "-iphonesimulator", process)

    prebuild_cmd = cfg.get("prebuild_cmd")
    if prebuild_cmd:
        info("prereqs: " + prebuild_cmd)
        if run_cmd(prebuild_cmd) != 0:
            err("prereq command failed"); sys.exit(1)

    sim_id = None
    device_name = os.environ.get("IOS_DEVICE") or cfg.get("ios_device")
    if device_name:
        sim_id = find_simulator(device_name)
        if not sim_id:
            warn(f"simulator '{device_name}' not found, falling back to auto-resolve")
    if sim_id is None:
        if not device_name:
            info("No simulator pinned — auto-resolving.")
        info("Pin one: set IOS_DEVICE env var, or set ios_device in [apple] of your project or ~/.admin/admin.toml")

    pre_build, post_build = build_icon_hooks(cfg)

    dev_loop_ios(
        project=project, scheme=scheme, config=config,
        subsystem=subsystem, process=process, predicate=predicate,
        app_pattern=app_pattern, extra_settings=extra_settings,
        build_action=build_action, bundle_id=bundle_id, sim_id=sim_id,
        pre_build=pre_build, post_build=post_build,
        display_name=_resolve_ios_dev_display_name(cfg),
        output_config=output_config,
        launch_args=launch_args,
    )


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

    prebuild_cmd = cfg.get("prebuild_cmd")
    if prebuild_cmd:
        info("prereqs: " + prebuild_cmd)
        if run_cmd(prebuild_cmd) != 0:
            err("prereq command failed"); sys.exit(1)

    info(f"Killing any running {process}...")
    subprocess.run(["pkill", "-x", process], capture_output=True)

    info(f"Building {scheme} ({config})...")
    rc = xcodebuild_filtered(project, scheme, config, "platform=macOS",
                             action=build_action,
                             extra_settings=_compose_sign_settings(cfg))
    if rc != 0:
        err("Build failed"); sys.exit(rc)

    app_path = find_app(_derived_glob(derived_name, config, "", process))
    if not app_path:
        err("Could not find built app"); sys.exit(1)

    info(f"Installing to {install_path}")
    if os.path.exists(install_path):
        shutil.rmtree(install_path)
    shutil.copytree(app_path, install_path)

    info(f"Launching {install_path}")
    subprocess.run(["open", install_path], check=True)
    ok(f"deploy mac → {install_path}")


def deploy_ios(cfg, *, clean=False):
    """Build and install a production iOS app on a connected device (USB or wireless). No icon swap."""
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    cfg = _with_global_defaults(cfg, "apple")
    scheme = cfg.get("ios_scheme")
    if not scheme:
        err("apple.ios_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("ios_deploy_config", "Release")
    process = _resolve_ios_process(cfg)
    bundle_id = _resolve_bundle(cfg, "ios")
    build_action = "clean build" if clean else "build"
    derived_name = _resolve_derived_data_name(cfg)

    restore_stranded_dev_icons(cfg)

    prebuild_cmd = cfg.get("prebuild_cmd")
    if prebuild_cmd:
        info("prereqs: " + prebuild_cmd)
        if run_cmd(prebuild_cmd) != 0:
            err("prereq command failed"); sys.exit(1)

    device_id, device_name, _udid = find_connected_device()
    info(f"Deploying to {device_name}")

    info(f"Building {scheme} for device ({config})...")
    rc = xcodebuild_filtered(project, scheme, config, "generic/platform=iOS",
                             action=build_action,
                             extra_settings=_compose_sign_settings(cfg))
    if rc != 0:
        diagnose_build_failure()
        err("Build failed"); sys.exit(rc)

    app_path = find_app(_derived_glob(derived_name, config, "-iphoneos", process))
    if not app_path:
        err("Could not find built app"); sys.exit(1)

    info(f"Installing to device {device_name}...")
    rc = install_on_device(device_id, app_path)
    if rc != 0:
        warn("Install failed — retrying once...")
        rc = install_on_device(device_id, app_path)
        if rc != 0:
            err("Install failed after retry.")
            err("Try plugging in via USB cable for a more reliable connection.")
            sys.exit(rc)

    if bundle_id:
        info(f"Launching {bundle_id}")
        launch_on_device(device_id, bundle_id)
    ok(f"deploy ios → {device_name}")


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
    extra_settings = _compose_dev_settings(cfg, bundle_id)
    build_action = "clean build" if clean else "build"
    app_pattern = _derived_glob(_resolve_derived_data_name(cfg), config, "-iphoneos", process)

    prebuild_cmd = cfg.get("prebuild_cmd")
    if prebuild_cmd:
        info("prereqs: " + prebuild_cmd)
        if run_cmd(prebuild_cmd) != 0:
            err("prereq command failed"); sys.exit(1)

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
                             extra_settings=_compose_sign_settings(cfg))
    if rc != 0:
        diagnose_build_failure()
        err("Build failed"); sys.exit(rc)
    ok("macOS build succeeded")


def build_ios(cfg, *, force=False):
    """Build iOS client from [apple] config (no run, no install)."""
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    cfg = _with_global_defaults(cfg, "apple")
    scheme = cfg.get("ios_scheme")
    if not scheme:
        err("apple.ios_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("ios_build_config", "Debug")
    sim = os.environ.get("IOS_DEVICE") or cfg.get("ios_device") or ""
    if sim:
        dest = "platform=iOS Simulator,name=" + sim
    else:
        # No simulator pinned — auto-resolve (booted first, then newest iPhone).
        # The bare "platform=iOS Simulator" destination is ambiguous on machines
        # with multiple runtimes and causes xcodebuild to fail before compiling.
        sim_id, _ = resolve_simulator()
        dest = f"platform=iOS Simulator,id={sim_id}"
    build_action = "clean build" if force else "build"
    restore_stranded_dev_icons(cfg)
    info(f"Building {scheme} ({config}) for iOS...")
    rc = xcodebuild_filtered(project, scheme, config, dest, action=build_action,
                             extra_settings=_compose_sign_settings(cfg))
    if rc != 0:
        diagnose_build_failure()
        err("Build failed"); sys.exit(rc)
    ok("iOS build succeeded")


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
    regenerate_icons(cfg)


def regenerate_icons(cfg):
    """Regenerate production icons from [apple.icons] config into the asset catalogs.

    Detects sizes from the existing PNGs in each appiconset and overwrites
    them with freshly generated ones (no DEV overlay). The asset catalog is
    the source of truth; running this makes committed PNGs match the glyph
    config. No backup is kept.
    """
    from .icons import generate_icons

    icon_dirs = _resolve_icon_dirs(cfg)
    if not icon_dirs:
        err("no icon asset dirs configured in [apple.icons]"); sys.exit(1)
    kwargs = _icon_generation_kwargs(cfg)
    if not (kwargs.get("glyph_text") or kwargs.get("glyph_svg")):
        err("[apple.icons] needs glyph_text or glyph_svg"); sys.exit(1)

    try:
        from PIL import Image as _Img
    except ImportError:
        err("Pillow not installed — pip install pillow"); sys.exit(1)

    import tempfile

    any_success = False
    for label, src_dir in icon_dirs.items():
        if not os.path.isdir(src_dir):
            warn(f"{label}: {src_dir} not a directory — skipping")
            continue
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
            warn(f"{label}: no PNGs in {src_dir} — skipping")
            continue

        sizes = sorted(size_to_file.keys())
        tmp_out = tempfile.mkdtemp(prefix=f"prod-icons-{label}-")
        try:
            gen_kwargs = dict(kwargs)
            gen_kwargs["sizes"] = sizes
            gen_kwargs["output_dir"] = tmp_out
            gen_kwargs["dev"] = False
            if not generate_icons(**gen_kwargs):
                err(f"{label}: icon generation failed")
                continue
            for size, orig_fname in size_to_file.items():
                gen_path = os.path.join(tmp_out, f"{size}.png")
                if os.path.isfile(gen_path):
                    shutil.copy2(gen_path, os.path.join(src_dir, orig_fname))
            ok(f"{label}: regenerated {len(sizes)} icons in {src_dir}")
            any_success = True
        finally:
            shutil.rmtree(tmp_out, ignore_errors=True)

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
