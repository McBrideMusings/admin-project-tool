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


def render_multi_target(cmd: Command) -> str:
    """A command that dispatches to one of several shell strings based on a target arg.

    Archetype shape:
        "build": {"kind": "multi-target", "desc": "...", "targets": {
            "mac": "xcodebuild ... -destination 'platform=macOS'",
            "ios": "xcodebuild ... -destination 'platform=iOS Simulator,...'",
        }}

    Generated function: `./admin build` → pick_target menu; `./admin build mac` → run mac.
    """
    if not cmd.targets:
        raise FragmentError(f"multi-target command '{cmd.name}' has no targets")
    func_name = "cmd_" + _safe_ident(cmd.name)
    targets_literal = "{" + ", ".join(
        f"{_pyrepr(k)}: {_pyrepr(v)}" for k, v in cmd.targets.items()
    ) + "}"
    labels_literal = "[" + ", ".join(
        f"({_pyrepr(k)}, {_pyrepr(k)})" for k in cmd.targets
    ) + "]"
    body = textwrap.dedent(f'''\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            from collections import OrderedDict
            _targets = {targets_literal}
            _menu = OrderedDict({labels_literal})
            picked = args[0] if args and not args[0].startswith("-") else None
            if picked is None:
                if len(_targets) == 1:
                    picked = next(iter(_targets))
                else:
                    picked = pick_target(_menu, {_pyrepr(cmd.desc + " — select target")})
                    if not picked:
                        return
            if picked not in _targets:
                err({_pyrepr(f"{cmd.name}: unknown target '")} + picked + "'")
                sys.exit(1)
            set_log_context({_pyrepr(cmd.name)}, picked)
            shell = _targets[picked]
            info({_pyrepr(f"running [" + cmd.name + ":")} + picked + "]: " + shell)
            rc = run_cmd(shell)
            if rc != 0:
                err({_pyrepr(cmd.name + " failed")})
                sys.exit(rc)
            ok({_pyrepr(cmd.name + " complete")})
    ''')
    return body


def render_apple_dev_loop(cmd: Command) -> str:
    """An apple dev loop — same multi-target shape, semantically a long-running build+watch.

    The underlying admin_lib.apple.dev_loop_* helpers take heavy project-specific
    config (scheme, process, subsystem, icons). That's out of scope for a shell-string
    archetype; projects that need the full loop use `[inline]`. This fragment handles
    the common case: a multi-target xcodebuild-based dev shell with pick_target.
    """
    return render_multi_target(cmd)


def render_docker_reload_loop(cmd: Command) -> str:
    """A docker command wrapped in admin_lib.docker.run_with_reload.

    The loop watches for SIGUSR1 / 'r' keypress and re-runs the shell command.
    Requires the 'docker' module to be in the archetype's modules list.
    """
    if not cmd.run:
        raise FragmentError(f"docker-reload-loop command '{cmd.name}' needs 'run'")
    func_name = "cmd_" + _safe_ident(cmd.name)
    body = textwrap.dedent(f'''\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            info({_pyrepr("reload loop: " + cmd.run)})
            run_with_reload({_pyrepr(cmd.run)}, target={_pyrepr(cmd.name)})
    ''')
    return body


_RENDERERS = {
    "shell": render_shell,
    "multi-target": render_multi_target,
    "apple-dev-loop": render_apple_dev_loop,
    "docker-reload-loop": render_docker_reload_loop,
}

KNOWN_KINDS = set(_RENDERERS)


def _safe_ident(name: str) -> str:
    return name.replace("-", "_").replace(":", "_").replace(".", "_")


def _pyrepr(s: str) -> str:
    """Python repr that prefers double quotes for readability."""
    r = repr(s)
    if r[0] == "'":
        return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'
    return r


def _scan_for_env_vars(
    text: str,
    pattern: object,
    seen: dict[str, dict],
    origin: str,
) -> None:
    """Extract env var names and defaults from a single string, tagging the origin."""
    for m in pattern.finditer(text):
        name = m.group(1)
        default = m.group(2)
        entry = seen.setdefault(name, {"default": default, "used_by": [origin]})
        if entry["default"] is None and default is not None:
            entry["default"] = default
        if origin not in entry["used_by"]:
            entry["used_by"].append(origin)


def collect_env_var_calls(
    commands: list[Command],
    urls: dict[str, str] | None = None,
    logs: dict[str, dict] | None = None,
) -> list[str]:
    """Scan command run-strings, URL values, and log-target paths/hosts for
    ${VAR} placeholders and emit register_env_var calls with origin breadcrumbs.

    Each call lets the runtime `env` command list referenced vars even before they're
    resolved, and annotate which command/url/log-target referenced each one. Returns
    Python source lines (no trailing newline).
    """
    import re
    pattern = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}")
    seen: dict[str, dict] = {}

    for cmd in commands:
        origin = f"cmd:{cmd.name}"
        _scan_for_env_vars(cmd.run, pattern, seen, origin)
        for tname, tval in cmd.targets.items():
            _scan_for_env_vars(tval, pattern, seen, f"{origin}:{tname}")

    for key, text in (urls or {}).items():
        _scan_for_env_vars(text, pattern, seen, f"url:{key}")

    for target_name, target in (logs or {}).items():
        for env_name, env_spec in target.get("envs", {}).items():
            origin = f"logs.{target_name}.{env_name}"
            for field in ("path", "host", "user"):
                val = env_spec.get(field)
                if isinstance(val, str):
                    _scan_for_env_vars(val, pattern, seen, origin)

    lines = []
    for name, entry in seen.items():
        parts = [_pyrepr(name)]
        if entry["default"] is not None:
            parts.append(f"default={_pyrepr(entry['default'])}")
        if entry["used_by"]:
            origins = "[" + ", ".join(_pyrepr(o) for o in entry["used_by"]) + "]"
            parts.append(f"used_by={origins}")
        lines.append(f"register_env_var({', '.join(parts)})")
    return lines
