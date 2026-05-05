"""Layer 3 — Docker, long-lived process, and remote container helpers.

Provides: run_with_reload, run_parallel, Docker build/deploy patterns,
remote Docker build, multi-target reload.
"""

import atexit
import glob
import os
import selectors
import shlex
import signal
import subprocess
import sys
import threading
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

    _user_exit = False

    def _on_sigint(signum, frame):
        nonlocal _user_exit
        _user_exit = True
        raise KeyboardInterrupt

    old_sigusr1 = signal.getsignal(signal.SIGUSR1)
    old_sigint = signal.getsignal(signal.SIGINT)
    signal.signal(signal.SIGUSR1, _handle_rebuild_signal)
    signal.signal(signal.SIGINT, _on_sigint)

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
                                _user_exit = True
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

    finally:
        signal.signal(signal.SIGUSR1, old_sigusr1)
        signal.signal(signal.SIGINT, old_sigint)
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


def _restore_image_from_inspect(inspect, image):
    """Return the image reference to use in a restore script.

    Prefers the running container's Image SHA over the tag name so the
    restore script stays valid even after the tag is overwritten by a new
    docker load.  Falls back to the tag when no SHA is present.
    """
    sha = (inspect.get("Image") or "").strip()
    return sha or image


def remote_recreate_container(host, image, container=None, dry_run=False):
    """Transport a local image to `host` and recreate the named container,
    preserving its existing run config (read via `docker inspect`).

    Intended for Unraid-style hosts whose containers were first created by the
    Docker GUI — the GUI's run args aren't known to us, so we replay them
    against the newly-loaded image. If the container doesn't exist on host,
    returns nonzero without touching anything. dry_run prints the reconstructed
    `docker run` command instead of executing.
    """
    import json
    import shlex

    if container is None:
        container = image.split(":", 1)[0].rsplit("/", 1)[-1]

    host_q = shlex.quote(host)
    image_q = shlex.quote(image)
    container_q = shlex.quote(container)

    # Inspect before docker load so we capture the old image SHA before the
    # tag is overwritten; also fails fast if the container doesn't exist yet.
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
    restore_image = _restore_image_from_inspect(inspect, image)

    if dry_run:
        run_cmd_parts = ["docker", "run", "-d", "--name", container] + args + [image]
        reconstructed = " ".join(shlex.quote(p) for p in run_cmd_parts)
        info("Dry run — docker run command that would be executed on remote:")
        print(f"  {reconstructed}\n")
        return 0

    info(f"Transferring image {image} to {host}... (may take a few minutes for large images)")
    rc, elapsed = _run_with_progress(f"docker save {image_q} | ssh {host_q} 'docker load'")
    if rc != 0:
        err("Image transfer failed")
        return rc
    info(f"Transfer complete in {elapsed:.0f}s")

    restore_path = f"/tmp/{container}-restore.sh"
    run_cmd_parts = ["docker", "run", "-d", "--name", container] + args + [restore_image]
    remote_run = " ".join(shlex.quote(p) for p in run_cmd_parts)
    restore_script = f"#!/bin/sh\n{remote_run}\n"
    write_proc = subprocess.run(
        ["ssh", host, f"cat > {shlex.quote(restore_path)} && chmod +x {shlex.quote(restore_path)}"],
        input=restore_script.encode(),
        capture_output=True,
    )
    if write_proc.returncode == 0:
        sha_note = f" (SHA: {restore_image[:12]})" if restore_image != image else ""
        info(f"Restore script saved: {host}:{restore_path}{sha_note}")
    else:
        err("Could not write restore script to remote — aborting deploy to preserve recovery path")
        return 1

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

    info(f"Pruning dangling images on {host}...")
    subprocess.run(["ssh", host, "docker", "image", "prune", "-f"], capture_output=True)

    snapshot_path = f"/tmp/{container}-lastdeploy.json"
    subprocess.run(
        ["ssh", host, f"docker inspect {container_q} > {shlex.quote(snapshot_path)}"],
        capture_output=True,
    )

    return 0


_HOST_CFG_CONSUMED = frozenset({
    "Binds", "PortBindings", "RestartPolicy", "NetworkMode", "ExtraHosts",
    "Devices", "CapAdd", "CapDrop", "Privileged", "Memory",
    "Tmpfs", "Sysctls", "SecurityOpt", "GroupAdd", "Ulimits",
})

