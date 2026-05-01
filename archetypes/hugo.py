"""'hugo' archetype — Hugo static site with dev server.

Default URL points at the Hugo dev server's standard port. Override
[urls.dev] in admin.toml if a project uses a different port.
"""

ARCHETYPE = {
    "name": "hugo",
    "modules": ["core"],
    "urls": {
        "dev": "http://localhost:1313",
    },
    "commands": {
        "build": {"desc": "Build production site",                      "steps": ["build"]},
        "dev":   {"desc": "Run Hugo dev server with drafts",            "steps": ["dev"]},
        "clean": {"desc": "Remove build artifacts",                     "steps": ["clean"]},
    },
    "actions": {
        "build": {"kind": "shell", "run": "hugo --minify",                      "desc": "Build production site"},
        "dev":   {"kind": "shell", "run": "hugo server -D --bind 0.0.0.0",      "desc": "Run Hugo dev server with drafts"},
        "clean": {"kind": "shell", "run": "rm -rf public resources",            "desc": "Remove build artifacts"},
    },
}
