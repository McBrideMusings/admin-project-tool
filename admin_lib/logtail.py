"""Log tailing for the [logs] table.

Reads named log sources registered from the manifest's [logs] table and streams
them. Every source has a `kind`; non-file kinds (command/docker) render their
output through the shared LogFormatter and can whole-line-highlight matches.
Default is 'follow from end' — no history is printed unless --tail N or --all.

Spec shape (parsed from [logs] by admin_lib/manifest.py::_parse_logs); every spec also
carries "kind" and a "highlight" list of regex strings:

    file    {"default_env": <str|None>,
             "envs": {"<env>": {"path", "host", "user"}, ...}}   # local or SSH file
    command {"run": <str>, "host": <str|None>, "user": <str|None>}  # stream stdout
    docker  {"container": <str>, "host": <str|None>, "user": <str|None>}  # docker logs -f
    pick    {"glob": <str>}                                       # pick a file, then tail

All string values may contain ${VAR} / ${VAR:-default}; these are resolved at
run time via resolve_env().
"""

import glob as globmod
import os
import re
import subprocess
import sys
import threading
import time
from collections import OrderedDict

from .core import (
    C,
    _children,
    command,
    err,
    info,
    pick_target_or_none,
    resolve_env,
    warn,
)
from .log_format import make_formatter
from .kinds import LOG_KINDS, register_log_handler, FragmentError

# Bold + reverse video — makes highlight-matching lines pop out of a noisy stream.
_HIGHLIGHT = "\033[1;7m"

# Serializes stdout writes so the main tail thread and the log-bridge sink
# (which runs on ThreadingHTTPServer worker threads, see _start_bridge_session)
# can't splice each other's lines or highlight escape sequences mid-write.
_OUTPUT_LOCK = threading.Lock()


class LineRenderer:
    """Route every tailed line through the shared LogFormatter (timestamp
    normalization + level coloring) and whole-line-highlight any line matching
    one of `highlight` (regex strings). Color/highlight is suppressed when
    stdout is not a TTY, so piped/captured output stays plain.
    """

    def __init__(self, highlight=None):
        self._fmt = make_formatter()
        self._use_color = sys.stdout.isatty()
        self._patterns = []
        for pat in highlight or []:
            try:
                self._patterns.append(re.compile(pat))
            except re.error as exc:
                warn(f"logs: ignoring bad highlight pattern {pat!r}: {exc}")

    def write(self, raw_line):
        out = self._fmt(raw_line)
        if self._use_color and self._patterns:
            body = out[:-1] if out.endswith("\n") else out
            if any(p.search(body) for p in self._patterns):
                out = f"{_HIGHLIGHT}{body}{C.RESET}\n"
        with _OUTPUT_LOCK:
            sys.stdout.write(out)
            sys.stdout.flush()


class _RawSink:
    """Default file/pick output: bytes straight to stdout, no formatting — keeps
    the fast, binary-safe path byte-identical when no highlight is configured.
    """

    def feed(self, data):
        sys.stdout.buffer.write(data)
        sys.stdout.flush()

    def flush(self):
        pass


class _RenderSink:
    """File/pick output when a highlight list is set: decode the byte stream,
    buffer partial lines across reads, and route each complete line through a
    LineRenderer so file kinds get the same formatter + whole-line highlight as
    command/docker. A trailing unterminated line is emitted on flush().
    """

    def __init__(self, highlight):
        self._r = LineRenderer(highlight)
        self._buf = ""

    def feed(self, data):
        self._buf += data.decode("utf-8", "replace")
        *lines, self._buf = self._buf.split("\n")
        for ln in lines:
            self._r.write(ln + "\n")

    def flush(self):
        if self._buf:
            self._r.write(self._buf)
            self._buf = ""


def _make_file_sink(highlight):
    return _RenderSink(highlight) if highlight else _RawSink()


