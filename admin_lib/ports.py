"""Port / socket helpers — port detection, listener identification, port-clearing.

Detection chain for `port_holders`:
  1. /proc/net/tcp{,6} + /proc/<pid>/fd  (Linux, no external deps)
  2. fuser -n tcp PORT                   (Linux, fallback)
  3. lsof -nP -iTCP:PORT -sTCP:LISTEN    (macOS / BSDs)

The /proc walk is required: many minimal Linux containers ship without lsof
or netstat, and silently returning [] there would make every kill-by-port
path a no-op.

Higher-level dev-process cleanup (pidfiles, argv/cwd sweep, four-pass nuke)
lives in the optional `ports_nuke` companion module.
"""

import os
import signal
import subprocess
import sys
import time


def kill_port(port, timeout=2.0):
    """SIGTERM any TCP listener on `port`. Silent no-op on misses or races.

    Returns the list of PIDs signalled (may be empty). Intended for "clear stale
    dev server on startup" — failures aren't fatal, the caller just wants the port.
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

    Tries /proc first, falls back to fuser, then lsof. Returns [] if nothing
    is listening or every detection method is unavailable.
    """
    pids = _port_holder_pids(port, timeout=timeout)
    holders = []
    seen = set()
    for pid in pids:
        if pid in seen:
            continue
        seen.add(pid)
        ppid, pcmd = _ps_parent(pid)
        holders.append({
            "pid": pid,
            "command": _ps_command(pid) or "?",
            "parent_pid": ppid,
            "parent_command": pcmd,
        })
    return holders



def _port_holder_pids(port, timeout=2.0):
    """Find listener pids via /proc, then fuser, then lsof."""
    inodes = _proc_net_listeners(port)
    pids = _pids_for_socket_inodes(inodes)
    if pids:
        return pids

    try:
        r = subprocess.run(
            ["fuser", "-n", "tcp", str(port)],
            capture_output=True, text=True, timeout=timeout,
        )
        # fuser prints PIDs to stdout as space-separated integers; stderr is
        # diagnostic noise (we'd previously scrape pids from path components
        # like "/proc/37/fd/0" if we parsed it). Stick to stdout.
        for tok in (r.stdout or "").split():
            try:
                pids.append(int(tok))
            except ValueError:
                continue
        if pids:
            return pids
    except (FileNotFoundError, subprocess.TimeoutExpired):
        pass

    try:
        r = subprocess.run(
            ["lsof", "-nP", f"-iTCP:{port}", "-sTCP:LISTEN"],
            capture_output=True, text=True, timeout=timeout,
        )
        for line in r.stdout.splitlines()[1:]:
            parts = line.split()
            if len(parts) >= 2:
                try:
                    pids.append(int(parts[1]))
                except ValueError:
                    continue
    except (FileNotFoundError, subprocess.TimeoutExpired):
        pass

    return pids


def _proc_net_listeners(port):
    """Parse /proc/net/tcp{,6} for sockets in LISTEN state on `port`. Returns [inode_str]."""
    port_hex = f"{port:04X}"
    inodes = []
    for path in ("/proc/net/tcp", "/proc/net/tcp6"):
        try:
            with open(path) as f:
                next(f, None)  # skip header
                for line in f:
                    parts = line.split()
                    if len(parts) < 10:
                        continue
                    if parts[3] != "0A":  # 0A = TCP_LISTEN
                        continue
                    if not parts[1].endswith(":" + port_hex):
                        continue
                    inodes.append(parts[9])
        except (FileNotFoundError, PermissionError, OSError):
            continue
    return inodes


def _pids_for_socket_inodes(inodes):
    """Walk /proc/<pid>/fd ONCE looking for any socket fd matching `inodes`."""
    if not inodes:
        return []
    targets = {f"socket:[{ino}]" for ino in inodes}
    self_pid = os.getpid()
    pids = []
    try:
        entries = os.listdir("/proc")
    except (OSError, FileNotFoundError):
        return pids
    for entry in entries:
        if not entry.isdigit():
            continue
        pid = int(entry)
        if pid == self_pid:
            continue
        fd_dir = f"/proc/{pid}/fd"
        try:
            fds = os.listdir(fd_dir)
        except (PermissionError, FileNotFoundError, OSError):
            continue
        for fd in fds:
            try:
                target = os.readlink(f"{fd_dir}/{fd}")
            except OSError:
                continue
            if target in targets:
                pids.append(pid)
                break
    return pids


def preflight_port(port, label=None, force=False, timeout=2.0):
    """Ensure `port` is free. If held: TTY → prompt; non-TTY or force=True → SIGTERM/KILL.

    Returns True if free or cleared, False if the user declined or kill failed.
    Use from a `dev` command to surface conflicts instead of silently failing.
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
        from .docker import _reload_active
        _in_reload = _reload_active
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
