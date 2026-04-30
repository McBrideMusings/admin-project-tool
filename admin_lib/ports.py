"""Port / socket helpers and bulletproof dev-process cleanup.

Generic TCP/UDP port utilities that project-specific dev commands can call.
Kept minimal — single-purpose functions, no global state.

Detection chain for `port_holders`:
  1. /proc/net/tcp{,6} + /proc/<pid>/fd  (Linux, no external deps)
  2. fuser -n tcp PORT                   (Linux, fallback)
  3. lsof -nP -iTCP:PORT -sTCP:LISTEN    (macOS / BSDs)

This must be robust: many minimal Linux containers ship with neither lsof
nor ss/netstat installed, and silently returning [] there means every kill-
by-port path becomes a no-op. Walking /proc directly has no dependency.

`nuke_dev_processes` is the high-level entry point used by project `kill`
commands — it composes port detection, pidfile reading, and argv/cwd
matching into a single 4-pass sweep that catches even orphaned processes
whose ports aren't bound and whose pidfiles are stale.
"""

import json
import os
import re
import signal
import subprocess
import sys
import time

from .core import info, ok


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


def list_processes(skip_self=True):
    """Yield `{pid, argv, cmdline, argv0, cwd}` for every readable /proc entry.

    Use this for precise process matching: argv is the actual token list (not
    a space-joined string), so callers can check "is `<path>/worker.js` a
    standalone argv element" without false-positives from shell wrappers
    (Claude Code, sudo, etc.) that embed a project path inside one big
    `bash -c '...'` argument.

    `cwd` is the symlink target of /proc/<pid>/cwd, or None if unreadable —
    useful for catching orphans whose argv doesn't include the project path
    (e.g. `sh -c 'cd web && npm run dev'`).
    """
    self_pid = os.getpid()
    try:
        entries = os.listdir("/proc")
    except (OSError, FileNotFoundError):
        return
    for entry in entries:
        if not entry.isdigit():
            continue
        pid = int(entry)
        if skip_self and pid == self_pid:
            continue
        try:
            with open(f"/proc/{pid}/cmdline", "rb") as f:
                raw = f.read()
        except (FileNotFoundError, PermissionError, OSError):
            continue
        if not raw:
            continue
        argv = [t.decode("utf-8", errors="replace") for t in raw.split(b"\x00") if t]
        if not argv:
            continue
        try:
            cwd = os.readlink(f"/proc/{pid}/cwd")
        except OSError:
            cwd = None
        yield {
            "pid": pid,
            "argv": argv,
            "cmdline": " ".join(argv),
            "argv0": argv[0],
            "cwd": cwd,
        }


def find_pids_by_cmdline(pattern):
    """Return pids whose joined argv matches `pattern` (regex).

    Convenience wrapper around `list_processes`. For most cleanup work,
    prefer `list_processes` directly so you can match on argv tokens or cwd
    instead of a flattened string — that avoids matching shell wrappers
    that happen to embed your search path inside a `bash -c` argument.
    """
    rx = re.compile(pattern)
    return [p["pid"] for p in list_processes() if rx.search(p["cmdline"])]


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


def kill_port_interactive(port):
    """Interactive kill-by-port for standalone `kill` commands.

    Prints who is on the port, prompts for confirmation, then kills.
    Returns True if cleared (or nothing was there), False if the user declined
    or the kill failed. Caller is responsible for sys.exit if needed.
    """
    holders = port_holders(port)
    if not holders:
        print(f"No process on port {port} — nothing to kill.")
        return True

    print(f"\nPort {port} is in use:")
    for h in holders:
        print(f"  pid {h['pid']:>6}: {h['command']}")
        if h["parent_pid"]:
            parent_cmd = h["parent_command"] or "?"
            print(f"          parent pid {h['parent_pid']}: {parent_cmd}")
    print()

    pids = [h["pid"] for h in holders]

    if not sys.stdin.isatty():
        print(f"Stdin is not a TTY — force-killing port {port} holders.", file=sys.stderr)
    else:
        try:
            input("Kill it? [Enter to kill, Ctrl+C to abort] ")
        except (EOFError, KeyboardInterrupt):
            print()
            return False

    if _kill_pids(pids):
        print(f"Port {port} cleared.")
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


