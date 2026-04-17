"""Port / socket helpers.

Generic TCP/UDP port utilities that project-specific dev commands can call.
Kept minimal — single-purpose functions, no global state.
"""

import os
import signal
import subprocess


def kill_port(port, timeout=2.0):
    """SIGTERM any process listening on the given TCP port (macOS `lsof -ti`).

    Silently no-ops if nothing is listening, the lookup times out, or the
    process has already exited by the time kill fires. Intended for "clear
    stale dev server on startup" flows where a failure to clean up is not
    fatal — the caller just wants the port free.

    Returns the list of PIDs that were signalled (may be empty).
    """
    try:
        result = subprocess.run(
            ["lsof", "-ti", f":{port}"],
            capture_output=True, text=True, timeout=timeout,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return []

    pids = []
    for raw in result.stdout.strip().splitlines():
        try:
            pid = int(raw)
        except ValueError:
            continue
        try:
            os.kill(pid, signal.SIGTERM)
            pids.append(pid)
        except (OSError, ProcessLookupError, PermissionError):
            pass
    return pids
