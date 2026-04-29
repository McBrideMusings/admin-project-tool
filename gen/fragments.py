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
    target_descs = cmd.meta.get("target_descs", {})
    menu_literal = "{" + ", ".join(
        f"{_pyrepr(k)}: {_pyrepr(target_descs.get(k, k))}" for k in cmd.targets
    ) + "}"
    body = textwrap.dedent(f'''\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            _targets = {targets_literal}
            _menu = {menu_literal}
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
            if shell.startswith('@'):
                run_command(shell[1:])
            else:
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

    This fragment handles the simple case: a multi-target xcodebuild-based dev
    shell with pick_target. Projects needing the full admin_lib.apple.dev_loop_*
    loop (icon swap, signal-driven reload, log streaming) should use a
    `kind = "python"` command that calls the dev_loop_* helper with project
    kwargs — see render_python.
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
            _fmt = make_column_formatter("docker", globals().get("_OUTPUT_CONFIG"))
            run_with_reload({_pyrepr(cmd.run)}, target={_pyrepr(cmd.name)}, formatter=_fmt.format_line)
    ''')
    return body


def render_python(cmd: Command) -> str:
    """A command whose `run` string is a block of Python statements executed in
    the scope of the bundled admin_lib — every top-level admin_lib symbol
    (`info`, `err`, `run_cmd`, `find_app`, `dev_loop_macos`, and everything else
    pulled in via the manifest's `modules = [...]`) is in scope.

    Unlike shell/multi-target kinds, the body is emitted verbatim. No `${VAR}`
    substitution is applied. For env access, call `os.environ.get(...)` directly
    — that keeps the generated script explicit about which env names it reads
    and keeps this renderer purely additive (no hidden substitution magic).

    This is the architected alternative to `[inline]` escape files: downstream
    projects that used to need a per-project admin_inline.py for stateful
    workflows (multi-stage builds, dev loops, orchestration) should instead
    extract the imperative body into an admin_lib module and call it from a
    `kind = "python"` command here.
    """
    if not cmd.run:
        raise FragmentError(f"python command '{cmd.name}' needs 'run' (a Python block)")
    func_name = "cmd_" + _safe_ident(cmd.name)
    indented = textwrap.indent(cmd.run.strip(), "    ")
    body = (
        f"@command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})\n"
        f"def {func_name}(args):\n"
        f"    set_log_context({_pyrepr(cmd.name)})\n"
        f"{indented}\n"
    )
    return body


def render_http(cmd: Command) -> str:
    """A command that makes an HTTP request. URL is in cmd.run; method and body are in cmd.meta.

    Non-2xx responses and network errors are warnings, not fatal — this kind is designed
    for post-deploy notifications where a missed ping shouldn't kill the build.
    """
    if not cmd.run:
        raise FragmentError(f"http command '{cmd.name}' requires 'run' (the URL)")
    func_name = "cmd_" + _safe_ident(cmd.name)
    method = cmd.meta.get("method", "POST")
    body = cmd.meta.get("body", "")
    url_repr = _pyrepr(cmd.run)
    method_repr = _pyrepr(method)

    lines = [
        f"@command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})",
        f"def {func_name}(args):",
        f"    import urllib.request as _ur, urllib.error as _ue",
        f"    _url = resolve_env({url_repr})",
        f"    info(\"http {method} \" + _url)",
    ]
    if body:
        body_repr = _pyrepr(body)
        lines += [
            f"    _body = resolve_env({body_repr}).encode()",
            f"    _req = _ur.Request(_url, data=_body, headers={{\"Content-Type\": \"application/json\"}}, method={method_repr})",
        ]
    else:
        lines += [f"    _req = _ur.Request(_url, method={method_repr})"]
    lines += [
        "    try:",
        "        with _ur.urlopen(_req, timeout=15) as _resp:",
        "            if _resp.status >= 400:",
        "                warn(\"http \" + str(_resp.status) + \" from \" + _url)",
        "            else:",
        "                ok(\"done\")",
        "    except (_ue.URLError, OSError) as _e:",
        "        warn(\"http request failed: \" + str(_e))",
        "",
    ]
    return "\n".join(lines)


