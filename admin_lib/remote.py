"""Layer 4 — SSH, rsync, and systemd helpers for remote host management.

Provides: ssh_cmd, require_host, scp_to, systemd unit management.
"""

import os
import subprocess
import sys

from .core import info, ok, warn, err, run_cmd


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
    """Run a command on a remote host via SSH. Returns exit code."""
    rc = subprocess.call(["ssh", host, cmd])
    if check and rc != 0:
        err(f"SSH command failed (exit {rc})")
    return rc


def scp_to(src, host, dest):
    """Copy a local file to a remote host via scp. Returns exit code."""
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
