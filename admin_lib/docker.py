"""Layer 3 — Docker, long-lived process, and remote container helpers.

Provides: run_parallel, Docker build/deploy patterns,
remote Docker build, multi-target reload.
"""

import glob
import os
import re
import signal
import subprocess
import sys
import threading
import time

from collections import OrderedDict

from .core import (
    C,
    get_log_dir,
    get_log_file,
    get_urls,
    _children,
    command,
    info,
    ok,
    warn,
    err,
    run_cmd,
    check_url,
    format_url_status,
    pick_target,
)

# Reload infrastructure lives in admin_lib.reload — it is not Docker's, and a
# `docker.run_with_reload` was invisible to every non-Docker project that needed
# exactly it. docker_dev_from_config still drives it; the pid-file helpers below
# are shared with it so a `docker dev` and a reload loop agree on the paths.
from .reload import (
    _ADMIN_PID_PREFIX,
    cleanup_pid_file,
    run_with_reload,
)

# Re-export remote helpers so existing projects importing from server still work
try:
    from .remote import require_host, ssh_cmd, _is_local_host
except ImportError:
    pass


# ---------------------------------------------------------------------------
# run_parallel — multiplexed output from multiple processes
# ---------------------------------------------------------------------------

def run_parallel(targets, formatter=None):
    """Run multiple processes in parallel with labeled, colored output.

    targets: list of (cmd, label, color) tuples.
    Returns list of return codes.
    """
    import threading

    os.makedirs(get_log_dir(), exist_ok=True)
    log_fh = open(get_log_file(), "a")
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

def docker_build(image, context_dir=".", platform=None, target=None):
    """Build a Docker image. Returns exit code.

    `target` selects a specific multi-stage build stage. Without it Docker
    picks the LAST stage in the Dockerfile, which can silently produce the
    wrong image when the file is laid out as `base` → `dev` (dev inherits
    from base for HMR convenience). Pass it explicitly for prod builds.
    """

    plat = f"--platform {platform} " if platform else ""
    tgt = f"--target {target} " if target else ""
    target_note = f" (target={target})" if target else ""
    info(f"Building Docker image: {image}{target_note}...")
    return run_cmd(f"docker build {plat}{tgt}-t {image} {context_dir}")


# ---------------------------------------------------------------------------
# docker-dev — build+run a project's Docker image in a reload loop
# ---------------------------------------------------------------------------
#
# Mirrors the vite-dev shape: project declares a `[docker_dev]` config
# table; the generated admin command calls `docker_dev_from_config(cfg)`
# which translates the schema into `docker build` + `docker run` argv
# and hands the joined shell command to `run_with_reload`.

