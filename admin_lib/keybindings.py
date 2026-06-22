"""Layer 3 — generic interactive keybindings for long-running actions.

Provides run_with_keybindings(): wraps a long-lived subprocess like
docker.run_with_reload does, but instead of restarting the wrapped process
on a keypress, it side-fires user-declared commands while the wrapped
process keeps running. Drives the `kind = "interactive-shell"` action kind.

Three target shapes per binding (exactly one):
    {"run": "<shell snippet>"}                  — fire and forget in a subshell
    {"action": "<action-name>"}                 — invoke a registered action
    {"signal": "TERM", "pid_file": "/path"}     — read PID, send signal
"""
from __future__ import annotations

import os
import selectors
import signal
import subprocess
import sys
import time

from .core import (
    get_log_dir,
    get_log_file,
    _children,
    info,
    ok,
    warn,
    err,
)
from .log_bridge import start_log_bridge, stop_log_bridge


def _safe_ident(name: str) -> str:
    return name.replace("-", "_").replace(":", "_").replace(".", "_")


def _wait_or_kill(proc, timeout=5):
    try:
        proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        try:
            proc.kill()
        except OSError:
            pass
        proc.wait()


def _resolve_action(name):
    """Look up cmd_<safe_ident> in module globals; call with []. Warn if missing."""
    func_name = "cmd_" + _safe_ident(name)
    fn = globals().get(func_name)
    if fn is None:
        warn(f"keybinding action '{name}': no function {func_name}() in scope")
        return
    try:
        fn([])
    except SystemExit:
        pass
    except Exception as e:
        err(f"keybinding action '{name}' raised: {e}")


def _send_signal(sig_name, pid_file):
    """Read PID from file, send signal. Stale/missing PID file → warn + no-op.

    `sig_name` can be given with or without the "SIG" prefix (TERM == SIGTERM).
    """
    raw = sig_name.upper()
    lookup = raw if raw.startswith("SIG") else "SIG" + raw
    try:
        sig = signal.Signals[lookup]
    except KeyError:
        err(f"keybinding signal: unknown signal name '{sig_name}'")
        return
    try:
        with open(pid_file) as f:
            pid = int(f.read().strip())
    except (OSError, ValueError) as e:
        warn(f"keybinding signal: can't read pid file '{pid_file}': {e}")
        return
    try:
        os.kill(pid, sig)
    except ProcessLookupError:
        warn(f"keybinding signal: pid {pid} (from {pid_file}) is gone")
    except OSError as e:
        err(f"keybinding signal: kill({pid}, {sig.name}) failed: {e}")


def _run_inline(snippet):
    """Fire a shell snippet without blocking the wrapped process. Output goes to admin's stdout."""
    try:
        subprocess.Popen(snippet, shell=True)
    except OSError as e:
        err(f"keybinding run: shell launch failed: {e}")


def _dispatch_binding(binding):
    if "run" in binding:
        _run_inline(binding["run"])
    elif "action" in binding:
        _resolve_action(binding["action"])
    elif "signal" in binding:
        _send_signal(binding["signal"], binding["pid_file"])


def _format_banner(keys, label):
    parts = []
    for ch, b in keys.items():
        lbl = b.get("label") or b.get("action") or b.get("signal") or "run"
        parts.append(f"[{ch}] {lbl}")
    parts.append("[Ctrl-C] exit")
    prefix = f"[{label}] " if label else ""
    return prefix + " · ".join(parts)


