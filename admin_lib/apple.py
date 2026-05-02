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

import atexit
import glob
import json
import os
import shutil
import signal
import subprocess
import sys

from .core import (
    C,
    LOG_DIR,
    LOG_FILE,
    _children,
    command,
    get_log_dir,
    info,
    ok,
    warn,
    err,
    register_cleanup,
    read_key,
)


# ---------------------------------------------------------------------------
# Reload infrastructure
# ---------------------------------------------------------------------------

_rebuild_requested = False


def _handle_rebuild_signal(signum, frame):
    global _rebuild_requested
    _rebuild_requested = True


def setup_reload(pid_file="/tmp/admin-run.pid"):
    """Register SIGUSR1 handler and write PID file."""
    signal.signal(signal.SIGUSR1, _handle_rebuild_signal)
    with open(pid_file, "w") as f:
        f.write(str(os.getpid()))
    atexit.register(cleanup_pid_file, pid_file)
    register_cleanup(lambda: cleanup_pid_file(pid_file))


def cleanup_pid_file(pid_file):
    """Remove PID file if it exists."""
    try:
        os.remove(pid_file)
    except FileNotFoundError:
        pass


def poll_for_reload(is_running_fn, timeout=1):
    """Poll for reload signal, 'r' keypress, or process exit.

    Returns True if reload requested, False if process exited or Ctrl+C.
    """
    import select
    import termios
    import tty

    global _rebuild_requested

    fd = sys.stdin.fileno()
    old_settings = termios.tcgetattr(fd)
    try:
        tty.setcbreak(fd)
        while True:
            if _rebuild_requested:
                return True
            if not is_running_fn():
                return False
            readable, _, _ = select.select([sys.stdin], [], [], timeout)
            if readable:
                ch = sys.stdin.read(1)
                if ch == "r":
                    return True
    except KeyboardInterrupt:
        return False
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old_settings)


def is_rebuild_requested():
    """Check if a rebuild has been requested (via signal or keypress)."""
    return _rebuild_requested


