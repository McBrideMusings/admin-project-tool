"""Layer 4 — SSH, rsync, and systemd helpers for remote host management.

Provides: ssh_cmd, require_host, scp_to, systemd unit management.
"""

import os
import subprocess
import sys

from .core import info, ok, warn, err, run_cmd


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
