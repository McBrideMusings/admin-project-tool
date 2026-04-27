"""Layer 3 — Docker, long-lived process, and remote container helpers.

Provides: run_with_reload, run_parallel, Docker build/deploy patterns,
remote Docker build, multi-target reload.
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
    LOG_DIR,
    LOG_FILE,
    _children,
    command,
    info,
    ok,
    warn,
    err,
    register_cleanup,
    run_cmd,
    _urls,
    check_url,
    format_url_status,
    pick_target,
)

# Re-export remote helpers so existing projects importing from server still work
try:
    from .remote import require_host, ssh_cmd
except ImportError:
    pass


# ---------------------------------------------------------------------------
# Reload infrastructure (server-style: selector-based I/O multiplexing)
# ---------------------------------------------------------------------------

_ADMIN_PID_PREFIX = "/tmp/admin-run"
_rebuild_requested = False
_reload_active = False  # True during in-loop restart; suppresses port prompts


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

def run_with_reload(cmd, target, shell=True, formatter=None):
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

            _line_buf = ""
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
                                raise KeyboardInterrupt
                            continue
                        data = key.fileobj.read1(4096) if hasattr(key.fileobj, 'read1') else os.read(key.fileobj.fileno(), 4096)
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
            finally:
                sel.close()
                if old_term is not None:
                    import termios
                    termios.tcsetattr(stdin_fd, termios.TCSADRAIN, old_term)
                if proc.poll() is None:
                    try:
                        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
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

    finally:
        signal.signal(signal.SIGUSR1, old_handler)
        _cleanup_pid_file(pid_file)


# ---------------------------------------------------------------------------
# run_parallel — multiplexed output from multiple processes
# ---------------------------------------------------------------------------

def run_parallel(targets, formatter=None):
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

        def stream(p, lbl, clr, idx, fmt=formatter):
            try:
                for line in p.stdout:
                    formatted = fmt(line) if fmt else line
                    prefixed = f"{clr}[{lbl}]{C.RESET} {formatted}"
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

def docker_build(image, context_dir=".", platform=None):
    """Build a Docker image. Returns exit code."""

    plat = f"--platform {platform} " if platform else ""
    info(f"Building Docker image: {image}...")
    return run_cmd(f"docker build {plat}-t {image} {context_dir}")


def docker_deploy(image, host, run_args=""):
    """Build, transfer, and restart a Docker container on a remote host.

    Returns exit code.
    """


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


def remote_recreate_container(host, image, container=None):
    """Transport a local image to a remote host and recreate the named container
    against that new image, preserving its existing run configuration (binds,
    ports, env, labels, restart policy, network, caps, user, hostname, mac).

    Intended for Unraid-style hosts where the container was first created by
    the Docker GUI from a template: we don't know the GUI's `docker run` args,
    so we read them off the live container with `docker inspect` and replay
    them unchanged against the newly-loaded image.

    If the target container does not exist on `host`, we print an actionable
    message and return nonzero without touching anything on the remote.

    Returns exit code.
    """
    import json
    import shlex

    if container is None:
        container = image.split(":", 1)[0].rsplit("/", 1)[-1]

    host_q = shlex.quote(host)
    image_q = shlex.quote(image)
    container_q = shlex.quote(container)

    info(f"Transferring image {image} to {host}...")
    rc = run_cmd(f"docker save {image_q} | ssh {host_q} 'docker load'")
    if rc != 0:
        err("Image transfer failed")
        return rc

    info(f"Inspecting existing container '{container}' on {host}...")
    proc = subprocess.run(
        ["ssh", host, "docker", "inspect", container],
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0 or not proc.stdout.strip() or proc.stdout.strip() == "[]":
        err(f"Container '{container}' does not exist on {host}.")
        print(
            f"\n  Create it from the Unraid Docker GUI using the template,"
            f" then re-run this command.\n"
        )
        return 1

    try:
        inspect = json.loads(proc.stdout)[0]
    except (json.JSONDecodeError, IndexError) as e:
        err(f"Could not parse docker inspect output: {e}")
        return 1

    args = _build_run_args_from_inspect(inspect)

    restore_path = f"/tmp/{container}-restore.sh"
    run_cmd_parts = ["docker", "run", "-d", "--name", container] + args + [image]
    remote_run = " ".join(shlex.quote(p) for p in run_cmd_parts)
    restore_script = f"#!/bin/sh\n{remote_run}\n"
    write_proc = subprocess.run(
        ["ssh", host, f"cat > {shlex.quote(restore_path)} && chmod +x {shlex.quote(restore_path)}"],
        input=restore_script.encode(),
        capture_output=True,
    )
    if write_proc.returncode == 0:
        info(f"Restore script saved: {host}:{restore_path}")
    else:
        warn("Could not write restore script to remote")

    info(f"Stopping + removing '{container}' on {host}...")
    rc = run_cmd(
        f"ssh {host_q} 'docker stop -t 10 {container_q} >/dev/null"
        f" && docker rm {container_q} >/dev/null'"
    )
    if rc != 0:
        err("Stop/remove failed")
        return rc

    info(f"Recreating '{container}' on {host} with preserved run config...")
    rc = run_cmd(f"ssh {host_q} {shlex.quote(remote_run)}")
    if rc != 0:
        err(f"Container start failed — to restore: ssh {host} bash {restore_path}")
        return rc

    ok(f"Deployed {image} to {host} as container '{container}'")

    snapshot_path = f"/tmp/{container}-lastdeploy.json"
    subprocess.run(
        ["ssh", host, f"docker inspect {container_q} > {shlex.quote(snapshot_path)}"],
        capture_output=True,
    )

    return 0


_HOST_CFG_CONSUMED = frozenset({
    "Binds", "PortBindings", "RestartPolicy", "NetworkMode", "ExtraHosts",
    "Devices", "CapAdd", "CapDrop", "Privileged", "Memory",
})

_HOST_CFG_EXPECTED = frozenset({
    # Runtime infrastructure / always-populated defaults we intentionally skip
    "ContainerIDFile", "LogConfig", "AutoRemove", "VolumeDriver", "VolumesFrom",
    "CgroupnsMode", "Dns", "DnsOptions", "DnsSearch", "Links", "OomScoreAdj",
    "PidMode", "PublishAllPorts", "ReadonlyRootfs", "UTSMode", "UsernsMode",
    "ShmSize", "Isolation", "Runtime", "CgroupParent", "BlkioWeight",
    "BlkioWeightDevice", "BlkioDeviceReadBps", "BlkioDeviceWriteBps",
    "BlkioDeviceReadIOps", "BlkioDeviceWriteIOps", "CpuPeriod", "CpuQuota",
    "CpuRealtimePeriod", "CpuRealtimeRuntime", "CpusetCpus", "CpusetMems",
    "MemoryReservation", "MemorySwap", "MemorySwappiness", "OomKillDisable",
    "NanoCpus", "CpuShares", "PidsLimit", "KernelMemory", "KernelMemoryTCP",
    "StorageOpt", "MaskedPaths", "ReadonlyPaths", "IpcMode", "DeviceCgroupRules",
    "Mounts",
    # Common fields with real functional impact — skipped (not yet translated to args)
    "Tmpfs", "Sysctls", "SecurityOpt", "GroupAdd", "Ulimits",
})

_CONFIG_CONSUMED = frozenset({
    "Env", "Labels", "User", "Hostname", "WorkingDir", "Tty", "OpenStdin",
})

_CONFIG_EXPECTED = frozenset({
    # Image-derived (intentionally not replayed — taken from new image)
    "Domainname", "AttachStdin", "AttachStdout", "AttachStderr", "ExposedPorts",
    "StdinOnce", "Cmd", "Healthcheck", "ArgsEscaped", "Image", "Volumes",
    "Entrypoint", "OnBuild", "StopSignal", "StopTimeout", "Shell", "MacAddress",
})


def _is_trivial(v):
    if v is None:
        return True
    if isinstance(v, (list, dict)) and not v:
        return True
    if isinstance(v, bool):
        return not v
    if isinstance(v, int):
        return v == 0
    if isinstance(v, str):
        return not v
    return False


def _build_run_args_from_inspect(inspect, warn_prefix=None):
    """Translate `docker inspect` JSON (single-container) into a list of
    `docker run` argv pieces that reproduce the container's runtime config.

    Only emits args for fields that are actually set, so unset/zero values
    don't override image defaults.  warn_prefix is prepended to any
    unconsumed-field warnings (e.g. "current" or "last-deploy") so callers
    that invoke this twice can distinguish the source.
    """
    host_cfg = inspect.get("HostConfig") or {}
    config = inspect.get("Config") or {}
    net_settings = inspect.get("NetworkSettings") or {}
    args = []

    for bind in host_cfg.get("Binds") or []:
        args += ["-v", bind]

    for cport, bindings in (host_cfg.get("PortBindings") or {}).items():
        for b in bindings or []:
            host_ip = b.get("HostIp") or ""
            host_port = b.get("HostPort") or ""
            spec = f"{host_ip}:{host_port}:{cport}" if host_ip else f"{host_port}:{cport}"
            args += ["-p", spec]

    for e in config.get("Env") or []:
        args += ["-e", e]

    rp = host_cfg.get("RestartPolicy") or {}
    rp_name = rp.get("Name") or ""
    if rp_name and rp_name != "no":
        if rp_name == "on-failure" and rp.get("MaximumRetryCount"):
            args += ["--restart", f"on-failure:{rp['MaximumRetryCount']}"]
        else:
            args += ["--restart", rp_name]

    net_mode = host_cfg.get("NetworkMode") or ""
    if net_mode and net_mode != "default":
        args += ["--network", net_mode]

    for h in host_cfg.get("ExtraHosts") or []:
        args += ["--add-host", h]

    for d in host_cfg.get("Devices") or []:
        path_on_host = d.get("PathOnHost", "")
        path_in_container = d.get("PathInContainer", "")
        perms = d.get("CgroupPermissions", "")
        spec = f"{path_on_host}:{path_in_container}"
        if perms and perms != "rwm":
            spec += f":{perms}"
        args += ["--device", spec]

    for c in host_cfg.get("CapAdd") or []:
        args += ["--cap-add", c]
    for c in host_cfg.get("CapDrop") or []:
        args += ["--cap-drop", c]

    if host_cfg.get("Privileged"):
        args += ["--privileged"]

    for k, v in (config.get("Labels") or {}).items():
        args += ["--label", f"{k}={v}"]

    if config.get("User"):
        args += ["--user", config["User"]]

    if config.get("Hostname"):
        args += ["--hostname", config["Hostname"]]

    mac = config.get("MacAddress") or net_settings.get("MacAddress") or ""
    if mac and net_mode not in ("host", "none") and not net_mode.startswith("container:"):
        args += ["--mac-address", mac]

    if config.get("WorkingDir"):
        args += ["--workdir", config["WorkingDir"]]

    if host_cfg.get("Memory"):
        args += ["--memory", str(host_cfg["Memory"])]

    if config.get("Tty"):
        args += ["-t"]
    if config.get("OpenStdin"):
        args += ["-i"]

    unknown_host = sorted(
        k for k, v in host_cfg.items()
        if k not in _HOST_CFG_CONSUMED and k not in _HOST_CFG_EXPECTED and not _is_trivial(v)
    )
    unknown_cfg = sorted(
        k for k, v in config.items()
        if k not in _CONFIG_CONSUMED and k not in _CONFIG_EXPECTED and not _is_trivial(v)
    )
    if unknown_host or unknown_cfg:
        label = f" ({warn_prefix})" if warn_prefix else ""
        warn(f"docker inspect{label} has fields not translated to docker run args:")
        for k in unknown_host:
            warn(f"  HostConfig.{k} = {host_cfg[k]!r}")
        for k in unknown_cfg:
            warn(f"  Config.{k} = {config[k]!r}")

    return args


def remote_container_diff(host, image, container=None):
    """Compare the live container's run config against the last recorded deploy.

    Fetches the current `docker inspect` output from the remote, reads the
    snapshot saved by the previous `remote_recreate_container` call, and shows
    any run-arg drift (Unraid GUI tweaks, manual docker commands, etc.).

    Returns 0 if no drift, 1 if drift found or no snapshot exists.
    """
    import difflib
    import json
    import shlex

    if container is None:
        container = image.split(":", 1)[0].rsplit("/", 1)[-1]

    snapshot_path = f"/tmp/{container}-lastdeploy.json"

    info(f"Fetching current inspect for '{container}' on {host}...")
    curr_proc = subprocess.run(
        ["ssh", host, "docker", "inspect", container],
        capture_output=True, text=True,
    )
    if curr_proc.returncode != 0 or not curr_proc.stdout.strip() or curr_proc.stdout.strip() == "[]":
        err(f"Container '{container}' not found on {host}")
        return 1
    try:
        curr_inspect = json.loads(curr_proc.stdout)[0]
    except (json.JSONDecodeError, IndexError):
        err("Could not parse current docker inspect output")
        return 1

    snap_proc = subprocess.run(
        ["ssh", host, f"cat {shlex.quote(snapshot_path)} 2>/dev/null"],
        capture_output=True, text=True,
    )
    if snap_proc.returncode != 0:
        err(f"SSH to {host} failed (rc {snap_proc.returncode})")
        return 1
    if not snap_proc.stdout.strip():
        err(f"No deploy snapshot found at {host}:{snapshot_path} — run a deploy first")
        return 1
    try:
        last_inspect = json.loads(snap_proc.stdout)[0]
    except (json.JSONDecodeError, IndexError):
        err("Could not parse deploy snapshot")
        return 1

    curr_args = _build_run_args_from_inspect(curr_inspect, warn_prefix="current")
    last_args = _build_run_args_from_inspect(last_inspect, warn_prefix="last-deploy")

    def _pair_args(flat):
        pairs = []
        i = 0
        while i < len(flat):
            flag = flat[i]
            if i + 1 < len(flat) and not flat[i + 1].startswith("-"):
                pairs.append(f"{flag} {flat[i + 1]}")
                i += 2
            else:
                pairs.append(flag)
                i += 1
        return sorted(pairs)

    last_lines = _pair_args(last_args)
    curr_lines = _pair_args(curr_args)

    diff = list(difflib.unified_diff(
        last_lines, curr_lines, lineterm="",
        fromfile="last-deploy", tofile="current",
    ))
    if not diff:
        ok(f"No run-config drift detected for '{container}'")
        return 0

    warn(f"Run-config drift detected for '{container}':")
    for line in diff:
        print(line)
    return 1


def ensure_deps(check_path, install_cmd):
    """Check for a dependency path and install if missing. Returns exit code or 0."""
    if os.path.exists(check_path):
        return 0

    info("Installing dependencies...")
    return run_cmd(install_cmd)


# ---------------------------------------------------------------------------
# Remote Docker helpers (rsync + build on remote)
# ---------------------------------------------------------------------------

_DEFAULT_RSYNC_EXCLUDES = [
    "node_modules", ".git", "tmp", "data", "*.db", "*.db-wal", "*.db-shm",
]


def remote_docker_build(host, image, local_dir=".", remote_dir=None,
                        excludes=None):
    """Rsync project to remote host and build Docker image there.

    host: SSH host (e.g. 'root@myserver')
    image: Docker image name
    local_dir: local directory to sync (default: current dir)
    remote_dir: remote build directory (default: /tmp/<image>-build)
    excludes: list of extra rsync excludes (added to defaults)
    Returns exit code.
    """


    if remote_dir is None:
        remote_dir = f"/tmp/{image}-build"

    all_excludes = list(_DEFAULT_RSYNC_EXCLUDES)
    if excludes:
        all_excludes.extend(excludes)
    exclude_flags = " ".join(f"--exclude {e}" for e in all_excludes)

    info("Syncing project files to remote...")
    rc = run_cmd(
        f"rsync -az --delete {exclude_flags} {local_dir}/ {host}:{remote_dir}/"
    )
    if rc != 0:
        err("rsync failed")
        return rc

    info("Building Docker image on remote...")
    from .remote import ssh_cmd
    rc = ssh_cmd(host, f"docker build -t {image} {remote_dir}", check=False)
    if rc == 0:
        ok(f"Image built on {host}: {image}")
    else:
        err("Remote Docker build failed")
    return rc


def remote_docker_restart(host, image, run_args=""):
    """Stop, remove, and restart a Docker container on a remote host.

    Returns exit code.
    """
    from .remote import ssh_cmd

    info("Restarting container...")
    rc = ssh_cmd(
        host,
        f"docker stop {image} 2>/dev/null || true;"
        f" docker rm {image} 2>/dev/null || true;"
        f" docker run -d --restart unless-stopped --name {image} {run_args} {image}",
        check=False,
    )
    if rc == 0:
        ok(f"Container {image} restarted on {host}")
    else:
        err("Container start failed")
    return rc


# ---------------------------------------------------------------------------
# Logs command
# ---------------------------------------------------------------------------

def register_logs_command(host, image):
    """Register a 'logs' command that tails remote Docker container logs."""

    @command("logs", f"Tail {image} container logs on remote")
    def cmd_logs(args):
        return run_cmd(f"ssh {host} docker logs -f --tail 100 {image}")


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
