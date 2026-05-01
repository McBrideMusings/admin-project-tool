"""'unraid-plugin' archetype — Unraid plugin pushed over SSH to a running Unraid host.

Runtime env vars:
    UNRAID_HOST    hostname or IP of the Unraid server
    UNRAID_USER    SSH user (default: root)
    PLUGIN_NAME    plugin directory name under /boot/config/plugins/
"""

ARCHETYPE = {
    "name": "unraid-plugin",
    "modules": ["core", "remote", "unraid"],
    "urls": {},
    "commands": {
        "build":     {"desc": "Build plugin (no-op by default)",          "steps": ["build"]},
        "deploy":    {"desc": "Push plugin source to Unraid USB",         "steps": ["deploy"]},
        "register":  {"desc": "Register/install the plugin on Unraid",   "steps": ["register"]},
        "uninstall": {"desc": "Uninstall the plugin from Unraid",        "steps": ["uninstall"]},
    },
    "actions": {
        "build": {
            "kind": "shell",
            "run": "echo 'unraid-plugin: no build step (source files are pushed directly)'",
            "desc": "Build plugin (no-op by default)",
        },
        "deploy": {
            "kind": "shell",
            "run": "rsync -av --delete ./ ${UNRAID_USER:-root}@${UNRAID_HOST}:/boot/config/plugins/${PLUGIN_NAME}/",
            "desc": "Push plugin source to Unraid USB",
        },
        "register": {
            "kind": "shell",
            "run": "ssh ${UNRAID_USER:-root}@${UNRAID_HOST} 'installplg /boot/config/plugins/${PLUGIN_NAME}/${PLUGIN_NAME}.plg'",
            "desc": "Register/install the plugin on Unraid",
        },
        "uninstall": {
            "kind": "shell",
            "run": "ssh ${UNRAID_USER:-root}@${UNRAID_HOST} 'removeplg ${PLUGIN_NAME}'",
            "desc": "Uninstall the plugin from Unraid",
        },
    },
}
