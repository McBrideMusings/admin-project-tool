"""Layer 4 — SSH, rsync, and systemd helpers for remote host management.

Provides: ssh_cmd, require_host, scp_to, systemd unit management.
"""

import os
import subprocess
import sys

from .core import info, ok, warn, err, run_cmd, resolve_env


def _is_local_host(host):
    """Return True if host (user@addr or bare addr) is the current machine.

    Primary check: ADMIN_LOCAL env var (set by per-command env injection or
    manually in .env as DEPLOY_LOCAL=true → ADMIN_LOCAL=true).
    Secondary: IP comparison via hostname -I (works when running directly on host).
    """
    if os.environ.get("ADMIN_LOCAL", "").lower() in ("1", "true", "yes"):
        return True
    import socket
    addr = host.split("@", 1)[-1]
    if addr in ("localhost", "127.0.0.1", "::1"):
        return True
    try:
        resolved_ips = {i[4][0] for i in socket.getaddrinfo(addr, None)}
    except socket.gaierror:
        return False
    if resolved_ips & {"127.0.0.1", "::1"}:
        return True
    try:
        r = subprocess.run(["hostname", "-I"], capture_output=True, text=True, timeout=2)
        if r.returncode == 0 and resolved_ips & set(r.stdout.split()):
            return True
    except Exception:
        pass
    return False


def require_host(env_var, label=None):
    """Read a host from an environment variable, or exit with a helpful message.

    Returns the host string (e.g. 'root@myserver').
    """
    host = os.environ.get(env_var, "")
    if not host:
        name = label or env_var
        err(f"{env_var} not set")
        print(f"\n  Set {env_var} to your {name} host, e.g.:")
        print(f"    {env_var}=root@myserver ./admin <command>\n")
        sys.exit(1)
    return host


def ssh_cmd(host, cmd, check=True):
    """Run a command on host, locally or via SSH. Returns exit code."""
    if _is_local_host(host):
        rc = subprocess.call(cmd, shell=True)
    else:
        rc = subprocess.call(["ssh", host, cmd])
    if check and rc != 0:
        err(f"Command failed (exit {rc})")
    return rc


def scp_to(src, host, dest):
    """Copy a local file to host, locally or via scp. Returns exit code."""
    import shutil
    if _is_local_host(host):
        info(f"Copying {os.path.basename(src)} to {dest}...")
        try:
            os.makedirs(os.path.dirname(dest) or ".", exist_ok=True)
            shutil.copy2(src, dest)
            ok(f"Copied to {dest}")
            return 0
        except OSError as e:
            err(f"Copy failed: {e}")
            return 1
    info(f"Copying {os.path.basename(src)} to {host}:{dest}...")
    rc = subprocess.call(["scp", src, f"{host}:{dest}"])
    if rc == 0:
        ok(f"Copied to {host}:{dest}")
    else:
        err("scp failed")
    return rc


def make_systemd_unit(description, exec_start, after="network.target",
                      restart="on-failure", env=None):
    """Generate a systemd unit file string.

    env: dict of environment variables to set, or None.
    """
    lines = [
        "[Unit]",
        f"Description={description}",
        f"After={after}",
        "",
        "[Service]",
        f"ExecStart={exec_start}",
        f"Restart={restart}",
    ]
    if env:
        for k, v in env.items():
            lines.append(f"Environment={k}={v}")
    lines.extend([
        "",
        "[Install]",
        "WantedBy=default.target",
    ])
    return "\n".join(lines) + "\n"


def systemd_user_install(host, name, unit_content):
    """Install and start a systemd user service on a remote host. Returns exit code."""
    import tempfile

    info(f"Installing systemd user service: {name}...")
    with tempfile.NamedTemporaryFile(mode="w", suffix=".service", delete=False) as f:
        f.write(unit_content)
        tmp_path = f.name

    try:
        rc = scp_to(tmp_path, host, f".config/systemd/user/{name}.service")
        if rc != 0:
            return rc
    finally:
        os.unlink(tmp_path)

    rc = ssh_cmd(host,
        f"mkdir -p ~/.config/systemd/user"
        f" && systemctl --user daemon-reload"
        f" && systemctl --user enable --now {name}",
    )
    if rc == 0:
        ok(f"Service {name} installed and started")
    return rc


def systemd_user_status(host, name):
    """Show status of a systemd user service on a remote host."""
    info(f"Status of {name} on {host}:")
    ssh_cmd(host, f"systemctl --user status {name}", check=False)


# ---------------------------------------------------------------------------
# [deploy] — push a local mirror directory to the host that runs the app
# ---------------------------------------------------------------------------

from .kinds import DEPLOY_KEYS as _DEPLOY_KEYS


