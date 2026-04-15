"""File-based log tailing.

Reads a named log target + environment out of a spec registered at generation
time, then streams the matching local file or a remote file over SSH. Default
is 'follow from end' — no history is printed unless --tail N or --all is given.

Spec shape (as emitted by gen/render.py):

    {
        "<target>": {
            "default_env": "<name>" | None,
            "envs": {
                "<env>": {"path": "<path>", "host": <str|None>, "user": <str|None>},
                ...
            },
        },
        ...
    }

All string values may contain ${VAR} / ${VAR:-default}; these are resolved at
run time via resolve_env().
"""

import os
import subprocess
import sys
import threading
import time
from collections import OrderedDict

from .core import (
    _children,
    command,
    err,
    info,
    pick_target,
    resolve_env,
    warn,
)


def _require_interactive(message):
    """Abort with an error if stdout is not a TTY (non-interactive context)."""
    if not sys.stdout.isatty():
        err(message)
        return False
    return True


def register_logs_command(spec):
    """Register a `logs` command driven by the given spec.

    Called from generated ./admin scripts. Re-registering overwrites any
    archetype-provided `logs` command because @command stores by name in an
    OrderedDict.
    """

    @command("logs", "Tail a log file (local or remote)")
    def cmd_logs(args):
        target_name, env_name, tail_n, show_all, follow = _parse_args(args)

        if target_name is None:
            if len(spec) == 1:
                target_name = next(iter(spec))
            else:
                if not _require_interactive(
                    f"logs: target required in non-interactive context "
                    f"(have: {', '.join(spec)})"
                ):
                    return 1
                menu = OrderedDict(
                    (t, ", ".join(spec[t]["envs"])) for t in spec
                )
                target_name = pick_target(menu, "Select log target")
                if not target_name:
                    return 1

        if target_name not in spec:
            err(f"logs: unknown target '{target_name}' (have: {', '.join(spec)})")
            sys.exit(1)

        target = spec[target_name]
        envs = target["envs"]

        if env_name is None:
            env_name = target.get("default_env")
            if env_name is None:
                if len(envs) == 1:
                    env_name = next(iter(envs))
                else:
                    if not _require_interactive(
                        f"logs: --env required for '{target_name}' in "
                        f"non-interactive context (have: {', '.join(envs)})"
                    ):
                        return 1
                    menu = OrderedDict(
                        (e, envs[e]["path"]) for e in envs
                    )
                    env_name = pick_target(menu, f"Select env for '{target_name}'")
                    if not env_name:
                        return 1

        if env_name not in envs:
            err(
                f"logs: unknown env '{env_name}' for '{target_name}' "
                f"(have: {', '.join(envs)})"
            )
            sys.exit(1)

        env_spec = envs[env_name]
        path = resolve_env(env_spec["path"])
        host = resolve_env(env_spec["host"]) if env_spec.get("host") else None
        user = resolve_env(env_spec["user"]) if env_spec.get("user") else None

        if host:
            return tail_remote(
                host, user, path,
                tail_n=tail_n, show_all=show_all, follow=follow,
            )
        return tail_local(
            path, tail_n=tail_n, show_all=show_all, follow=follow,
        )


def _parse_args(args):
    target_name = None
    env_name = None
    tail_n = 0
    show_all = False
    follow = True

    i = 0
    while i < len(args):
        a = args[i]
        if a == "--env":
            if i + 1 >= len(args):
                err("logs: --env requires a value")
                sys.exit(1)
            env_name = args[i + 1]
            i += 2
        elif a.startswith("--env="):
            env_name = a.split("=", 1)[1]
            i += 1
        elif a == "--tail":
            if i + 1 >= len(args):
                err("logs: --tail requires a value")
                sys.exit(1)
            tail_n = _parse_int(args[i + 1])
            i += 2
        elif a.startswith("--tail="):
            tail_n = _parse_int(a.split("=", 1)[1])
            i += 1
        elif a == "--all":
            show_all = True
            i += 1
        elif a == "--no-follow":
            follow = False
            i += 1
        elif not a.startswith("-") and target_name is None:
            target_name = a
            i += 1
        else:
            err(f"logs: unknown arg '{a}'")
            sys.exit(1)

    return target_name, env_name, tail_n, show_all, follow


def _parse_int(s):
    try:
        return int(s)
    except ValueError:
        err(f"logs: expected integer, got '{s}'")
        sys.exit(1)


# ---------------------------------------------------------------------------
# Local tailing
# ---------------------------------------------------------------------------