def register_logs_command(spec):
    """Register a `logs` command driven by the given spec.

    Called from generated ./admin scripts. Re-registering overwrites any
    archetype-provided `logs` command because @command stores by name in an
    OrderedDict.
    """

    @command("logs", "Tail a log target (file / command / docker / pick)")
    def cmd_logs(args):
        target_name, env_name, tail_n, show_all, follow = _parse_args(args)

        if target_name is None:
            if len(spec) == 1:
                target_name = next(iter(spec))
            else:
                menu = OrderedDict(
                    (t, _menu_label(spec[t])) for t in spec
                )
                target_name = pick_target_or_none(
                    menu,
                    "Select log target",
                    f"logs: target required in non-interactive context "
                    f"(have: {', '.join(spec)})",
                )
                if target_name is None:
                    return 1

        if target_name not in spec:
            err(f"logs: unknown target '{target_name}' (have: {', '.join(spec)})")
            sys.exit(1)

        target = spec[target_name]
        kind = target.get("kind", "file")
        return LOG_KINDS[kind].handler(
            target, target_name, env_name, tail_n, show_all, follow,
        )


# ---------------------------------------------------------------------------
# Per-kind dispatch — one handler each, uniform signature. Each handler binds
# into its admin_lib.kinds.LogKind via register_log_handler (below), and the
# import-time coverage check guarantees every declared log kind got a handler —
# mirroring the action-kind factory binding, so adding a kind forces a handler
# here with no separate sync test.
# ---------------------------------------------------------------------------

def _resolve_opt(target, key):
    val = target.get(key)
    return resolve_env(val) if val else None


def _tail_command_kind(target, target_name, env_name, tail_n, show_all, follow):
    return tail_command(
        resolve_env(target["run"]),
        _resolve_opt(target, "host"),
        _resolve_opt(target, "user"),
        target.get("highlight", []),
    )


def _tail_docker_kind(target, target_name, env_name, tail_n, show_all, follow):
    return tail_docker(
        resolve_env(target["container"]),
        _resolve_opt(target, "host"),
        _resolve_opt(target, "user"),
        tail_n=tail_n, show_all=show_all, follow=follow,
        highlight=target.get("highlight", []),
    )


def _tail_pick_kind(target, target_name, env_name, tail_n, show_all, follow):
    return tail_pick(
        resolve_env(target["glob"]),
        tail_n=tail_n, show_all=show_all, follow=follow,
        highlight=target.get("highlight", []),
    )


def _tail_file_kind(target, target_name, env_name, tail_n, show_all, follow):
    envs = target["envs"]

    if env_name is None:
        env_name = target.get("default_env")
        if env_name is None:
            if len(envs) == 1:
                env_name = next(iter(envs))
            else:
                menu = OrderedDict(
                    (e, envs[e]["path"]) for e in envs
                )
                env_name = pick_target_or_none(
                    menu,
                    f"Select env for '{target_name}'",
                    f"logs: --env required for '{target_name}' in "
                    f"non-interactive context (have: {', '.join(envs)})",
                )
                if env_name is None:
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
    highlight = target.get("highlight", [])

    if host:
        return tail_remote(
            host, user, path,
            tail_n=tail_n, show_all=show_all, follow=follow, highlight=highlight,
        )
    return tail_local(
        path, tail_n=tail_n, show_all=show_all, follow=follow, highlight=highlight,
    )


register_log_handler("file", _tail_file_kind)
register_log_handler("command", _tail_command_kind)
register_log_handler("docker", _tail_docker_kind)
register_log_handler("pick", _tail_pick_kind)

# Import-time coverage check: every declared log kind must have a handler bound.
# Replaces the old LOG_KINDS<->LOG_KIND_HANDLERS sync test.
_unbound_log = sorted(n for n, spec in LOG_KINDS.items() if spec.handler is None)
if _unbound_log:
    raise FragmentError(f"log kinds with no registered handler: {_unbound_log}")


# ---------------------------------------------------------------------------
# Non-file kinds: command / docker / pick
# ---------------------------------------------------------------------------

def _menu_label(spec):
    kind = spec.get("kind", "file")
    if kind == "file":
        return ", ".join(spec["envs"])
    if kind == "command":
        return f"command: {spec['run']}"
    if kind == "docker":
        suffix = f" @ {spec['host']}" if spec.get("host") else ""
        return f"docker: {spec['container']}{suffix}"
    if kind == "pick":
        return f"pick: {spec['glob']}"
    return kind


