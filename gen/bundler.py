"""admin_lib bundler — inlines admin_lib modules into a generated admin script.

Called from inside the generator (gen/render.py), not as a user-facing tool.
Strips relative imports so each module's source is valid in a flat namespace.
Also offers a post-bundle reachability strip that drops top-level defs no
runtime entry point can reach.
"""
from __future__ import annotations

import ast
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
        # All relative imports become `pass` (or get deleted at module level)
        # because bundled modules share a single flat namespace — the imported
        # names are already available. At module scope no statement is required,
        # so we drop the line entirely; inside functions/try blocks `pass` keeps
        # the surrounding statement valid.
        def _repl(m):
            match_text = m.group(0)
            indent = match_text[: len(match_text) - len(match_text.lstrip())]
            indent = indent.lstrip("\n")
            if indent == "":
                return ""
            return f"{indent}pass\n"
        src = _STRIP_RELATIVE_IMPORT_RE.sub(_repl, src)
        parts.append(f"# === admin_lib.{name} ===\n{src.rstrip()}\n")
    return "\n".join(parts)


# Symbols that are entry points by virtue of how the runtime invokes them
# (the harness, dispatcher, decorators, registration helpers). Anything called
# only via dynamic dispatch should appear here OR be referenced as a string
# literal somewhere in the bundle (we sweep for that too).
_HARD_KEEP: frozenset[str] = frozenset({
    # Boot + dispatcher
    "boot", "dispatch_target", "run_command", "command", "subcommand_help",
    # Registration helpers always invoked from the orchestration tail
    "register_builtin_logs_command", "register_cleanup", "register_env_var",
    # Per-command env injection (called from generated cmd_* functions)
    "_apply_cmd_env",
    # Logging + URLs + provenance set up at bundle init
    "set_log_context", "set_log_context_path", "configure_logging",
    "set_urls", "check_url", "format_url_status", "set_generator_commit",
    # TUI + interactive primitives
    "_tui_menu", "pick_target", "add_spacer", "read_key",
    # Print / log helpers (called from project [actions] kind=python bodies)
    "info", "ok", "warn", "err",
    # Color/path constants
    "C", "LOG_DIR", "LOG_FILE",
    # Process tracking + shared registries
    "_children", "_urls", "_commands",
    # Generic command-body helpers
    "run_cmd", "resolve_env",
    # Manifest-emitted config dicts (set by render.py)
    "_APPLE_CONFIG", "_SERVER_CONFIG", "_OUTPUT_CONFIG",
    "_DEV_KILL_CONFIG", "_CLEAN_DEV_CONFIG", "_VITE_DEV_CONFIG",
    "_DOCKER_DEV_CONFIG", "_DEPLOY_CONFIG",
})


def strip_unreachable(source: str) -> tuple[str, int, list[str]]:
    """Drop top-level functions, classes, and constants the bundle can't reach.

    Reachability starts from every `cmd_*` function the generator emitted, the
    `_HARD_KEEP` set, any name referenced in a module-level non-def statement
    (so `register_cleanup(_x)` keeps `_x`), and any name appearing as a string
    literal anywhere in the bundle (covers dynamic dispatch via `getattr`,
    registry tables, etc.).

    Returns (stripped_source, n_dropped, dropped_names).
    """
    tree = ast.parse(source)

    # Collect top-level removable defs.
    top_defs: dict[str, ast.AST] = {}
    for n in tree.body:
        if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            top_defs[n.name] = n
        elif isinstance(n, ast.Assign):
            for tgt in n.targets:
                if isinstance(tgt, ast.Name):
                    top_defs.setdefault(tgt.id, n)
                    break
        elif isinstance(n, ast.AnnAssign) and isinstance(n.target, ast.Name):
            top_defs.setdefault(n.target.id, n)

    if not top_defs:
        return source, 0, []

    top_names = set(top_defs)

    def referenced_names(node: ast.AST) -> set[str]:
        out: set[str] = set()
        for sub in ast.walk(node):
            if isinstance(sub, ast.Name) and sub.id in top_names:
                out.add(sub.id)
        return out

    # Edges of the call graph: each top-level def → set of top-level names
    # referenced anywhere inside its body / decorators / RHS.
    edges = {name: referenced_names(node) for name, node in top_defs.items()}

    # Module-level statements that are NOT removable defs are always-evaluated
    # at import time — anything they reference is implicitly an entry point.
    module_level_refs: set[str] = set()
    for n in tree.body:
        if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            continue
        if isinstance(n, ast.Assign) and any(
            isinstance(t, ast.Name) and t.id in top_defs for t in n.targets
        ):
            continue
        if (
            isinstance(n, ast.AnnAssign)
            and isinstance(n.target, ast.Name)
            and n.target.id in top_defs
        ):
            continue
        module_level_refs |= referenced_names(n)

    # Any string literal whose value happens to name a top-level symbol is a
    # potential dynamic-dispatch target. Conservative and cheap. Docstrings
    # are excluded — they describe symbols, they don't dispatch to them.
    # We treat any standalone string-Expr as a docstring: bundled modules'
    # docstrings land mid-file as orphan Expr statements after inlining, so
    # checking only `body[0]` of Module/FunctionDef/ClassDef would miss them.
    docstring_constants: set[int] = set()
    for parent in ast.walk(tree):
        body = getattr(parent, "body", None)
        if not isinstance(body, list):
            continue
        for stmt in body:
            if (
                isinstance(stmt, ast.Expr)
                and isinstance(stmt.value, ast.Constant)
                and isinstance(stmt.value.value, str)
            ):
                docstring_constants.add(id(stmt.value))

    string_refs: set[str] = set()
    for sub in ast.walk(tree):
        if isinstance(sub, ast.Constant) and isinstance(sub.value, str):
            if id(sub) in docstring_constants:
                continue
            if sub.value in top_names:
                string_refs.add(sub.value)

    # Entry points: cmd_* stubs + hard-keep + module-level refs + string refs.
    entry: set[str] = {n for n in top_names if n.startswith("cmd_")}
    entry |= _HARD_KEEP & top_names
    entry |= module_level_refs
    entry |= string_refs

    # Walk the call graph from entry points.
    reachable: set[str] = set()
    frontier = list(entry)
    while frontier:
        cur = frontier.pop()
        if cur in reachable:
            continue
        reachable.add(cur)
        frontier.extend(ref for ref in edges.get(cur, ()) if ref not in reachable)

    unreachable = top_names - reachable
    if not unreachable:
        return source, 0, []

    # Compute line ranges to drop. For decorated FunctionDef/ClassDef, start
    # from the first decorator's line so the decorator goes too.
    drops: list[tuple[int, int]] = []
    for name in unreachable:
        node = top_defs[name]
        start = node.lineno
        if hasattr(node, "decorator_list") and node.decorator_list:
            start = min(start, node.decorator_list[0].lineno)
        drops.append((start, node.end_lineno))

    drops.sort()
    lines = source.splitlines(keepends=True)
    keep = [True] * len(lines)
    for start, end in drops:
        for i in range(start - 1, end):
            if 0 <= i < len(keep):
                keep[i] = False
    new_source = "".join(line for line, k in zip(lines, keep) if k)
    return new_source, len(unreachable), sorted(unreachable)


