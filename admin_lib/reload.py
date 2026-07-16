"""Reload infrastructure for long-lived dev processes — SIGUSR1 + 'r' keypress.

Two shapes sit on one piece of state, because dev commands come in two shapes:

  run_with_reload(cmd, target)   — this module owns the loop. For a process
      that stays up on its own (a server, a bundler, an Electron shell): it
      streams the child's output, and on reload tears the child down and
      relaunches it.

  setup_reload() + is_rebuild_requested()/clear_rebuild_request() — the CALLER
      owns the loop. For a build-install-launch cycle (apple's dev loops) where
      "reload" means run the whole cycle again, not restart one child.

Both previously kept their own `_rebuild_requested` flag and their own SIGUSR1
handler — docker.py for the first, apple.py for the second. One process only
ever runs one dev loop, so the split never produced a live bug, but it meant
two definitions of "a reload was requested" and a `run_with_reload` filed under
`docker.` that had nothing to do with Docker and so got re-implemented by
projects that never thought to look there. The state lives here now, once.

`admin logs`/`kill` and the reload signal find a running dev loop through the
PID files this module writes under _ADMIN_PID_PREFIX.
"""
from __future__ import annotations

import atexit
import glob
import os
import selectors
import signal
import subprocess
import sys
import time

from .core import _children, get_log_dir, get_log_file, info, register_cleanup, warn

_ADMIN_PID_PREFIX = "/tmp/admin-run"
_rebuild_requested = False
_reload_active = False  # True during in-loop restart; suppresses port prompts


def _handle_rebuild_signal(signum, frame):
    global _rebuild_requested
    _rebuild_requested = True


def is_rebuild_requested():
    """Check if a rebuild has been requested (via signal or keypress)."""
    return _rebuild_requested


def clear_rebuild_request():
    """Reset the rebuild request flag."""
    global _rebuild_requested
    _rebuild_requested = False


def cleanup_pid_file(pid_file):
    """Remove PID file if it exists."""
    try:
        os.remove(pid_file)
    except OSError:
        pass


def _cleanup_all_pid_files():
    for pidfile in glob.glob(f"{_ADMIN_PID_PREFIX}-*.pid"):
        try:
            with open(pidfile) as f:
                pid = int(f.read().strip())
            if pid == os.getpid():
                os.unlink(pidfile)
        except (OSError, ValueError):
            pass


register_cleanup(_cleanup_all_pid_files)


def setup_reload(pid_file=f"{_ADMIN_PID_PREFIX}.pid"):
    """Register SIGUSR1 handler and write PID file.

    For loops the caller owns; run_with_reload does its own equivalent setup.
    """
    signal.signal(signal.SIGUSR1, _handle_rebuild_signal)
    with open(pid_file, "w") as f:
        f.write(str(os.getpid()))
    atexit.register(cleanup_pid_file, pid_file)
    register_cleanup(lambda: cleanup_pid_file(pid_file))


def _wait_or_kill(proc, timeout=5):
    """Wait for proc to exit; escalate to SIGKILL if it ignores SIGTERM."""
    try:
        proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        try:
            proc.kill()
        except OSError:
            pass
        proc.wait()


def _preexec_pdeathsig():
    """preexec_fn that asks the kernel to SIGTERM us when our parent dies.

    Linux-only via prctl(PR_SET_PDEATHSIG). The whole point: if admin is
    SIGKILL'd, the terminal gets closed, or the user's shell dies abruptly,
    atexit handlers don't run and cleanup is skipped — leaving cargo-watch /
    nodemon / esbuild supervisors as orphans that respawn the dev server on
    the next file change. Setting this on the spawned shell means the kernel
    tears the whole tree down for us, regardless of how admin exits.

    Silent no-op on non-Linux. The startup sweep (kill_stale_supervisors)
    is the cross-platform fallback.
    """
    try:
        import ctypes
        PR_SET_PDEATHSIG = 1
        libc = ctypes.CDLL("libc.so.6", use_errno=True)
        libc.prctl(PR_SET_PDEATHSIG, signal.SIGTERM, 0, 0, 0)
    except Exception:
        pass
    # Become a session/group leader so killpg() from the parent reaches the
    # whole tree (start_new_session=True at the Popen level already does
    # this, but if it isn't passed for some reason, this is a backstop).
    try:
        os.setsid()
    except OSError:
        pass


def poll_for_reload(is_running_fn, timeout=1):
    """Poll for reload signal, 'r' keypress, or process exit.

    Returns True if reload requested, False if process exited or Ctrl+C.
    """
    import select
    import termios
    import tty

    fd = sys.stdin.fileno()
    old_settings = termios.tcgetattr(fd)
    try:
        tty.setcbreak(fd)
        while True:
            if _rebuild_requested:
                return True
            if not is_running_fn():
                return False
            readable, _, _ = select.select([sys.stdin], [], [], timeout)
            if readable:
                ch = sys.stdin.read(1)
                if ch == "r":
                    return True
    except KeyboardInterrupt:
        return False
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old_settings)