def _ssh_target(host, user):
    return f"{user}@{host}" if user else host


def _start_bridge_session(highlight):
    """If [log_bridge] is configured, start the browser-console bridge so client
    `console.*` lines interleave with this tailed stream, rendered through the
    same LineRenderer (see references/log-bridge.md). Returns a stop() callable;
    a no-op stop() when the bridge isn't enabled. Imports are lazy so the module
    still exec's under the test harness (which strips package-relative imports).
    """
    try:
        from .log_bridge import log_bridge_enabled, start_log_bridge, stop_log_bridge
        from .log_server import set_log_server_sink
        from .core import get_log_file
    except Exception:
        return lambda: None
    if not log_bridge_enabled():
        return lambda: None
    # Bridged [client] lines render through the same formatter+highlight as the
    # tailed source, so the two streams look uniform when interleaved.
    set_log_server_sink(LineRenderer(highlight).write)
    # append_log_bridge_entries needs a real log_path or it drops entries; fall
    # back to a temp file when the command isn't tee'd to one.
    start_log_bridge(get_log_file() or "/tmp/admin-project-tool-logs-bridge.log")

    def _stop():
        stop_log_bridge()
        set_log_server_sink(None)

    return _stop


def tail_command(run, host, user, highlight):
    """Run `run` and stream its stdout through the renderer.

    The command owns its own history/follow semantics — encode `--follow` (or
    equivalent) in the command string itself.
    """
    if host:
        target = _ssh_target(host, user)
        info(f"logs: ssh {target}: {run}")
        argv = ["ssh", target, run]
    else:
        info(f"logs: {run}")
        argv = ["sh", "-c", run]
    stop_bridge = _start_bridge_session(highlight)
    try:
        return _stream_proc(argv, LineRenderer(highlight))
    finally:
        stop_bridge()


def tail_docker(container, host, user, tail_n=0, show_all=False, follow=True,
                highlight=None):
    """Stream `docker logs` for a container, locally or over SSH."""
    if show_all:
        tail_arg = "all"
    elif tail_n > 0:
        tail_arg = str(int(tail_n))
    else:
        tail_arg = "0"
    follow_flag = " -f" if follow else ""
    docker_cmd = f"docker logs --tail {tail_arg}{follow_flag} {_shell_quote(container)}"
    if host:
        target = _ssh_target(host, user)
        info(f"logs: ssh {target}: {docker_cmd}")
        argv = ["ssh", target, docker_cmd]
    else:
        info(f"logs: {docker_cmd}")
        argv = ["sh", "-c", docker_cmd]
    stop_bridge = _start_bridge_session(highlight)
    try:
        # docker writes container stderr to its own stderr; merge so nothing is lost.
        return _stream_proc(argv, LineRenderer(highlight), merge_stderr=True)
    finally:
        stop_bridge()


def tail_pick(pattern, tail_n=0, show_all=False, follow=True, highlight=None):
    """Glob `pattern`, let the user pick a file, then tail it (honoring highlight)."""
    matches = sorted(
        globmod.glob(pattern),
        key=lambda p: os.path.getmtime(p) if os.path.exists(p) else 0,
        reverse=True,
    )
    if not matches:
        err(f"logs: no files match {pattern}")
        return 1
    if len(matches) == 1:
        chosen = matches[0]
    else:
        menu = OrderedDict()
        for f in matches:
            try:
                menu[f] = f"{os.path.relpath(f)}  ({os.path.getsize(f):,}b)"
            except OSError:
                menu[f] = os.path.relpath(f)
        chosen = pick_target_or_none(
            menu,
            "Pick a log file",
            f"logs: pick requires a TTY ({len(matches)} files match {pattern})",
        )
        if chosen is None:
            return 1
    return tail_local(chosen, tail_n=tail_n, show_all=show_all, follow=follow,
                      highlight=highlight)


