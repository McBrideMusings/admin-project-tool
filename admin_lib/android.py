"""Android dev loop — build, install, launch, and stream logcat.

Unlike a physical iOS device, `adb logcat` reads a system-wide ring buffer and
is completely independent of who launched the app. So this keeps React Native's
own CLI for build+install+launch and streams the log alongside it, rather than
driving gradle directly. (The iOS device path can't do that: its only log
source is `devicectl process launch --console`, which is bound to the launch —
see admin_lib/react_native.py.) Less reimplementation, same result.

logcat is filtered to the app's own pid. The unfiltered buffer is every process
on the device — thousands of lines a minute of unrelated system chatter that
buries the app's output. The pid is resolved after launch and re-resolved on
each rebuild, since a relaunched app gets a new one.
"""
from __future__ import annotations

import shlex
import subprocess
import sys

from .core import _children, err, get_log_file, info, run_cmd, warn
from .reload import (
    cleanup_pid_file,
    clear_rebuild_request,
    is_rebuild_requested,
    poll_for_reload,
    setup_reload,
)
from .streams import stop_stream, stream_subprocess


def adb_devices() -> list[tuple[str, str]]:
    """Return [(serial, state), ...] for attached devices/emulators."""
    try:
        out = subprocess.run(["adb", "devices"], capture_output=True, text=True, timeout=15)
    except (OSError, subprocess.TimeoutExpired):
        return []
    devices = []
    for line in out.stdout.splitlines()[1:]:
        parts = line.split()
        if len(parts) >= 2:
            devices.append((parts[0], parts[1]))
    return devices


def resolve_android_device(serial: str | None = None) -> str | None:
    """Pick a usable device serial, or None with a diagnostic already printed.

    An 'unauthorized' or 'offline' device is reported by name rather than
    silently skipped — it is almost always a phone waiting on the "Allow USB
    debugging?" prompt, and "no devices found" would send the user hunting for
    the wrong problem.
    """
    devices = adb_devices()
    if serial:
        for s, state in devices:
            if s == serial:
                if state != "device":
                    err(f"device {s} is '{state}' — check the device's USB-debugging prompt")
                    return None
                return s
        err(f"device {serial} not attached")
        return None

    usable = [s for s, state in devices if state == "device"]
    if len(usable) == 1:
        return usable[0]
    if not usable:
        for s, state in devices:
            warn(f"device {s} is '{state}', not usable")
        err("no Android device or emulator attached — start an emulator or plug in a device")
        return None
    err(f"multiple Android devices attached ({', '.join(usable)}) — "
        f"set react_native.android_serial in admin.toml")
    return None


def _adb(serial: str | None, *args: str) -> list[str]:
    return ["adb"] + (["-s", serial] if serial else []) + list(args)


def app_pid(package: str, serial: str | None = None) -> str | None:
    """Return the running app's pid as a string, or None if not running."""
    try:
        out = subprocess.run(_adb(serial, "shell", "pidof", "-s", package),
                             capture_output=True, text=True, timeout=10)
    except (OSError, subprocess.TimeoutExpired):
        return None
    pid = out.stdout.strip()
    return pid or None


def is_android_app_running(package: str, serial: str | None = None) -> bool:
    return app_pid(package, serial) is not None


def stream_logcat(pid: str, log_file=None, serial=None, formatter=None):
    """Stream `adb logcat` for one pid to stdout and log_file.

    Returns (proc, thread, log_fh) — caller must call stop_stream().
    """
    if log_file is None:
        log_file = get_log_file()

    proc = subprocess.Popen(
        _adb(serial, "logcat", f"--pid={pid}", "-v", "time"),
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        bufsize=1, universal_newlines=True,
    )
    _children.append(proc)

    thread, log_fh = stream_subprocess(proc, log_file=log_file, formatter=formatter)
    return proc, thread, log_fh


def dev_loop_android(app_dir, package, pid_file="/tmp/admin-run.pid",
                     serial=None, variant=None, output_config=None):
    """Build, install, launch, and stream logcat. Loops on 'r'/SIGUSR1.

    app_dir: dir holding package.json (Metro must already be serving).
    package: applicationId of the build being run (the debug one for dev).
    """
    from .log_columns import make_column_formatter

    serial = resolve_android_device(serial)
    if serial is None:
        sys.exit(1)

    setup_reload(pid_file)
    mode = f" --mode {variant}" if variant else ""

    while True:
        clear_rebuild_request()
        subprocess.run(_adb(serial, "shell", "am", "force-stop", package),
                       capture_output=True)
        # Drop everything buffered from the previous run so the stream starts at
        # this launch — logcat's ring buffer survives app restarts.
        subprocess.run(_adb(serial, "logcat", "-c"), capture_output=True)

        rc = run_cmd(
            f"cd {shlex.quote(str(app_dir))} && npx react-native run-android"
            f" --no-packager --deviceId {shlex.quote(serial)}{mode}"
        )
        if rc != 0:
            err("Build/install failed")
            if is_rebuild_requested():
                warn("Rebuild requested during failed build, retrying...")
                continue
            cleanup_pid_file(pid_file)
            sys.exit(1)

        pid = app_pid(package, serial)
        if pid is None:
            err(f"{package} is not running after launch — cannot attach logcat")
            err("check that react_native.android_package matches the debug applicationId")
            cleanup_pid_file(pid_file)
            sys.exit(1)

        fmt = make_column_formatter("generic", output_config)
        log_proc, _thread, log_fh = stream_logcat(
            pid, log_file=get_log_file(), serial=serial, formatter=fmt.format_line)

        info("Press r to rebuild, Ctrl+C to quit")
        print()

        got_reload = poll_for_reload(lambda: is_android_app_running(package, serial))

        stop_stream(log_proc, log_fh)
        print()

        if got_reload:
            info("Reload signal received — rebuilding...")
        else:
            info(f"Stopping {package}...")
        subprocess.run(_adb(serial, "shell", "am", "force-stop", package),
                       capture_output=True)

        if not got_reload:
            cleanup_pid_file(pid_file)
            info("Done.")
            return
