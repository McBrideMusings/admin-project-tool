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
        "build": {
            "run": "hugo --minify",
            "desc": "Build production site",
        },
        "dev": {
            "run": "hugo server -D --bind 0.0.0.0",
            "desc": "Run Hugo dev server with drafts",
        },
        "clean": {
            "run": "rm -rf public resources",
            "desc": "Remove build artifacts",
        },
    },
}
