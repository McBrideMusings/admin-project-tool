"""Layer 3 — Docker/web server/long-lived process helpers.

Provides: run_with_reload, run_parallel, Docker build/deploy patterns,
multi-target reload.
"""

import atexit
import glob
import os
import selectors
import signal
import subprocess
import sys
import time

from collections import OrderedDict

from .core import (
    C,
    _children,
    _custom_commands,
    command,
    info,
    ok,
    warn,
    err,
    register_cleanup,
    LOG_DIR,
    LOG_FILE,
    _urls,
    check_url,
    format_url_status,
    pick_target,
)


# ---------------------------------------------------------------------------
# Reload infrastructure (server-style: selector-based I/O multiplexing)
# ---------------------------------------------------------------------------

_ADMIN_PID_PREFIX = "/tmp/admin-run"
_rebuild_requested = False


def _handle_rebuild_signal(signum, frame):
    global _rebuild_requested
    _rebuild_requested = True


def _cleanup_pid_file(path):
    try:
        os.unlink(path)
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


# ---------------------------------------------------------------------------
# run_with_reload — long-lived process with SIGUSR1 reload support
# ---------------------------------------------------------------------------

def run_with_reload(cmd, target, shell=True):
    """Run a long-lived process with SIGUSR1 reload + 'r' keypress support.

    Uses selector-based I/O multiplexing. Supports quick-exit detection
    with retry backoff.
    """
    global _rebuild_requested
    pid_file = f"{_ADMIN_PID_PREFIX}-{target}.pid"
    os.makedirs(LOG_DIR, exist_ok=True)

    with open(pid_file, "w") as f:
        f.write(str(os.getpid()))
    atexit.register(_cleanup_pid_file, pid_file)

    old_handler = signal.getsignal(signal.SIGUSR1)
    signal.signal(signal.SIGUSR1, _handle_rebuild_signal)

    try:
        while True:
            _rebuild_requested = False
            launch_time = time.monotonic()
            info(f"Starting {target}...")

            log_fh = open(LOG_FILE, "a")
            proc = subprocess.Popen(
                cmd, shell=shell,
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                start_new_session=True,
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
                        info("Reload requested — restarting...")
                        try:
                            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
                        except OSError:
                            proc.terminate()
                        proc.wait()
                        break

                    events = sel.select(timeout=0.5)
                    for key, _ in events:
                        if key.data == "stdin":
                            ch = os.read(stdin_fd, 1)
                            if ch in (b"r", b"R"):
                                _rebuild_requested = True
                            elif ch == b"\x03":
                                raise KeyboardInterrupt
                            continue
                        data = key.fileobj.read1(4096) if hasattr(key.fileobj, 'read1') else os.read(key.fileobj.fileno(), 4096)
                        if data:
                            text = data.decode("utf-8", errors="replace")
                            sys.stdout.write(text)
                            sys.stdout.flush()
                            log_fh.write(text)
                            log_fh.flush()

                if proc.stdout:
                    remaining = proc.stdout.read()
                    if remaining:
                        text = remaining.decode("utf-8", errors="replace")
                        sys.stdout.write(text)
                        sys.stdout.flush()
                        log_fh.write(text)
                        log_fh.flush()
            finally:
                sel.close()
                if old_term is not None:
                    import termios
                    termios.tcsetattr(stdin_fd, termios.TCSADRAIN, old_term)
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

    finally:
        signal.signal(signal.SIGUSR1, old_handler)
        _cleanup_pid_file(pid_file)


# ---------------------------------------------------------------------------
# run_parallel — multiplexed output from multiple processes
# ---------------------------------------------------------------------------

def run_parallel(targets):
    """Run multiple processes in parallel with labeled, colored output.

    targets: list of (cmd, label, color) tuples.
    Returns list of return codes.
    """
    import threading

    os.makedirs(LOG_DIR, exist_ok=True)
    log_fh = open(LOG_FILE, "a")
    procs = []
    threads = []
    return_codes = [None] * len(targets)

    for i, (cmd, label, color) in enumerate(targets):
        proc = subprocess.Popen(
            cmd, shell=True,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            bufsize=1, universal_newlines=True,
            start_new_session=True,
        )
        _children.append(proc)
        procs.append(proc)

        def stream(p, lbl, clr, idx):
            try:
                for line in p.stdout:
                    prefixed = f"{clr}[{lbl}]{C.RESET} {line}"
                    sys.stdout.write(prefixed)
                    sys.stdout.flush()
                    log_fh.write(f"[{lbl}] {line}")
                    log_fh.flush()
            except (IOError, ValueError):
                pass
            p.wait()
            return_codes[idx] = p.returncode
            if p in _children:
                _children.remove(p)

        t = threading.Thread(target=stream, args=(proc, label, color, i), daemon=True)
        t.start()
        threads.append(t)

    try:
        for t in threads:
            t.join()
    except KeyboardInterrupt:
        for p in procs:
            try:
                os.killpg(os.getpgid(p.pid), signal.SIGTERM)
            except OSError:
                p.terminate()
        for p in procs:
            p.wait()
    finally:
        log_fh.close()

    return return_codes


# ---------------------------------------------------------------------------
# Docker helpers
# ---------------------------------------------------------------------------

def docker_build(image, context_dir="."):
    """Build a Docker image. Returns exit code."""
    from .core import run_cmd
    info(f"Building Docker image: {image}...")
    return run_cmd(f"docker build -t {image} {context_dir}")


def docker_deploy(image, host, run_args=""):
    """Build, transfer, and restart a Docker container on a remote host.

    Returns exit code.
    """
    from .core import run_cmd

    info(f"Transferring image to {host}...")
    rc = run_cmd(f"docker save {image} | ssh {host} 'docker load'")
    if rc != 0:
        err("Image transfer failed")
        return rc

    info(f"Restarting container on {host}...")
    rc = run_cmd(
        f"ssh {host} '"
        f"docker stop {image} 2>/dev/null || true;"
        f" docker rm {image} 2>/dev/null || true;"
        f" docker run -d --restart unless-stopped --name {image} {run_args} {image}'"
    )
    if rc == 0:
        ok(f"Deployed {image} to {host}")
    return rc


def ensure_deps(check_path, install_cmd):
    """Check for a dependency path and install if missing. Returns exit code or 0."""
    if os.path.exists(check_path):
        return 0
    from .core import run_cmd
    info("Installing dependencies...")
    return run_cmd(install_cmd)


# ---------------------------------------------------------------------------
# Multi-target reload command
# ---------------------------------------------------------------------------

def register_server_reload_command(pid_prefix=None):
    """Register a reload command that sends SIGUSR1 to all running targets."""
    prefix = pid_prefix or _ADMIN_PID_PREFIX

    @command("reload", "Signal running admin to rebuild+relaunch")
    def cmd_reload(args):
        pid_files = glob.glob(f"{prefix}-*.pid")
        target_filter = args[0] if args else None

        if not pid_files:
            err("No running targets found (no PID files)")
            return 1

        sent = 0
        for pidfile in pid_files:
            basename = os.path.basename(pidfile)
            target_name = basename.replace("admin-run-", "").replace(".pid", "")

            if target_filter and target_filter not in target_name:
                continue

            try:
                with open(pidfile) as f:
                    pid = int(f.read().strip())
            except (OSError, ValueError):
                warn(f"Cannot read {pidfile} — removing stale file")
                _cleanup_pid_file(pidfile)
                continue

            try:
                os.kill(pid, 0)
            except OSError:
                warn(f"PID {pid} ({target_name}) is not running — removing stale file")
                _cleanup_pid_file(pidfile)
                continue

            os.kill(pid, signal.SIGUSR1)
            ok(f"Reload signal sent to {target_name} (PID {pid})")
            sent += 1

        if sent == 0:
            err("No live targets found")
            return 1
        return 0


# ---------------------------------------------------------------------------
# Open URL command
# ---------------------------------------------------------------------------

def register_open_command(urls=None):
    """Register an 'open' command that shows URL status and opens in browser.

    urls: list of (label, url) tuples. If None, uses URLs from set_urls().
    """
    @command("open", "Open in browser")
    def cmd_open(args):
        import webbrowser

        target_urls = urls if urls is not None else _urls
        if not target_urls:
            err("No URLs configured")
            return 1

        url_map = {label: url for label, url in target_urls}
        target = args[0].lower() if args else None

        if target:
            match = None
            for label in url_map:
                if label.lower() == target:
                    match = label
                    break
            if not match:
                err(f"Unknown target: {args[0]} (use: {', '.join(l.lower() for l in url_map)})")
                return 1
            url = url_map[match]
            live = check_url(url.rstrip("/") + "/health")
            print(format_url_status(match, url, live))
            webbrowser.open(url)
            return 0

        label_width = max(len(l) for l in url_map) + 2
        for label, url in target_urls:
            live = check_url(url.rstrip("/") + "/health")
            print(format_url_status(label, url, live, label_width))
        print()

        targets = OrderedDict((label, url) for label, url in target_urls)
        choice = pick_target(targets, prompt="Open in browser:")
        if choice:
            webbrowser.open(url_map[choice])
            ok(f"Opened {choice}")
        return 0
