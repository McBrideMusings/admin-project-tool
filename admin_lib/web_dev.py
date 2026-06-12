"""Web-dev launch helpers for mixed frontend+backend stacks.

Concrete helper functions that project `admin.toml` `[commands]` entries
(kind = "python") call with their own ports, paths, and env. Each function
is the top-level body of what used to live in a per-project admin_inline.py;
moving them here is how we keep the declarative manifest as the source of
truth.

Assumes a bundled-context scope (info/ok/err/warn/run_cmd/register_cleanup/
run_with_reload/ensure_deps all in scope after inlining). Import `from .core`
etc. is stripped by the bundler but kept here so the module remains
individually importable for tests and IDE tooling.
"""

import os
import shlex
import signal
import socket
import subprocess
import time

from .core import (
    get_log_dir,
    info, ok, warn, err, register_cleanup, resolve_env,
)
from .docker import ensure_deps, run_with_reload, _wait_or_kill, _preexec_pdeathsig
from .ports import preflight_port
from .ports_nuke import kill_stale_supervisors


def parse_dev_mounts(raw):
    """Parse a `DEV_MOUNTS`-style env value: `name=path,name=path` → [(name, path)].

    Returns None if any entry is malformed (missing `=`, empty name/path, or
    a name that isn't a simple identifier). Returns [] for empty/None input.
    The caller is expected to treat None as a refuse-to-start error and log it.
    """
    mounts = []
    for entry in (raw or "").split(","):
        entry = entry.strip()
        if not entry:
            continue
        if "=" not in entry:
            err(f"DEV_MOUNTS entry missing '=': {entry!r}")
            return None
        name, path = entry.split("=", 1)
        name = name.strip()
        path = os.path.expanduser(path.strip())
        if not name or not path:
            err(f"DEV_MOUNTS entry has empty name or path: {entry!r}")
            return None
        if name in (".", ".."):
            err(f"DEV_MOUNTS name must not be '.' or '..': {name!r}")
            return None
        mounts.append((name, path))
    return mounts


def setup_dev_mounts(data_dir, mounts):
    """Validate that every mount target exists, then ensure `data_dir/<name>`
    is a symlink to its target. Refuses to clobber non-symlinks. Returns 0 on
    success, 1 on any validation failure.
    """
    if mounts is None:
        return 1
    if not mounts:
        return 0

    missing = [(n, p) for n, p in mounts if not os.path.isdir(p)]
    if missing:
        err("DEV_MOUNTS targets unavailable — refusing to start:")
        for name, path in missing:
            err(f"  {name}: {path}")
        info("Reconnect the share(s) and retry, or unset DEV_MOUNTS.")
        return 1

    for name, target in mounts:
        link = os.path.join(data_dir, name)
        os.makedirs(os.path.dirname(link), exist_ok=True)
        if os.path.islink(link):
            if os.readlink(link) == target:
                continue
            os.unlink(link)
        elif os.path.exists(link):
            err(f"{link} exists and is not a symlink — refusing to overwrite")
            return 1
        os.symlink(target, link)
        ok(f"mount: {link} -> {target}")
    return 0


def _detect_pkg_manager(frontend_dir):
    """Return 'bun' if a bun lockfile exists in frontend_dir, else 'npm'."""
    for name in ("bun.lockb", "bun.lock"):
        if os.path.exists(os.path.join(frontend_dir, name)):
            return "bun"
    return "npm"