def _stream_proc(argv, renderer, merge_stderr=False):
    """Spawn argv, stream stdout line-by-line through `renderer`.

    merge_stderr folds stderr into stdout (used for `docker logs`, which writes
    container stderr separately). Otherwise stderr is captured and surfaced on a
    non-zero exit. Ctrl-C terminates the child and returns cleanly.
    """
    proc = subprocess.Popen(
        argv,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT if merge_stderr else subprocess.PIPE,
        start_new_session=True,
        bufsize=1,
        text=True,
        errors="replace",
    )
    _children.append(proc)

    stderr_chunks = []
    stderr_thread = None
    if not merge_stderr:
        def _drain_stderr():
            for line in iter(proc.stderr.readline, ""):
                stderr_chunks.append(line)
        stderr_thread = threading.Thread(target=_drain_stderr, daemon=True)
        stderr_thread.start()

    interrupted = False
    try:
        try:
            for line in iter(proc.stdout.readline, ""):
                renderer.write(line)
        except KeyboardInterrupt:
            interrupted = True
            proc.terminate()
    finally:
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
        if stderr_thread:
            stderr_thread.join(timeout=1)
        if proc in _children:
            _children.remove(proc)

    rc = proc.returncode
    if not interrupted and rc not in (0, None) and not merge_stderr:
        text = "".join(stderr_chunks).rstrip()
        if rc == 255 and argv[0] == "ssh":
            err(f"logs: ssh to {argv[1]} failed (exit 255) — check host, key, and network")
        else:
            err(f"logs: source exited {rc}")
        for line in text.splitlines():
            err(f"  {line}")
    return 0 if interrupted else (rc or 0)


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

def tail_local(path, tail_n=0, show_all=False, follow=True, poll_interval=0.25,
               highlight=None):
    """Stream a local log file.

    show_all=True dumps from start; else tail_n>0 emits last N lines or cursor
    starts at EOF. follow=True tracks appends and reopens on inode change or
    size shrink (handles rotation and truncation-style buffers). A non-empty
    highlight routes output through a LineRenderer (formatter + whole-line
    highlight); otherwise bytes stream raw and binary-safe.
    """
    sink = _make_file_sink(highlight)
    if not os.path.exists(path):
        if not follow:
            err(f"logs: file not found: {path}")
            return 1
        info(f"logs: waiting for {path}...")
        while not os.path.exists(path):
            try:
                time.sleep(poll_interval)
            except KeyboardInterrupt:
                return 0

    f = open(path, "rb")
    try:
        # The 512-byte peek runs in every mode, including the default EOF path
        # that immediately discards these bytes. That is intentional: the
        # warning is most valuable precisely when the user is tailing from EOF
        # and about to dump raw bytes straight to the terminal. Skipping the
        # peek to save ~512 bytes of I/O in the common case would silently
        # remove the warning where it matters most.
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

        _drain(f, sink)

        if not follow:
            sink.flush()
            return 0

        info(f"logs: following {path} (Ctrl-C to stop)")
        inode = os.fstat(f.fileno()).st_ino
        pos = f.tell()
        try:
            while True:
                time.sleep(poll_interval)
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
                    sink.feed(chunk)
                    pos = f.tell()
        except KeyboardInterrupt:
            sink.flush()
            return 0
    finally:
        f.close()
    return 0


def _drain(f, sink):
    while True:
        chunk = f.read(65536)
        if not chunk:
            return
        sink.feed(chunk)


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

def tail_remote(host, user, path, tail_n=0, show_all=False, follow=True,
                highlight=None):
    """Tail a remote log file over SSH.

    Shells out to `ssh [-l user] host 'tail ... path'`. Uses `-F` for follow
    so the remote side handles rotation/truncation. Ctrl-C terminates the ssh
    child cleanly. A non-empty highlight routes output through a LineRenderer.
    """
    target = f"{user}@{host}" if user else host
    sink = _make_file_sink(highlight)

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

    # readline() is adequate here because OpenSSH reliably newline-terminates
    # stderr messages before exiting, so the daemon surfaces each line as soon
    # as it arrives. Switching to chunked raw reads would only matter for
    # clients that emit a partial trailing line and then close the pipe, which
    # isn't something OpenSSH does in practice.
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
                sink.feed(chunk)
        except KeyboardInterrupt:
            proc.terminate()
    finally:
        sink.flush()
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