def render_sequence(cmd: Command) -> str:
    """A command that runs a list of other commands in order, stopping on first failure."""
    steps = cmd.meta.get("steps", [])
    if not steps:
        raise FragmentError(f"sequence command '{cmd.name}' has no steps")
    func_name = "cmd_" + _safe_ident(cmd.name)
    steps_literal = "[" + ", ".join(_pyrepr(s) for s in steps) + "]"
    body = textwrap.dedent(f"""\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            for _step in {steps_literal}:
                run_command(_step)
    """)
    return body


def render_npm(cmd: Command) -> str:
    """A command that runs an npm (or bun) script. run = script name (e.g. 'build', 'dev')."""
    if not cmd.run:
        raise FragmentError(f"npm command '{cmd.name}' requires 'run' (script name)")
    func_name = "cmd_" + _safe_ident(cmd.name)
    script_repr = _pyrepr(cmd.run)
    body = textwrap.dedent(f"""\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            _pkg = "bun" if os.path.isfile("bun.lockb") else "npm"
            rc = run_cmd(_pkg + " run " + {script_repr})
            if rc != 0:
                err(_pkg + " run " + {script_repr} + " failed")
                sys.exit(rc)
            ok(_pkg + " run " + {script_repr} + " complete")
    """)
    return body


def render_apple(cmd: Command) -> str:
    """Call an admin_lib.apple function with _APPLE_CONFIG. run = function name."""
    if not cmd.run:
        raise FragmentError(f"apple command '{cmd.name}' requires 'run' (function name)")
    func_name = "cmd_" + _safe_ident(cmd.name)
    body = textwrap.dedent(f"""\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            {cmd.run}(globals().get("_APPLE_CONFIG") or {{}})
    """)
    return body


def render_sync(cmd: Command) -> str:
    """A command that syncs a local directory to a destination. run = dest path, meta.src = source dir."""
    if not cmd.run:
        raise FragmentError(f"sync command '{cmd.name}' requires 'run' (destination path)")
    func_name = "cmd_" + _safe_ident(cmd.name)
    src = cmd.meta.get("src", "dist/")
    body = textwrap.dedent(f"""\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            rc = sync({_pyrepr(src)}, resolve_env({_pyrepr(cmd.run)}))
            if rc != 0:
                sys.exit(rc)
    """)
    return body


def render_action(cmd: Command) -> str:
    """Render a command as a registered action (hidden from TUI/--help, callable via run_command)."""
    src = render_command(cmd)
    ident = _safe_ident(cmd.name)
    lines = src.rstrip("\n").split("\n")
    out = []
    func_renamed = False
    for line in lines:
        if line.startswith("@command("):
            continue
        if not func_renamed and line.startswith("def cmd_"):
            line = "def _act_" + line[len("def cmd_"):]
            func_renamed = True
        out.append(line)
    out.append(f"register_action({_pyrepr(cmd.name)}, _act_{ident})")
    return "\n".join(out) + "\n"


_RENDERERS = {
    "shell": render_shell,
    "multi-target": render_multi_target,
    "apple-dev-loop": render_apple_dev_loop,
    "docker-reload-loop": render_docker_reload_loop,
    "python": render_python,
    "http": render_http,
    "sequence": render_sequence,
    "npm": render_npm,
    "sync": render_sync,
    "apple": render_apple,
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

    The origin-string format ("cmd:<name>", "url:<key>", "logs.<target>.<env>", …)
    is a stable compatibility contract shared with the runtime renderer; see
    the contract block above `register_env_var` in admin_lib/core.py before
    changing it.
    """
    import re
    pattern = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}")
    # kind=python commands read env via os.environ.get("NAME") /
    # os.environ.get("NAME", "default") instead of ${VAR}. Scanning both lets
    # `./admin env` surface the same origin breadcrumbs for either kind.
    py_env_pattern = re.compile(
        r"""os\.environ\.get\(\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]"""
        r"""(?:\s*,\s*(?:['"]([^'"]*)['"]|[^)]+?))?\s*\)"""
    )
    seen: dict[str, dict] = {}

    for cmd in commands:
        origin = f"cmd:{cmd.name}"
        if cmd.kind != "sequence":
            _scan_for_env_vars(cmd.run, pattern, seen, origin)
        if cmd.kind == "python":
            _scan_for_env_vars(cmd.run, py_env_pattern, seen, origin)
        if cmd.kind == "http" and cmd.meta.get("body"):
            _scan_for_env_vars(cmd.meta["body"], pattern, seen, origin)
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