def run_with_reload(cmd, target, shell=True, formatter=None):
    """Run a long-lived process with SIGUSR1 reload + 'r' keypress support.

    Uses selector-based I/O multiplexing. Supports quick-exit detection
    with retry backoff.
    """
    global _rebuild_requested
    pid_file = f"{_ADMIN_PID_PREFIX}-{target}.pid"
    os.makedirs(get_log_dir(), exist_ok=True)

    with open(pid_file, "w") as f:
        f.write(str(os.getpid()))
    atexit.register(cleanup_pid_file, pid_file)

    _user_exit = False

    def _on_sigint(signum, frame):
        nonlocal _user_exit
        _user_exit = True
        raise KeyboardInterrupt

    def _on_sigterm(signum, frame):
        raise SystemExit(0)

    old_sigusr1 = signal.getsignal(signal.SIGUSR1)
    old_sigint = signal.getsignal(signal.SIGINT)
    old_sigterm = signal.getsignal(signal.SIGTERM)
    signal.signal(signal.SIGUSR1, _handle_rebuild_signal)
    signal.signal(signal.SIGINT, _on_sigint)
    signal.signal(signal.SIGTERM, _on_sigterm)

    _line_buf = ""

    def _emit(text, final=False):
        """Write one chunk of child output to the log file and the terminal.

        The log file always gets the raw text. The terminal gets it through the
        formatter, which is why the chunk is re-split into whole lines first: a
        read lands wherever the pipe happened to fill, and a formatter that lays
        out columns can only do that on a complete line. `final` emits whatever
        partial line is still buffered — correct only once the child is gone and
        no newline is coming, hence the flag rather than doing it every chunk.
        """
        nonlocal _line_buf
        log_fh.write(text)
        log_fh.flush()
        if formatter:
            _line_buf += text
            while "\n" in _line_buf:
                line, _line_buf = _line_buf.split("\n", 1)
                sys.stdout.write(formatter(line + "\n"))
            if final and _line_buf:
                sys.stdout.write(formatter(_line_buf + "\n"))
                _line_buf = ""
        else:
            sys.stdout.write(text)
        sys.stdout.flush()

    try:
        while True:
            _rebuild_requested = False
            _line_buf = ""
            launch_time = time.monotonic()
            info(f"Starting {target}...")

            log_fh = open(get_log_file(), "a")
            proc = subprocess.Popen(
                cmd, shell=shell,
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                start_new_session=True,
                preexec_fn=_preexec_pdeathsig,
            )
            _children.append(proc)

            sel = selectors.DefaultSelector()
            sel.register(proc.stdout, selectors.EVENT_READ)
            stdin_fd = sys.stdin.fileno()
            old_term = None
            try:
                import tty, termios
                old_term = termios.tcgetattr(stdin_fd)
                tty.setcbreak(stdin_fd)
                sel.register(stdin_fd, selectors.EVENT_READ, "stdin")
            except Exception:
                pass

            try:
                while proc.poll() is None:
                    if _rebuild_requested:
                        global _reload_active
                        info("Reload requested — restarting...")
                        _reload_active = True
                        try:
                            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
                        except OSError:
                            proc.terminate()
                        _wait_or_kill(proc)
                        _reload_active = False
                        break

                    events = sel.select(timeout=0.5)
                    for key, _ in events:
                        if key.data == "stdin":
                            ch = os.read(stdin_fd, 1)
                            if ch in (b"r", b"R"):
                                _rebuild_requested = True
                            elif ch == b"\x03":
                                _user_exit = True
                                raise KeyboardInterrupt
                            continue
                        data = key.fileobj.read1(4096) if hasattr(key.fileobj, 'read1') else os.read(key.fileobj.fileno(), 4096)
                        if data:
                            _emit(data.decode("utf-8", errors="replace"))

                if proc.stdout:
                    remaining = proc.stdout.read()
                    if remaining:
                        _emit(remaining.decode("utf-8", errors="replace"), final=True)
            finally:
                sel.close()
                if old_term is not None:
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

            if _rebuild_requested:
                continue

            elapsed = time.monotonic() - launch_time
            rc = proc.returncode
            if elapsed < 2.0:
                warn(f"Process exited in {elapsed:.1f}s (code {rc}) — waiting before retry...")
                time.sleep(1)
            return rc

    except KeyboardInterrupt:
        # The SIGINT handler installed above raises this so the inner
        # try/finally tears the child process down cleanly. Swallow it here
        # so Ctrl+C exits with a quiet "Stopped." instead of dumping the
        # full traceback for the selector.poll() call we were sitting in.
        info("Stopped.")
        return 130
    finally:
        signal.signal(signal.SIGUSR1, old_sigusr1)
        signal.signal(signal.SIGINT, old_sigint)
        signal.signal(signal.SIGTERM, old_sigterm)
        cleanup_pid_file(pid_file)
