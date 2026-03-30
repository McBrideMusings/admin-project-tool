"""Layer 4 — Unraid plugin deployment helpers.

Provides: plugin_deploy, plugin_remove for Unraid .plg plugin development.
"""

import os
import subprocess
import time

from .core import info, ok, warn, err, run_cmd
from .remote import ssh_cmd


def plugin_deploy(host, plg_path, plg_template, local_src, remote_dest,
                  chmod_dirs=None):
    """Deploy an Unraid plugin: register .plg if needed, then rsync source files.

    host: SSH host (e.g. 'root@unraid')
    plg_path: remote path for .plg file (e.g. /boot/config/plugins/myplugin.plg)
    plg_template: .plg XML template string (TIMESTAMP placeholder gets replaced)
    local_src: local glob pattern for source files
    remote_dest: remote directory to rsync into
    chmod_dirs: list of remote dirs to chmod +x after sync
    Returns exit code.
    """
    rc = ssh_cmd(host, f"test -f {plg_path}", check=False)
    if rc != 0:
        info("Plugin not registered — installing .plg...")
        rendered = plg_template.replace("TIMESTAMP", str(int(time.time())))
        install_cmd = (
            f"cat > {plg_path} << 'PLGEOF'\n{rendered}\nPLGEOF"
        )
        rc = ssh_cmd(host, install_cmd, check=False)
        if rc != 0:
            err("Failed to write .plg file")
            return rc
        info("Running plugin install script...")
        rc = ssh_cmd(host, f"/usr/local/sbin/plugin install {plg_path}", check=False)
        if rc != 0:
            err("Plugin install failed")
            return rc
        ok("Plugin registered")

    info("Syncing source files...")
    rc = run_cmd(f"rsync -avz --delete {local_src} {host}:{remote_dest}")
    if rc != 0:
        err("rsync failed")
        return rc

    if chmod_dirs:
        for d in chmod_dirs:
            ssh_cmd(host, f"chmod +x {d}/* 2>/dev/null || true", check=False)

    ok("Deploy complete")
    return 0


def plugin_remove(host, plugin_name, plugin_dir):
    """Fully remove an Unraid plugin: uninstall, remove .plg, clean up.

    host: SSH host
    plugin_name: plugin name (used for .plg path and emhttp dir)
    plugin_dir: USB-persistent plugin directory to remove
    """
    plg = f"/boot/config/plugins/{plugin_name}.plg"

    info(f"Removing plugin {plugin_name}...")
    ssh_cmd(host, f"/usr/local/sbin/plugin remove {plg} 2>/dev/null || true",
            check=False)
    ssh_cmd(host,
        f"rm -f {plg}"
        f" && rm -rf /usr/local/emhttp/plugins/{plugin_name}"
        f" && rm -rf {plugin_dir}",
        check=False,
    )
    ok(f"Plugin {plugin_name} fully removed")
