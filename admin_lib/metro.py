"""React Native Metro bundler lifecycle.

Metro is the JS bundler a React Native app fetches its bundle from when it
launches; without one running, a debug build shows the red "No script URL
provided" screen instead of the app. One Metro serves every target of a
project — iOS simulator, physical device, and Android emulator all pull from
the same port — so it is started once and reused rather than being owned by
any single dev command.

Metro runs detached (start_new_session) and is left running when the dev
command exits, for two reasons. A dev loop that rebuilds and relaunches the
app would otherwise take the bundler down and pay a cold start on every cycle;
and `admin dev ios` followed by `admin dev android` should hit a warm bundler
rather than race two of them onto the same port. Callers stop it explicitly
via stop_metro() — typically from a `down` command.

Stdlib only: /status is polled with urllib rather than curl so the readiness
check does not depend on a binary being present.
"""
from __future__ import annotations

import os
import subprocess
import time
import urllib.error
import urllib.request

from .core import err, info

DEFAULT_PORT = 8081

# Metro answers /status with this exact body. Checking the body rather than
# just a 200 avoids mistaking some other service already holding the port
# (a stray vite, a tunnel) for a live bundler — that misread would otherwise
# surface much later as an unexplained red screen on the device.
_STATUS_BODY = "packager-status:running"


def metro_running(port: int = DEFAULT_PORT, timeout: float = 1.0) -> bool:
    """True if a Metro bundler is answering on `port`."""
    url = f"http://127.0.0.1:{port}/status"
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:
            return _STATUS_BODY in resp.read(200).decode("utf-8", errors="replace")
    except (urllib.error.URLError, OSError, ValueError):
        return False


def _pid_alive(pid_file: str) -> int | None:
    """Return the pid recorded in pid_file if that process is still alive."""
    if not pid_file or not os.path.isfile(pid_file):
        return None
    try:
        with open(pid_file) as fh:
            pid = int(fh.read().strip())
        os.kill(pid, 0)
    except (ValueError, OSError):
        return None
    return pid


def ensure_metro(cwd, port: int = DEFAULT_PORT, log_file: str | None = None,
                 pid_file: str | None = None, timeout: float = 60.0) -> bool:
    """Start Metro in `cwd` unless one is already serving `port`.

    Returns True if this call started one, False if it was already up.
    Raises RuntimeError if a freshly started Metro never answers /status —
    the caller must not go on to build and launch an app that would only show
    a red screen.
    """
    if metro_running(port):
        info(f"Metro already on :{port}")
        return False

    if log_file:
        log_dir = os.path.dirname(log_file)
        if log_dir:
            os.makedirs(log_dir, exist_ok=True)
        out = open(log_file, "a")
    else:
        out = subprocess.DEVNULL

    try:
        proc = subprocess.Popen(
            ["npx", "react-native", "start", "--port", str(port)],
            cwd=str(cwd),
            stdout=out, stderr=subprocess.STDOUT,
            stdin=subprocess.DEVNULL,
            start_new_session=True,
        )
    except OSError as exc:
        # No npx on PATH — surface it as the same RuntimeError every other
        # failure here raises, so callers need one except clause, not two.
        raise RuntimeError(f"could not start Metro: {exc}") from exc
    finally:
        if log_file:
            out.close()   # the child holds its own dup of the fd

    if pid_file:
        pid_dir = os.path.dirname(pid_file)
        if pid_dir:
            os.makedirs(pid_dir, exist_ok=True)
        with open(pid_file, "w") as fh:
            fh.write(str(proc.pid))

    info(f"starting Metro on :{port}")
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if metro_running(port):
            info(f"Metro ready on :{port}")
            return True
        if proc.poll() is not None:
            raise RuntimeError(
                f"Metro exited immediately (code {proc.returncode})"
                + (f" — see {log_file}" if log_file else "")
            )
        time.sleep(0.25)

    raise RuntimeError(
        f"Metro did not answer :{port}/status within {timeout:.0f}s"
        + (f" — see {log_file}" if log_file else "")
    )


def stop_metro(pid_file: str | None = None, port: int = DEFAULT_PORT) -> bool:
    """Stop a Metro started by ensure_metro. Returns True if one was stopped.

    Kills the process group, not just the pid: `npx` execs node as a child, so
    signalling the recorded pid alone can leave the bundler holding the port.

    The port is the source of truth, not the pid file. A pid file goes stale
    easily — ensure_metro deliberately writes none when it finds a bundler
    already up, and a hard-killed admin never cleans one up — so trusting it
    alone would report "no Metro running" at a port that is still answering,
    and leave it answering. The pid is only ever signalled after the port
    confirms a live bundler, which also rules out killpg'ing an unrelated
    process group that happens to have inherited a recycled pid.
    """
    running = metro_running(port)
    pid = _pid_alive(pid_file)

    if not running:
        if pid_file:
            _remove(pid_file)      # nothing on the port; drop any stale record
        return False

    if pid is None:
        err(f"Metro is answering on :{port} but no pid file records it — "
            f"admin did not start it, so it is not admin's to stop")
        return False

    try:
        os.killpg(os.getpgid(pid), 15)
    except OSError:
        try:
            os.kill(pid, 15)
        except OSError:
            pass
    _remove(pid_file)

    deadline = time.monotonic() + 5.0
    while time.monotonic() < deadline:
        if not metro_running(port):
            return True
        time.sleep(0.1)
    err(f"Metro still answering on :{port} after SIGTERM")
    return False


def _remove(path: str | None):
    if not path:
        return
    try:
        os.remove(path)
    except OSError:
        pass