def run_with_keybindings(cmd, keys, *, label=None, formatter=None, shell=True):
    """Run `cmd` as a long-lived subprocess; dispatch keystrokes to bindings.

    keys: dict[str, dict] — single-char key → {label?, run|action|signal+pid_file}.
    Lifecycle mirrors docker.run_with_reload's selector/cbreak loop, but on a
    recognized keypress we side-fire the binding's target instead of restarting
    the wrapped process. Ctrl-C terminates the wrapped process and exits.
    """
    os.makedirs(get_log_dir(), exist_ok=True)
    start_log_bridge(get_log_file())
    _user_exit = False

    def _on_sigint(signum, frame):
        nonlocal _user_exit
        _user_exit = True
        raise KeyboardInterrupt

    def _on_sigterm(signum, frame):
        raise SystemExit(0)

    old_sigint = signal.getsignal(signal.SIGINT)
    old_sigterm = signal.getsignal(signal.SIGTERM)
    signal.signal(signal.SIGINT, _on_sigint)
    signal.signal(signal.SIGTERM, _on_sigterm)

    log_fh = open(get_log_file(), "a")
    proc = subprocess.Popen(
        cmd, shell=shell,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    _children.append(proc)

    sel = selectors.DefaultSelector()
    sel.register(proc.stdout, selectors.EVENT_READ)
    stdin_fd = sys.stdin.fileno() if sys.stdin.isatty() else None
    old_term = None
    if stdin_fd is not None:
        try:
            import tty, termios
            old_term = termios.tcgetattr(stdin_fd)
            tty.setcbreak(stdin_fd)
            sel.register(stdin_fd, selectors.EVENT_READ, "stdin")
        except Exception:
            old_term = None
            stdin_fd = None

    if keys:
        info(_format_banner(keys, label))

    _line_buf = ""
    launch_time = time.monotonic()
    try:
        while proc.poll() is None:
            events = sel.select(timeout=0.5)
            for key, _ in events:
                if key.data == "stdin":
                    ch = os.read(stdin_fd, 1)
                    if ch == b"\x03":  # Ctrl-C
                        _user_exit = True
                        raise KeyboardInterrupt
                    if ch == b"\x04":  # Ctrl-D
                        _user_exit = True
                        raise KeyboardInterrupt
                    try:
                        decoded = ch.decode("utf-8")
                    except UnicodeDecodeError:
                        continue
                    binding = keys.get(decoded)
                    if binding is not None:
                        _dispatch_binding(binding)
                    continue
                data = key.fileobj.read1(4096) if hasattr(key.fileobj, "read1") else os.read(key.fileobj.fileno(), 4096)
                if data:
                    text = data.decode("utf-8", errors="replace")
                    log_fh.write(text)
                    log_fh.flush()
                    if formatter:
                        _line_buf += text
                        while "\n" in _line_buf:
                            line, _line_buf = _line_buf.split("\n", 1)
                            sys.stdout.write(formatter(line + "\n"))
                        sys.stdout.flush()
                    else:
                        sys.stdout.write(text)
                        sys.stdout.flush()

        if proc.stdout:
            remaining = proc.stdout.read()
            if remaining:
                text = remaining.decode("utf-8", errors="replace")
                log_fh.write(text)
                log_fh.flush()
                if formatter:
                    _line_buf += text
                    while "\n" in _line_buf:
                        line, _line_buf = _line_buf.split("\n", 1)
                        sys.stdout.write(formatter(line + "\n"))
                    if _line_buf:
                        sys.stdout.write(formatter(_line_buf + "\n"))
                else:
                    sys.stdout.write(text)
                sys.stdout.flush()

        elapsed = time.monotonic() - launch_time
        rc = proc.returncode
        if elapsed < 2.0 and rc != 0:
            warn(f"Process exited in {elapsed:.1f}s (code {rc})")
        return rc

    except KeyboardInterrupt:
        info("Stopped.")
        return 130
    finally:
        sel.close()
        if old_term is not None and stdin_fd is not None:
            import termios
            termios.tcsetattr(stdin_fd, termios.TCSADRAIN, old_term)
        if proc.poll() is None:
            try:
                kill_sig = signal.SIGINT if _user_exit else signal.SIGTERM
                os.killpg(os.getpgid(proc.pid), kill_sig)
            except OSError:
                proc.terminate()
            _wait_or_kill(proc)
        if proc in _children:
            _children.remove(proc)
        log_fh.close()
        stop_log_bridge()
        signal.signal(signal.SIGINT, old_sigint)
        signal.signal(signal.SIGTERM, old_sigterm)