def _docker_dev_build_cmd(cfg):
    """Render the `docker build && docker run` shell string from a
    `[docker_dev]` config table. Pure (no side effects); the caller
    invokes it inside the reload loop and handles bind-mount mkdir's.

    Schema (every field optional unless noted):
      image_tag          str   — required; what to tag the build and run
      build_context      str   — defaults to "."
      container_name     str   — `--name` flag
      gpu                bool|str|list — true / "all" / ["device=0", ...]
      env_file           str   — path; emitted as --env-file when the
                                 file exists at run time (relative to cwd)
      pre_kill           bool  — call cmd_kill before launching
      ports              list of {host: int, container: int}
      bind_mounts        list of {host: str, container: str, mode?: str,
                                  mkdir?: bool}  — host path is project-
                                  relative; `mkdir=true` creates it first
      optional_mounts    list of {env: str, container: str, mode?: str}
                                — host path comes from env var; skipped
                                  silently when env unset
      env                list of {name: str, value: str}
                                — literal -e flags emitted into argv
    """
    import shlex

    image = cfg.get("image_tag")
    if not image:
        raise ValueError("docker_dev: 'image_tag' is required")
    context = cfg.get("build_context", ".")
    target = cfg.get("target")
    # When the Docker daemon runs on a different host than where admin
    # runs (e.g. code-server mounting Unraid's docker.sock), `os.getcwd()`
    # returns a path the daemon can't see. `host_root` overrides that
    # base for resolving relative bind-mount paths: callers point it at
    # the daemon-visible path of the project root (e.g. an Unraid share
    # path) and the rest of the config stays portable. Unset → original
    # cwd-based resolution.
    host_root = cfg.get("host_root") or os.getcwd()

    # Build phase — docker layer caching keeps this near-instant when
    # nothing changed.
    #
    # `DOCKER_BUILDKIT=1` and `--progress=plain` are non-negotiable: the
    # legacy builder suppresses per-step output when stdout isn't a TTY
    # (which it never is inside run_with_reload, since stdout is a pipe).
    # BuildKit streams line-buffered progress that survives the pipe and
    # threads cleanly through the column formatter — without these,
    # `./admin dev` looks frozen for 10+ minutes during a cold rebuild.
    #
    # `target` selects a specific multi-stage stage (e.g. a dedicated
    # `FROM ... AS dev` block that installs frontend dev-deps and runs
    # bun + vite in watch mode) so the same Dockerfile can serve both
    # prod and dev iteration without forking the file.
    target_flag = f" --target={shlex.quote(target)}" if target else ""
    build_cmd = (
        f"DOCKER_BUILDKIT=1 docker build --progress=plain{target_flag} "
        f"-t {shlex.quote(image)} {shlex.quote(context)}"
    )

    # Run phase — assemble argv up to the trailing "$@" / image splice.
    run_argv = ["docker", "run", "--rm"]

    container_name = cfg.get("container_name")
    if container_name:
        run_argv += ["--name", container_name]

    gpu = cfg.get("gpu")
    if gpu is True or gpu == "all":
        run_argv += ["--gpus", "all"]
    elif isinstance(gpu, list) and gpu:
        run_argv += ["--gpus", ",".join(gpu)]
    elif isinstance(gpu, str) and gpu:
        run_argv += ["--gpus", gpu]

    for p in cfg.get("ports") or ():
        host_port = p.get("host")
        cont_port = p.get("container", host_port)
        if host_port is None:
            continue
        run_argv += ["-p", f"{host_port}:{cont_port}"]

    for m in cfg.get("bind_mounts") or ():
        host = m.get("host")
        container = m.get("container")
        if not host or not container:
            continue
        mode = m.get("mode")
        # Resolve relative paths against `host_root` (defaults to cwd)
        # so the resulting shell command has no $(pwd) substitutions to
        # mess with — `shlex.quote` can wrap the whole flag cleanly.
        host_resolved = host if os.path.isabs(host) else os.path.normpath(os.path.join(host_root, host))
        flag = f"{host_resolved}:{container}" + (f":{mode}" if mode else "")
        run_argv += ["-v", flag]

    # `--device` flags. These differ from bind mounts: the kernel registers
    # the device with the container's cgroup so it's actually usable, not
    # just visible. Required for GPU/VAAPI passthrough (`-v /dev/dri`
    # alone doesn't work). Stash, Plex, ersatztv on this daemon all use
    # this exact pattern for Intel iGPU access.
    for d in cfg.get("devices") or ():
        host = d.get("host")
        container = d.get("container", host)
        if not host:
            continue
        flag = f"{host}:{container}" if host != container else host
        run_argv += ["--device", flag]

    # Anonymous volumes — pinned to specific in-container paths with no
    # host source. Used to mask a subtree of a bind mount so the value
    # baked into the image wins over what the host has.
    #
    # Canonical use case: bind-mount the project root to /app for live
    # source editing, then mask /app/node_modules + /app/web/node_modules
    # so the container's `bun install` results aren't clobbered by an
    # empty (or wrong-platform) host node_modules. Without this, dev hot
    # reload through volume mounts is unworkable on cross-platform setups.
    for p in cfg.get("anonymous_volumes") or ():
        path = p if isinstance(p, str) else p.get("path")
        if not path:
            continue
        run_argv += ["-v", path]

    # Optional mounts gated on env vars. We can't expand env vars at
    # render time (would bake host-specific paths into the shell string
    # and freeze them for the loop's lifetime), so we emit shell
    # conditionals that re-check at each invocation.
    optional_mount_shell = []
    for m in cfg.get("optional_mounts") or ():
        env = m.get("env")
        container = m.get("container")
        if not env or not container:
            continue
        mode = m.get("mode")
        suffix = f":{mode}" if mode else ""
        optional_mount_shell.append(
            f'[ -n "${{{env}:-}}" ] && set -- "$@" -v "${{{env}}}:{container}{suffix}"'
        )

    # Env-file: only emitted when the file exists at run time. Same
    # rationale as optional mounts.
    env_file = cfg.get("env_file")
    env_file_shell = ""
    if env_file:
        env_file_shell = f'[ -f {shlex.quote(env_file)} ] && set -- "$@" --env-file {shlex.quote(env_file)}'

    for e in cfg.get("env") or ():
        name = e.get("name")
        value = e.get("value")
        if not name:
            continue
        if value is None:
            run_argv += ["-e", name]
        else:
            run_argv += ["-e", f"{name}={value}"]

    run_argv.append(image)

    # Stitch it together. `set --` resets `$@` so optional mount/env-file
    # conditionals can accumulate into it; the final `docker run … "$@"`
    # splices them in.
    quoted_run = " ".join(shlex.quote(a) for a in run_argv)
    parts = [
        "set -e",
        build_cmd,
        "set --",
    ]
    parts += optional_mount_shell
    if env_file_shell:
        parts.append(env_file_shell)
    # Insert the "$@" splice right after `docker run --rm` in the argv.
    # Easier: emit the full argv and append "$@" at the end — docker is
    # fine with -v / --env-file after the image name? No, docker run
    # rejects flags after the image. So we emit run argv WITHOUT the
    # image, then "$@", then image.
    argv_no_image = run_argv[:-1]
    quoted_no_image = " ".join(shlex.quote(a) for a in argv_no_image)
    parts.append(f'exec {quoted_no_image} "$@" {shlex.quote(image)}')
    return "\n".join(parts)


def docker_dev_from_config(cfg):
    """Adapter: read a manifest `[docker_dev]` table and call
    `run_with_reload` with the rendered docker build+run command. Honors
    `pre_kill = true` by invoking the project's `cmd_kill` first.

    Returns the exit code from `run_with_reload`.
    """
    if not cfg:
        err("docker_dev_from_config: empty config")
        return 1

    # Pre-create bind-mount host dirs that opted into mkdir. Skips
    # absolute paths (those belong to system state, not project state).
    for m in cfg.get("bind_mounts") or ():
        host = m.get("host")
        if host and not host.startswith("/") and m.get("mkdir", True):
            os.makedirs(host, exist_ok=True)

    if cfg.get("pre_kill"):
        # cmd_kill is generated when the project has a kill command. We
        # look it up via the caller's globals — see render_docker_dev.
        kill = cfg.get("_pre_kill_callable")
        if callable(kill):
            kill([])

    try:
        cmd = _docker_dev_build_cmd(cfg)
    except ValueError as e:
        err(f"docker_dev: {e}")
        return 1

    target = cfg.get("target", "dev")
    return run_with_reload(cmd, target=target)


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


def _default_container_name(image):
    """Derive a sensible default container name from an image reference."""
    return image.split(":", 1)[0].rsplit("/", 1)[-1]


def _docker_argv(host, local, argv):
    """Wrap a docker argv list with `ssh host` when the target is remote."""
    return argv if local else ["ssh", host, *argv]


