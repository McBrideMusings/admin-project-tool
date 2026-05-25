"""'hugo' archetype — Hugo static site with dev server.

Default URL points at the Hugo dev server's standard port. Override
[urls.dev] in admin.toml if a project uses a different port.

Runtime env var:
    HUGO_SOURCE   path to the Hugo project root (default "."). Set this when
                  the site lives in a subdirectory of the repo (e.g. a
                  monorepo with the Hugo project under site/). It is passed to
                  `hugo -s` and used to locate build artifacts for `clean`.
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
        "build": {"kind": "shell", "run": "hugo --minify -s ${HUGO_SOURCE:-.}",                   "desc": "Build production site"},
        "dev":   {"kind": "shell", "run": "hugo server -D --bind 0.0.0.0 -s ${HUGO_SOURCE:-.}",   "desc": "Run Hugo dev server with drafts"},
        "clean": {"kind": "shell", "run": "rm -rf ${HUGO_SOURCE:-.}/public ${HUGO_SOURCE:-.}/resources", "desc": "Remove build artifacts"},
    },
}
