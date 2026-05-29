"""Post-action remote job watcher registry.

Actions opt in by declaring `watch = "<kind>"` in admin.toml. After the
shell command completes successfully, the named watcher scans stdout for
job IDs and blocks until the remote job finishes.

Built-in watcher kinds:
    github-action   — extracts GitHub Actions run IDs via actions/runs/<id>
                      URLs, then calls `gh run watch` for each.

To add a project-specific watcher: subclass Watcher and call register_watcher().
"""
from __future__ import annotations

import re
import subprocess

from .core import info, warn


class Watcher:
    def extract(self, output: str) -> list[str]:
        raise NotImplementedError

    def watch(self, ids: list[str]) -> int:
        raise NotImplementedError


_REGISTRY: dict[str, Watcher] = {}


def register_watcher(kind: str, watcher: Watcher) -> None:
    _REGISTRY[kind] = watcher


def run_watcher(kind: str, output: str) -> None:
    """Look up watcher by kind, extract IDs from output, and track each one."""
    w = _REGISTRY.get(kind)
    if not w:
        warn(f"no watcher registered for kind '{kind}'")
        return
    ids = w.extract(output)
    if not ids:
        return
    w.watch(ids)


class _GithubActionWatcher(Watcher):
    _RUN_URL_RE = re.compile(r"actions/runs/(\d+)")

    def extract(self, output: str) -> list[str]:
        return list(dict.fromkeys(self._RUN_URL_RE.findall(output)))

    def watch(self, ids: list[str]) -> int:
        for run_id in ids:
            info(f"tracking GitHub Action run {run_id}")
            rc = subprocess.call(["gh", "run", "watch", run_id])
            if rc != 0:
                warn(f"gh run watch {run_id} exited with {rc}")
        return 0


register_watcher("github-action", _GithubActionWatcher())
