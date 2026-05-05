"""Code-fragment renderer.

A fragment turns a single Command into the body of a Python function in the
generated ./admin script. The minimal slice ships only the 'shell' fragment;
later kinds (multi-target, apple-dev-loop, docker-reload-loop) plug in here
without changing the manifest format or merge logic.
"""
from __future__ import annotations

import re
import textwrap

from gen.manifest import Command

_ENV_VAR_RE = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}")
_PY_ENV_RE = re.compile(
    r"""os\.environ\.get\(\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]"""
    r"""(?:\s*,\s*(?:['"]([^'"]*)['"]|[^)]+?))?\s*\)"""
)


class FragmentError(Exception):
    pass


def render_command(cmd: Command) -> str:
    """Render a step-runner @command function. Commands are pure routing — no work here.

    Injects set_log_context at the top so each command run gets its own timestamped log.
    """
    func_name = "cmd_" + _safe_ident(cmd.name)
    steps = cmd.meta.get("steps", [])
    if not steps:
        raise FragmentError(f"command '{cmd.name}' has no steps")
    steps_literal = "[" + ", ".join(_pyrepr(s) for s in steps) + "]"
    log_cfg = cmd.meta.get("logging", {})
    if log_cfg.get("log") is False:
        log_line = ""
    elif "log_file" in log_cfg:
        log_line = f"    set_log_context_path({_pyrepr(log_cfg['log_file'])})\n"
    else:
        retain_arg = f", retain={log_cfg['log_retain']}" if "log_retain" in log_cfg else ""
        log_line = f"    set_log_context({_pyrepr(cmd.name)}{retain_arg})\n"
    return (
        f"@command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})\n"
        f"def {func_name}(args):\n"
        f"{log_line}"
        f"    for _step in {steps_literal}:\n"
        f"        run_command(_step, args)\n"
    )


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
    target_order = cmd.meta.get("target_order")
    if target_order is not None:
        order_literal = "[" + ", ".join(_pyrepr(e) for e in target_order) + "]"
        # 12 spaces: matches the 8-space common indent of the textwrap.dedent block below,
        # plus 4 spaces to land at function-body level after stripping.
        order_segment = f"            _target_order = {order_literal}\n"
        order_kwarg = f", order=_target_order"
    else:
        order_segment = ""
        order_kwarg = ""
    body = textwrap.dedent(f'''\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            _targets = {targets_literal}
            _menu = {menu_literal}
{order_segment}            if args and not args[0].startswith("-"):
                picked, rest_args = args[0], args[1:]
            else:
                picked, rest_args = None, args
            if picked is None:
                if len(_targets) == 1:
                    picked = next(iter(_targets))
                else:
                    picked = pick_target(_menu, {_pyrepr(cmd.desc + " — select target")}{order_kwarg})
                    if not picked:
                        return
            if picked not in _targets:
                err({_pyrepr(f"{cmd.name}: unknown target '")} + picked + "'")
                sys.exit(1)
            set_log_context({_pyrepr(cmd.name)}, picked)
            shell = _targets[picked]
            if shell.startswith('@'):
                run_command(shell[1:], rest_args)
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
        raise FragmentError(f"python action '{cmd.name}' needs 'run' (a Python block)")
    func_name = "cmd_" + _safe_ident(cmd.name)
    indented = textwrap.indent(cmd.run.strip(), "    ")
    log_cfg = cmd.meta.get("logging", {})
    retain_arg = f", retain={log_cfg['log_retain']}" if "log_retain" in log_cfg else ""
    if log_cfg.get("log") is False:
        log_line = ""
    elif "log_file" in log_cfg:
        log_line = f"    set_log_context_path({_pyrepr(log_cfg['log_file'])}{retain_arg})\n"
    else:
        log_line = ""
    body = (
        f"@command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})\n"
        f"def {func_name}(args):\n"
        f"{log_line}"
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



def render_npm(cmd: Command) -> str:
    """A command that runs an npm (or bun) script. run = script name (e.g. 'build', 'dev')."""
    if not cmd.run:
        raise FragmentError(f"npm command '{cmd.name}' requires 'run' (script name)")
    func_name = "cmd_" + _safe_ident(cmd.name)
    script_repr = _pyrepr(cmd.run)
    body = textwrap.dedent(f"""\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            _pkg = "bun" if (os.path.isfile("bun.lockb") or os.path.isfile("bun.lock")) else "npm"
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


def render_kill_port(cmd: Command) -> str:
    """A command that kills processes listening on one or more TCP ports, with confirmation.

    run = space-separated port numbers (defaults; can be overridden by CLI args at runtime).
    Requires the 'ports' module in the manifest's modules list.
    """
    func_name = "cmd_" + _safe_ident(cmd.name)
    raw = cmd.run.strip() if cmd.run else ""
    default_ports: list[str] = []
    if raw:
        for p in raw.split():
            if not p.isdigit():
                raise FragmentError(
                    f"kill-port command '{cmd.name}': run must be port numbers, got {p!r}"
                )
        default_ports = raw.split()

    desc = cmd.desc
    if not default_ports:
        desc = (desc + " [PORT]").strip() if desc else "[PORT]"

    lines = [
        f"@command({_pyrepr(cmd.name)}, {_pyrepr(desc)})",
        f"def {func_name}(args):",
    ]
    if default_ports:
        default_list = "[" + ", ".join(default_ports) + "]"
        lines += [
            f"    _to_kill = {default_list}",
            f"    if args:",
            f"        try:",
            f"            _to_kill = [int(a) for a in args]",
            f"        except ValueError as _e:",
            f'            err("kill-port: invalid port number: " + str(_e))',
            f"            sys.exit(1)",
        ]
    else:
        lines += [
            f"    if not args:",
            f'        err("kill-port: port argument required")',
            f"        sys.exit(1)",
            f"    try:",
            f"        _to_kill = [int(a) for a in args]",
            f"    except ValueError as _e:",
            f'        err("kill-port: invalid port number: " + str(_e))',
            f"        sys.exit(1)",
        ]
    lines += [
        f"    for _port in _to_kill:",
        f"        if not kill_port_interactive(_port):",
        f"            sys.exit(1)",
        "",
    ]
    return "\n".join(lines)


def render_action(cmd: Command) -> str:
    """Render an action using kind-based renderers (hidden from TUI/--help, callable via run_command)."""
    renderer = _ACTION_RENDERERS.get(cmd.kind)
    if renderer is None:
        raise FragmentError(
            f"unknown action kind '{cmd.kind}' for action '{cmd.name}' "
            f"(known kinds: {sorted(_ACTION_RENDERERS)})"
        )
    src = renderer(cmd)
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


def render_nuke_dev(cmd: Command) -> str:
    """Kill all dev-server processes for this project. Reads `[dev_kill]` config table.

    Body is a single function call; the config (ports, pidfiles, js_files,
    web_subdirs) lives entirely in the manifest. Requires the 'ports' module.
    """
    func_name = "cmd_" + _safe_ident(cmd.name)
    body = textwrap.dedent(f"""\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            nuke_dev_from_config(globals().get("_DEV_KILL_CONFIG") or {{}})
    """)
    return body


def render_clean_dev(cmd: Command) -> str:
    """Clean caches/outputs and (if corrupt or --force) drop the SQLite DB.

    Reads `[clean_dev]` config table. Calls cmd_kill first if `pre_kill = true`
    so file handles are released before rmtree. Requires the 'cleanup' module.
    """
    func_name = "cmd_" + _safe_ident(cmd.name)
    body = textwrap.dedent(f"""\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            _cfg = globals().get("_CLEAN_DEV_CONFIG") or {{}}
            if _cfg.get("pre_kill", True) and "cmd_kill" in globals():
                cmd_kill(args)
            clean_dev_from_config(_cfg, force=("--force" in args or "--db" in args))
    """)
    return body


def render_install_template(cmd: Command) -> str:
    """Install an Unraid Docker GUI template via scp.

    Reads `[install_template]` config (src, name) and UNRAID_HOST/UNRAID_USER
    env. Body is a single function call into admin_lib.unraid; the heavy
    lifting (default src, name from APP_IMAGE, dst path conventions) lives
    there. Requires the 'unraid' module.
    """
    func_name = "cmd_" + _safe_ident(cmd.name)
    body = textwrap.dedent(f"""\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            return install_docker_template_from_config(globals().get("_INSTALL_TEMPLATE_CONFIG") or {{}})
    """)
    return body


def render_server_reload(cmd: Command) -> str:
    """Send SIGUSR1 to dev-loop pidfiles to trigger an in-process rebuild.

    Pairs with `run_with_reload` (and therefore `vite-dev` / `docker-reload-loop`):
    those write /tmp/admin-run-<target>.pid and trap SIGUSR1 to restart the
    inner process. Useful as `commands.reload` so editors can wire a save
    hook to `./admin reload` without spawning a fresh dev session.

    Optional `args[0]`: target name substring filter ("dev" by default
    matches `cmd_dev`'s pidfile if there's only one target).

    Requires the 'docker' module.
    """
    func_name = "cmd_" + _safe_ident(cmd.name)
    body = textwrap.dedent(f"""\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            return reload_running_targets(args[0] if args else None)
    """)
    return body


def render_vite_dev(cmd: Command) -> str:
    """Run a Vite frontend + backend reload loop with project-local data dirs.

    Reads `[vite_dev]` config table. Requires the 'web_dev' module.
    """
    func_name = "cmd_" + _safe_ident(cmd.name)
    body = textwrap.dedent(f"""\
        @command({_pyrepr(cmd.name)}, {_pyrepr(cmd.desc)})
        def {func_name}(args):
            _cfg = globals().get("_VITE_DEV_CONFIG") or {{}}
            if _cfg.get("pre_kill") and "cmd_kill" in globals():
                cmd_kill(args)
            return vite_dev_from_config(_cfg)
    """)
    return body


_ACTION_RENDERERS = {
    "shell": render_shell,
    "multi-target": render_multi_target,
    "apple-dev-loop": render_apple_dev_loop,
    "docker-reload-loop": render_docker_reload_loop,
    "python": render_python,
    "http": render_http,
    "npm": render_npm,
    "sync": render_sync,
    "apple": render_apple,
    "kill-port": render_kill_port,
    "nuke-dev": render_nuke_dev,
    "clean-dev": render_clean_dev,
    "vite-dev": render_vite_dev,
    "server-reload": render_server_reload,
    "install-template": render_install_template,
}

# Kinds whose action body is generated from a named manifest config table
# rather than a `run` string.
CONFIGLESS_KINDS = frozenset({
    "nuke-dev", "clean-dev", "vite-dev", "server-reload", "install-template",
})

KNOWN_KINDS = set(_ACTION_RENDERERS)


# Action kinds that pull additional admin_lib modules into the bundle. The
# resolver consults this so projects don't have to declare these modules
# explicitly — using a `kind` is enough.
KIND_MODULES: dict[str, list[str]] = {
    "kill-port": ["ports", "ports_nuke"],
    "nuke-dev": ["ports", "ports_nuke"],
    "docker-reload-loop": ["docker"],
    "server-reload": ["docker"],
    "install-template": ["remote", "unraid"],
}


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
    config_tables: dict[str, dict] | None = None,
) -> list[str]:
    """Scan command run-strings, URL values, log-target paths/hosts, AND
    config tables for ${VAR} placeholders and emit register_env_var calls
    with origin breadcrumbs.

    Each call lets the runtime `env` command list referenced vars even before they're
    resolved, and annotate which command/url/log-target referenced each one. Returns
    Python source lines (no trailing newline).

    `config_tables` is `{table_name: dict}` for the new configless-kind tables
    ([deploy], [vite_dev], [dev_kill], [clean_dev]). Every `${VAR}` placeholder
    found anywhere in those tables is registered, AND any `{env, default?}`
    sub-table (the convention used by clean_dev.paths, dev_kill.pidfiles,
    vite_dev.mounts) is registered as `register_env_var(env, default=…)`.

    The origin-string format ("cmd:<name>", "url:<key>", "logs.<target>.<env>",
    "<table>:…") is a stable compatibility contract shared with the runtime
    renderer; see the contract block above `register_env_var` in
    admin_lib/core.py before changing it.
    """
    seen: dict[str, dict] = {}

    for cmd in commands:
        origin = f"cmd:{cmd.name}"
        if cmd.kind != "steps":
            _scan_for_env_vars(cmd.run, _ENV_VAR_RE, seen, origin)
        if cmd.kind == "python":
            _scan_for_env_vars(cmd.run, _PY_ENV_RE, seen, origin)
        if cmd.kind == "http" and cmd.meta.get("body"):
            _scan_for_env_vars(cmd.meta["body"], _ENV_VAR_RE, seen, origin)
        for tname, tval in cmd.targets.items():
            _scan_for_env_vars(tval, _ENV_VAR_RE, seen, f"{origin}:{tname}")

    for key, text in (urls or {}).items():
        _scan_for_env_vars(text, _ENV_VAR_RE, seen, f"url:{key}")

    for target_name, target in (logs or {}).items():
        for env_name, env_spec in target.get("envs", {}).items():
            origin = f"logs.{target_name}.{env_name}"
            for field in ("path", "host", "user"):
                val = env_spec.get(field)
                if isinstance(val, str):
                    _scan_for_env_vars(val, _ENV_VAR_RE, seen, origin)

    for table_name, table in (config_tables or {}).items():
        if table:
            _scan_config_table(table, _ENV_VAR_RE, seen, f"{table_name}")

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


def _scan_config_table(value, pattern, seen, origin):
    """Recursively scan a manifest config table for ${VAR} placeholders and
    `{env, default?}` sub-tables. Registers each found var into `seen`.

    Strings: scanned via `pattern` (the `${VAR:-default}` regex).
    Dicts containing an `env` key: registered directly as that env var, with
    `default` taken from the same dict if present (matches the runtime
    `os.environ.get(env, default)` shape used by all configless adapters).
    Dicts and lists: recursed into.
    """
    if isinstance(value, str):
        _scan_for_env_vars(value, pattern, seen, origin)
        return
    if isinstance(value, dict):
        if isinstance(value.get("env"), str) and "{" not in value["env"] and "$" not in value["env"]:
            name = value["env"]
            entry = seen.setdefault(name, {"default": None, "used_by": []})
            if entry["default"] is None and "default" in value:
                entry["default"] = str(value["default"])
            if origin not in entry["used_by"]:
                entry["used_by"].append(origin)
        for k, v in value.items():
            _scan_config_table(v, pattern, seen, f"{origin}.{k}")
        return
    if isinstance(value, list):
        for i, item in enumerate(value):
            _scan_config_table(item, pattern, seen, f"{origin}[{i}]")
        return
