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
