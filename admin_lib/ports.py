"""Port / socket helpers.

Generic TCP/UDP port utilities that project-specific dev commands can call.
Kept minimal — single-purpose functions, no global state.
"""

import os
import signal
import subprocess
import sys
import time


def kill_port(port, timeout=2.0):
    """SIGTERM any process listening on the given TCP port (macOS `lsof -ti`).

    Silently no-ops if nothing is listening, the lookup times out, or the
    process has already exited by the time kill fires. Intended for "clear
    stale dev server on startup" flows where a failure to clean up is not
    fatal — the caller just wants the port free.

    Returns the list of PIDs that were signalled (may be empty).
    """
    pids = [h["pid"] for h in port_holders(port, timeout=timeout)]
    for pid in pids:
        try:
            os.kill(pid, signal.SIGTERM)
        except (OSError, ProcessLookupError, PermissionError):
            pass
    return pids


def port_holders(port, timeout=2.0):
    """Return [{pid, command, parent_pid, parent_command}] for TCP listeners on `port`.

    Uses `lsof -nP -iTCP:PORT -sTCP:LISTEN`. Returns [] if lsof is missing,
    times out, or nothing is listening. `command` is the full argv from `ps`
    (falls back to lsof's COMMAND column on failure).
    """
    try:
        result = subprocess.run(
            ["lsof", "-nP", f"-iTCP:{port}", "-sTCP:LISTEN"],
            capture_output=True, text=True, timeout=timeout,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return []

    holders = []
    seen = set()
    for line in result.stdout.splitlines()[1:]:
        parts = line.split()
        if len(parts) < 2:
            continue
        try:
            pid = int(parts[1])
        except ValueError:
            continue
        if pid in seen:
            continue
        seen.add(pid)
        ppid, pcmd = _ps_parent(pid)
        holders.append({
            "pid": pid,
            "command": _ps_command(pid) or parts[0],
            "parent_pid": ppid,
            "parent_command": pcmd,
        })
    return holders


def preflight_port(port, label=None, force=False, timeout=2.0):
    """Ensure `port` is free. If something is listening, show what and prompt to kill.

    Returns True if the port is free (or was freed), False if the user declined
    or kills failed. Behavior:
      - port idle -> return True silently
      - port held + force=True -> SIGTERM (then SIGKILL stragglers), return True if cleared
      - port held + non-TTY stdin -> force-kill (same as force=True, logs to stderr)
      - port held + TTY -> show holders, prompt `Kill it? [y/N]`, act on response

    Pair with kill_port for the no-questions-asked variant. Use this from any
    `dev` command that wants to surface "another dev server is already running"
    rather than silently inheriting a partially-broken state.
    """
    holders = port_holders(port, timeout=timeout)
    if not holders:
        return True

    tag = f" ({label})" if label else ""
    print(f"\nPort {port}{tag} is already in use:", file=sys.stderr)
    for h in holders:
        parent = ""
        if h["parent_pid"]:
            parent_cmd = h["parent_command"] or "?"
            parent = f"  parent pid {h['parent_pid']}: {parent_cmd}"
        print(f"  pid {h['pid']:>6}: {h['command']}{parent}", file=sys.stderr)

    try:
        from . import docker as _docker
        _in_reload = getattr(_docker, "_reload_active", False)
    except ImportError:
        _in_reload = False

    if force or _in_reload:
        pass  # fall through to kill
    elif not sys.stdin.isatty():
        print(
            f"Stdin is not a TTY — force-killing port {port}{tag} holders.",
            file=sys.stderr,
        )
        # fall through to kill
    else:
        try:
            input("Kill it? [Enter to kill, Ctrl+C to abort] ")
        except (EOFError, KeyboardInterrupt):
            print(file=sys.stderr)
            return False

    pids = [h["pid"] for h in holders]
    cleared = _kill_pids(pids, timeout=timeout)
    if cleared:
        print(f"Port {port} cleared.", file=sys.stderr)
    else:
        print(f"Failed to clear port {port}.", file=sys.stderr)
    return cleared


def _ps_command(pid):
    try:
        r = subprocess.run(
            ["ps", "-p", str(pid), "-o", "command="],
            capture_output=True, text=True, timeout=1.0,
        )
        return r.stdout.strip() or None
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return None


def _ps_parent(pid):
    try:
        r = subprocess.run(
            ["ps", "-p", str(pid), "-o", "ppid="],
            capture_output=True, text=True, timeout=1.0,
        )
        ppid_str = r.stdout.strip()
        if not ppid_str:
            return (None, None)
        ppid = int(ppid_str)
    except (FileNotFoundError, subprocess.TimeoutExpired, ValueError):
        return (None, None)
    return (ppid, _ps_command(ppid))


def _kill_pids(pids, timeout=2.0):
    for pid in pids:
        try:
            os.kill(pid, signal.SIGTERM)
        except (OSError, ProcessLookupError, PermissionError):
            pass
    deadline = time.time() + timeout
    remaining = list(pids)
    while remaining and time.time() < deadline:
        time.sleep(0.1)
        remaining = [p for p in remaining if _pid_alive(p)]
    for pid in remaining:
        try:
            os.kill(pid, signal.SIGKILL)
        except (OSError, ProcessLookupError, PermissionError):
            pass
    time.sleep(0.1)
    return not any(_pid_alive(p) for p in pids)


def _pid_alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    except OSError:
        return False