def tail_local(path, tail_n=0, show_all=False, follow=True):
    """Stream a local log file.

    show_all=True dumps from the start; otherwise tail_n (if >0) emits the last
    N lines or the cursor starts at EOF. When follow=True the cursor then
    tracks appends, reopening on inode change or size shrink (rotation or
    truncation-style circular buffers).
    """
    if not os.path.exists(path):
        if not follow:
            err(f"logs: file not found: {path}")
            return 1
        info(f"logs: waiting for {path}...")
        while not os.path.exists(path):
            try:
                time.sleep(0.5)
            except KeyboardInterrupt:
                return 0

    f = open(path, "rb")
    try:
        head = f.read(512)
        if b"\x00" in head:
            warn(f"logs: {path} appears to be binary; output may garble the terminal")
        f.seek(0)

        if show_all:
            f.seek(0)
        elif tail_n > 0:
            _seek_last_n_lines(f, tail_n)
        else:
            f.seek(0, 2)

        _drain(f)

        if not follow:
            return 0

        info(f"logs: following {path} (Ctrl-C to stop)")
        inode = os.fstat(f.fileno()).st_ino
        pos = f.tell()
        try:
            while True:
                time.sleep(0.25)
                try:
                    st = os.stat(path)
                except FileNotFoundError:
                    continue

                if st.st_ino != inode or st.st_size < pos:
                    warn(f"logs: {path} rotated/truncated — reopening")
                    f.close()
                    f = open(path, "rb")
                    inode = os.fstat(f.fileno()).st_ino
                    pos = 0

                chunk = f.read(65536)
                if chunk:
                    sys.stdout.buffer.write(chunk)
                    sys.stdout.flush()
                    pos = f.tell()
        except KeyboardInterrupt:
            return 0
    finally:
        f.close()
    return 0


def _drain(f):
    while True:
        chunk = f.read(65536)
        if not chunk:
            return
        sys.stdout.buffer.write(chunk)
        sys.stdout.flush()


def _seek_last_n_lines(f, n):
    """Position f so that reading forward yields the last n lines (or fewer if
    the file is shorter).
    """
    f.seek(0, 2)
    end = f.tell()
    if end == 0:
        return

    # If the file doesn't end with \n, the trailing fragment counts as its own
    # line, so we need one fewer embedded newline before it to get n lines.
    f.seek(end - 1)
    target = n if f.read(1) == b"\n" else n - 1

    chunk_size = 8192
    pos = end
    newlines = 0
    while pos > 0 and newlines <= target:
        read_size = min(chunk_size, pos)
        pos -= read_size
        f.seek(pos)
        newlines += f.read(read_size).count(b"\n")

    if newlines <= target:
        f.seek(0)
        return

    f.seek(pos)
    data = f.read(end - pos)
    skip = newlines - target
    i = 0
    while skip > 0 and i < len(data):
        if data[i:i + 1] == b"\n":
            skip -= 1
        i += 1
    f.seek(pos + i)


# ---------------------------------------------------------------------------
# Remote tailing (SSH)
# ---------------------------------------------------------------------------

def tail_remote(host, user, path, tail_n=0, show_all=False, follow=True):
    """Tail a remote log file over SSH.

    Shells out to `ssh [-l user] host 'tail ... path'`. Uses `-F` for follow
    so the remote side handles rotation/truncation. Ctrl-C terminates the ssh
    child cleanly.
    """
    target = f"{user}@{host}" if user else host

    if show_all:
        head = "tail -n +1"
    elif tail_n > 0:
        head = f"tail -n {int(tail_n)}"
    else:
        head = "tail -n 0"

    follow_flag = " -F" if follow else ""
    remote = f"{head}{follow_flag} {_shell_quote(path)}"

    info(f"logs: ssh {target}: {remote}")
    proc = subprocess.Popen(
        ["ssh", target, remote],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        start_new_session=True,
    )
    _children.append(proc)

    stderr_chunks = []

    def _drain_stderr():
        for line in iter(proc.stderr.readline, b""):
            stderr_chunks.append(line)

    stderr_thread = threading.Thread(target=_drain_stderr, daemon=True)
    stderr_thread.start()

    try:
        try:
            while True:
                chunk = proc.stdout.read1(4096) if hasattr(proc.stdout, "read1") \
                    else proc.stdout.read(4096)
                if not chunk:
                    break
                sys.stdout.buffer.write(chunk)
                sys.stdout.flush()
        except KeyboardInterrupt:
            proc.terminate()
    finally:
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
        stderr_thread.join(timeout=1)
        if proc in _children:
            _children.remove(proc)

    rc = proc.returncode
    if rc != 0:
        stderr_text = b"".join(stderr_chunks).decode("utf-8", errors="replace").rstrip()
        if rc == 255:
            err(f"logs: ssh to {target} failed (exit 255) — check host, key, and network")
        else:
            err(f"logs: ssh to {target} exited {rc}")
        if stderr_text:
            for line in stderr_text.splitlines():
                err(f"  {line}")
    return rc


def _shell_quote(s):
    return "'" + s.replace("'", "'\\''") + "'"
