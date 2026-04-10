"""Minimal admin_lib bundler for v2.

Inlines the requested admin_lib modules into a single self-contained Python
string. For the minimal v2 slice this only supports 'core' (which has no
relative imports). Richer modules (apple/docker/etc.) will be added as
archetypes that need them are introduced.

This is intentionally simpler than the v1 bundle script — it doesn't need
to handle rebundles, lazy relative imports, or legacy markers. It's called
from inside the generator, not as a user-facing tool.
"""
from __future__ import annotations

import hashlib
import re
from pathlib import Path


class BundlerError(Exception):
    pass


# Strips `from .mod import X` (single line), `from . import foo as bar`, and
# multi-line parenthesized relative imports. All bundled modules share one flat
# namespace after inlining, so relative imports become no-ops.
_STRIP_RELATIVE_IMPORT_RE = re.compile(
    r"^\s*from\s+\.[\w.]*\s+import\s*(?:\([^)]*\)|[^\n]*)\n",
    re.MULTILINE,
)


def inline_admin_lib(admin_lib_dir: Path, modules: list[str]) -> str:
    """Return a Python string containing the concatenated admin_lib modules.

    Each module's source is wrapped with a `# === admin_lib.<name> ===` banner
    for readability. Relative imports (`from .foo import ...`) are stripped.
    The caller (render.py) is responsible for adding the shebang, header, and
    project-specific code.
    """
    parts: list[str] = []
    for name in modules:
        src_path = admin_lib_dir / f"{name}.py"
        if not src_path.exists():
            raise BundlerError(f"admin_lib module not found: {src_path}")
        src = src_path.read_text()
        # Replace with `pass` (matching leading whitespace) so any enclosing
        # try/except block stays syntactically valid.
        def _repl(m):
            match_text = m.group(0)
            indent = match_text[: len(match_text) - len(match_text.lstrip())]
            return f"{indent}pass\n"
        src = _STRIP_RELATIVE_IMPORT_RE.sub(_repl, src)
        parts.append(f"# === admin_lib.{name} ===\n{src.rstrip()}\n")
    return "\n".join(parts)


def lib_hash(admin_lib_dir: Path) -> str:
    """SHA256 of all admin_lib/*.py content, first 12 hex chars.

    Same algorithm as v1 bundle so lib versioning stays comparable.
    """
    h = hashlib.sha256()
    for name in sorted(p.name for p in admin_lib_dir.iterdir() if p.suffix == ".py"):
        h.update((admin_lib_dir / name).read_bytes())
    return h.hexdigest()[:12]
