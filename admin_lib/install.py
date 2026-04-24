"""Recursive dependency installer.

Walks the project tree, detects nested package-manager manifests, and runs
the appropriate install command for each. Opt-in via `modules = ["install"]`
in admin.toml; adds an `install` command to ./admin automatically.
"""

import os
import subprocess
import sys

from .core import command, err, info, ok

_SKIP_DIRS = {
    "node_modules", ".git", "target", "zig-cache", "zig-out", ".zig-cache",
    "zig-pkg", ".venv", "venv", "dist", "build", "__pycache__", ".cargo",
}

# Each entry: (manifest_filename, command_list, shadowed_by)
# shadowed_by: if any of these files exist in the same dir, skip this detector.
# Ordered so lockfiles (more specific) are checked before generic manifests.
_DETECTORS = [
    ("bun.lockb",          ["bun", "install"],                        []),
    ("package-lock.json",  ["npm", "install"],                        []),
    ("yarn.lock",          ["yarn", "install"],                       []),
    ("pnpm-lock.yaml",     ["pnpm", "install"],                       []),
    ("package.json",       ["npm", "install"],                        ["bun.lockb", "package-lock.json", "yarn.lock", "pnpm-lock.yaml"]),
    ("Cargo.toml",         ["cargo", "fetch"],                        []),
    ("go.mod",             ["go", "mod", "download"],                 []),
    ("pyproject.toml",     ["uv", "sync"],                            []),
    ("requirements.txt",   ["pip", "install", "-r", "requirements.txt"], ["pyproject.toml"]),
]


def _find_installs(root):
    """Return list of (dirpath, command_list) for every detectable package manager under root."""
    jobs = []
    for dirpath, dirnames, filenames in os.walk(root, topdown=True):
        dirnames[:] = sorted(d for d in dirnames if d not in _SKIP_DIRS)
        file_set = set(filenames)
        for manifest, cmd, shadowed_by in _DETECTORS:
            if manifest in file_set and not any(s in file_set for s in shadowed_by):
                jobs.append((dirpath, cmd))
                break
    return jobs


@command("install", "Install dependencies for all nested package managers")
def cmd_install(args):
    root = os.getcwd()
    jobs = _find_installs(root)
    if not jobs:
        info("no package manager manifests found")
        return
    info(f"found {len(jobs)} project(s):")
    for d, cmd in jobs:
        rel = os.path.relpath(d, root) or "."
        info(f"  {rel}  ({' '.join(cmd)})")
    print()
    for d, cmd in jobs:
        rel = os.path.relpath(d, root) or "."
        info(f"installing {rel}")
        rc = subprocess.call(cmd, cwd=d)
        if rc != 0:
            err(f"install failed in {rel} (exit {rc})")
            sys.exit(rc)
    ok("all installs complete")
