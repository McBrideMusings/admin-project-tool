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


def port_free(port):
    """Authoritative free/busy probe via bind(). True only if nothing is
    *listening* on the port, on either IP stack.

    This is the source of truth for "is the port free" — NOT the lsof/proc
    holder lookup. A bind() touches no process file tables, so it can't hang on
    a stale/disconnected network mount the way `lsof` can (lsof has to stat
    every process's open fds, and one fd pointing into a wedged SMB/NFS mount
    blocks the whole scan). port_holders is then only used to *name* the holder
    for the kill prompt; if it fails, that must never be read as "port is free".

    The probe mirrors what a real dev server does, so "free" means "the server
    could bind here", not the stricter "nothing whatsoever touches this port":

    - SO_REUSEADDR is set (as essentially every server sets it), so a connection
      socket left in TIME_WAIT by a just-exited server reads as FREE. Without
      this, a back-to-back `admin dev` restart would be refused for ~30-60s even
      though nothing is listening — the server itself would have rebound fine.
    - It probes the wildcard AND loopback of both stacks (see _PORT_PROBE_ADDRS).
      SO_REUSEADDR lets a wildcard bind succeed *over* a live loopback listener,
      so the wildcard probe alone would false-"free" a Vite-on-127.0.0.1; the
      exact-address probe still collides and reports busy.
    - Only EADDRINUSE counts as busy. An IPv6 probe failing because the stack is
      disabled or absent (EADDRNOTAVAIL / EAFNOSUPPORT) means "skip this family",
      NOT "busy" — otherwise a host with IPv6 off would report every free port
      occupied, the inverse silent-failure.
    """
    import errno
    import socket
    # Probe the wildcard AND loopback of each stack: a listener bound to a
    # specific address (Vite on 127.0.0.1, a server on ::1) only collides with a
    # bind to that same address, so the wildcard probe alone would miss it.
    probes = [
        (socket.AF_INET, "0.0.0.0"),
        (socket.AF_INET, "127.0.0.1"),
        (socket.AF_INET6, "::"),
        (socket.AF_INET6, "::1"),
    ]
    for family, host in probes:
        try:
            s = socket.socket(family, socket.SOCK_STREAM)
        except OSError:
            continue  # family unsupported on this host — skip it
        try:
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            if family == socket.AF_INET6:
                # Probe each stack independently so a dual-stack bind doesn't
                # collide with the IPv4 probe (or vice versa).
                try:
                    s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
                except (OSError, AttributeError):
                    pass
            s.bind((host, port))
        except OSError as e:
            if e.errno == errno.EADDRINUSE:
                return False
            # EADDRNOTAVAIL / EAFNOSUPPORT / etc — this address/family isn't
            # usable here; that's not the port being held. Skip and keep going.
        finally:
            s.close()
    return True


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


def preflight_port(port, label=None, force=False, timeout=2.0, project_root=None):
    """Ensure `port` is free. If held: TTY → prompt; non-TTY or force=True → SIGTERM/KILL.

    Returns True if free or cleared, False if the user declined, the kill
    failed, or the port is held but no holder could be identified. Use from a
    `dev` command to surface conflicts instead of silently failing.

    Free/busy is decided by `port_free` (a bind probe), NOT by whether a holder
    was found. This is the fix for the silent-pass bug: on macOS `lsof` can hang
    on a stale network mount and time out, and the old code read that empty
    result as "port is free" and marched straight into a bind failure. Holder
    lookup is now only for *naming* what to kill; when it can't, we say so and
    refuse to proceed rather than pretend the port is clear.
    """
    tag = f" ({label})" if label else ""

    if port_free(port):
        return True

    # Port is occupied. Name the holder(s): lsof/proc first (fast on Linux),
    # then a ps + per-pid-cwd sweep scoped to the project (the only thing that
    # works when lsof hangs on macOS).
    holders = port_holders(port, timeout=timeout)
    if not holders:
        try:
            from .ports_nuke import project_dev_holders
            holders = project_dev_holders(project_root or os.getcwd(), timeout=timeout)
        except Exception:
            holders = []

    print(f"\nPort {port}{tag} is already in use:", file=sys.stderr)
    if holders:
        for h in holders:
            parent = ""
            if h.get("parent_pid"):
                parent_cmd = h.get("parent_command") or "?"
                parent = f"  parent pid {h['parent_pid']}: {parent_cmd}"
            print(f"  pid {h['pid']:>6}: {h.get('command') or '?'}{parent}", file=sys.stderr)
    else:
        # Busy, but nothing could be named. Do NOT silently proceed — that's the
        # exact failure this function exists to prevent.
        print(
            "  (could not identify the holder — `lsof` timed out, likely blocked\n"
            "   on a slow/disconnected network mount, and no project dev process matched)",
            file=sys.stderr,
        )
        print(f"Free port {port}{tag} manually (e.g. `admin kill`) and retry.", file=sys.stderr)
        return False

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

    _kill_pids([h["pid"] for h in holders], timeout=timeout)
    # Re-probe with bind, not pid-liveness: a child can respawn, or the socket
    # can linger after the pid dies. The bind probe is the real answer.
    if port_free(port):
        print(f"Port {port} cleared.", file=sys.stderr)
        return True
    print(f"Failed to clear port {port}.", file=sys.stderr)
    return False



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