_HOST_CFG_EXPECTED = frozenset({
    # Runtime infrastructure — always-populated defaults, no docker run equivalent
    # LogConfig is always {"Type": "json-file", "Config": {}} by default; no --log-driver arg needed
    "ContainerIDFile", "LogConfig", "AutoRemove", "VolumeDriver", "VolumesFrom",
    "CgroupnsMode", "Dns", "DnsOptions", "DnsSearch", "Links", "OomScoreAdj",
    "PidMode", "PublishAllPorts", "ReadonlyRootfs", "UTSMode", "UsernsMode",
    "ShmSize", "Isolation", "Runtime", "CgroupParent",
    # cgroup / CPU resource limits — intentionally skipped (rare on Unraid)
    "BlkioWeight", "BlkioWeightDevice", "BlkioDeviceReadBps", "BlkioDeviceWriteBps",
    "BlkioDeviceReadIOps", "BlkioDeviceWriteIOps", "CpuPeriod", "CpuQuota",
    "CpuRealtimePeriod", "CpuRealtimeRuntime", "CpusetCpus", "CpusetMems",
    "MemoryReservation", "MemorySwap", "MemorySwappiness", "OomKillDisable",
    "NanoCpus", "CpuShares", "PidsLimit", "KernelMemory", "KernelMemoryTCP",
    # Filesystem / path / security metadata — skipped (no practical docker run equivalent)
    "StorageOpt", "MaskedPaths", "ReadonlyPaths", "IpcMode", "DeviceCgroupRules",
    "Mounts",
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

    for mount_path, opts in (host_cfg.get("Tmpfs") or {}).items():
        spec = f"{mount_path}:{opts}" if opts else mount_path
        args += ["--tmpfs", spec]

    for key, val in (host_cfg.get("Sysctls") or {}).items():
        args += ["--sysctl", f"{key}={val}"]

    for opt in host_cfg.get("SecurityOpt") or []:
        args += ["--security-opt", opt]

    for group in host_cfg.get("GroupAdd") or []:
        args += ["--group-add", group]

    for u in host_cfg.get("Ulimits") or []:
        name = u.get("Name", "")
        soft = u.get("Soft", 0)
        hard = u.get("Hard", 0)
        if name:
            spec = f"{name}={soft}" if soft == hard else f"{name}={soft}:{hard}"
            args += ["--ulimit", spec]

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


def remote_container_diff(host, image, container=None, output_format="text"):
    """Diff live container run config against the snapshot from the last deploy.

    Surfaces drift from manual `docker` commands or Unraid GUI tweaks.
    output_format: "text" (default) or "json". Returns 0 if no drift, 1 otherwise.
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

    def _build_run_cmd(args_list):
        parts = ["docker", "run", "-d", "--name", container] + args_list + [image]
        return " ".join(shlex.quote(p) for p in parts)

    reconstructed = _build_run_cmd(curr_args)

    if output_format == "json":
        import json as _json
        result = {
            "container": container,
            "drift": bool(diff),
            "last_args": last_args,
            "current_args": curr_args,
            "diff": diff,
        }
        if diff:
            result["reconstructed_run"] = reconstructed
        print(_json.dumps(result, indent=2))
        return 1 if diff else 0

    if not diff:
        ok(f"No run-config drift detected for '{container}'")
        return 0

    warn(f"Run-config drift detected for '{container}':")
    for line in diff:
        print(line)
    print(f"\nReconstructed docker run (current config):\n  {reconstructed}")
    return 1


def _run_with_progress(cmd, interval=10):
    """Run a shell command, printing elapsed time every interval seconds.

    Returns (returncode, elapsed_seconds).
    """
    start = time.monotonic()
    done = threading.Event()

    def _progress_ticker():
        while not done.wait(timeout=interval):
            elapsed = int(time.monotonic() - start)
            sys.stdout.write(f"  ... still transferring ({elapsed}s elapsed)\n")
            sys.stdout.flush()

    ticker = threading.Thread(target=_progress_ticker, daemon=True)
    ticker.start()
    rc = run_cmd(cmd)
    done.set()
    ticker.join()
    return rc, time.monotonic() - start


def ensure_deps(check_path, install_cmd):
    """Check for a dependency path and install if missing. Returns exit code or 0."""
    if os.path.exists(check_path):
        return 0

    info("Installing dependencies...")
    return run_cmd(install_cmd)


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
