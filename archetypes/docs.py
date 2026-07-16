"""'docs' archetype — a VitePress docs site served with hot reload.

Mix into any project that ships docs:

    archetypes = ["go-cli", "docs"]

By default it runs the `docs:dev` script from the project root. A site with its
own package.json under docs/ points at it:

    [docs]
    dir = "docs"

bun vs npm is decided by the lockfile next to that package.json, so a bun docs
site inside an npm project (or the reverse) needs no extra config.
"""

ARCHETYPE = {
    "name": "docs",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "docs": {"desc": "Serve the docs site with hot reload", "steps": ["docs"], "group": 2, "priority": 30},
    },
    "actions": {
        "docs": {"kind": "docs", "desc": "Serve the docs site with hot reload"},
    },
}