def _wait_for_port(port, timeout=15, interval=0.25):
    """Poll until a TCP port accepts connections or timeout expires."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.5):
                return True
        except OSError:
            time.sleep(interval)
    warn(f"Port {port} not ready after {timeout}s — continuing anyway")
    return False


def run_vite_dev(
    vite_port,
    backend_port,
    data_dir,
    frontend_dir,
    backend_dir,
    backend_cmd,
    backend_env=None,
    vite_env=None,
    dev_mounts_raw=None,
    vite_log_name="vite.log",
):
    """Launch Vite + a SIGUSR1-reloadable backend. Vite runs in the background;
    backend runs in the foreground inside `run_with_reload`. Reads `DEV_MOUNTS`
    env at call time to symlink into `data_dir`. Returns run_with_reload's rc.
    """
    pkg = _detect_pkg_manager(frontend_dir)
    rc = ensure_deps(os.path.join(frontend_dir, "node_modules"),
                      f"cd {shlex.quote(frontend_dir)} && {pkg} install")
    if rc != 0:
        err(f"{pkg} install failed")
        return rc

    os.makedirs(data_dir, exist_ok=True)

    mounts = parse_dev_mounts(dev_mounts_raw if dev_mounts_raw is not None
                              else os.environ.get("DEV_MOUNTS"))
    rc = setup_dev_mounts(data_dir, mounts)
    if rc != 0:
        return rc

    # Reap any orphaned supervisor processes (cargo-watch, nodemon, etc.)
    # left behind by a prior dev session that wasn't shut down cleanly.
    # Without this, the orphan keeps watching files and respawns the listener
    # the moment we put one back on the port.
    project_root = os.path.dirname(os.path.abspath(backend_dir)) or os.getcwd()
    kill_stale_supervisors(project_root)

    if not preflight_port(vite_port, label="vite", project_root=project_root):
        return 1
    if not preflight_port(backend_port, label="backend", project_root=project_root):
        return 1

    vite_log = os.path.join(get_log_dir(), vite_log_name)
    os.makedirs(get_log_dir(), exist_ok=True)
    vite_fh = open(vite_log, "w")
    merged_vite_env = {"VITE_PORT": str(vite_port), **(vite_env or {})}
    vite_env_parts = [f"{k}={shlex.quote(str(v))}" for k, v in merged_vite_env.items()]
    vite_env_str = " ".join(vite_env_parts)
    inner = f"{pkg} run dev"
    if vite_env_str:
        # Env prefix must apply to `npm run dev`, not to `cd`. Putting it
        # before the chain leaks the var only to `cd`, which ignores it.
        inner = f"{vite_env_str} {inner}"
    vite_cmd = f"cd {shlex.quote(frontend_dir)} && {inner}"
    vite_proc = subprocess.Popen(
        vite_cmd,
        shell=True,
        stdin=subprocess.DEVNULL,
        stdout=vite_fh, stderr=subprocess.STDOUT,
        start_new_session=True,
        preexec_fn=_preexec_pdeathsig,
    )

    def _stop_vite():
        try:
            os.killpg(os.getpgid(vite_proc.pid), signal.SIGTERM)
        except (ProcessLookupError, PermissionError, OSError):
            pass
        try:
            _wait_or_kill(vite_proc, timeout=3)
        except Exception:
            pass
        finally:
            vite_fh.close()
    register_cleanup(_stop_vite)

    ready = _wait_for_port(vite_port)

    domain = os.environ.get("DEV_DOMAIN", "").strip()
    dev_url = f"https://{vite_port}.{domain}" if domain else f"http://localhost:{vite_port}"
    if ready:
        ok(f"Vite dev server: {dev_url}")
    else:
        warn(f"Vite dev server (may not be ready): {dev_url}")
    info(f"Vite log: {vite_log}")
    print()

    allowed_roots = ",".join(os.path.realpath(p) for _, p in (mounts or []))

    env_parts = [f"{k}={shlex.quote(str(v))}" for k, v in (backend_env or {}).items()]
    env_parts.append(f"PORT={backend_port}")
    if allowed_roots:
        env_parts.append(f"ALLOWED_SYMLINK_ROOTS={allowed_roots}")
    env_str = " ".join(env_parts)

    # `exec env ...` so the shell replaces itself with the backend binary
    # (cargo-watch / nodemon / etc.) instead of fork-exec'ing it. Without
    # this, admin's PR_SET_PDEATHSIG only reaches the shell, which then
    # ignores SIGTERM unless we kill its whole process group — and on a
    # rough exit (terminal close, SIGKILL) admin doesn't get to do that,
    # leaving cargo-watch orphaned with the listener still running.
    # With `exec`, the supervisor IS the direct child of admin and the
    # kernel's parent-death signal kills it directly.
    return run_with_reload(
        f"cd {shlex.quote(backend_dir)} && exec env {env_str} {backend_cmd}",
        "dev",
    )


def vite_dev_from_config(cfg):
    """Adapter: read a manifest `[vite_dev]` table and call run_vite_dev.

    Schema keys: vite_port, backend_port, data_dir, data_subdirs (each with
    name/env/is_data_root?), frontend_dir, backend_dir, backend_cmd,
    forward_env, vite_env, mounts, backend_extra_env. See run_vite_dev for the
    underlying parameters; this adapter just translates the toml shape.
    """
    if not cfg:
        err("vite_dev_from_config: empty config")
        return 1

    project_data = os.path.abspath(cfg.get("data_dir", "data"))
    subdirs = list(cfg.get("data_subdirs") or [])
    backend_env = dict(cfg.get("backend_extra_env") or {})

    data_roots = [s for s in subdirs if s.get("is_data_root")]
    if len(data_roots) > 1:
        names = [s.get("name", "?") for s in data_roots]
        err(f"vite_dev: multiple data_subdirs have is_data_root=true: {names}")
        return 1

    data_root = None
    for i, sub in enumerate(subdirs):
        path = os.path.join(project_data, sub["name"])
        os.makedirs(path, exist_ok=True)
        env_name = sub.get("export_as") or sub.get("env")
        if env_name:
            backend_env[env_name] = path
        if sub.get("is_data_root") or (data_root is None and i == 0):
            data_root = path
    if data_root is None:
        os.makedirs(project_data, exist_ok=True)
        data_root = project_data

    # Convenience: expose the resolved data_root under a single env name
    # without requiring a no-op subdir entry. Common for backends that take
    # one ROOT_PATH-style variable and discover everything underneath it.
    root_env = cfg.get("data_root_env")
    if root_env:
        backend_env[root_env] = data_root

    for name in cfg.get("forward_env") or ():
        v = os.environ.get(name, "").strip()
        if v:
            backend_env[name] = v

    mount_parts = []
    for m in cfg.get("mounts") or ():
        name = m.get("name")
        if not name:
            continue
        if "path" in m:
            path = resolve_env(m["path"])
        else:
            path = os.environ.get(m.get("env", ""), resolve_env(m.get("default", "")))
        if path:
            mount_parts.append(f"{name}={path}")
    mounts_raw = ",".join(mount_parts) if mount_parts else None

    return run_vite_dev(
        vite_port=cfg["vite_port"],
        backend_port=cfg["backend_port"],
        data_dir=data_root,
        frontend_dir=cfg.get("frontend_dir", "web"),
        backend_dir=cfg.get("backend_dir", "."),
        backend_cmd=cfg.get("backend_cmd", "node server.js"),
        backend_env=backend_env,
        vite_env=cfg.get("vite_env") or {},
        dev_mounts_raw=mounts_raw,
    )