def _parse_env_file(path):
    """Best-effort .env parser — KEY=value pairs, # comments, blank lines.
    Returns a dict. Missing file → empty dict (caller decides if that's OK).
    """
    if not path or not os.path.exists(path):
        return {}
    out = {}
    with open(path, "r") as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            if "=" not in line:
                continue
            k, v = line.split("=", 1)
            k = k.strip()
            v = v.strip()
            # Do NOT strip surrounding quotes: docker's --env-file injects the
            # post-'=' string verbatim (quotes become literal chars in the value).
            # Stripping here would render the wrong value and make the drift check
            # fire forever (target FOO=bar baz vs live FOO="bar baz").
            if k:
                out[k] = v
    return out


def remote_recreate_container(host, image, container=None, target_cfg=None,
                              force=False, dry_run=False):
    """Recreate a container on `host` from a local image.

    Two modes:
      target_cfg=None — legacy path. Inspect the live container, replay its
        config against the new image. Container MUST exist.
      target_cfg=<dict> — TOML-canonical. Render argv from the `[docker_run]`
        table. If the container exists, diff target vs live; abort with a
        drift report unless `force=True`. If the container doesn't exist,
        create it from the cfg directly.

    `force=True` skips the drift check (still computed, just doesn't abort).
    `dry_run=True` prints the would-run command instead of executing.
    """
    import json
    import shlex

    if container is None:
        container = _default_container_name(image)

    host_q = shlex.quote(host)
    image_q = shlex.quote(image)
    container_q = shlex.quote(container)
    local = _is_local_host(host)
    location = "locally" if local else f"on {host}"

    info(f"Inspecting existing container '{container}' {location}...")
    proc = subprocess.run(
        _docker_argv(host, local, ["docker", "inspect", container]),
        capture_output=True, text=True,
    )
    container_exists = (
        proc.returncode == 0 and proc.stdout.strip() and proc.stdout.strip() != "[]"
    )

    if not container_exists and target_cfg is None:
        err(f"Container '{container}' does not exist {location}.")
        print(
            f"\n  Create it from the Unraid Docker GUI using the template,"
            f" then re-run this command.\n"
        )
        return 1

    inspect = None
    if container_exists:
        try:
            inspect = json.loads(proc.stdout)[0]
        except (json.JSONDecodeError, IndexError) as e:
            err(f"Could not parse docker inspect output: {e}")
            return 1

    if target_cfg is not None:
        # TOML-canonical path: render from cfg, check drift, use cfg argv.
        env_file_path = target_cfg.get("env_file")
        env_file_vars = _parse_env_file(env_file_path) if env_file_path else {}
        args = _build_run_args_from_cfg(target_cfg, env_file_vars=env_file_vars)
        if container_exists:
            img_env, img_labels, img_workdir, img_user = _fetch_image_metadata(host, local, image)
            live_args = _live_argv_for_drift(
                inspect, image_env=img_env, image_labels=img_labels,
                image_workdir=img_workdir, image_user=img_user,
            )
            in_target_only, in_live_only = _compute_drift(args, live_args)
            if in_target_only or in_live_only:
                report = _format_drift_report(in_target_only, in_live_only)
                if force:
                    warn("Drift detected — proceeding with --force (live state will be discarded):")
                    print(report)
                else:
                    err(report)
                    return 1
            else:
                info("No drift between live container and [docker_run] — proceeding")
    else:
        # Legacy inspect-and-recreate path.
        args = _build_run_args_from_inspect(inspect)

    restore_image = _restore_image_from_inspect(inspect, image) if inspect else image
    run_cmd_parts = ["docker", "run", "-d", "--name", container] + args + [image]
    remote_run = " ".join(shlex.quote(p) for p in run_cmd_parts)

    if dry_run:
        info(f"Dry run — docker run command that would be executed {location}:")
        print(f"  {remote_run}\n")
        return 0

    if local:
        info(f"Running locally — skipping image transfer (image already present)")
    else:
        info(f"Transferring image {image} to {host}... (may take a few minutes for large images)")
        rc, elapsed = _run_with_progress(f"docker save {image_q} | ssh {host_q} 'docker load'")
        if rc != 0:
            err("Image transfer failed")
            return rc
        info(f"Transfer complete in {elapsed:.0f}s")

    restore_path = f"/tmp/{container}-restore.sh"
    if container_exists:
        restore_cmd_parts = ["docker", "run", "-d", "--name", container] + args + [restore_image]
        restore_run = " ".join(shlex.quote(p) for p in restore_cmd_parts)
        restore_script = f"#!/bin/sh\n{restore_run}\n"

        if local:
            try:
                with open(restore_path, "w") as f:
                    f.write(restore_script)
                import stat as _stat
                os.chmod(restore_path, os.stat(restore_path).st_mode | _stat.S_IXUSR | _stat.S_IXGRP)
                write_ok = True
            except OSError:
                write_ok = False
        else:
            write_proc = subprocess.run(
                ["ssh", host, f"cat > {shlex.quote(restore_path)} && chmod +x {shlex.quote(restore_path)}"],
                input=restore_script.encode(),
                capture_output=True,
            )
            write_ok = write_proc.returncode == 0

        if not write_ok:
            err("Could not write restore script — aborting deploy to preserve recovery path")
            return 1
        sha_note = f" (SHA: {restore_image[:12]})" if restore_image != image else ""
        info(f"Restore script saved: {restore_path}{sha_note}")

    if container_exists:
        info(f"Stopping + removing '{container}' {location}...")
        stop_rm_shell = f"docker stop -t 10 {container_q} >/dev/null && docker rm {container_q} >/dev/null"
        rc = run_cmd(stop_rm_shell if local else f"ssh {host_q} '{stop_rm_shell}'")
        if rc != 0:
            err("Stop/remove failed")
            return rc

    config_source = "[docker_run] in admin.toml" if target_cfg is not None else "preserved run config"
    info(f"Recreating '{container}' {location} from {config_source}...")
    rc = run_cmd(remote_run if local else f"ssh {host_q} {shlex.quote(remote_run)}")
    if rc != 0:
        restore_hint = f"bash {restore_path}" if local else f"ssh {host} bash {restore_path}"
        err(f"Container start failed — to restore: {restore_hint}")
        return rc

    ok(f"Deployed {image} as container '{container}' {location}")

    info(f"Pruning dangling images {location}...")
    subprocess.run(
        _docker_argv(host, local, ["docker", "image", "prune", "-f"]),
        capture_output=True,
    )

    snapshot_path = f"/tmp/{container}-lastdeploy.json"
    snap_shell = f"docker inspect {container_q} > {shlex.quote(snapshot_path)}"
    if local:
        subprocess.run(snap_shell, shell=True, capture_output=True)
    else:
        subprocess.run(["ssh", host, snap_shell], capture_output=True)

    return 0


