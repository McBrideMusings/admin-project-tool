"""'stash-plugin' archetype — Stash plugin built with rollup and synced to the plugin dir.

Runtime env vars:
    STASH_PLUGIN_DIR   target directory on the Stash host (default: ~/.stash/plugins/plugin)
"""

ARCHETYPE = {
    "name": "stash-plugin",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "build":  {"desc": "Bundle the plugin with rollup",              "steps": ["build"]},
        "dev":    {"desc": "Watch and rebuild on source changes",         "steps": ["dev"]},
        "deploy": {"desc": "Sync built plugin to the Stash plugin directory", "steps": ["deploy"]},
        "clean":  {"desc": "Remove build artifacts",                     "steps": ["clean"]},
    },
    "actions": {
        "build":  {"kind": "shell", "run": "npx rollup -c",                                                              "desc": "Bundle the plugin with rollup"},
        "dev":    {"kind": "shell", "run": "npx rollup -c -w",                                                           "desc": "Watch and rebuild on source changes"},
        "deploy": {"kind": "shell", "run": "rsync -av --delete dist/ ${STASH_PLUGIN_DIR:-$HOME/.stash/plugins/plugin}/", "desc": "Sync built plugin to the Stash plugin directory"},
        "clean":  {"kind": "shell", "run": "rm -rf dist",                                                                "desc": "Remove build artifacts"},
    },
}
