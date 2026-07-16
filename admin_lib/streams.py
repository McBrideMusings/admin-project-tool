"""Pump a subprocess's output to the terminal and the log file at once.

The shape every live-log dev loop needs: a child is already running, and its
output must reach the terminal (formatted, for a human watching) and the log
file (for reading afterwards) from one read of one pipe. Splitting the pump
from the argv-building keeps the platform-specific part — `simctl spawn … log
stream` for a simulator, `devicectl … --console` for a device, `adb logcat` for
Android — in the module that owns that platform, while the pumping and teardown
live here once.

These were previously private to apple.py (`stream_console_output`/
`stop_os_log`) despite having nothing Apple-specific in them, which is the same
misfiling that hid `run_with_reload` inside docker.py: android's logcat loop
went and grew its own copy rather than find them.
"""
from __future__ import annotations

import sys
import threading

from .core import _children
from .reload import _wait_or_kill


def stream_subprocess(proc, log_file=None, formatter=None):
    """Stream proc's stdout to the terminal and log_file in a daemon thread.

    proc:      a Popen with stdout=PIPE (text mode) already running.
    log_file:  path to append to; None streams to the terminal only.
    formatter: optional callable(line) -> line, applied before both writes.
    Returns (thread, log_fh) — pass log_fh to stop_stream() to close it.
    """
    log_fh = open(log_file, "a") if log_file else None

    def _pump():
        try:
            for line in proc.stdout:
                out = formatter(line) if formatter else line
                sys.stdout.write(out)
                sys.stdout.flush()
                if log_fh:
                    log_fh.write(out)
                    log_fh.flush()
        except (IOError, ValueError):
            # The pipe closing under us on teardown is the normal exit path.
            pass

    thread = threading.Thread(target=_pump, daemon=True)
    thread.start()
    return thread, log_fh


def stop_stream(proc, log_fh=None):
    """Terminate a streaming subprocess and close its log file."""
    proc.terminate()
    _wait_or_kill(proc, timeout=3)
    if proc in _children:
        _children.remove(proc)
    if log_fh:
        log_fh.close()
