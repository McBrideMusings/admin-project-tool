"""'cloudflare-workers' archetype — a Cloudflare Worker managed with wrangler.

Provides the standard Worker lifecycle via the wrangler CLI (invoked through
`npx` so a project-local devDependency resolves without a global install):

    dev            wrangler dev          local dev server
    deploy         wrangler deploy       ship the Worker + any static assets
    migrate        wrangler d1 migrations apply ${D1_DATABASE_NAME} --remote
    migrate-local  same, against the local dev D1
    tail           wrangler tail         stream live production logs

Runtime env vars:
    D1_DATABASE_NAME   the D1 database/binding name (only needed for migrate*)
    PROD_URL           public URL for TUI url status (optional)

The migrate commands assume a D1 database; a Worker without D1 can simply
ignore them. Projects with a non-standard layout (static site built into a
subdir, an export step before deploy, a dev server that also runs another
process) should override `build`/`dev`/`deploy` in their admin.toml — this
archetype owns the plain wrangler lifecycle and composes with others.
"""

ARCHETYPE = {
    "name": "cloudflare-workers",
    "modules": ["core"],
    "urls": {
        "dev":  "http://localhost:8787",
        "prod": "${PROD_URL:-http://localhost:8787}",
    },
    "commands": {
        "dev":           {"desc": "Run the Worker locally (wrangler dev)",            "steps": ["worker-dev"],     "group": 1, "priority": 20},
        "deploy":        {"desc": "Deploy the Worker + assets (wrangler deploy)",      "steps": ["worker-deploy"],  "group": 1, "priority": 30},
        "migrate":       {"desc": "Apply D1 migrations (remote)",                      "steps": ["d1-migrate"],     "group": 2, "priority": 10},
        "migrate-local": {"desc": "Apply D1 migrations against the local dev D1",      "steps": ["d1-migrate-local"], "group": 2, "priority": 20},
        "tail":          {"desc": "Stream live production logs (wrangler tail)",       "steps": ["worker-tail"],    "group": 2, "priority": 30},
    },
    "actions": {
        "worker-dev":        {"kind": "shell", "run": "npx wrangler dev",                                                  "desc": "Run the Worker locally (wrangler dev)"},
        "worker-deploy":     {"kind": "shell", "run": "npx wrangler deploy",                                               "desc": "Deploy the Worker + assets (wrangler deploy)"},
        "d1-migrate":        {"kind": "shell", "run": "npx wrangler d1 migrations apply ${D1_DATABASE_NAME} --remote",     "desc": "Apply D1 migrations (remote)"},
        "d1-migrate-local":  {"kind": "shell", "run": "npx wrangler d1 migrations apply ${D1_DATABASE_NAME} --local",      "desc": "Apply D1 migrations against the local dev D1"},
        "worker-tail":       {"kind": "shell", "run": "npx wrangler tail",                                                 "desc": "Stream live production logs (wrangler tail)"},
    },
}