_HOST_CFG_CONSUMED = frozenset({
    "Binds", "PortBindings", "RestartPolicy", "NetworkMode", "ExtraHosts",
    "Devices", "DeviceRequests", "CapAdd", "CapDrop", "Privileged", "Memory",
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


_ENV_REF_RE = __import__("re").compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}")


def _expand_env_refs(value, env_vars):
    """Substitute ${VAR} references in `value` from `env_vars`. Returns
    (substituted, all_resolved). all_resolved=False means at least one
    ${VAR} couldn't be resolved — caller should drop the entry rather than
    emit a literal ${VAR} that would poison the drift comparison.
    """
    all_resolved = True
    def sub(m):
        nonlocal all_resolved
        v = env_vars.get(m.group(1))
        if v is None:
            all_resolved = False
            return m.group(0)
        return v
    return _ENV_REF_RE.sub(sub, value), all_resolved


def _build_run_args_from_cfg(cfg, env_vars=None, env_file_vars=None):
    """Render argv pieces (no `docker run -d --name X` prefix, no image) from
    an `[docker_run]` config table — the prod counterpart to
    `_docker_dev_build_cmd`. Output shape matches `_build_run_args_from_inspect`
    so the two can be compared for drift.

    env_vars: dict of host env (defaults to os.environ); used to expand
    `optional_mounts` (skipped silently when the gating env is unset).
    env_file_vars: optional dict of pre-parsed env_file values; merged into
    the emitted `-e NAME=VALUE` pieces. When None, env_file is NOT auto-read
    here (caller handles it) so this function stays pure.
    """
    if env_vars is None:
        env_vars = os.environ

    args = []

    # Bind mounts (absolute host paths in prod; no host_root rewriting).
    # Normalize mode="rw" (and unset) to no suffix — matches docker's default
    # and mirrors the live-side strip in _live_argv_for_drift so the drift
    # comparison doesn't false-positive on cosmetic mode differences.
    def _mount_flag(host, container, mode):
        if mode and mode != "rw":
            return f"{host}:{container}:{mode}"
        return f"{host}:{container}"

    for m in cfg.get("bind_mounts") or ():
        host = m.get("host")
        container = m.get("container")
        if not host or not container:
            continue
        args += ["-v", _mount_flag(host, container, m.get("mode"))]

    # Optional mounts gated on host env vars — expand at render time
    for m in cfg.get("optional_mounts") or ():
        env = m.get("env")
        container = m.get("container")
        if not env or not container:
            continue
        host = env_vars.get(env, "").strip()
        if not host:
            continue
        args += ["-v", _mount_flag(host, container, m.get("mode"))]

    # Anonymous volumes
    for p in cfg.get("anonymous_volumes") or ():
        path = p if isinstance(p, str) else p.get("path")
        if not path:
            continue
        args += ["-v", path]

    # Ports — host may be int, "PORT", or "IP:PORT"; container is int.
    # ${VAR} in the host is expanded (same as env/labels) so a Tailscale-bound
    # port can reference e.g. "${UNRAID_HOST}:8080" instead of hardcoding the IP.
    for p in cfg.get("ports") or ():
        host_port = p.get("host")
        cont_port = p.get("container", host_port)
        if host_port is None:
            continue
        host_str = str(host_port)
        if "${" in host_str:
            host_str, all_ok = _expand_env_refs(host_str, env_vars)
            if not all_ok:
                warn(f"docker_run.ports: host {host_port!r} references unset env var — skipping")
                continue
        host_ip = ""
        if ":" in host_str:
            host_ip, host_str = host_str.rsplit(":", 1)
        spec = f"{host_ip}:{host_str}:{cont_port}/tcp" if host_ip else f"{host_str}:{cont_port}/tcp"
        args += ["-p", spec]

    # Env list — literal -e flags, with ${VAR} substitution at render time.
    # `name = "X"` with no `value` → inherit X from process env (same as `value = "${X}"`).
    # `value = "${X}"` → substitute from process env.
    # If any referenced var isn't set, skip the entry (warn) so drift
    # comparison doesn't see a literal `${X}` on one side.
    seen_env_names = set()
    for e in cfg.get("env") or ():
        name = e.get("name")
        if not name:
            continue
        seen_env_names.add(name)
        value = e.get("value")
        if value is None:
            resolved = env_vars.get(name)
            if resolved is None:
                warn(f"docker_run.env: {name!r} has no value and is not in process env — skipping")
                continue
            args += ["-e", f"{name}={resolved}"]
        else:
            resolved, all_ok = _expand_env_refs(value, env_vars)
            if not all_ok:
                warn(f"docker_run.env: {name} value {value!r} references unset env var — skipping")
                continue
            args += ["-e", f"{name}={resolved}"]

    # env_file — merge in any keys not already set by the literal env list
    # so cfg.env wins on conflict (matches docker's --env / --env-file order)
    if env_file_vars:
        for name in sorted(env_file_vars.keys()):
            if name in seen_env_names:
                continue
            args += ["-e", f"{name}={env_file_vars[name]}"]

    # Restart policy
    restart = cfg.get("restart")
    if restart and restart != "no":
        args += ["--restart", restart]

    # Memory cap (--memory, e.g. "24g"). Blast-radius net: bounds container RAM so
    # a runaway process (e.g. a VMAF ffmpeg buffering decoded frames) gets
    # cgroup-OOM-killed inside the container instead of taking the host down. The
    # inspect/drift side already reads HostConfig.Memory → --memory, so a deploy
    # that sets this stays drift-clean.
    memory = cfg.get("memory")
    if memory:
        args += ["--memory", str(memory)]

    # Network mode (rare in [docker_run] but supported for symmetry)
    network = cfg.get("network")
    if network and network != "default":
        args += ["--network", network]

    # User (UID[:GID] or name). Unraid runs containers as nobody:users ("99:100"),
    # which differs from the image's baked USER. The live-side drift filter only
    # strips --user when it equals the image user, so without this knob the cfg
    # can't reproduce a non-image user and a --force deploy would silently drop
    # it — reverting to the image default and changing ownership of written files.
    user = cfg.get("user")
    if user:
        args += ["--user", str(user)]

    # Devices (e.g. /dev/dri)
    for d in cfg.get("devices") or ():
        host = d.get("host")
        container = d.get("container", host)
        if not host:
            continue
        perms = d.get("permissions", "rwm")
        spec = f"{host}:{container}"
        if perms and perms != "rwm":
            spec += f":{perms}"
        args += ["--device", spec]

    # GPU — same translation as the dev path
    gpu = cfg.get("gpu")
    if gpu is True or gpu == "all":
        args += ["--gpus", "all"]
    elif isinstance(gpu, list) and gpu:
        args += ["--gpus", ",".join(gpu)]
    elif isinstance(gpu, str) and gpu:
        args += ["--gpus", gpu]

    # Labels — same ${VAR} substitution as env values, skip on unresolved refs.
    # Used for e.g. `net.unraid.docker.managed=dockerman` so the Unraid GUI
    # keeps the container visible in its managed-containers view after our
    # `docker run` recreate (the GUI normally adds these labels itself).
    for lbl in cfg.get("labels") or ():
        key = lbl.get("key")
        if not key:
            continue
        value = lbl.get("value", "")
        if value:
            value, all_ok = _expand_env_refs(value, env_vars)
            if not all_ok:
                warn(f"docker_run.labels: {key} value {lbl.get('value')!r} references unset env var — skipping")
                continue
        args += ["--label", f"{key}={value}"]

    # Linux capabilities + security knobs. `cap_add` is needed for e.g.
    # intel_gpu_top (CAP_PERFMON → i915 perf PMU access). These mirror the
    # HostConfig keys the drift filter reads back from `docker inspect`, so a
    # deploy that sets them stays drift-clean.
    for c in cfg.get("cap_add") or ():
        args += ["--cap-add", c]
    for c in cfg.get("cap_drop") or ():
        args += ["--cap-drop", c]
    if cfg.get("privileged"):
        args += ["--privileged"]
    for opt in cfg.get("security_opt") or ():
        args += ["--security-opt", opt]
    for key, val in (cfg.get("sysctls") or {}).items():
        args += ["--sysctl", f"{key}={val}"]

    return args


def render_run_args(cfg, env_vars=None):
    """Render a `[docker_run]` table to a shlex-quoted, eval-ready run-args
    string — the argv that sits between `docker run -d --name X` and the image,
    assembled exactly as `remote_recreate_container` does (same renderer, same
    env_file merge, same quoting).

    Lets an out-of-tree native-build script capture `$(admin run-args)` instead
    of re-parsing `admin.toml` itself, so its recreate command can never drift
    from what `./admin deploy` would run (issue #429).

    Note: an `env_file` is expanded inline into `-e NAME=VALUE` tokens, so any
    secrets it holds appear in the returned string — treat the output as
    sensitive (same exposure `remote_recreate_container` puts on the remote
    command line, but now also locally capturable).
    """
    import shlex
    cfg = cfg or {}
    env_file_path = cfg.get("env_file")
    env_file_vars = _parse_env_file(env_file_path) if env_file_path else {}
    args = _build_run_args_from_cfg(cfg, env_vars=env_vars, env_file_vars=env_file_vars)
    return " ".join(shlex.quote(p) for p in args)


def _normalize_mem_bytes(value):
    """Canonicalize a Docker --memory value to an integer byte-count string.

    The TOML side emits a human suffix ("24g"); `docker inspect` reports the
    raw byte count ("25769803776"). A plain string compare flags these as
    drift even though they're the same cap, so we resolve both to bytes before
    comparing. Docker's size suffixes are 1024-based (b/k/m/g, case-insensitive)
    and accept a fractional magnitude ("1.5g"), matching docker's RAMInBytes.
    Returns the value unchanged if it doesn't parse as a size.
    """
    m = re.fullmatch(r"\s*(\d+(?:\.\d+)?)\s*([bkmgBKMG]?)\s*", str(value))
    if not m:
        return str(value)
    mult = {"": 1, "b": 1, "k": 1024, "m": 1024 ** 2, "g": 1024 ** 3}[m.group(2).lower()]
    return str(int(float(m.group(1)) * mult))


# Flags whose value is a byte size — normalized to bytes before drift compare
# so "24g" and "25769803776" don't read as divergent.
_MEM_FLAGS = {"--memory", "-m", "--memory-swap", "--memory-reservation", "--shm-size"}


def _argv_to_pair_set(argv):
    """Canonicalize an argv-pieces list into a sortable set of (flag, value)
    strings, so two lists with different orderings compare cleanly.

    Boolean flags (no value, e.g. --privileged) get an empty value.
    Byte-size flags are normalized to a byte count so unit notation
    ("24g" vs "25769803776") doesn't register as drift.
    """
    pairs = set()
    i = 0
    while i < len(argv):
        flag = argv[i]
        if i + 1 < len(argv) and not argv[i + 1].startswith("-"):
            value = argv[i + 1]
            if flag in _MEM_FLAGS:
                value = _normalize_mem_bytes(value)
            pairs.add(f"{flag}\t{value}")
            i += 2
        else:
            pairs.add(f"{flag}\t")
            i += 1
    return pairs


def _compute_drift(target_argv, live_argv):
    """Diff target (from TOML) against live (from docker inspect).

    Returns (in_target_only, in_live_only) — each a sorted list of
    "flag\\tvalue" strings. Strict semantics: any divergence in either
    direction counts as drift.
    """
    target_set = _argv_to_pair_set(target_argv)
    live_set = _argv_to_pair_set(live_argv)
    in_target_only = sorted(target_set - live_set)
    in_live_only = sorted(live_set - target_set)
    return in_target_only, in_live_only


def _format_drift_report(in_target_only, in_live_only):
    """Render a drift report for human eyeballs. Returns the formatted
    string (no I/O; caller prints).

    Format:
        Drift detected between live container and admin.toml ([docker_run]):

          target has, live missing:
            --gpus      all
            -p          100.64.0.10:9091:9091/tcp

          live has, target missing:
            -p          :9091:9091/tcp

        Refusing to deploy. Either back-port the live values to admin.toml,
        or rerun with --force to overwrite (live state will be discarded).
    """
    if not in_target_only and not in_live_only:
        return ""

    def _fmt_block(label, entries):
        if not entries:
            return ""
        rows = []
        flag_width = max(len(e.split("\t", 1)[0]) for e in entries)
        for entry in entries:
            flag, value = entry.split("\t", 1)
            rows.append(f"    {flag.ljust(flag_width)}  {value}")
        return f"  {label}:\n" + "\n".join(rows) + "\n"

    out = ["Drift detected between live container and admin.toml ([docker_run]):\n"]
    out.append(_fmt_block("target has, live missing", in_target_only))
    out.append(_fmt_block("live has, target missing", in_live_only))
    out.append(
        "Refusing to deploy. Either back-port the live values to admin.toml,\n"
        "or rerun with --force to overwrite (live state will be discarded)."
    )
    return "\n".join(s for s in out if s)


def _live_argv_for_drift(inspect, image_env=None, image_labels=None,
                         image_workdir=None, image_user=None):
    """Build the live-side argv for drift comparison.

    Strips noise that's never expressible from TOML so the report only shows
    actionable drift:
      - image-baked env vars (image_env: ["NAME=VALUE", ...])
      - image-baked labels (image_labels: {k: v})
      - image's WorkingDir / User (when live matches)
      - Docker autogen `--hostname <12-char-hex>` and `--mac-address` on bridge
      - default `--network bridge` (Docker's implicit default)
      - bind-mount `:rw` suffix (Docker's default mount mode)
    """
    args = _build_run_args_from_inspect(inspect)
    image_env_set = set(image_env or [])
    image_labels = image_labels or {}

    filtered = []
    i = 0
    while i < len(args):
        flag = args[i]
        val = args[i + 1] if i + 1 < len(args) and not args[i + 1].startswith("-") else None

        # --network bridge is Docker's implicit default; not actionable
        if flag == "--network" and val == "bridge":
            i += 2; continue
        # Image-baked env vars
        if flag == "-e" and val in image_env_set:
            i += 2; continue
        # Unraid dockerMan diagnostic env vars (HOST_CONTAINERNAME / HOST_HOSTNAME
        # / HOST_OS / HOST_CONFIGURABLE). Added by Unraid when it creates the
        # container; not re-added when we deploy via `docker run` from outside
        # the GUI, so flagging drift on them just clutters every deploy.
        if flag == "-e" and val and val.split("=", 1)[0] in (
            "HOST_CONTAINERNAME", "HOST_HOSTNAME", "HOST_OS", "HOST_CONFIGURABLE",
        ):
            i += 2; continue
        # Image-baked labels (live emits "k=v"; image_labels is {k:v})
        if flag == "--label" and val and "=" in val:
            k, v = val.split("=", 1)
            if image_labels.get(k) == v:
                i += 2; continue
        # --hostname: always stripped. Cfg has no `hostname` field, so any
        # value live carries is either Docker's autogen 12-hex (no signal) or
        # an Unraid-GUI default (often the container name, also no signal).
        # Preserving it would force every deploy through `--force` for no
        # gain. If a project ever needs declared hostnames, add a knob then.
        if flag == "--hostname":
            if val is not None:
                i += 2
            else:
                i += 1
            continue
        # Docker autogenerated MAC on bridge network (always per-container)
        if flag == "--mac-address" and val:
            i += 2; continue
        # WorkingDir / User baked into the image
        if flag == "--workdir" and val == image_workdir:
            i += 2; continue
        if flag == "--user" and val == image_user:
            i += 2; continue
        # Strip `:rw` from bind specs — rw is Docker's default mount mode
        if flag == "-v" and val and val.endswith(":rw"):
            filtered += [flag, val[:-3]]
            i += 2; continue

        filtered.append(flag)
        if val is not None:
            filtered.append(val)
            i += 2
        else:
            i += 1
    return filtered


def _fetch_image_metadata(host, local, image):
    """Return (env, labels, working_dir, user) baked into the image, or
    ([], {}, None, None) on any inspect failure.

    Uses `docker image inspect` (not bare `docker inspect`) so a container
    sharing the image's base name doesn't shadow the image lookup —
    `docker inspect muxarr` would return the muxarr CONTAINER when one
    exists, and its env would falsely be treated as "image-baked", silently
    stripping every container env from the drift report.

    Uses full JSON (no Go `--format` template) because ssh-via-argv mangles
    `{{json}}` placeholders into an "unclosed action" parse error.
    """
    import json
    proc = subprocess.run(
        _docker_argv(host, local, ["docker", "image", "inspect", image]),
        capture_output=True, text=True,
    )
    if proc.returncode != 0 or not proc.stdout.strip():
        return [], {}, None, None
    try:
        parsed = json.loads(proc.stdout)
        if not parsed:
            return [], {}, None, None
        config = parsed[0].get("Config") or {}
        return (
            config.get("Env") or [],
            config.get("Labels") or {},
            config.get("WorkingDir") or None,
            config.get("User") or None,
        )
    except (json.JSONDecodeError, ValueError, IndexError, AttributeError):
        return [], {}, None, None


def _fetch_image_env(host, local, image):
    """Back-compat shim — returns just the env list. Prefer
    `_fetch_image_metadata` for full filtering data."""
    env, _, _, _ = _fetch_image_metadata(host, local, image)
    return env


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

    # DeviceRequests is where `--gpus` lives in `docker inspect` output.
    # `--gpus all`               → Driver=nvidia, Count=-1
    # `--gpus N`                 → Driver=nvidia, Count=N
    # `--gpus "device=ID,ID"`    → Driver=nvidia, Count=0, DeviceIDs=[...]
    # Missing this translation silently strips GPU access on inspect-and-recreate
    # deploys (e.g. ffmpeg loses CUDA → "Cannot load libcuda.so.1").
    for req in host_cfg.get("DeviceRequests") or []:
        driver = req.get("Driver") or ""
        if driver and driver != "nvidia":
            continue  # non-nvidia device requests aren't expressible via --gpus
        device_ids = req.get("DeviceIDs") or []
        count = req.get("Count") or 0
        if device_ids:
            # Emit as a bare value (no embedded double quotes) — shlex.quote
            # downstream wraps the whole arg correctly if needed. Literal
            # `"device=..."` would survive shell escaping and reach docker
            # with the quote chars intact, which docker's --gpus parser rejects.
            args += ["--gpus", f"device={','.join(device_ids)}"]
        elif count == -1:
            args += ["--gpus", "all"]
        elif count > 0:
            args += ["--gpus", str(count)]

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
        container = _default_container_name(image)

    snapshot_path = f"/tmp/{container}-lastdeploy.json"
    local = _is_local_host(host)
    location = "locally" if local else f"on {host}"

    info(f"Fetching current inspect for '{container}' {location}...")
    curr_proc = subprocess.run(
        _docker_argv(host, local, ["docker", "inspect", container]),
        capture_output=True, text=True,
    )

    if curr_proc.returncode != 0 or not curr_proc.stdout.strip() or curr_proc.stdout.strip() == "[]":
        err(f"Container '{container}' not found {location}")
        return 1
    try:
        curr_inspect = json.loads(curr_proc.stdout)[0]
    except (json.JSONDecodeError, IndexError):
        err("Could not parse current docker inspect output")
        return 1

    if local:
        try:
            with open(snapshot_path) as f:
                snap_stdout = f.read()
        except OSError:
            err(f"No deploy snapshot found at {snapshot_path} — run a deploy first")
            return 1
    else:
        snap_proc = subprocess.run(
            ["ssh", host, f"cat {shlex.quote(snapshot_path)} 2>/dev/null"],
            capture_output=True, text=True,
        )
        if snap_proc.returncode != 0:
            err(f"Could not reach {host} (rc {snap_proc.returncode})")
            return 1
        snap_stdout = snap_proc.stdout

    if not snap_stdout.strip():
        err(f"No deploy snapshot found at {snapshot_path} — run a deploy first")
        return 1
    try:
        last_inspect = json.loads(snap_stdout)[0]
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

def remote_logs(host, image, container=None, tail=200, since=None, follow=True):
    """Tail container logs locally or via SSH.

    since:  optional `docker logs --since` value in seconds (int). When set,
            history is shown from that point forward.
    follow: when True, append `-f` to stream live (the default — plain
            `./admin logs live` follows). With a `--since` window and no
            explicit `-f`, callers pass follow=False to dump-and-exit.
    """
    import shlex
    if container is None:
        container = _default_container_name(image)
    container_q = shlex.quote(container)
    flags = []
    if since is not None:
        flags.append(f"--since {int(since)}s")
    if follow:
        flags.append("-f")
    flags.append(f"--tail={tail}")
    logs_cmd = f"docker logs {' '.join(flags)} {container_q}"
    if _is_local_host(host):
        return run_cmd(logs_cmd)
    return run_cmd(f"ssh {shlex.quote(host)} {logs_cmd}")


def _parse_since_offset(spec):
    """Convert a relative DD:HH:MM:SS offset into total seconds (int).

    Fields are right-aligned, so shorter forms work too: 'SS', 'MM:SS',
    'HH:MM:SS', 'DD:HH:MM:SS'. e.g. '02:00:00' -> 7200 (2 hours ago).
    Raises ValueError on malformed input.
    """
    parts = str(spec).split(":")
    if not 1 <= len(parts) <= 4:
        raise ValueError(f"--from expects up to DD:HH:MM:SS, got {spec!r}")
    try:
        nums = [int(p) for p in parts]
    except ValueError:
        raise ValueError(f"--from fields must be integers, got {spec!r}")
    if any(n < 0 for n in nums):
        raise ValueError(f"--from fields must be non-negative, got {spec!r}")
    mult = [1, 60, 3600, 86400]  # seconds, minutes, hours, days (reversed order)
    return sum(n * mult[i] for i, n in enumerate(reversed(nums)))


def _ts_epoch(line):
    """Extract a leading RFC3339 UTC timestamp from a (possibly ANSI-wrapped) log
    line and return it as an epoch float. None when the line has no timestamp.

    Matches the backend's tracing format, e.g. `2026-06-03T03:16:11.686512Z`.
    """
    import re
    import datetime
    clean = re.sub(r"\x1b\[[0-9;]*m", "", line)
    m = re.match(r"\s*(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(\.\d+)?Z", clean)
    if not m:
        return None
    base, frac = m.group(1), (m.group(2) or "")
    frac = frac[:7]  # %f accepts at most 6 fractional digits (keep the dot + 6)
    fmt = "%Y-%m-%dT%H:%M:%S.%f" if frac else "%Y-%m-%dT%H:%M:%S"
    try:
        dt = datetime.datetime.strptime(base + frac, fmt)
    except ValueError:
        return None
    return dt.replace(tzinfo=datetime.timezone.utc).timestamp()


def _lines_since(lines, cutoff_epoch):
    """Return (filtered_lines, matched). Once a line at/after cutoff_epoch is
    seen, that line and every line after it (including untimestamped
    continuation lines) is included.
    """
    out, started = [], False
    for line in lines:
        if not started:
            ep = _ts_epoch(line)
            if ep is not None and ep >= cutoff_epoch:
                started = True
        if started:
            out.append(line)
    return out, started


def _newest_log(prefix, log_dir=None):
    """Newest `<prefix>.log` / `<prefix>.<timestamp>.log` in log_dir by mtime, or None."""
    log_dir = log_dir or get_log_dir() or os.path.join(os.getcwd(), "tmp")
    cands = glob.glob(os.path.join(log_dir, f"{prefix}.log"))
    cands += glob.glob(os.path.join(log_dir, f"{prefix}.*.log"))
    if not cands:
        return None
    return max(cands, key=os.path.getmtime)


def tail_dev_log(prefix="dev", since=None):
    """Trail the newest local `<prefix>` log file.

    since=None -> last 100 lines, then follow.
    since=<seconds> -> print lines from (now - since) forward (keyed off the
        RFC3339 timestamps the backend emits), then follow. If no timestamped
        line falls within the window, warn and follow new output only.
    """
    path = _newest_log(prefix)
    if not path:
        err(f"no {prefix} logs found in {get_log_dir() or 'tmp'}")
        return 1
    info(f"trailing {os.path.relpath(path)}")
    if since is None:
        return subprocess.call(["tail", "-n", "100", "-f", path])
    cutoff = time.time() - since
    with open(path, errors="replace") as f:
        lines = f.readlines()
    filtered, matched = _lines_since(lines, cutoff)
    if matched:
        sys.stdout.write("".join(filtered))
        sys.stdout.flush()
    else:
        warn(
            f"no timestamped lines within the last {since}s in "
            f"{os.path.basename(path)}; trailing new output only"
        )
    return subprocess.call(["tail", "-n", "0", "-f", path])


def logs_dispatch(args, image, host=None):
    """Route `./admin logs [dev|live] [--from DD:HH:MM:SS]`.

    No target -> a tiny dev|live picker (never a file list).
    `dev`  -> trail the newest local dev log file (tail_dev_log).
    `live` -> deployed container logs via docker logs (local or over SSH).

    `--from DD:HH:MM:SS` (relative offset) shows everything from that point
    forward and then keeps trailing, for both targets.
    """
    args = list(args or [])
    target = args[0] if args and args[0] in ("dev", "live") else None
    rest = args[1:] if target else args

    if target is None:
        menu = OrderedDict([
            ("dev", "dev   local dev server logs"),
            ("live", "live  deployed container logs"),
        ])
        target = pick_target(menu, "which logs?")
        if not target:
            return 0

    since = None
    if "--from" in rest:
        i = rest.index("--from")
        if i + 1 >= len(rest):
            err("--from requires a DD:HH:MM:SS value")
            return 1
        try:
            since = _parse_since_offset(rest[i + 1])
        except ValueError as e:
            err(str(e))
            return 1

    if target == "live":
        if host is None:
            user = os.environ.get("UNRAID_USER", "root")
            host = f"{user}@{os.environ.get('UNRAID_HOST')}"
        return remote_logs(host, image, since=since, follow=True)
    return tail_dev_log(since=since)


def register_logs_command(host, image):
    """Register a 'logs' command that tails remote Docker container logs."""

    @command("logs", f"Tail {image} container logs on remote")
    def cmd_logs(args):
        return run_cmd(f"ssh {host} docker logs -f --tail 100 {image}")


# ---------------------------------------------------------------------------
# Multi-target reload command
# ---------------------------------------------------------------------------

def reload_running_targets(target_filter=None, pid_prefix=None):
    """Send SIGUSR1 to every admin-managed dev target (or one matching `target_filter`).

    Looks at /tmp/admin-run-*.pid (overrideable), reads each pid, and
    signals it. Stale pidfiles (missing process) are unlinked. Returns 0 if
    at least one signal was delivered, 1 otherwise.

    The shape used by the `server-reload` action kind. Lives at module level
    so the kind fragment can call it directly without instantiating the
    closure-style helper below.
    """
    prefix = pid_prefix or _ADMIN_PID_PREFIX
    pid_files = glob.glob(f"{prefix}-*.pid")

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
            cleanup_pid_file(pidfile)
            continue

        try:
            os.kill(pid, 0)
        except OSError:
            warn(f"PID {pid} ({target_name}) is not running — removing stale file")
            cleanup_pid_file(pidfile)
            continue

        os.kill(pid, signal.SIGUSR1)
        ok(f"Reload signal sent to {target_name} (PID {pid})")
        sent += 1

    if sent == 0:
        err("No live targets found")
        return 1
    return 0


def register_server_reload_command(pid_prefix=None):
    """Register a reload command that sends SIGUSR1 to all running targets."""
    @command("reload", "Signal running admin to rebuild+relaunch")
    def cmd_reload(args):
        return reload_running_targets(args[0] if args else None, pid_prefix=pid_prefix)


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

        target_urls = urls if urls is not None else get_urls()
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