def clear_rebuild_request():
    """Reset the rebuild request flag."""
    global _rebuild_requested
    _rebuild_requested = False


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
    """Run xcodebuild with grep filtering, return exit code.

    Filters output to show only BUILD/error/warning lines. Raw output is
    teed to /tmp/admin-xcode-build.log for post-failure diagnostics
    (see diagnose_build_failure()).

    Warning — ``extra_settings`` is a raw xcodebuild settings string forwarded
    verbatim to the command line. Settings passed this way apply *globally*
    across every target in the build, including SwiftPM resource bundles
    (swift-crypto, swift-nio, etc.). Only ``PRODUCT_BUNDLE_IDENTIFIER`` is
    known to be SwiftPM-safe as a global override. Passing ``PRODUCT_NAME``
    or ``INFOPLIST_KEY_CFBundleDisplayName`` causes "Multiple commands
    produce" build failures because SwiftPM bundles try to emit duplicate
    outputs under the overridden name. For product-name / display-name
    differentiation on macOS dev builds, use ``dev_macos_postprocess()``
    (rename + plutil + ad-hoc re-sign) instead of xcodebuild flags. See the
    module docstring for the full dev/prod side-by-side convention.
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


def dev_macos_postprocess(built_app_path, dev_product_name, dev_display_name):
    """Post-process a macOS Debug build to be a side-by-side dev app.

    Renames ``<built>.app`` → ``<dev_product_name>.app`` next to the original,
    patches ``CFBundleDisplayName`` in ``Contents/Info.plist``, and re-signs
    the bundle ad-hoc so macOS accepts the modified contents. Returns the
    path to the renamed dev app.

    Intended to run on the Debug build output *after* xcodebuild succeeds.
    Pair with ``PRODUCT_BUNDLE_IDENTIFIER=<mac-dev-bundle>`` passed as
    ``extra_settings`` to ``xcodebuild_filtered`` so the dev app also has a
    distinct bundle ID. See the module docstring for the full convention.

    Warning (see also ``xcodebuild_filtered``): do NOT try to differentiate
    the product name or display name via xcodebuild flags — ``PRODUCT_NAME``
    and ``INFOPLIST_KEY_CFBundleDisplayName`` cascade to SwiftPM resource
    bundles and break the build. Rename-then-plist-patch via this helper is
    the supported path.
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
        ["codesign", "--force", "--sign", "-", dest],
        capture_output=True,
    )
    if r.returncode != 0:
        warn(f"ad-hoc codesign failed: "
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

def find_simulator(device_name):
    """Find a simulator device ID by name. Returns device_id or None."""
    import re
    result = subprocess.run(
        ["xcrun", "simctl", "list", "devices", "available"],
        capture_output=True, text=True,
    )
    for line in result.stdout.splitlines():
        if device_name in line:
            m = re.search(r'[A-F0-9-]{36}', line)
            if m:
                return m.group()
    return None


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
    """Boot a simulator and open Simulator.app."""
    subprocess.run(["xcrun", "simctl", "boot", device_id], capture_output=True)
    subprocess.run(["open", "-a", "Simulator"])


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


def install_and_launch_sim(device_id, app_path, bundle_id):
    """Install and launch an app on a simulator."""
    subprocess.run(["xcrun", "simctl", "install", device_id, app_path])
    subprocess.run(["xcrun", "simctl", "launch", device_id, bundle_id])


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

    json_path = os.path.join(LOG_DIR, "devices.json")
    os.makedirs(LOG_DIR, exist_ok=True)
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

    json_path = os.path.join(LOG_DIR, "processes.json")
    os.makedirs(LOG_DIR, exist_ok=True)
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


def stream_console_output(proc, log_file=None, formatter=None):
    """Stream a devicectl --console process's stdout in a background thread.

    proc:      Popen process (from launch_on_device with console=True).
    log_file:  path to tee output to (defaults to tmp/console.log).
    formatter: optional callable(line) -> line for column formatting.
    Returns (thread, log_fh) — caller should close log_fh and terminate proc.
    """
    import threading

    if log_file is None:
        _log_dir = get_log_dir() or "tmp"
        os.makedirs(_log_dir, exist_ok=True)
        log_file = os.path.join(_log_dir, "console.log")
    log_fh = open(log_file, "a")

    def _stream():
        try:
            for line in proc.stdout:
                out = formatter(line) if formatter else line
                sys.stdout.write(out)
                sys.stdout.flush()
                log_fh.write(out)
                log_fh.flush()
        except ValueError:
            pass

    thread = threading.Thread(target=_stream, daemon=True)
    thread.start()
    return thread, log_fh


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
    _, log_fh = stream_console_output(console_proc, log_file=log_file, formatter=fmt.format_line)

    try:
        console_proc.wait()
    except KeyboardInterrupt:
        pass
    finally:
        stop_os_log(console_proc, log_fh)
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
    Returns (proc, thread, log_fh) — caller should call stop_os_log().
    """
    import threading

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
    log_fh = open(log_file, "a")

    def _stream():
        try:
            for line in proc.stdout:
                out = formatter(line) if formatter else line
                sys.stdout.write(out)
                sys.stdout.flush()
                log_fh.write(out)
                log_fh.flush()
        except (IOError, ValueError):
            pass

    thread = threading.Thread(target=_stream, daemon=True)
    thread.start()
    return proc, thread, log_fh


def stop_os_log(proc, log_fh=None):
    """Terminate an os log stream process and clean up."""
    proc.terminate()
    try:
        proc.wait(timeout=3)
    except subprocess.TimeoutExpired:
        proc.kill()
    if proc in _children:
        _children.remove(proc)
    if log_fh:
        log_fh.close()


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

    try:
        for label, src_dir in icon_dirs.items():
            if not os.path.isdir(src_dir):
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
# Dev loops
# ---------------------------------------------------------------------------

def dev_loop_macos(project, scheme, config, destination, process, subsystem,
                   predicate, app_pattern, pid_file="/tmp/admin-run.pid",
                   extra_settings="", build_action="build",
                   pre_build=None, post_build=None, pre_launch=None,
                   is_running_fn=None, kill_fn=None, output_config=None):
    """Full macOS dev loop: build, launch, stream logs, reload on 'r'/SIGUSR1.

    pre_build:     callable() run before each build (e.g. swap dev icons)
    post_build:    callable() run after each build completes (e.g. restore icons)
    pre_launch:    callable(app_path) run after build, before launch (e.g. install to /Applications)
                   May return a new app_path to launch instead.
    is_running_fn: callable() -> bool, custom process check (default: pgrep -x process)
    kill_fn:       callable() to kill the running app (default: pkill -x process)
    """
    import time
    from .log_columns import make_column_formatter

    setup_reload(pid_file)

    def _default_is_running():
        r = subprocess.run(["pgrep", "-x", process], capture_output=True)
        return r.returncode == 0

    def _default_kill():
        subprocess.run(["pkill", "-x", process], capture_output=True)

    check_running = is_running_fn or _default_is_running
    kill_app = kill_fn or _default_kill

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

        info(f"Launching {app_path}")
        subprocess.run(["open", app_path], check=True)

        for _ in range(10):
            if check_running():
                break
            time.sleep(0.3)

        fmt = make_column_formatter("apple_os_log", output_config)
        log_proc, log_thread, log_fh = stream_os_log(
            predicate, log_file=LOG_FILE, formatter=fmt.format_line)

        info("Press r to rebuild, Ctrl+C to quit")
        print()

        got_reload = poll_for_reload(check_running)

        stop_os_log(log_proc, log_fh)
        print()

        if got_reload:
            info("Reload signal received — rebuilding...")
        else:
            info(f"Stopping {process}...")
        kill_app()

        if not got_reload:
            cleanup_pid_file(pid_file)
            return


def dev_loop_ios(project, scheme, config, subsystem, process, predicate,
                 app_pattern, pid_file="/tmp/admin-run.pid",
                 extra_settings="", build_action="build",
                 bundle_id=None, sim_id=None,
                 pre_build=None, post_build=None, display_name=None,
                 output_config=None):
    """Full iOS simulator dev loop: build, install, stream logs, reload on 'r'/SIGUSR1.

    bundle_id:  bundle ID for launch/terminate (defaults to subsystem)
    sim_id:     simulator device ID (if None, auto-resolves via resolve_simulator)
    pre_build:  callable() run before each build (e.g. swap dev icons)
    post_build: callable() run after each build completes (e.g. restore icons)
    """
    import time
    from .log_columns import make_column_formatter

    setup_reload(pid_file)

    launch_bundle = bundle_id or subsystem

    if sim_id is None:
        sim_id, sim_name = resolve_simulator()
    boot_simulator(sim_id)
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
        install_and_launch_sim(sim_id, app_path, launch_bundle)

        def _is_running():
            return is_sim_app_running(sim_id, launch_bundle)

        for _ in range(10):
            if _is_running():
                break
            time.sleep(0.3)

        fmt = make_column_formatter("apple_os_log", output_config)
        log_proc, log_thread, log_fh = stream_os_log(
            predicate, log_file=LOG_FILE, sim_id=sim_id, formatter=fmt.format_line)

        info("Press r to rebuild, Ctrl+C to quit")
        print()

        got_reload = poll_for_reload(_is_running)

        stop_os_log(log_proc, log_fh)
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

    Launches via devicectl --console with OS_ACTIVITY_DT_MODE=YES so os_log
    messages are non-redacted. Single process replaces the old lldb +
    idevicesyslog approach.

    bundle_id:  bundle ID for launch/terminate (defaults to subsystem)
    device_id:  CoreDevice identifier (if None, auto-resolves via find_connected_device)
    pre_build:  callable() run before each build (e.g. swap dev icons)
    post_build: callable() run after each build completes (e.g. restore icons)
    """
    if output_config is None:
        output_config = globals().get("_OUTPUT_CONFIG")
    launch_bundle = bundle_id or subsystem

    if device_id is None:
        device_id, device_name, _udid = find_connected_device()
    else:
        device_name = device_id

    destination = "generic/platform=iOS"

    terminate_device_app(device_id, launch_bundle)

    if skip_build:
        app_path = find_app(app_pattern)
        if not app_path:
            err("Could not find previously built app — run without cache to rebuild")
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
            sys.exit(1)

        app_path = find_app(app_pattern)
        if not app_path:
            err("Could not find built app")
            sys.exit(1)

        launch_bundle = app_bundle_identifier(app_path) or bundle_id or subsystem

        if display_name:
            patch_ios_display_name(app_path, display_name)

        info(f"Installing on {device_name}...")
        rc = install_on_device(device_id, app_path)
        if rc != 0:
            err("Install failed")
            sys.exit(1)

    info(f"Launching on {device_name}...")
    console_proc = launch_on_device(
        device_id, launch_bundle, console=True,
        env=_DEVICE_LOG_ENV)

    from .log_columns import make_column_formatter
    fmt = make_column_formatter("generic", output_config)
    _, log_fh = stream_console_output(console_proc, log_file=LOG_FILE, formatter=fmt.format_line)

    info("Streaming logs — Ctrl+C to quit")
    print()

    try:
        console_proc.wait()
    except KeyboardInterrupt:
        pass
    finally:
        stop_os_log(console_proc, log_fh)
        terminate_device_app(device_id, launch_bundle)
        print()
        info("Done.")


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
    """Return the ``buildSettings`` dict for the PBXNativeTarget that backs
    a given scheme, at the requested configuration (Debug by default).

    Walks pbxproj: ``PBXNativeTarget(name=scheme_name)`` →
    ``buildConfigurationList`` → ``XCBuildConfiguration(name=configuration)``
    → ``buildSettings``. Returns ``{}`` if any link can't be resolved —
    callers must tolerate empty results and fall back to manifest defaults.

    Note: this only sees settings written *into the pbxproj itself*. Values
    sourced from ``.xcconfig`` files or Xcode variable substitutions
    (``$(PRODUCT_NAME)``) come back as literals and need separate handling
    by the caller.
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


def _resolve_mac_bundle(cfg):
    """macOS production bundle ID. Reads ``mac_bundle`` from the manifest if
    set, otherwise pulls ``PRODUCT_BUNDLE_IDENTIFIER`` from the mac scheme's
    target in the pbxproj.
    """
    return cfg.get("mac_bundle") or _xcode_setting(
        cfg, cfg.get("mac_scheme"), "PRODUCT_BUNDLE_IDENTIFIER"
    )


def _resolve_ios_bundle(cfg):
    """iOS production bundle ID. Same fallback chain as
    :func:`_resolve_mac_bundle` but for the iOS scheme.
    """
    return cfg.get("ios_bundle") or _xcode_setting(
        cfg, cfg.get("ios_scheme"), "PRODUCT_BUNDLE_IDENTIFIER"
    )


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


def _resolve_subsystem(cfg):
    """``os_log`` subsystem used in log-stream predicates. Defaults to the
    macOS bundle ID, since ``Logger(subsystem: Bundle.main.bundleIdentifier!)``
    is the canonical Swift pattern. Returns "" so callers can pass it
    directly to predicate builders without a None check.
    """
    return cfg.get("subsystem") or _resolve_mac_bundle(cfg) or ""


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


def _icon_generation_kwargs(cfg):
    """Return kwargs dict forwarded to generate_icons() from [apple.icons]."""
    i = cfg.get("icons") or {}
    out = {}
    for k in ("bg_color", "glyph_text", "glyph_svg", "glyph_color", "glyph_scale"):
        if k in i:
            out[k] = i[k]
    return out


def build_icon_hooks(cfg, platforms=("mac", "ios"), backup_dir="tmp/icon-backup"):
    """Return (pre_build, post_build) callables for dev loops.

    pre_build backs up committed production icons and swaps in dev-overlay
    versions generated from [apple.icons] config. post_build restores the
    committed icons. Returns (None, None) if [apple.icons] is missing or
    empty, so the caller can pass them directly to dev_loop_* without
    conditional plumbing.

    pre_build also registers post_build as a cleanup hook so an interrupted
    dev run (Ctrl-C, crash) still restores the production icons. Without
    this, the dev-overlay PNGs leak into the asset catalog and contaminate
    the next ``deploy`` build with a DEV banner.
    """
    icon_dirs = _resolve_icon_dirs(cfg, platforms)
    if not icon_dirs:
        return (None, None)
    icon_kwargs = _icon_generation_kwargs(cfg)
    if not (icon_kwargs.get("glyph_text") or icon_kwargs.get("glyph_svg")):
        return (None, None)

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
    """Patch ``CFBundleDisplayName`` in a built iOS .app and re-sign.

    Used by the iOS dev loops to give the dev install a distinct home-screen
    name (e.g. "CCTool DEV") without touching the Xcode project. The global
    xcodebuild flag ``INFOPLIST_KEY_CFBundleDisplayName`` is unsafe — it
    cascades into SwiftPM resource bundles and corrupts them — so we patch
    the built app's Info.plist directly and re-sign with the original
    identity (preserving entitlements so the app still installs on device).

    No-op when ``display_name`` is falsy.
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
    if not os.path.isdir(backup_dir):
        return
    icon_dirs = _resolve_icon_dirs(cfg, platforms)
    if not icon_dirs:
        return
    warn(f"Stranded dev icons found in {backup_dir} (interrupted dev run?) — restoring originals")
    restore_icons(icon_dirs, backup_dir)


def _resolve_mac_dev_bundle(cfg):
    """macOS dev bundle ID. Default: ``<mac_bundle>.dev``."""
    base = _resolve_mac_bundle(cfg)
    return cfg.get("mac_dev_bundle") or (f"{base}.dev" if base else None)


def _resolve_ios_dev_bundle(cfg):
    """iOS dev bundle ID. Default: ``<ios_bundle>.dev``."""
    base = _resolve_ios_bundle(cfg)
    return cfg.get("ios_dev_bundle") or (f"{base}.dev" if base else None)


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

    If mac_dev_process is set, filter by that name *and* DerivedData in argv
    so the prod binary (same scheme name, different install path) isn't
    counted. Otherwise fall back to pgrep -x mac_process.
    """
    dev_name = _resolve_mac_dev_process(cfg)
    prod_name = _resolve_mac_process(cfg)

    if dev_name:
        def _check():
            r = subprocess.run(["pgrep", "-af", "DerivedData"],
                               capture_output=True, text=True)
            return any(dev_name in ln for ln in r.stdout.splitlines())
        return _check

    if prod_name:
        def _check():
            r = subprocess.run(["pgrep", "-x", prod_name], capture_output=True)
            return r.returncode == 0
        return _check

    return lambda: False


def _make_mac_dev_kill(cfg):
    """Return a callable that kills only the dev instance (not prod)."""
    dev_name = _resolve_mac_dev_process(cfg)
    prod_name = _resolve_mac_process(cfg)

    if dev_name:
        def _kill():
            r = subprocess.run(["pgrep", "-af", "DerivedData"],
                               capture_output=True, text=True)
            for line in r.stdout.splitlines():
                parts = line.split(None, 1)
                if len(parts) < 2 or dev_name not in parts[1]:
                    continue
                try:
                    os.kill(int(parts[0]), signal.SIGTERM)
                except (ProcessLookupError, ValueError):
                    pass
        return _kill

    if prod_name:
        def _kill():
            subprocess.run(["pkill", "-x", prod_name], capture_output=True)
        return _kill

    return lambda: None


def _compose_dev_settings(cfg, dev_bundle_id):
    """Combine manifest ``dev_build_settings`` with a platform-specific
    ``PRODUCT_BUNDLE_IDENTIFIER`` override.

    ``dev_build_settings`` is meant for cross-platform extras only. The
    bundle ID is platform-specific (``mac_dev_bundle`` / ``ios_dev_bundle``)
    and must not be hard-coded into ``dev_build_settings`` — doing so would
    force one platform's ID onto the other (e.g. macOS dev ID leaking into
    an iOS build, breaking the post-build sanity check).

    The platform-specific override is appended last so it wins if the legacy
    manifest still carries a ``PRODUCT_BUNDLE_IDENTIFIER=`` in its
    ``dev_build_settings`` string. Emits a warning when that happens so
    consumers know to clean up the manifest.
    """
    extras = (cfg.get("dev_build_settings") or "").strip()
    if "PRODUCT_BUNDLE_IDENTIFIER" in extras:
        warn(
            "dev_build_settings contains PRODUCT_BUNDLE_IDENTIFIER — prefer "
            "mac_dev_bundle / ios_dev_bundle keys; the platform-specific "
            "value will override."
        )
    if not dev_bundle_id:
        return extras
    override = f"PRODUCT_BUNDLE_IDENTIFIER={dev_bundle_id}"
    return f"{extras} {override}".strip() if extras else override


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
    scheme = cfg.get("mac_scheme")
    if not scheme:
        err("apple.mac_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("mac_build_config", "Debug")
    process = _resolve_mac_process(cfg)
    dev_process = _resolve_mac_dev_process(cfg) or process
    subsystem = _resolve_subsystem(cfg)
    extra_settings = _compose_dev_settings(cfg, _resolve_mac_dev_bundle(cfg))
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
        if _dev_product or _dev_display:
            def pre_launch(built_app):
                return dev_macos_postprocess(built_app, _dev_product, _dev_display)

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
    )


def dev_ios(cfg, *, log_level="app", clean=False, output_config=None):
    """iOS simulator dev loop driven by [apple] config."""
    if output_config is None:
        output_config = globals().get("_OUTPUT_CONFIG")
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    scheme = cfg.get("ios_scheme")
    if not scheme:
        err("apple.ios_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("ios_build_config", "Debug")
    process = _resolve_ios_process(cfg)
    subsystem = _resolve_subsystem(cfg)
    ios_dev_bundle = _resolve_ios_dev_bundle(cfg)
    bundle_id = ios_dev_bundle or _resolve_ios_bundle(cfg)
    extra_settings = _compose_dev_settings(cfg, ios_dev_bundle)
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

    pre_build, post_build = build_icon_hooks(cfg)

    dev_loop_ios(
        project=project, scheme=scheme, config=config,
        subsystem=subsystem, process=process, predicate=predicate,
        app_pattern=app_pattern, extra_settings=extra_settings,
        build_action=build_action, bundle_id=bundle_id, sim_id=sim_id,
        pre_build=pre_build, post_build=post_build,
        display_name=_resolve_ios_dev_display_name(cfg),
        output_config=output_config,
    )


def deploy_mac(cfg, *, clean=False):
    """Build and install a production macOS app to /Applications. No icon swap."""
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
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
                             action=build_action, extra_settings="")
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
    scheme = cfg.get("ios_scheme")
    if not scheme:
        err("apple.ios_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("ios_deploy_config", "Release")
    process = _resolve_ios_process(cfg)
    bundle_id = _resolve_ios_bundle(cfg)
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
                             action=build_action, extra_settings="")
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
    scheme = cfg.get("ios_scheme")
    if not scheme:
        err("apple.ios_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("ios_build_config", "Debug")
    process = _resolve_ios_process(cfg)
    subsystem = _resolve_subsystem(cfg)
    ios_dev_bundle = _resolve_ios_dev_bundle(cfg)
    bundle_id = ios_dev_bundle or _resolve_ios_bundle(cfg)
    extra_settings = _compose_dev_settings(cfg, ios_dev_bundle)
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
    scheme = cfg.get("mac_scheme")
    if not scheme:
        err("apple.mac_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("mac_build_config", "Debug")
    build_action = "clean build" if force else "build"
    restore_stranded_dev_icons(cfg)
    info(f"Building {scheme} ({config}) for macOS...")
    rc = xcodebuild_filtered(project, scheme, config, "platform=macOS",
                             action=build_action)
    if rc != 0:
        diagnose_build_failure()
        err("Build failed"); sys.exit(rc)
    ok("macOS build succeeded")


def build_ios(cfg, *, force=False):
    """Build iOS client from [apple] config (no run, no install)."""
    if not cfg:
        err("[apple] table missing from admin.toml"); sys.exit(1)
    scheme = cfg.get("ios_scheme")
    if not scheme:
        err("apple.ios_scheme required"); sys.exit(1)
    project = _detect_project_path(cfg)
    config = cfg.get("ios_build_config", "Debug")
    sim = os.environ.get("IOS_DEVICE", "") or cfg.get("ios_device", "")
    dest = ("platform=iOS Simulator,name=" + sim) if sim else "platform=iOS Simulator"
    build_action = "clean build" if force else "build"
    restore_stranded_dev_icons(cfg)
    info(f"Building {scheme} ({config}) for iOS...")
    rc = xcodebuild_filtered(project, scheme, config, dest, action=build_action)
    if rc != 0:
        diagnose_build_failure()
        err("Build failed"); sys.exit(rc)
    ok("iOS build succeeded")


def get_ios_log_bundle(cfg, *, prod=False):
    """Return the iOS bundle ID for log streaming from [apple] config.

    prod=False → ios_dev_bundle, prod=True → ios_bundle.
    Calls sys.exit(1) if neither is configured.
    """
    bundle = _resolve_ios_bundle(cfg) if prod else _resolve_ios_dev_bundle(cfg)
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
