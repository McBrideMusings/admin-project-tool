"""Render a ResolvedManifest into a complete ./admin script (as a string).

The emitted script is self-contained Python 3.11+ that depends only on the
standard library. It runs with no install beyond a working `python3`.

Structure of the generated file:

    #!/usr/bin/env python3
    # @bundled admin_lib=... sha=... modules=... generator_commit=...
    # Regenerate: ~/.admin/init-admin --regenerate
    <python version guard>
    <inlined admin_lib modules>
    # === project: <name> ===
    <register_env_var calls>
    <set_urls call>
    <command functions>
    <inline-file contents, if any>
    <boot() at __main__>
"""
from __future__ import annotations

import subprocess
import textwrap
from pathlib import Path

from gen.archetypes import ResolvedManifest
from gen.bundler import inline_admin_lib, lib_hash
from gen.fragments import collect_env_var_calls, render_command
from gen.manifest import Command, SPACER

ADMIN_LIB_VERSION = "2.0.0-dev"
BUNDLED_MARKER = "# @bundled"
PROJECT_MARKER = "# === project"


class RenderError(Exception):
    pass


def render(
    resolved: ResolvedManifest,
    admin_lib_dir: Path,
    generator_commit: str,
    inline_body: str | None = None,
) -> str:
    """Return the full text of the generated ./admin script."""
    m = resolved.manifest

    # Presence of [logs] in the manifest pulls in the logtail module.
    modules = list(resolved.modules)
    if m.logs and "logtail" not in modules:
        modules.append("logtail")
        resolved.modules = modules

    lh = lib_hash(admin_lib_dir)
    modules_csv = ",".join(resolved.modules)

    header = textwrap.dedent(f"""\
        #!/usr/bin/env python3
        {BUNDLED_MARKER} admin_lib={ADMIN_LIB_VERSION} sha={lh} modules={modules_csv} generator_commit={generator_commit}
        # Generated from admin.toml by admin-project-tool.
        # Regenerate: ~/.admin/init-admin --regenerate
        \"\"\"admin — task runner for {m.project_name}.\"\"\"
        import sys
        if sys.version_info < tuple(int(x) for x in "{m.python_min}".split(".")):
            sys.exit("admin requires Python {m.python_min}+")
        import os
        """)

    lib_src = inline_admin_lib(admin_lib_dir, resolved.modules)

    command_list = list(resolved.commands.values())
    env_calls = collect_env_var_calls(command_list, resolved.urls, m.logs)
    env_block = "\n".join(env_calls) + ("\n" if env_calls else "")

    url_pairs = ", ".join(
        f'({_pyrepr(label)}, {_pyrepr(url)})' for label, url in resolved.urls.items()
    )
    url_call = f"set_urls([{url_pairs}])\n" if url_pairs else ""

    # Emit the [apple] table as a module-level _APPLE_CONFIG dict the apple
    # archetype's kind:"python" commands read. Empty table → no emission.
    apple_decl = ""
    if m.apple:
        apple_decl = f"_APPLE_CONFIG = {_pyliteral(m.apple)}\n"

    # Emit the [output] table as _OUTPUT_CONFIG for log column configuration.
    output_decl = ""
    if m.output:
        output_decl = f"_OUTPUT_CONFIG = {_pyliteral(m.output)}\n"

    # Emit command function defs in display order, interleaving add_spacer()
    # calls for spacer entries. Registration order == display order.
    command_blocks: list[str] = []
    for entry in resolved.display_order:
        if entry == SPACER:
            command_blocks.append("add_spacer()\n")
            continue
        command_blocks.append(render_command(resolved.commands[entry]))
    commands_src = "\n".join(command_blocks)

    # Emit register_logs_command AFTER the archetype command functions so that
    # a manifest [logs] definition supersedes any archetype-supplied `logs` command
    # (the @command decorator overwrites by name in _commands).
    logs_src = ""
    if m.logs:
        logs_src = f"register_logs_command({_pyliteral(m.logs)})\n\n"

    inline_src = ""
    if inline_body:
        inline_src = f"# === inline ===\n{inline_body.rstrip()}\n\n"

    boot_src = textwrap.dedent(f"""\
        if __name__ == "__main__":
            boot({_pyrepr(m.project_name)})
        """)

    project_section = (
        f"{PROJECT_MARKER}: {m.project_name} ===\n"
        f"{env_block}"
        f"{url_call}"
        f"{apple_decl}"
        f"{output_decl}"
        f"\n{commands_src}\n"
        f"{logs_src}"
        f"{inline_src}"
        f"{boot_src}"
    )

    return header + "\n" + lib_src + "\n" + project_section


def generator_commit_sha(repo_dir: Path, allow_dirty: bool = False) -> str:
    """Return the admin-project-tool HEAD SHA, with +dirty suffix if working tree is dirty.

    Prefers a VERSION file (written by install.sh) over a live git call, so
    the installed copy at ~/.admin/ doesn't need to be a git repo. Raises
    RenderError if dirty and allow_dirty is False.
    """
    version_file = repo_dir / "VERSION"
    if version_file.exists():
        sha = version_file.read_text().strip()
        if sha.endswith("+dirty") and not allow_dirty:
            raise RenderError(
                f"admin-project-tool was installed from a dirty tree ({sha}); "
                "reinstall from a clean tree or pass --force-dirty"
            )
        return sha

    try:
        sha = subprocess.check_output(
            ["git", "-C", str(repo_dir), "rev-parse", "HEAD"],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        raise RenderError(f"failed to read generator git SHA: {e}") from e

    dirty = subprocess.run(
        ["git", "-C", str(repo_dir), "status", "--porcelain"],
        check=True, capture_output=True, text=True,
    ).stdout.strip()
    if dirty:
        if not allow_dirty:
            raise RenderError(
                f"admin-project-tool working tree is dirty at {repo_dir}; "
                "commit changes or pass --force-dirty"
            )
        return sha + "+dirty"
    return sha


def _pyrepr(s: str) -> str:
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


def _pyliteral(value) -> str:
    """Render a nested structure of dicts / strings / None / bools / ints as a
    Python literal. Used to emit the logs spec into the generated script.
    Any ${VAR} placeholders are preserved verbatim — resolution happens at run time.
    """
    if value is None or isinstance(value, bool):
        return repr(value)
    if isinstance(value, (int, float)):
        return repr(value)
    if isinstance(value, str):
        return _pyrepr(value)
    if isinstance(value, dict):
        parts = [f"{_pyrepr(str(k))}: {_pyliteral(v)}" for k, v in value.items()]
        return "{" + ", ".join(parts) + "}"
    if isinstance(value, (list, tuple)):
        parts = [_pyliteral(v) for v in value]
        return "[" + ", ".join(parts) + "]"
    raise RenderError(f"cannot serialize to python literal: {type(value).__name__}")
