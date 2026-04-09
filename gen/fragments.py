"""Code-fragment renderer.

A fragment turns a single Command into the body of a Python function in the
generated ./admin script. The minimal slice ships only the 'shell' fragment;
later kinds (multi-target, apple-dev-loop, docker-reload-loop) plug in here
without changing the manifest format or merge logic.
"""
from __future__ import annotations

import textwrap

from gen.manifest import Command


class FragmentError(Exception):
    pass


def render_command(cmd: Command) -> str:
    """Return a Python function definition for the given command.

    The emitted function expects to be in scope of admin_lib.core (info, run_cmd,
    register_env_var, ok, err — already imported by the generated header).
    """
    renderer = _RENDERERS.get(cmd.kind)
    if renderer is None:
        raise FragmentError(
            f"unknown command kind '{cmd.kind}' for command '{cmd.name}' "
            f"(known kinds: {sorted(_RENDERERS)})"
        )
    return renderer(cmd)


def render_shell(cmd: Command) -> str:
    """A command that runs a single shell string. Most common kind by far."""
    func_name = "cmd_" + _safe_ident(cmd.name)
    body = textwrap.dedent(f'''\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            info({_pyrepr(f"running: {cmd.run}")})
            rc = run_cmd({_pyrepr(cmd.run)})
            if rc != 0:
                err({_pyrepr(cmd.name + " failed")})
                sys.exit(rc)
            ok({_pyrepr(cmd.name + " complete")})
    ''')
    return body


_RENDERERS = {
    "shell": render_shell,
}


def _safe_ident(name: str) -> str:
    return name.replace("-", "_").replace(":", "_").replace(".", "_")


def _pyrepr(s: str) -> str:
    """Python repr that prefers double quotes for readability."""
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


def collect_env_var_calls(commands: list[Command]) -> list[str]:
    """Scan command run-strings for ${VAR} placeholders and emit register_env_var calls.

    Each call lets the runtime `env` command list referenced vars even before they're
    resolved. Returns Python source lines (no trailing newline).
    """
    import re
    pattern = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}")
    seen: dict[str, str | None] = {}
    for cmd in commands:
        for m in pattern.finditer(cmd.run):
            name = m.group(1)
            default = m.group(2)
            if name not in seen or (seen[name] is None and default is not None):
                seen[name] = default
    lines = []
    for name, default in seen.items():
        if default is None:
            lines.append(f"register_env_var({_pyrepr(name)})")
        else:
            lines.append(f"register_env_var({_pyrepr(name)}, default={_pyrepr(default)})")
    return lines