# Pattern matching `# === admin_lib.<name> ===` banner lines.
_MODULE_BANNER_RE = re.compile(r"^# === admin_lib\.[\w_]+ ===\s*$", re.MULTILINE)


def compact_whitespace(source: str) -> str:
    """Tidy a stripped bundle: drop empty module sections, coalesce blank runs.

    After reachability-based stripping, modules can lose every def while
    leaving behind their banner, docstring, imports, and a graveyard of
    blank lines. This pass:
      1. Drops module sections whose body has no functions/classes/assignments
         (banner + docstring + imports only) — the section is purely vestigial
         once all its symbols are gone.
      2. Coalesces any run of 3+ blank lines down to 2.
    """
    # Step 1: drop empty module sections.
    banners = list(_MODULE_BANNER_RE.finditer(source))
    if banners:
        # Append a sentinel for the final section's end (the project marker
        # or EOF).
        proj_marker = source.find("# === project")
        end_pos = proj_marker if proj_marker != -1 else len(source)
        spans: list[tuple[int, int]] = []
        for i, m in enumerate(banners):
            section_start = m.start()
            section_end = banners[i + 1].start() if i + 1 < len(banners) else end_pos
            spans.append((section_start, section_end))

        # Decide which sections to drop: those with no def/class/assignment.
        keep_mask = [True] * len(source)
        any_dropped = False
        for start, end in spans:
            section = source[start:end]
            try:
                tree = ast.parse(section)
            except SyntaxError:
                continue
            has_real_code = any(
                isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef, ast.Assign, ast.AnnAssign))
                for n in tree.body
            )
            if not has_real_code:
                for i in range(start, end):
                    keep_mask[i] = False
                any_dropped = True
        if any_dropped:
            source = "".join(c for c, k in zip(source, keep_mask) if k)

    # Step 2: drop pure-decoration divider comments and coalesce blank runs.
    lines = source.splitlines(keepends=True)
    out: list[str] = []
    blank_run = 0
    for line in lines:
        stripped = line.strip()
        # Long horizontal-rule comment lines (`# -----...`) are visual section
        # dividers in the source. They orient a developer reading admin_lib but
        # are pure noise once bundled.
        if stripped.startswith("# -") and set(stripped.lstrip("# ")) <= {"-"} and len(stripped) >= 20:
            continue
        if stripped == "":
            blank_run += 1
            if blank_run <= 2:
                out.append(line)
        else:
            blank_run = 0
            out.append(line)
    return "".join(out)


def lib_hash(admin_lib_dir: Path) -> str:
    """SHA256 of all admin_lib/*.py content, first 12 hex chars."""
    h = hashlib.sha256()
    for name in sorted(p.name for p in admin_lib_dir.iterdir() if p.suffix == ".py"):
        h.update((admin_lib_dir / name).read_bytes())
    return h.hexdigest()[:12]
