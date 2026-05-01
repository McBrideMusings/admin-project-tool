"""'simple' archetype — minimal build/test/clean shell strings.

Used as the fallback when no other archetype detects, and as a seed for
projects that just need a couple of shell commands stitched into the admin harness.
"""

ARCHETYPE = {
    "name": "simple",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "build": {"desc": "Build the project", "steps": ["build"]},
        "test":  {"desc": "Run tests",          "steps": ["test"]},
        "clean": {"desc": "Remove build artifacts", "steps": ["clean"]},
    },
    "actions": {
        "build": {"kind": "shell", "run": "echo 'TODO: set run in admin.toml [actions.build]'", "desc": "Build the project"},
        "test":  {"kind": "shell", "run": "echo 'TODO: set run in admin.toml [actions.test]'",  "desc": "Run tests"},
        "clean": {"kind": "shell", "run": "echo 'TODO: set run in admin.toml [actions.clean]'", "desc": "Remove build artifacts"},
    },
}
