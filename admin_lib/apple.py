"""Layer 2 — Xcode/Swift/iOS helpers for Apple projects.

Provides: reload infrastructure, Xcode build helpers, simulator helpers,
log predicates, app finding.
"""

import atexit
import glob
import os
import signal
import subprocess
import sys

from . import core as _core
from .core import (
    C,
    _children,
    command,
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

def find_app(pattern):
    """Glob for a built .app bundle and return the first match, or None."""
    matches = glob.glob(os.path.expanduser(pattern))
    return matches[0] if matches else None


def xcodebuild_filtered(project, scheme, config, destination, action="build",
                         extra_settings="", allow_provisioning=True):
    """Run xcodebuild with grep filtering, return exit code.

    Filters output to show only BUILD/error/warning lines.
    """
    from .core import run_cmd

    parts = [f'xcodebuild']
    if project:
        parts.append(f'-project "{project}"')
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
        f'{build_cmd} | grep -E "BUILD|error:|warning:" ; exit ${{PIPESTATUS[0]}}'
    )


def clean_derived_data(project_name):
    """Remove DerivedData for a specific project."""
    pattern = os.path.expanduser(f"~/Library/Developer/Xcode/DerivedData/{project_name}-*")
    for d in glob.glob(pattern):
        subprocess.run(["rm", "-rf", d])


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

    json_path = os.path.join(_core.LOG_DIR, "devices.json")
    os.makedirs(_core.LOG_DIR, exist_ok=True)
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

    json_path = os.path.join(_core.LOG_DIR, "processes.json")
    os.makedirs(_core.LOG_DIR, exist_ok=True)
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


def stream_console_output(proc, log_file=None):
    """Stream a devicectl --console process's stdout in a background thread.

    proc:     Popen process (from launch_on_device with console=True).
    log_file: path to tee output to (optional).
    Returns (thread, log_fh) — caller should close log_fh and terminate proc.
    """
    import threading

    log_fh = open(log_file, "a") if log_file else None

    def _stream():
        try:
            for line in proc.stdout:
                sys.stdout.write(line)
                sys.stdout.flush()
                if log_fh:
                    log_fh.write(line)
                    log_fh.flush()
        except ValueError:
            pass

    thread = threading.Thread(target=_stream, daemon=True)
    thread.start()
    return thread, log_fh


def device_log_attach(bundle_id, log_file=None):
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
    _, log_fh = stream_console_output(console_proc, log_file=log_file)

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

def stream_os_log(predicate, log_file=None, sim_id=None):
    """Start streaming macOS/simulator logs in a background thread.

    predicate: log stream --predicate string
    log_file: path to tee output to (optional)
    sim_id: if set, uses xcrun simctl spawn for simulator logs
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

    log_fh = open(log_file, "a") if log_file else None

    def _stream():
        try:
            for line in proc.stdout:
                sys.stdout.write(line)
                sys.stdout.flush()
                if log_fh:
                    log_fh.write(line)
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
                   is_running_fn=None, kill_fn=None):
    """Full macOS dev loop: build, launch, stream logs, reload on 'r'/SIGUSR1.

    pre_build:     callable() run before each build (e.g. swap dev icons)
    post_build:    callable() run after each build completes (e.g. restore icons)
    pre_launch:    callable(app_path) run after build, before launch (e.g. install to /Applications)
                   May return a new app_path to launch instead.
    is_running_fn: callable() -> bool, custom process check (default: pgrep -x process)
    kill_fn:       callable() to kill the running app (default: pkill -x process)
    """
    import time

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

        log_proc, log_thread, log_fh = stream_os_log(
            predicate, log_file=_core.LOG_FILE)

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
                 pre_build=None, post_build=None):
    """Full iOS simulator dev loop: build, install, stream logs, reload on 'r'/SIGUSR1.

    bundle_id:  bundle ID for launch/terminate (defaults to subsystem)
    sim_id:     simulator device ID (if None, auto-resolves via resolve_simulator)
    pre_build:  callable() run before each build (e.g. swap dev icons)
    post_build: callable() run after each build completes (e.g. restore icons)
    """
    import time

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
        info("Installing on simulator...")
        install_and_launch_sim(sim_id, app_path, launch_bundle)

        def _is_running():
            return is_sim_app_running(sim_id, launch_bundle)

        for _ in range(10):
            if _is_running():
                break
            time.sleep(0.3)

        log_proc, log_thread, log_fh = stream_os_log(
            predicate, log_file=_core.LOG_FILE, sim_id=sim_id)

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
                    pre_build=None, post_build=None,
                    skip_build=False):
    """Build, install, and run on a physical iOS device with log streaming.

    Launches via devicectl --console with OS_ACTIVITY_DT_MODE=YES so os_log
    messages are non-redacted. Single process replaces the old lldb +
    idevicesyslog approach.

    bundle_id:  bundle ID for launch/terminate (defaults to subsystem)
    device_id:  CoreDevice identifier (if None, auto-resolves via find_connected_device)
    pre_build:  callable() run before each build (e.g. swap dev icons)
    post_build: callable() run after each build completes (e.g. restore icons)
    """
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

        info(f"Installing on {device_name}...")
        rc = install_on_device(device_id, app_path)
        if rc != 0:
            err("Install failed")
            sys.exit(1)

    info(f"Launching on {device_name}...")
    console_proc = launch_on_device(
        device_id, launch_bundle, console=True,
        env=_DEVICE_LOG_ENV)

    _, log_fh = stream_console_output(console_proc, log_file=_core.LOG_FILE)

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
