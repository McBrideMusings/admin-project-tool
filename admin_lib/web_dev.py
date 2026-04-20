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
import signal
import subprocess

from .core import (
    LOG_DIR,
    info, ok, warn, err, register_cleanup,
)
from .docker import ensure_deps, run_with_reload
from .ports import preflight_port


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
        if "/" in name or name in (".", ".."):
            err(f"DEV_MOUNTS name must be a simple identifier: {name!r}")
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


def run_vite_rust_dev(
    vite_port,
    backend_port,
    data_dir,
    frontend_dir,
    backend_dir,
    backend_cmd,
    backend_env=None,
    dev_mounts_raw=None,
    vite_log_name="vite.log",
):
    """Launch a Vite dev server + a cargo/rust backend with a reload loop.

    Used by projects whose dev flow is "vite frontend in the background +
    backend with SIGUSR1-driven reload in the foreground." Replaces the per-
    project admin_inline.py pattern of the same shape.

    Arguments:
        vite_port:        int, the Vite port (killed on startup).
        backend_port:     int, the backend port (killed on startup).
        data_dir:         path, created if missing; receives DEV_MOUNTS symlinks.
        frontend_dir:     path containing `package.json` + `npm run dev` target.
        backend_dir:      path to `cd` into before running backend_cmd.
        backend_cmd:      shell string for the reload-looped backend process.
        backend_env:      optional dict[str, str] prepended as `K=V` env assignments.
        vite_log_name:    filename under LOG_DIR for vite stdout+stderr.

    Reads the `DEV_MOUNTS` env var at call time. Returns the return code of
    `run_with_reload`.
    """
    rc = ensure_deps(os.path.join(frontend_dir, "node_modules"),
                      f"cd {frontend_dir} && npm install")
    if rc != 0:
        err("npm install failed")
        return rc

    os.makedirs(data_dir, exist_ok=True)

    mounts = parse_dev_mounts(dev_mounts_raw if dev_mounts_raw is not None
                              else os.environ.get("DEV_MOUNTS"))
    rc = setup_dev_mounts(data_dir, mounts)
    if rc != 0:
        return rc

    if not preflight_port(vite_port, label="vite"):
        return 1
    if not preflight_port(backend_port, label="backend"):
        return 1

    vite_log = os.path.join(LOG_DIR, vite_log_name)
    os.makedirs(LOG_DIR, exist_ok=True)
    vite_fh = open(vite_log, "w")
    vite_proc = subprocess.Popen(
        f"cd {frontend_dir} && npm run dev",
        shell=True,
        stdin=subprocess.DEVNULL,
        stdout=vite_fh, stderr=subprocess.STDOUT,
        start_new_session=True,
    )

    def _stop_vite():
        try:
            os.killpg(os.getpgid(vite_proc.pid), signal.SIGTERM)
        except (ProcessLookupError, PermissionError, OSError):
            pass
        finally:
            vite_fh.close()
    register_cleanup(_stop_vite)

    dev_url = os.environ.get("DEV_URL", f"http://localhost:{vite_port}").strip()
    ok(f"Vite dev server: {dev_url}")
    info(f"Vite log: {vite_log}")
    print()

    allowed_roots = ",".join(os.path.realpath(p) for _, p in (mounts or []))

    env_parts = []
    for k, v in (backend_env or {}).items():
        env_parts.append(f"{k}={v}")
    env_parts.append(f"PORT={backend_port}")
    if allowed_roots:
        env_parts.append(f"ALLOWED_SYMLINK_ROOTS={allowed_roots}")
    env = " ".join(env_parts)

    return run_with_reload(
        f"cd {backend_dir} && {env} {backend_cmd}",
        "dev",
    )