def deploy_files(cfg, dry_run=False):
    """Push the local mirror directory declared in ``[deploy]`` to its target.

    The manifest table names a directory in the repo that mirrors, file for
    file, a directory on the machine that runs the app — an appdata folder, a
    config dir, a static-asset root. Editing the local copy and running the
    deploy command is then the whole update path: no image rebuild, no GUI.

        [deploy]
        src     = "deploy/appdata"                    # repo-relative, required
        dest    = "/mnt/user/appdata/my-app"          # path on the target, required
        host    = "${UNRAID_USER:-root}@${UNRAID_HOST}"   # omit for a local copy
        exclude = ["*.log"]                           # rsync --exclude patterns
        delete  = false                               # default false — see below
        chown   = "${APP_UID:-99}:${APP_GID:-100}"    # UID:GID — see below

    ``delete`` defaults to **false** here, the opposite of :func:`core.sync`.
    A mirror directory usually points at a folder the running app also writes
    into (a history file, a database, logs). Mirroring exactly would delete
    that state on every deploy, so the safe default is additive; set
    ``delete = true`` when the local copy genuinely owns the whole directory.

    ``chown`` is the "UID:GID" the target should own the synced files as,
    handed to rsync as ``--chown``; :func:`core.sync` documents what goes wrong
    without it. It has no default here — an archetype that also knows the uid
    the app runs as fills it in first (see
    :func:`deploy_cfg_with_chown_default`); ``deploy_files`` only reads it.

    Returns an exit code.
    """
    from .core import sync

    cfg = cfg or {}
    unknown = set(cfg) - _DEPLOY_KEYS
    if unknown:
        err(f"[deploy]: unknown key(s): {', '.join(sorted(unknown))}")
        return 1

    src = cfg.get("src")
    dest = cfg.get("dest")
    if not src or not dest:
        err("[deploy] needs both 'src' (local dir) and 'dest' (path on the target)")
        return 1

    src = resolve_env(str(src))
    dest = resolve_env(str(dest))
    if not os.path.isdir(src):
        err(f"[deploy] src is not a directory: {src}")
        return 1

    host = cfg.get("host")
    if host:
        host = resolve_env(str(host))
        target = dest if _is_local_host(host) else f"{host}:{dest}"
    else:
        target = dest

    excludes = [resolve_env(str(e)) for e in (cfg.get("exclude") or [])]

    chown = cfg.get("chown")
    if chown:
        chown = resolve_env(str(chown))

    # `post_sync` runs where the files landed — over SSH for a remote target,
    # locally when the target is this machine — because the thing it exists for
    # is fixing up what the sync just wrote.
    #
    # The common case is ownership. rsync carries the sending machine's uid/gid
    # onto directories it creates, and a container running as a fixed uid can
    # then read but not write them; the app dies on first write and, under a
    # restart policy, keeps dying. `post_sync = "chown -R 99:100 /path"` is the
    # fix, and it has to run after the sync and before anything reads the result.
    #
    # This key was accepted by the manifest validator for some time before
    # anything consumed it, so a manifest could declare it, pass `admin check`,
    # and silently not run it — a worse failure than rejecting the key, because
    # the config reads as though the problem is handled.
    post_sync_cmd = cfg.get("post_sync")
    post_sync = None
    if post_sync_cmd:
        post_sync_cmd = resolve_env(str(post_sync_cmd))

        def post_sync(_dest, _cmd=post_sync_cmd, _host=host):
            if dry_run:
                info(f"Would run post_sync: {_cmd}")
                return
            info(f"post_sync: {_cmd}")
            rc = ssh_cmd(_host, _cmd) if _host else subprocess.call(_cmd, shell=True)
            if rc != 0:
                # Loud, but not fatal to the sync: the files are already there,
                # and reporting success here would hide a half-finished deploy.
                err(f"post_sync failed (exit {rc}): {_cmd}")

    return sync(
        src, target,
        excludes=excludes or None,
        delete=bool(cfg.get("delete", False)),
        dry_run=dry_run,
        post_sync=post_sync,
        chown=chown,
    )


def deploy_cfg_with_chown_default(deploy_cfg, user):
    """Return ``deploy_cfg`` with ``chown`` defaulted to ``user`` (or None).

    The uid a deployed directory has to be writable by is exactly the uid the
    app already declares it runs as, so a project that states one shouldn't
    have to type it twice. An explicit ``[deploy].chown`` always wins. Called
    from the docker archetype's "deploy" action, which reads the uid off
    ``[docker_run].user``.
    """
    if deploy_cfg and user and not deploy_cfg.get("chown"):
        return dict(deploy_cfg, chown=user)
    return deploy_cfg or None
