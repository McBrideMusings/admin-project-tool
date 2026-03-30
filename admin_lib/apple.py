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
    _custom_commands,
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

def find_connected_device():
    """Find a connected iOS device via xcrun devicectl.

    Returns (device_id, device_name) or calls sys.exit(1).
    """
    import json

    info("Finding connected iOS device...")
    json_path = os.path.join(_core.LOG_DIR, "devices.json")
    os.makedirs(_core.LOG_DIR, exist_ok=True)
    result = subprocess.run(
        ["xcrun", "devicectl", "list", "devices", "--json-output", json_path],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        err("Failed to list devices. Is a device connected?")
        sys.exit(1)

    try:
        with open(json_path) as f:
            data = json.load(f)
        devices = [
            d for d in data.get("result", {}).get("devices", [])
            if d.get("connectionProperties", {}).get("transportType") in ("wired", "localNetwork")
        ]
    except (json.JSONDecodeError, KeyError, FileNotFoundError):
        devices = []

    if not devices:
        err("No connected iOS device found.")
        err("Connect via USB, or enable wireless debugging:")
        err("  Xcode > Window > Devices and Simulators > select device > Connect via network")
        sys.exit(1)

    device = devices[0]
    device_id = device.get("identifier", "")
    device_name = device.get("deviceProperties", {}).get("name", "Unknown")
    info(f"Target device: {device_name} ({device_id})")
    return device_id, device_name


def install_on_device(device_id, app_path):
    """Install an app on a physical iOS device. Returns exit code."""
    from .core import run_cmd
    return run_cmd(
        f'xcrun devicectl device install app --device "{device_id}" "{app_path}" 2>&1'
    )


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


def swap_dev_icons(generate_script, dev_icon_dir, icon_dirs):
    """Run icon generation script and copy dev icons into asset catalogs.

    generate_script: path to the Python script that generates dev icons
    dev_icon_dir: directory where the script writes output (with label subdirs)
    icon_dirs: same dict as backup_icons
    Returns True on success, False if generation failed.
    """
    import shutil
    info("Generating dev icons...")
    rc = subprocess.run(
        [sys.executable, generate_script],
        capture_output=True, text=True,
    )
    if rc.returncode != 0:
        warn(f"Dev icon generation failed: {rc.stderr}")
        return False
    for label, dest_dir in icon_dirs.items():
        src = os.path.join(dev_icon_dir, label)
        if not os.path.isdir(src):
            continue
        for fname in os.listdir(src):
            if fname.endswith(".png"):
                shutil.copy2(os.path.join(src, fname), os.path.join(dest_dir, fname))
    return True


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
