"""Bulletproof dev-process cleanup — port + pidfile + argv sweep.

Companion to `ports.py`. Used by archetypes' `kill` / `nuke-dev` commands.
Calls into `ports.py` for port_holders and _kill_pids.
"""

import json
import os
import re
import subprocess
import sys

from .core import err, info, ok, resolve_env
from .ports import port_holders, port_free, _kill_pids, _ps_parent


def list_processes(skip_self=True):
    """Yield {pid, argv, cmdline, argv0, cwd} for every visible process.

    Linux: walks /proc (no external deps). macOS/BSD (no /proc): falls back to
    `ps`. Either way argv is a token list so callers can match individual tokens
    without false positives from `bash -c '...'` wrappers. Note: in the `ps`
    fallback `cwd` is None (ps can't report it) and argv is recovered by a naive
    split, so callers that need a process's cwd should fill it lazily with
    `_pid_cwd` only for the small set they actually care about.
    """
    if os.path.isdir("/proc"):
        yield from _list_processes_proc(skip_self)
    else:
        yield from _list_processes_ps(skip_self)


def _list_processes_proc(skip_self=True):
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


def _list_processes_ps(skip_self=True):
    self_pid = os.getpid()
    try:
        r = subprocess.run(
            ["ps", "-Ao", "pid=,command="],
            capture_output=True, text=True, timeout=5,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return
    for line in r.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        parts = line.split(None, 1)
        if len(parts) < 2:
            continue
        try:
            pid = int(parts[0])
        except ValueError:
            continue
        if skip_self and pid == self_pid:
            continue
        cmdline = parts[1]
        # ps space-joins argv; a naive split is good enough for marker/cwd
        # matching (dev command paths don't contain spaces). cwd is unknown
        # here — _pid_cwd fills it on demand for the candidates that need it.
        argv = cmdline.split()
        yield {
            "pid": pid,
            "argv": argv,
            "cmdline": cmdline,
            "argv0": argv[0] if argv else "",
            "cwd": None,
        }


def _pid_cwd(pid, timeout=2.0):
    """Working directory of `pid`, or None. /proc on Linux; per-pid `lsof` on
    macOS/BSD. The lsof form is scoped to the cwd fd (`-d cwd`), so it stats one
    descriptor — it does NOT walk the process's data fds, which is what makes a
    full `lsof` scan hang on a wedged network mount.
    """
    try:
        return os.readlink(f"/proc/{pid}/cwd")
    except OSError:
        pass
    try:
        r = subprocess.run(
            ["lsof", "-a", "-p", str(pid), "-d", "cwd", "-Fn"],
            capture_output=True, text=True, timeout=timeout,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return None
    for ln in r.stdout.splitlines():
        if ln.startswith("n"):
            return ln[1:]
    return None


def _cwd_in_root(cwd, project_root):
    """True if `cwd` is `project_root` or a directory beneath it. A falsy/None
    cwd (e.g. `_pid_cwd` couldn't resolve it) is never inside the root.
    """
    if not cwd:
        return False
    return cwd == project_root or cwd.startswith(project_root + os.sep)


# Heuristics for "this looks like a dev process" — used when lsof can't map a
# port to a pid (macOS + a stale mount) and we must identify holders by what
# they are instead of what socket they hold.
_DEV_CMDLINE_MARKERS = (
    "cargo watch", "cargo-watch", "watchexec", "nodemon",
    "vite", "esbuild", "npm run dev", "bun run dev", "tsc -w", "tsc --watch",
)
_DEV_RUNTIMES = {"node", "bun", "deno", "python", "python3"}


def _is_project_dev_proc(proc, project_root):
    """True if `proc` is one of *this project's* dev processes.

    Deliberately narrow: a bare language runtime (node/bun/python) only counts
    when the script it runs lives inside project_root. Without that, unrelated
    tooling that merely runs with cwd inside the project — MCP servers, npx
    packages, editors — would be swept up and killed. Unambiguous dev markers
    (cargo-watch, vite, a target/debug build, …) are trusted on their own and
    scoped to the project by the caller's per-pid cwd check.
    """
    cmdline = proc.get("cmdline") or ""
    if any(m in cmdline for m in _DEV_CMDLINE_MARKERS):
        return True
    if "target/debug/" in cmdline or "target/release/" in cmdline:
        return True
    base = os.path.basename(proc.get("argv0") or "")
    if base in _DEV_RUNTIMES:
        prefix = os.path.abspath(project_root) + os.sep
        return any(tok.startswith(prefix) for tok in (proc.get("argv") or [])[1:])
    return False


def project_dev_holders(project_root, timeout=2.0):
    """Holder dicts (ports.port_holders shape) for this project's dev processes.

    The fallback for naming a port's holder when lsof can't map port→pid — i.e.
    macOS where a stale mount hangs the full lsof scan. We can't tell *which*
    port each process listens on without lsof, so on this degraded path every
    dev process rooted in `project_root` is treated as a holder and the prior
    session is torn down — fine, because `admin dev` is about to start a fresh
    one. Scoping is by per-pid cwd, so unrelated processes are never touched.
    """
    project_root = os.path.abspath(project_root)
    holders = []
    for proc in list_processes():
        if not _is_project_dev_proc(proc, project_root):
            continue
        cwd = proc.get("cwd") or _pid_cwd(proc["pid"], timeout=timeout)
        if not _cwd_in_root(cwd, project_root):
            continue
        ppid, pcmd = _ps_parent(proc["pid"])
        holders.append({
            "pid": proc["pid"],
            "command": proc.get("cmdline") or "?",
            "parent_pid": ppid,
            "parent_command": pcmd,
        })
    return holders


def _pid_ppid_map():
    """{pid: ppid} for every visible process, or {} if ps is unavailable.

    One `ps -Ao pid=,ppid=` call — portable across macOS and Linux, unlike
    `ps --ppid` (GNU-only) or `ps -P` (which on macOS is not a ppid filter).
    """
    try:
        r = subprocess.run(
            ["ps", "-Ao", "pid=,ppid="],
            capture_output=True, text=True, timeout=5,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return {}
    out = {}
    for line in r.stdout.splitlines():
        parts = line.split()
        if len(parts) < 2:
            continue
        try:
            out[int(parts[0])] = int(parts[1])
        except ValueError:
            continue
    return out


def child_pids(pid):
    """Every descendant of `pid`, deepest first.

    Deepest-first so a caller killing the list in order reaps grandchildren
    before the parent that would otherwise reparent them to init.
    """
    tree = {}
    for child, parent in _pid_ppid_map().items():
        tree.setdefault(parent, []).append(child)

    out = []

    def walk(p, depth=0):
        if depth > 20:  # a real tree can't cycle; guard against a bogus ps
            return
        for c in tree.get(p, ()):
            if c == p:
                continue
            walk(c, depth + 1)
            out.append(c)

    walk(pid)
    return out


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


def kill_stale_supervisors(project_root, *, markers=None):
    """Kill orphaned file-watcher / dev-supervisor processes inside a project.

    Catches cargo-watch / nodemon / etc. that survived a previous admin dev
    session because admin was SIGKILL'd, OOM-killed, or its terminal was
    closed without giving atexit a chance to run. They sit in /proc with no
    parent admin, react to file changes from the *next* dev session, and
    silently relaunch the listener — which then fights the new admin's
    listener for the port.

    Match is project-scoped: cmdline must contain a known supervisor marker
    AND cwd must be inside `project_root`. Returns the count of pids killed.
    """
    if markers is None:
        # Match both the shell-wrapper form ("cargo watch", with a space —
        # how it appears in `sh -c '... cargo watch ...'`) AND the bare
        # binary form ("cargo-watch", with a hyphen — how /proc reports it
        # once the shell exec's into the binary or the shell dies leaving
        # an orphan). Same idea for the others.
        markers = (
            "cargo watch", "cargo-watch",
            "watchexec",
            "nodemon",
            "tsc -w", "tsc --watch",
        )
    project_root = os.path.abspath(project_root)
    self_pid = os.getpid()
    stale = []
    for proc in list_processes():
        if proc["pid"] == self_pid:
            continue
        cmdline = proc.get("cmdline") or ""
        if not any(m in cmdline for m in markers):
            continue
        # Marker matched — only now fetch cwd (per-pid, lazy) to scope to the
        # project. Doing it after the marker filter keeps the per-pid lsof on
        # macOS down to the handful of supervisor candidates.
        cwd = proc.get("cwd") or _pid_cwd(proc["pid"])
        if not _cwd_in_root(cwd, project_root):
            continue
        stale.append(proc["pid"])
    if not stale:
        return 0
    info(f"Found {len(stale)} orphaned supervisor(s) in {project_root}: {stale}")
    _kill_pids(stale, timeout=3.0)
    return len(stale)


def kill_dev_interactive(port, project_root=None):
    """Kill a project's dev server: port holder + any orphaned supervisor.

    What `admin kill` should call instead of `kill_port_interactive` for
    projects with a watcher-driven backend (cargo-watch, nodemon, ...).
    The plain port-only kill leaves the supervisor running; the supervisor
    immediately respawns a new listener on file save and you wonder why
    the next `admin dev` reports "address already in use".

    `project_root` defaults to the current working directory — admin commands
    run from the project root, so this is usually correct.

    Returns True if everything cleaned up, False if the user aborted the
    port-kill prompt.
    """
    if project_root is None:
        project_root = os.getcwd()
    if not kill_port_interactive(port):
        return False
    killed_orphans = kill_stale_supervisors(project_root)
    if killed_orphans:
        ok(f"Cleared {killed_orphans} orphaned supervisor process(es).")
    return True


def nuke_dev_processes(
    project_root,
    *,
    ports=(),
    pidfiles=(),
    js_files=(),
    web_subdirs=(),
    runtimes=("bun", "node", "python", "python3"),
    web_argv_markers=("vite", "esbuild", "npm run dev"),
    supervisor_markers=None,
    patterns=(),
):
    """Bulletproof dev-server cleanup. Four passes, each a fallback for the previous.

    Why: simple "kill the bun process I just started" fails the moment a
    previous run was force-killed, ssh-disconnected, OOM'd, or left a stale
    pidfile. This sweep gives guaranteed cleanup regardless.

    Passes: (1) bound-port listeners + their bun/node/python supervisor
    parents; (2) pidfiles ({"path", "format": "int"|"json", "key"?}), stale
    ones unlinked; (3) /proc argv/cwd matching against project_root + js_files
    + web_subdirs + web_argv_markers; (3b) declared argv `patterns`, scoped to
    project_root unless a pattern sets global=true; (4) project-scoped sweep for orphaned
    file-watcher supervisors (cargo-watch / nodemon / watchexec / `tsc -w`)
    via `kill_stale_supervisors` — catches the case where the listener is
    already dead but its watcher would respawn one on the next file change.

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
    # project_dev_holders is the lsof-can't-map-the-port fallback. It returns
    # the SAME project-scoped set regardless of port, and its per-pid cwd sweep
    # is the slow part on a wedged mount — so compute it at most once, lazily,
    # and reuse it across every busy port rather than re-sweeping per port.
    fallback_holders = None
    for entry in ports:
        if isinstance(entry, (tuple, list)):
            port, _label = entry[0], (entry[1] if len(entry) > 1 else None)
        else:
            port = entry
        holders = port_holders(port)
        if not holders and not port_free(port):
            # Busy but lsof couldn't name it (macOS + a stale mount that hangs
            # the scan). Fall back to ps + per-pid cwd to find the dev holders.
            if fallback_holders is None:
                fallback_holders = project_dev_holders(project_root)
            holders = fallback_holders
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
                # Children first: a pidfile names a supervisor (a bun/electron/
                # cargo parent), and SIGTERM to it does not reliably take its
                # children with it — they get reparented and keep the port. Kill
                # the tree bottom-up, which is what `pkill -P <pid>; kill <pid>`
                # was doing by hand.
                _kill_set(child_pids(pid), f"pidfile {os.path.basename(path)} children")
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

    # 3b. Declared argv patterns.
    #
    # For dev processes the first three passes can't see: a hugo/wrangler/cargo
    # binary that binds no port we listed, writes no pidfile, and isn't a JS
    # runtime. Projects were reaching for `pkill -f 'hugo server'` here, which
    # matches by argv across the whole machine — it will happily kill the same
    # dev server running from a different checkout. These patterns are scoped to
    # processes whose cwd is inside project_root unless a pattern opts out with
    # global=true, so the default is narrower than the pkill it replaces.
    for spec in patterns or ():
        if isinstance(spec, str):
            spec = {"match": spec}
        needle = spec.get("match") or ""
        if not needle:
            continue
        is_regex = bool(spec.get("regex"))
        is_global = bool(spec.get("global"))
        try:
            rx = re.compile(needle) if is_regex else None
        except re.error as e:
            err(f"[dev_kill].patterns: bad regex {needle!r}: {e}")
            sys.exit(1)
        hits = set()
        for proc in list_processes():
            if proc["pid"] in killed:
                continue
            cmdline = proc["cmdline"] or ""
            if rx.search(cmdline) if rx else (needle in cmdline):
                if not is_global:
                    cwd = proc["cwd"] or _pid_cwd(proc["pid"]) or ""
                    if not _cwd_in_root(cwd, project_root):
                        continue
                hits.add(proc["pid"])
        _kill_set(hits, f"pattern {needle!r}")

    # 4. File-watcher / supervisor orphans.
    sup_kwargs = {"markers": supervisor_markers} if supervisor_markers else {}
    sup_count = kill_stale_supervisors(project_root, **sup_kwargs)
    if sup_count:
        # kill_stale_supervisors already SIGTERM'd them; we just track the
        # count for the summary line.
        killed.update([f"sup{i}" for i in range(sup_count)])

    if killed:
        ok(f"dev server stopped ({len(killed)} processes)")
    else:
        info("nothing was running")
    return len(killed)


def nuke_dev_from_config(cfg):
    """Adapter: read a manifest `[dev_kill]` table and call nuke_dev_processes.

    Schema: ports, pidfiles, js_files, web_subdirs, runtimes, patterns. ${VAR} placeholders
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
    def _port(value):
        # Ports accept "${PORT:-3300}" the same way pidfile paths accept ${VAR};
        # without this a project whose port is env-driven has to hardcode it here
        # and silently drifts from the port its dev server actually binds.
        if isinstance(value, str):
            resolved = resolve_env(value).strip()
            try:
                return int(resolved)
            except ValueError:
                err(f"[dev_kill].ports: {value!r} resolved to {resolved!r}, which is not a port")
                sys.exit(1)
        return value

    ports = []
    for entry in cfg.get("ports") or ():
        if isinstance(entry, dict):
            ports.append((_port(entry.get("port")), entry.get("label")))
        else:
            ports.append(_port(entry))
    return nuke_dev_processes(
        project_root=os.getcwd(),
        ports=ports,
        pidfiles=pidfiles,
        js_files=tuple(cfg.get("js_files") or ()),
        web_subdirs=tuple(cfg.get("web_subdirs") or ()),
        runtimes=tuple(cfg.get("runtimes") or ("bun", "node", "python", "python3")),
        supervisor_markers=tuple(cfg.get("supervisor_markers") or ()) or None,
        patterns=tuple(cfg.get("patterns") or ()),
    )


# ---------------------------------------------------------------------------
# Live-vs-orphan dev precheck.
#
# nuke_dev_processes kills whatever matches; a *precheck* must not. The point is
# to reap the corpse of a previous dev run (its terminal is gone) while never
# killing the session a human is sitting in right now. The two are told apart by
# walking a process's ancestry looking for a real controlling terminal: a chain
# that crosses a TTY belongs to a live interactive session (abort — tell the user
# to Ctrl-C it); a chain that reaches init with no TTY is an orphan (reap it).
# ---------------------------------------------------------------------------

def _proc_tty_map():
    """{pid: (ppid, has_tty)} for every visible process.

    has_tty is True when the process owns a controlling terminal. Linux reads
    /proc/<pid>/stat (the tty_nr field, 0 == none); macOS/BSD (no /proc) parses
    `ps -Ao pid=,ppid=,tty=` (a tty of '??' or '-' == none). One snapshot, walked
    by has_tty_ancestor.
    """
    if os.path.isdir("/proc"):
        return _proc_tty_map_proc()
    return _proc_tty_map_ps()


def _proc_tty_map_proc():
    out = {}
    try:
        entries = os.listdir("/proc")
    except OSError:
        return out
    for entry in entries:
        if not entry.isdigit():
            continue
        pid = int(entry)
        try:
            with open(f"/proc/{pid}/stat") as f:
                data = f.read()
        except (OSError, FileNotFoundError, PermissionError):
            continue
        # comm (field 2) is wrapped in parens and may itself contain spaces or
        # parens; split on the LAST ')' so the numeric fields after it line up
        # regardless. After it: [0]=state [1]=ppid [2]=pgrp [3]=session [4]=tty_nr
        rparen = data.rfind(")")
        if rparen < 0:
            continue
        fields = data[rparen + 1:].split()
        if len(fields) < 5:
            continue
        try:
            ppid = int(fields[1])
            tty_nr = int(fields[4])
        except ValueError:
            continue
        out[pid] = (ppid, tty_nr != 0)
    return out


def _proc_tty_map_ps():
    out = {}
    try:
        r = subprocess.run(
            ["ps", "-Ao", "pid=,ppid=,tty="],
            capture_output=True, text=True, timeout=5,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return out
    for line in r.stdout.splitlines():
        parts = line.split(None, 2)
        if len(parts) < 2:
            continue
        try:
            pid = int(parts[0])
            ppid = int(parts[1])
        except ValueError:
            continue
        tty = parts[2].strip() if len(parts) > 2 else ""
        out[pid] = (ppid, tty not in ("", "??", "-"))
    return out


def has_tty_ancestor(pid, tmap=None, *, max_depth=32):
    """True if `pid` or any ancestor up to init owns a controlling terminal.

    The live-vs-orphan test. `tmap` is a {pid: (ppid, has_tty)} snapshot from
    _proc_tty_map — built on demand when omitted, injectable for tests. A chain
    that hits a pid absent from the snapshot (a race, or a permission gap) is
    treated as orphaned: better to reap a corpse than refuse to start dev.
    """
    if tmap is None:
        tmap = _proc_tty_map()
    p = pid
    depth = 0
    seen = set()
    while p and p > 1 and depth < max_depth and p not in seen:
        seen.add(p)
        entry = tmap.get(p)
        if entry is None:
            return False
        ppid, has_tty = entry
        if has_tty:
            return True
        p = ppid
        depth += 1
    return False


def _argv_in_project(proc, project_root):
    """True if any argv token of `proc` is a path at or under project_root.

    Narrows a bare-runtime match to this project's own scripts:
    `bun /root/projects/muxarr/worker.js` matches, `node /root/.npm/_npx/.../x`
    (whose cwd merely happens to be under the project) does not.
    """
    root = os.path.abspath(project_root)
    prefix = root + os.sep
    for tok in proc.get("argv") or ():
        if tok == root or tok.startswith(prefix):
            return True
    return False


def _container_running(name):
    """True if a docker container named exactly `name` is up. A missing docker
    binary or any error is treated as not-running — a broken/absent docker must
    never block starting dev.
    """
    try:
        r = subprocess.run(
            ["docker", "ps", "--filter", f"name=^{name}$", "--format", "{{.Names}}"],
            capture_output=True, text=True, timeout=5,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired, OSError):
        return False
    return any(line.strip() == name for line in r.stdout.splitlines())


def dev_precheck(
    project_root,
    *,
    runtimes=("bun", "node", "vite", "esbuild"),
    accept_runtimes=(),
    containers=(),
    reap_pidfiles=(),
    label="dev",
    timeout=2.0,
):
    """Refuse to start a dev loop when a live one already owns this project.

    Classifies every process rooted in project_root as *live* (its ancestry
    crosses a real TTY — an interactive session a human is in) or *orphan* (its
    chain reaches init with no TTY — a corpse from a dead session). Orphans are
    reaped silently; a live session, or any running `containers` entry, aborts
    with a non-zero return so the caller can tell the user to Ctrl-C it.

    Scoping mirrors nuke_dev_processes: a process counts only when its cwd is
    inside project_root AND (its argv0 basename is in `runtimes` with an argv
    token inside the project, OR its basename is in `accept_runtimes` — the
    cwd-scoped-alone class, for helpers like ffmpeg that carry no project path in
    argv). Returns 0 to proceed, 1 to abort.
    """
    project_root = os.path.abspath(project_root)
    fail = False

    for name in containers:
        if _container_running(name):
            err(f"[{label}] ABORT: container {name!r} is running — "
                "Ctrl-C its foreground session first.")
            fail = True

    runtime_set = set(runtimes)
    accept_set = set(accept_runtimes)
    self_pid = os.getpid()
    tmap = None
    live = []
    orphans = []

    for proc in list_processes():
        pid = proc["pid"]
        if pid == self_pid:
            continue
        # Cheapest filter first (argv0 basename, straight from ps/cmdline) so the
        # per-pid cwd lookup (lsof on macOS) only runs for the handful of runtime
        # candidates, never the whole process table.
        base = os.path.basename(proc.get("argv0") or "")
        if base not in runtime_set and base not in accept_set:
            continue
        cwd = proc.get("cwd") or _pid_cwd(pid, timeout=timeout)
        if not _cwd_in_root(cwd, project_root):
            continue
        if base in runtime_set and not _argv_in_project(proc, project_root):
            continue
        if tmap is None:
            tmap = _proc_tty_map()
        (live if has_tty_ancestor(pid, tmap) else orphans).append(pid)

    if orphans:
        info(f"[{label}] reaping {len(orphans)} orphaned process(es): {sorted(orphans)}")
        _kill_pids(sorted(orphans), timeout=3.0)
        for path in reap_pidfiles:
            try:
                os.unlink(path)
            except OSError:
                pass

    if live:
        err(f"[{label}] ABORT: a live {label} session owns these PIDs: "
            f"{sorted(live)} — Ctrl-C that session first.")
        fail = True

    return 1 if fail else 0


def dev_precheck_from_config(cfg):
    """Adapter: read a manifest `[dev_precheck]` table and call dev_precheck.

    Schema: runtimes, accept_runtimes, containers, reap_pidfiles, label. ${VAR}
    placeholders in container names and pidfile paths resolve via core.resolve_env
    at call time.
    """
    cfg = cfg or {}
    containers = tuple(c for c in (resolve_env(c) for c in (cfg.get("containers") or ())) if c)
    reap = tuple(p for p in (resolve_env(p) for p in (cfg.get("reap_pidfiles") or ())) if p)
    kwargs = {"containers": containers, "reap_pidfiles": reap}
    if cfg.get("runtimes"):
        kwargs["runtimes"] = tuple(cfg["runtimes"])
    if cfg.get("accept_runtimes"):
        kwargs["accept_runtimes"] = tuple(cfg["accept_runtimes"])
    if cfg.get("label"):
        kwargs["label"] = cfg["label"]
    return dev_precheck(project_root=os.getcwd(), **kwargs)
