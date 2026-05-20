"""Render a ResolvedManifest into a complete ./admin script (as a string).

The emitted script is self-contained Python 3.11+ that depends only on the
standard library. It runs with no install beyond a working `python3`.

Structure of the generated file:

    #!/usr/bin/env python3
    # @bundled admin_lib=... sha=... modules=... generator_commit=...
    # Regenerate: admin regenerate
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
from gen.bundler import inline_admin_lib, lib_hash, strip_unreachable, compact_whitespace
from gen.fragments import collect_env_var_calls, render_command, render_action, _pyrepr
from gen.manifest import SPACER

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
    strip_dead_code: bool = True,
) -> str:
    """Return the full text of the generated ./admin script.

    `strip_dead_code` runs `bundler.strip_unreachable` on the assembled output,
    dropping top-level admin_lib defs that no entry point can reach. Pass False
    to disable for debugging.
    """
    m = resolved.manifest

    # Presence of [logs] in the manifest pulls in the logtail module.
    modules = list(resolved.modules)
    if m.logs and "logtail" not in modules:
        modules.append("logtail")
        resolved.modules = modules

    # Presence of [log_bridge] pulls in the log_bridge and log_server modules.
    if m.log_bridge:
        for mod in ("log_server", "log_bridge"):
            if mod not in modules:
                modules.append(mod)
        resolved.modules = modules

    # vite-dev actions require the web_dev module.
    if any(c.kind == "vite-dev" for c in resolved.actions.values()):
        if "web_dev" not in resolved.modules:
            resolved.modules = list(resolved.modules) + ["web_dev"]

    # kill-port actions require the ports module.
    kill_port_cmds = [c for c in resolved.actions.values() if c.kind == "kill-port"]
    if kill_port_cmds and "ports" not in resolved.modules:
        names = ", ".join(f"'{c.name}'" for c in kill_port_cmds)
        raise RenderError(
            f"command(s) {names} use kind='kill-port' but 'ports' is not in the module list; "
            f"add modules = [\"ports\"] to admin.toml"
        )

    lh = lib_hash(admin_lib_dir)
    modules_csv = ",".join(resolved.modules)

    # Bundle modules first so we know which __future__ features need to be
    # hoisted into the header. Python requires `from __future__ import …`
    # to appear before any other statement except the module docstring,
    # so it has to slot in immediately after the `"""admin — …"""` line.
    future_imports, lib_src = inline_admin_lib(admin_lib_dir, resolved.modules)

    header_lines = [
        "#!/usr/bin/env python3",
        f"{BUNDLED_MARKER} admin_lib={ADMIN_LIB_VERSION} sha={lh} modules={modules_csv} generator_commit={generator_commit}",
        "# Generated from admin.toml by admin-project-tool.",
        "# Regenerate: admin regenerate",
        f'"""admin — task runner for {m.project_name}."""',
    ]
    if future_imports:
        header_lines.append(f"from __future__ import {', '.join(future_imports)}")
    header_lines.extend([
        "import sys",
        f'if sys.version_info < tuple(int(x) for x in "{m.python_min}".split(".")):',
        f'    sys.exit("admin requires Python {m.python_min}+")',
        "import os",
    ])
    header = "\n".join(header_lines) + "\n"

    command_list = list(resolved.commands.values()) + list(resolved.actions.values())
    env_calls = collect_env_var_calls(
        command_list,
        resolved.urls,
        m.logs,
        config_tables={
            "deploy": m.deploy,
            "vite_dev": m.vite_dev,
            "docker_dev": m.docker_dev,
            "dev_kill": m.dev_kill,
            "clean_dev": m.clean_dev,
            "install_template": m.install_template,
        },
    )
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

    # Emit the [server] table as _SERVER_CONFIG for server build/dev/deploy commands.
    server_decl = ""
    if m.server:
        server_decl = f"_SERVER_CONFIG = {_pyliteral(m.server)}\n"

    # Emit the [output] table as _OUTPUT_CONFIG for log column configuration.
    output_decl = ""
    if m.output:
        output_decl = f"_OUTPUT_CONFIG = {_pyliteral(m.output)}\n"

    # Emit [dev_kill], [clean_dev], [vite_dev], [deploy] config tables. Each
    # is read by its corresponding kind renderer's generated function body.
    dev_kill_decl = f"_DEV_KILL_CONFIG = {_pyliteral(m.dev_kill)}\n" if m.dev_kill else ""
    clean_dev_decl = f"_CLEAN_DEV_CONFIG = {_pyliteral(m.clean_dev)}\n" if m.clean_dev else ""
    vite_dev_decl = f"_VITE_DEV_CONFIG = {_pyliteral(m.vite_dev)}\n" if m.vite_dev else ""
    docker_dev_decl = f"_DOCKER_DEV_CONFIG = {_pyliteral(m.docker_dev)}\n" if m.docker_dev else ""
    deploy_decl = f"_DEPLOY_CONFIG = {_pyliteral(m.deploy)}\n" if m.deploy else ""
    install_template_decl = f"_INSTALL_TEMPLATE_CONFIG = {_pyliteral(m.install_template)}\n" if m.install_template else ""

    # Emit configure_logging() call if [logging] is configured.
    logging_decl = ""
    if m.logging:
        logging_decl = f"configure_logging({_pyliteral(m.logging)})\n"

    # Emit configure_log_bridge() call if [log_bridge] is configured. The
    # project name is injected at render time so the runtime side doesn't need
    # a separate setter for it.
    log_bridge_decl = ""
    if m.log_bridge:
        lb_cfg = {**m.log_bridge, "project": m.project_name}
        log_bridge_decl = f"configure_log_bridge({_pyliteral(lb_cfg)})\n"

    # Emit actions first (they must be registered before commands that call them).
    action_blocks: list[str] = [render_action(a) for a in resolved.actions.values()]
    actions_src = "\n".join(action_blocks)

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

    actions_block = f"\n{actions_src}\n" if actions_src else ""
    project_section = (
        f"{PROJECT_MARKER}: {m.project_name} ===\n"
        f"{env_block}"
        f"{url_call}"
        f"set_generator_commit({_pyrepr(generator_commit)})\n"
        f"{apple_decl}"
        f"{server_decl}"
        f"{output_decl}"
        f"{dev_kill_decl}"
        f"{clean_dev_decl}"
        f"{vite_dev_decl}"
        f"{docker_dev_decl}"
        f"{deploy_decl}"
        f"{install_template_decl}"
        f"{logging_decl}"
        f"{log_bridge_decl}"
        f"{actions_block}"
        f"\n{commands_src}\n"
        f"register_builtin_logs_command()\n"
        f"{logs_src}"
        f"{inline_src}"
        f"{boot_src}"
    )

    full = header + "\n" + lib_src + "\n" + project_section
    if strip_dead_code:
        # Iterate to a fixed point. Each pass can expose new dead code: e.g.
        # a string-literal forward-ref like `def f() -> "Foo":` keeps Foo alive
        # until f itself is dropped, after which the string is gone too.
        total_dropped = 0
        while True:
            full, n_dropped, _dropped = strip_unreachable(full)
            total_dropped += n_dropped
            if n_dropped == 0:
                break
        if total_dropped:
            full = full.replace(
                f"{BUNDLED_MARKER} admin_lib={ADMIN_LIB_VERSION}",
                f"{BUNDLED_MARKER} admin_lib={ADMIN_LIB_VERSION} stripped={total_dropped}",
                1,
            )
            full = compact_whitespace(full)
    return full


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
