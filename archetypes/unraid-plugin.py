"""'unraid-plugin' archetype — Unraid plugin pushed over SSH to a running Unraid host.

Runtime env vars:
    UNRAID_HOST    hostname or IP of the Unraid server
    UNRAID_USER    SSH user (default: root)
    PLUGIN_NAME    plugin directory name under /boot/config/plugins/
    DEPLOY_LOCAL   set to true when running from the Unraid host itself (skips SSH)
"""

ARCHETYPE = {
    "name": "unraid-plugin",
    "modules": ["core", "remote", "unraid"],
    "urls": {},
    "commands": {
        "build":     {"desc": "Build plugin (no-op by default)",         "steps": ["build"]},
        "deploy":    {"desc": "Push plugin source to Unraid USB",        "steps": ["deploy"],    "env": {"ADMIN_LOCAL": "${DEPLOY_LOCAL:-false}"}},
        "register":  {"desc": "Register/install the plugin on Unraid",  "steps": ["register"],  "env": {"ADMIN_LOCAL": "${DEPLOY_LOCAL:-false}"}},
        "uninstall": {"desc": "Uninstall the plugin from Unraid",       "steps": ["uninstall"], "env": {"ADMIN_LOCAL": "${DEPLOY_LOCAL:-false}"}},
    },
    "actions": {
        "build": {
            "kind": "shell",
            "run": "echo 'unraid-plugin: no build step (source files are pushed directly)'",
            "desc": "Build plugin (no-op by default)",
        },
        "deploy": {
            "kind": "python",
            "run": (
                "user = os.environ.get('UNRAID_USER', 'root')\n"
                "host = f\"{user}@{os.environ.get('UNRAID_HOST')}\"\n"
                "plugin = os.environ.get('PLUGIN_NAME')\n"
                "dest = f'/boot/config/plugins/{plugin}'\n"
                "dest_arg = dest if _is_local_host(host) else f'{host}:{dest}'\n"
                "sys.exit(run_cmd(f'rsync -av --delete ./ {dest_arg}/'))"
            ),
            "desc": "Push plugin source to Unraid USB",
        },
        "register": {
            "kind": "python",
            "run": (
                "user = os.environ.get('UNRAID_USER', 'root')\n"
                "host = f\"{user}@{os.environ.get('UNRAID_HOST')}\"\n"
                "plugin = os.environ.get('PLUGIN_NAME')\n"
                "sys.exit(ssh_cmd(host, f'installplg /boot/config/plugins/{plugin}/{plugin}.plg'))"
            ),
            "desc": "Register/install the plugin on Unraid",
        },
        "uninstall": {
            "kind": "python",
            "run": (
                "user = os.environ.get('UNRAID_USER', 'root')\n"
                "host = f\"{user}@{os.environ.get('UNRAID_HOST')}\"\n"
                "plugin = os.environ.get('PLUGIN_NAME')\n"
                "sys.exit(ssh_cmd(host, f'removeplg {plugin}'))"
            ),
            "desc": "Uninstall the plugin from Unraid",
        },
    },
}
