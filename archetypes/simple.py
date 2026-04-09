"""'simple' archetype — minimal build/test/clean shell strings.

Successor to v1's basic.py template. Used as the fallback when no other
archetype detects, and as a seed for projects that just need a couple of
project-specific shell commands stitched into the admin harness.
"""

ARCHETYPE = {
    "name": "simple",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "build": {
            "run": "echo 'TODO: set [commands.build.run] in admin.toml'",
            "desc": "Build the project",
        },
        "test": {
            "run": "echo 'TODO: set [commands.test.run] in admin.toml'",
            "desc": "Run tests",
        },
        "clean": {
            "run": "echo 'TODO: set [commands.clean.run] in admin.toml'",
            "desc": "Remove build artifacts",
        },
    },
}
