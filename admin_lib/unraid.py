"""Layer 4 — Unraid plugin + docker template deployment helpers.

Provides: plugin_deploy/plugin_remove for .plg plugin distribution, and
install_docker_template_from_config for registering a Docker container
template in the Unraid Docker GUI.
"""

import os
import time

from .core import info, ok, warn, err, run_cmd
from .remote import ssh_cmd, scp_to, _is_local_host


# Where Unraid's Docker GUI scans for user-defined container templates. Files
# matching the `my-*.xml` pattern under this dir show up under the "Add
# Container" picker on the host's webui. We scp into here so a project can be
# spun up from the GUI without anyone hand-editing the XML on the server.
_UNRAID_TEMPLATES_DIR = "/boot/config/plugins/dockerMan/templates-user"


def _slug_from_app_image():
    """Strip a registry prefix and `:tag` from APP_IMAGE → bare slug.

    `ghcr.io/me/my-app:latest` → `my-app`. Same logic the docker-unraid
    `logs` action uses to pick a container name.
    """
    image = os.environ.get("APP_IMAGE", "").strip()
    if not image:
        return ""
    return image.split(":", 1)[0].rsplit("/", 1)[-1]


def ensure_docker_template(cfg):
    """Auto-install the Unraid Docker GUI template on first deploy if needed.

    No-op when:
    - The local template XML doesn't exist (nothing to install)
    - UNRAID_HOST isn't configured
    - The template is already present on the host

    Non-fatal: warns on scp failure so a bad template never blocks a deploy.
    """
    cfg = cfg or {}
    src = cfg.get("src") or "deploy/unraid-template.xml"
    if not os.path.exists(src):
        return

    name = (cfg.get("name") or _slug_from_app_image()).strip()
    if not name:
        warn("ensure-template: cannot determine template name — set APP_IMAGE or [install_template].name")
        return

    host = os.environ.get("UNRAID_HOST", "").strip()
    if not host:
        return
    user = os.environ.get("UNRAID_USER", "root").strip() or "root"
    full_host = f"{user}@{host}"

    dst = f"{_UNRAID_TEMPLATES_DIR}/my-{name}.xml"
    if ssh_cmd(full_host, f"test -f {dst}", check=False) == 0:
        return

    info(f"First deploy: installing Unraid template → {full_host}:{dst}")
    rc = scp_to(src, full_host, dst)
    if rc == 0:
        ok(f"Template installed as my-{name}.xml in Unraid Docker GUI")
    else:
        warn("Template install failed (non-fatal) — run: ./admin install-template")


def install_docker_template_from_config(cfg):
    """Install a Docker container template into Unraid's GUI via scp.

    `[install_template]` config keys (all optional):
        src   path to the local XML file. Default: deploy/unraid-template.xml
        name  template basename without the `my-` prefix or `.xml` suffix.
              Default: derived from APP_IMAGE (e.g. `web-file-manager`).

    Reads UNRAID_HOST (required) and UNRAID_USER (default `root`) from env.
    Returns 0 on success, non-zero on validation/scp failure.
    """
    cfg = cfg or {}
    src = cfg.get("src") or "deploy/unraid-template.xml"
    if not os.path.exists(src):
        err(f"install-template: source XML not found: {src}")
        return 1

    name = (cfg.get("name") or _slug_from_app_image()).strip()
    if not name:
        err("install-template: cannot determine template name — set APP_IMAGE or [install_template].name")
        return 1

    host = os.environ.get("UNRAID_HOST", "").strip()
    if not host:
        err("install-template: UNRAID_HOST is not set")
        return 1
    user = os.environ.get("UNRAID_USER", "root").strip() or "root"

    dst = f"{_UNRAID_TEMPLATES_DIR}/my-{name}.xml"
    full_host = f"{user}@{host}"
    info(f"Installing template: {src} → {full_host}:{dst}")
    rc = scp_to(src, full_host, dst)
    if rc == 0:
        ok(f"Template installed. Open the Unraid Docker GUI → Add Container → Template = my-{name}")
    return rc


def plugin_deploy(host, plg_path, plg_template, local_src, remote_dest,
                  chmod_dirs=None):
    """Deploy an Unraid plugin: register .plg if missing, then rsync source files.

    plg_template's TIMESTAMP placeholder is filled at deploy time. chmod_dirs
    get `chmod +x` applied to their contents. Returns rc.
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
    dest = remote_dest if _is_local_host(host) else f"{host}:{remote_dest}"
    rc = run_cmd(f"rsync -avz --delete {local_src} {dest}")
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
