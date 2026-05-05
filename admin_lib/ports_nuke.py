"""Bulletproof dev-process cleanup — port + pidfile + argv sweep.

Companion to `ports.py`. Used by archetypes' `kill` / `nuke-dev` commands.
Calls into `ports.py` for port_holders and _kill_pids.
"""

import json
import os
import re
import sys

from .core import info, ok, resolve_env
from .ports import port_holders, _kill_pids


def list_processes(skip_self=True):
    """Yield {pid, argv, cmdline, argv0, cwd} for every readable /proc entry.

    argv is the actual token list (not space-joined), so callers can match on
    individual tokens without false positives from `bash -c '...'` wrappers.
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
    """Return pids whose joined argv matches `pattern` (regex)."""
    rx = re.compile(pattern)
    return [p["pid"] for p in list_processes() if rx.search(p["cmdline"])]


def kill_port_interactive(port):
    """Show port holders, prompt, then kill. Returns True if cleared."""
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
    """Bulletproof dev-server cleanup. Three passes, each a fallback for the previous.

    Why: simple "kill the bun process I just started" fails the moment a
    previous run was force-killed, ssh-disconnected, OOM'd, or left a stale
    pidfile. This sweep gives guaranteed cleanup regardless.

    Passes: (1) bound-port listeners + their bun/node/python supervisor
    parents; (2) pidfiles ({"path", "format": "int"|"json", "key"?}), stale
    ones unlinked; (3) /proc argv/cwd matching against project_root + js_files
    + web_subdirs + web_argv_markers.

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
            if ppid and any(k in pcmd for k in ("bun", "node", "npm", "python")):
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


def nuke_dev_from_config(cfg):
    """Adapter: read a manifest `[dev_kill]` table and call nuke_dev_processes.

    Schema: ports, pidfiles, js_files, web_subdirs, runtimes. ${VAR} placeholders
    in path/default strings are resolved via core.resolve_env at call time.
    """
    if not cfg:
        info("nothing was running")
        return 0
    pidfiles = []
    for p in cfg.get("pidfiles") or ():
        if "env" in p:
            path = os.environ.get(p["env"], resolve_env(p.get("default", "")))
        else:
            path = resolve_env(p.get("path", ""))
        if not path:
            continue
        pidfiles.append({
            "path": path,
            "format": p.get("format", "int"),
            "key": p.get("key", "pid"),
        })
    ports = []
    for entry in cfg.get("ports") or ():
        if isinstance(entry, dict):
            ports.append((entry.get("port"), entry.get("label")))
        else:
            ports.append(entry)
    return nuke_dev_processes(
        project_root=os.getcwd(),
        ports=ports,
        pidfiles=pidfiles,
        js_files=tuple(cfg.get("js_files") or ()),
        web_subdirs=tuple(cfg.get("web_subdirs") or ()),
        runtimes=tuple(cfg.get("runtimes") or ("bun", "node", "python", "python3")),
    )
