#!/usr/bin/env python3
"""Print the Python archetype loader's resolved command/action/url set as JSON.

Used by internal/archetypeparity/parity_test.go (Go) to prove the Go loader
(internal/archetype) resolves each archetypes/<name>/archetype.toml directory
to the same command and action set that the Python loader
(admin_lib/archetypes.py) produces for archetypes/<name>.py.

This module only reads admin_lib.manifest / admin_lib.archetypes — it never
modifies them.

Usage:
    python3 tools/archetype_parity.py <archetype-name>

Prints one JSON object to stdout:

    {
        "urls": {label: url, ...},
        "commands": {
            name: {"desc": ..., "steps": [...], "group": N, "priority": N,
                   "env": {...} | null, "logging": {...} | null}
        },
        "actions": {
            name: {"kind": ..., "run": ..., "desc": ..., "targets": {...}}
        }
    }

Keys are sorted (json.dumps(..., sort_keys=True)) so two runs of the same
input produce byte-identical output, comparable with a plain diff.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT))

from admin_lib.manifest import parse as parse_manifest  # noqa: E402
from admin_lib.archetypes import resolve  # noqa: E402


def build(archetype_name: str) -> dict:
    manifest = parse_manifest(
        {"project_name": "parity", "archetypes": [archetype_name]},
        source="<archetype_parity>",
    )
    resolved = resolve(manifest, REPO_ROOT / "archetypes")

    commands = {}
    for name, cmd in resolved.commands.items():
        logging_meta = cmd.meta.get("logging") or None
        commands[name] = {
            "desc": cmd.desc,
            "steps": list(cmd.meta.get("steps", [])),
            "group": cmd.group,
            "priority": cmd.priority,
            "env": cmd.meta.get("env"),
            "logging": logging_meta,
        }

    actions = {}
    for name, act in resolved.actions.items():
        actions[name] = {
            "kind": act.kind,
            "run": act.run,
            "desc": act.desc,
            "targets": dict(act.targets),
        }

    return {
        "urls": dict(resolved.urls),
        "commands": commands,
        "actions": actions,
    }


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: archetype_parity.py <archetype-name>", file=sys.stderr)
        return 2
    result = build(sys.argv[1])
    print(json.dumps(result, sort_keys=True, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