def nuke_dev_processes(
    project_root,
    *,
    ports=(),
    pidfiles=(),
    js_files=(),
    web_subdirs=(),
    runtimes=("bun", "node", "python", "python3"),
    web_argv_markers=("vite", "esbuild", "npm run dev"),
):
    """Bulletproof dev-server cleanup. Four passes, each a fallback for the previous.

    The lazy version ("kill the bun process I just started") fails the
    moment a previous run was force-killed, ssh-disconnected, OOM-killed, or
    its pidfile got stale. This sweep is what to call from a `kill` command
    when you want guaranteed cleanup regardless of how the prior run died:

      1. Bound ports (via /proc/net/tcp, fuser, or lsof). Kills the listener
         AND its bun/node/python supervisor parent — the supervisor often
         outlives a port reset and would respawn the listener.
      2. Pidfiles. Each entry: `{path, format: "int" | "json", key: "pid"}`.
         JSON pidfiles read `key` from the parsed object. Stale pidfiles
         (process gone) are silently unlinked.
      3. argv/cwd sweep. Walks /proc/<pid>/cmdline + cwd to find:
           a. argv0 basename ∈ runtimes AND argv contains an exact match for
              one of project_root/<js_files>
           b. argv0 absolute path inside any web_subdirs (catches the bare
              esbuild service worker that has only a path-style argv0)
           c. cwd inside any web_subdirs AND cmdline contains any
              web_argv_markers (catches `sh -c 'cd web && npm run dev'`-style
              wrappers that have NO project path in their argv at all)

    Args:
        project_root:    absolute path to the project, used to anchor argv/cwd
                         matching. Pass `os.getcwd()` for the typical case.
        ports:           iterable of `int` or `(port, label)`. Each port's
                         bound listeners + likely supervisor parents are killed.
        pidfiles:        iterable of `dict`s describing pidfile locations.
                         {"path": "...", "format": "int"|"json", "key": "pid"}
                         (key defaults to "pid" for json format).
        js_files:        filenames (e.g. ["main.js", "worker.js"]) located
                         directly under project_root that bun/node may run.
        web_subdirs:     subdir names (e.g. ["web"]) under project_root.
        runtimes:        argv0 basenames considered "is running our js_files".
                         Default covers the common bun/node/python cases.
        web_argv_markers: substrings in cmdline that mark a process as
                         frontend-related. Default catches vite/esbuild/npm.

    Returns: number of distinct pids killed.
    """
    project_root = os.path.abspath(project_root)
    js_paths = {os.path.join(project_root, name) for name in js_files}
    web_dirs = [os.path.join(project_root, sub) for sub in web_subdirs]

    killed = set()

    def _kill_set(pids, label):
        pids = sorted({int(p) for p in pids if p and int(p) != os.getpid() and int(p) not in killed})
        if not pids:
            return
        info(f"killing {label}: {pids}")
        _kill_pids(pids, timeout=3.0)
        killed.update(pids)

    # 1. Ports.
    for entry in ports:
        if isinstance(entry, (tuple, list)):
            port, _label = entry[0], (entry[1] if len(entry) > 1 else None)
        else:
            port = entry
        holders = port_holders(port)
        if not holders:
            continue
        pids = set()
        for h in holders:
            pids.add(h["pid"])
            ppid = h.get("parent_pid")
            pcmd = h.get("parent_command") or ""
            if ppid and any(k in pcmd for k in ("bun", "node", "npm", "python", "main.js")):
                pids.add(ppid)
        _kill_set(pids, f"port-{port} holders")

    # 2. Pidfiles.
    for spec in pidfiles:
        path = spec.get("path")
        if not path:
            continue
        fmt = spec.get("format", "int")
        key = spec.get("key", "pid")
        pid = None
        try:
            with open(path) as f:
                if fmt == "json":
                    pid = int(json.load(f).get(key) or 0)
                else:
                    pid = int(f.read().strip())
        except (FileNotFoundError, ValueError, OSError, json.JSONDecodeError):
            pid = None
        if pid and pid != os.getpid() and pid not in killed:
            try:
                os.kill(pid, 0)
                _kill_set([pid], f"pidfile {os.path.basename(path)}")
            except ProcessLookupError:
                pass
        try:
            os.unlink(path)
        except OSError:
            pass

    # 3. argv/cwd sweep.
    runtime_set = set(runtimes)
    straggler_pids = set()
    for proc in list_processes():
        if proc["pid"] in killed:
            continue
        argv = proc["argv"]
        argv0 = proc["argv0"] or ""
        cwd = proc["cwd"] or ""
        argv0_base = os.path.basename(argv0)

        if argv0_base in runtime_set and any(tok in js_paths for tok in argv):
            straggler_pids.add(proc["pid"])
            continue
        if any(argv0.startswith(d + os.sep) for d in web_dirs):
            straggler_pids.add(proc["pid"])
            continue
        if any(cwd == d or cwd.startswith(d + os.sep) for d in web_dirs):
            if any(t in proc["cmdline"] for t in web_argv_markers):
                straggler_pids.add(proc["pid"])
                continue
    if straggler_pids:
        _kill_set(straggler_pids, "argv/cwd-matched stragglers")

    if killed:
        ok(f"dev server stopped ({len(killed)} processes)")
    else:
        info("nothing was running")
    return len(killed)
