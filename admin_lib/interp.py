"""Runtime manifest interpreter (ADR-0006).

The default execution path: instead of generating a self-contained ./admin
script, the installed tool reads admin.toml, merges archetypes, and registers
real command/action callables into the admin_lib.core registry, then dispatches
via boot(). `admin compile` packages this same interpreter + admin_lib + the
manifest into a standalone zipapp.

Each function here is the runtime equivalent of one renderer that used to live
in gen/fragments.py: where the renderer emitted a string of Python, the
interpreter builds the live closure that does the same work. Because these are
real functions, tracebacks point at this file with real line numbers — the
reason Design 1 (full port, no exec of generated strings) was chosen over
exec'ing rendered text.

The two kinds that are inherently source-as-string — `kind = "python"` (a Python
block authored in an archetype/manifest) and the `[inline]` escape file — are
compiled and exec'd in a flat namespace that mirrors the old bundle's module
globals. For `[inline]` the real file path is used so its tracebacks stay real;
for `kind = "python"` a synthetic `<python-action:NAME>` filename is used, since
that source only ever exists as a string.
"""
from __future__ import annotations

import os
import sys

from admin_lib import core
from admin_lib.core import (
    add_spacer,
    command,
    configure_logging,
    err,
    info,
    ok,
    pick_target,
    print_url_status,
    register_action,
    register_builtin_logs_command,
    register_env_var,
    resolve_env,
    run_cmd,
    run_command,
    set_log_context,
    set_log_context_path,
    set_urls,
    sync,
    _apply_cmd_env,
)


class InterpError(Exception):
    pass


# Config tables exposed as module-level globals to kind="python" / [inline] code,
# keyed by the global name the body reads (e.g. globals().get("_APPLE_CONFIG")).
_CONFIG_GLOBALS = {
    "_APPLE_CONFIG": "apple",
    "_SERVER_CONFIG": "server",
    "_OUTPUT_CONFIG": "output",
    "_DEV_KILL_CONFIG": "dev_kill",
    "_CLEAN_DEV_CONFIG": "clean_dev",
    "_VITE_DEV_CONFIG": "vite_dev",
    "_DOCKER_DEV_CONFIG": "docker_dev",
    "_DOCKER_RUN_CONFIG": "docker_run",
    "_DEPLOY_CONFIG": "deploy",
    "_INSTALL_TEMPLATE_CONFIG": "install_template",
    "_FIXTURES_CONFIG": "fixtures",
}


# ---------------------------------------------------------------------------
# Flat namespace (mirrors the old bundle's single module scope)
# ---------------------------------------------------------------------------

def build_namespace(resolved) -> dict:
    """Return a dict mirroring the flat namespace a bundled ./admin would have.

    Imports each admin_lib module the manifest pulls in (in canonical bundle
    order so later modules shadow earlier on name collisions, matching the
    bundler), copies its module-level names, and injects the [apple]/[server]/…
    config dicts under the global names that kind="python" bodies read.

    Used only as the globals() for exec'd kind="python" / [inline] code; the
    ported kind closures call admin_lib functions directly and don't need it.
    """
    from gen.archetypes import _order_modules

    ns: dict = {}
    for mod_name in _order_modules(set(resolved.modules)):
        try:
            mod = __import__(f"admin_lib.{mod_name}", fromlist=["*"])
        except ImportError:
            continue
        for k, v in vars(mod).items():
            if not k.startswith("__"):
                ns[k] = v

    m = resolved.manifest
    for global_name, table_attr in _CONFIG_GLOBALS.items():
        ns[global_name] = getattr(m, table_attr, None) or {}

    ns.setdefault("os", os)
    ns.setdefault("sys", sys)
    return ns


# Core module globals that boot()/set_log_context reassign *after* a body's
# namespace is built. A bundled ./admin read these as live module globals; the
# interpreter must re-sync them into an exec namespace at call time so bare-name
# reads (e.g. `LOG_FILE`) in kind="python" / [inline] bodies see current values.
_REASSIGNABLE_CORE_GLOBALS = ("LOG_DIR", "LOG_FILE", "LOG_CONFIG", "_project_root", "_urls")


def _sync_core_globals(g: dict) -> None:
    for k in _REASSIGNABLE_CORE_GLOBALS:
        g[k] = getattr(core, k)


# ---------------------------------------------------------------------------
# Per-kind closure factories (runtime equivalents of gen/fragments renderers)
# ---------------------------------------------------------------------------

def _make_shell(cmd, ns):
    run = cmd.run
    name = cmd.name
    pty = bool(cmd.meta.get("pty"))
    watch_kind = cmd.meta.get("watch")

    def _fn(args):
        info(f"running: {run}")
        collected = [] if watch_kind else None
        rc = run_cmd(run, pty=pty, collect=collected) if pty else run_cmd(run, collect=collected)
        if rc != 0:
            err(name + " failed")
            sys.exit(rc)
        if watch_kind:
            from admin_lib.watch import run_watcher
            run_watcher(watch_kind, "".join(collected))
        ok(name + " complete")
    return _fn


def _make_shell_passthrough(cmd, ns):
    import shlex
    import subprocess
    run = cmd.run
    name = cmd.name
    interactive = bool(cmd.meta.get("interactive"))

    def _fn(args):
        quoted = " ".join(shlex.quote(a) for a in args)
        cmdline = run + ((" " + quoted) if quoted else "")
        info("running: " + cmdline)
        if interactive:
            warn = core.warn
            warn("interactive mode: output is not logged to tmp/run.log")
            rc = subprocess.call(cmdline, shell=True)
        else:
            rc = run_cmd(cmdline)
        if rc != 0:
            err(name + " failed")
            sys.exit(rc)
        ok(name + " complete")
    return _fn


def _make_python_script(cmd, ns):
    import shlex
    run = cmd.run
    name = cmd.name

    def _fn(args):
        if not os.path.exists(run):
            err(name + ": script not found: " + run)
            sys.exit(1)
        quoted = " ".join(shlex.quote(a) for a in args)
        cmdline = shlex.quote(sys.executable) + " " + shlex.quote(run) + ((" " + quoted) if quoted else "")
        info("running: " + cmdline)
        rc = run_cmd(cmdline)
        sys.exit(rc)
    return _fn


def _make_multi_target(cmd, ns):
    name = cmd.name
    desc = cmd.desc
    targets = dict(cmd.targets)
    target_descs = cmd.meta.get("target_descs", {})
    menu = {k: target_descs.get(k, k) for k in targets}
    target_order = cmd.meta.get("target_order")
    target_pty = cmd.meta.get("target_pty", {})
    target_watch = cmd.meta.get("target_watch", {})

    # Resolve per-target requires at build time: parent default unless the child
    # overrides (None = inherit, list = replace). Mirrors render_multi_target.
    parent_requires = cmd.meta.get("requires") or []
    target_requires_meta = cmd.meta.get("target_requires", {})
    resolved_target_requires = {}
    for tname in targets:
        override = target_requires_meta.get(tname)
        resolved_target_requires[tname] = list(parent_requires) if override is None else list(override)
    has_requires = any(resolved_target_requires.values())

    def _fn(args):
        if args and not args[0].startswith("-"):
            picked, rest_args = args[0], args[1:]
        else:
            picked, rest_args = None, args
        if picked is None:
            if len(targets) == 1:
                picked = next(iter(targets))
            else:
                picked = pick_target(menu, desc + " — select target", order=target_order)
                if not picked:
                    return
        if picked not in targets:
            err(f"{name}: unknown target '" + picked + "'")
            sys.exit(1)
        set_log_context(name, picked)
        if has_requires:
            reqs = resolved_target_requires.get(picked) or []
            if reqs:
                from admin_lib.guards import run_requires
                rc = run_requires(reqs)
                if rc != 0:
                    sys.exit(rc)
        shell = targets[picked]
        if shell.startswith("@"):
            run_command(shell[1:], rest_args)
        else:
            collected = [] if target_watch.get(picked) else None
            info(f"running [{name}:" + picked + "]: " + shell)
            rc = run_cmd(shell, pty=target_pty.get(picked, False), collect=collected)
            if rc != 0:
                err(name + " failed")
                sys.exit(rc)
            if target_watch.get(picked):
                from admin_lib.watch import run_watcher
                run_watcher(target_watch[picked], "".join(collected))
            ok(name + " complete")
    return _fn


def _make_docker_reload_loop(cmd, ns):
    run = cmd.run
    name = cmd.name

    def _fn(args):
        from admin_lib.docker import run_with_reload
        from admin_lib.log_columns import make_column_formatter
        info("reload loop: " + run)
        fmt = make_column_formatter("docker", ns.get("_OUTPUT_CONFIG"))
        run_with_reload(run, target=name, formatter=fmt.format_line)
    return _fn


def _make_python(cmd, ns):
    """Compile a kind="python" body into a real function.

    The body is the function body authored in an archetype/manifest. It is
    compiled once under a synthetic filename (its source only ever exists as a
    string) and runs with `ns` as globals so it sees the flat admin_lib names
    and the _*_CONFIG dicts, exactly as the bundled equivalent did.
    """
    import textwrap
    name = cmd.name
    if not cmd.run:
        raise InterpError(f"python action '{name}' needs 'run' (a Python block)")
    log_cfg = cmd.meta.get("logging", {})
    src = "def __py_action(args):\n" + textwrap.indent(cmd.run.strip(), "    ") + "\n"
    code = compile(src, f"<python-action:{name}>", "exec")
    local_ns = dict(ns)
    exec(code, local_ns)
    fn = local_ns["__py_action"]

    retain = log_cfg.get("log_retain")
    log_file = log_cfg.get("log_file")

    def _fn(args):
        if log_file is not None:
            if retain is not None:
                set_log_context_path(log_file, retain=retain)
            else:
                set_log_context_path(log_file)
        # Refresh reassignable core globals (LOG_FILE, etc.) so the body sees
        # post-boot / post-set_log_context values rather than the build-time snapshot.
        _sync_core_globals(local_ns)
        return fn(args)
    return _fn


def _make_http(cmd, ns):
    import urllib.request as ur
    import urllib.error as ue
    url_tmpl = cmd.run
    method = cmd.meta.get("method", "POST")
    body_tmpl = cmd.meta.get("body", "")
    warn = core.warn

    def _fn(args):
        url = resolve_env(url_tmpl)
        info(f"http {method} " + url)
        if body_tmpl:
            data = resolve_env(body_tmpl).encode()
            req = ur.Request(url, data=data, headers={"Content-Type": "application/json"}, method=method)
        else:
            req = ur.Request(url, method=method)
        try:
            with ur.urlopen(req, timeout=15) as resp:
                if resp.status >= 400:
                    warn("http " + str(resp.status) + " from " + url)
                else:
                    ok("done")
        except (ue.URLError, OSError) as e:
            warn("http request failed: " + str(e))
    return _fn


def _make_npm(cmd, ns):
    script = cmd.run

    def _fn(args):
        pkg = "bun" if (os.path.isfile("bun.lockb") or os.path.isfile("bun.lock")) else "npm"
        rc = run_cmd(pkg + " run " + script)
        if rc != 0:
            err(pkg + " run " + script + " failed")
            sys.exit(rc)
        ok(pkg + " run " + script + " complete")
    return _fn


def _make_apple(cmd, ns):
    func_name = cmd.run

    def _fn(args):
        from admin_lib import apple as apple_mod
        getattr(apple_mod, func_name)(ns.get("_APPLE_CONFIG") or {})
    return _fn


def _make_sync(cmd, ns):
    dest = cmd.run
    src = cmd.meta.get("src", "dist/")

    def _fn(args):
        rc = sync(src, resolve_env(dest))
        if rc != 0:
            sys.exit(rc)
    return _fn


def _make_kill_port(cmd, ns):
    name = cmd.name
    raw = cmd.run.strip() if cmd.run else ""
    default_ports = []
    if raw:
        for p in raw.split():
            if not p.isdigit():
                raise InterpError(f"kill-port command '{name}': run must be port numbers, got {p!r}")
        default_ports = [int(p) for p in raw.split()]

    def _parse_ports(values):
        try:
            return [int(a) for a in values]
        except ValueError as e:
            err("kill-port: invalid port number: " + str(e))
            sys.exit(1)

    def _fn(args):
        from admin_lib.ports_nuke import kill_port_interactive
        if default_ports:
            to_kill = _parse_ports(args) if args else default_ports
        else:
            if not args:
                err("kill-port: port argument required")
                sys.exit(1)
            to_kill = _parse_ports(args)
        for port in to_kill:
            if not kill_port_interactive(port):
                sys.exit(1)
    return _fn


def _make_nuke_dev(cmd, ns):
    def _fn(args):
        from admin_lib.ports_nuke import nuke_dev_from_config
        nuke_dev_from_config(ns.get("_DEV_KILL_CONFIG") or {})
    return _fn


def _kill_command_exists():
    """True if a command/action named 'kill' is registered (configless pre_kill)."""
    return "kill" in core._all_commands() or "kill" in core._actions


def _make_clean_dev(cmd, ns):
    def _fn(args):
        from admin_lib.cleanup import clean_dev_from_config
        cfg = ns.get("_CLEAN_DEV_CONFIG") or {}
        if cfg.get("pre_kill", True) and _kill_command_exists():
            run_command("kill", args)
        clean_dev_from_config(cfg, force=("--force" in args or "--db" in args))
    return _fn


def _make_fixtures_generate(cmd, ns):
    def _fn(args):
        from admin_lib.fixtures import fixtures_generate_from_config
        rc = fixtures_generate_from_config(ns.get("_FIXTURES_CONFIG") or {}, force=("--force" in args))
        if rc:
            sys.exit(rc)
    return _fn


def _make_fixtures_reset(cmd, ns):
    def _fn(args):
        from admin_lib.fixtures import fixtures_reset_from_config
        rc = fixtures_reset_from_config(ns.get("_FIXTURES_CONFIG") or {})
        if rc:
            sys.exit(rc)
    return _fn


def _make_vite_dev(cmd, ns):
    def _fn(args):
        from admin_lib.web_dev import vite_dev_from_config
        cfg = ns.get("_VITE_DEV_CONFIG") or {}
        if cfg.get("pre_kill") and _kill_command_exists():
            run_command("kill", args)
        return vite_dev_from_config(cfg)
    return _fn


def _make_docker_dev(cmd, ns):
    def _fn(args):
        from admin_lib.docker import docker_dev_from_config
        cfg = dict(ns.get("_DOCKER_DEV_CONFIG") or {})
        if cfg.get("pre_kill") and _kill_command_exists():
            cfg["_pre_kill_callable"] = lambda a: run_command("kill", a)
        return docker_dev_from_config(cfg)
    return _fn


def _make_server_reload(cmd, ns):
    def _fn(args):
        from admin_lib.docker import reload_running_targets
        return reload_running_targets(args[0] if args else None)
    return _fn


def _make_install_template(cmd, ns):
    def _fn(args):
        from admin_lib.unraid import install_docker_template_from_config
        return install_docker_template_from_config(ns.get("_INSTALL_TEMPLATE_CONFIG") or {})
    return _fn


def _make_interactive_shell(cmd, ns):
    run = cmd.run
    name = cmd.name
    keys = cmd.keys
    if not run:
        raise InterpError(f"interactive-shell command '{name}' needs 'run'")

    def _fn(args):
        from admin_lib.keybindings import run_with_keybindings
        from admin_lib.log_format import make_formatter
        info(f"running: {run}")
        fmt = make_formatter()
        rc = run_with_keybindings(run, keys, label=name, formatter=fmt)
        if rc not in (0, 130):
            err(name + " failed")
            sys.exit(rc)
        ok(name + " complete")
    return _fn


def _make_print_urls(cmd, ns):
    def _fn(args):
        print_url_status()
    return _fn


_FACTORIES = {
    "shell": _make_shell,
    "shell-passthrough": _make_shell_passthrough,
    "multi-target": _make_multi_target,
    "apple-dev-loop": _make_multi_target,  # same shape as multi-target
    "docker-reload-loop": _make_docker_reload_loop,
    "docker-dev": _make_docker_dev,
    "interactive-shell": _make_interactive_shell,
    "python": _make_python,
    "python-script": _make_python_script,
    "http": _make_http,
    "npm": _make_npm,
    "sync": _make_sync,
    "apple": _make_apple,
    "kill-port": _make_kill_port,
    "nuke-dev": _make_nuke_dev,
    "clean-dev": _make_clean_dev,
    "fixtures-generate": _make_fixtures_generate,
    "fixtures-reset": _make_fixtures_reset,
    "vite-dev": _make_vite_dev,
    "server-reload": _make_server_reload,
    "install-template": _make_install_template,
    "print-urls": _make_print_urls,
}


# ---------------------------------------------------------------------------
# Command (step-runner) builder
# ---------------------------------------------------------------------------

def _make_step_runner(cmd):
    name = cmd.name
    steps = cmd.meta.get("steps", [])
    if not steps:
        raise InterpError(f"command '{name}' has no steps")
    log_cfg = cmd.meta.get("logging", {})
    env_map = cmd.meta.get("env")

    def _fn(args):
        if log_cfg.get("log") is False:
            pass
        elif "log_file" in log_cfg:
            set_log_context_path(log_cfg["log_file"])
        else:
            retain = log_cfg.get("log_retain")
            if retain is not None:
                set_log_context(name, retain=retain)
            else:
                set_log_context(name)
        if env_map:
            _apply_cmd_env(env_map)
        for step in steps:
            run_command(step, args)
    return _fn


# ---------------------------------------------------------------------------
# Action builder (kind dispatch + requires injection)
# ---------------------------------------------------------------------------

def _build_action_fn(cmd, ns):
    factory = _FACTORIES.get(cmd.kind)
    if factory is None:
        raise InterpError(
            f"unknown action kind '{cmd.kind}' for action '{cmd.name}' "
            f"(known kinds: {sorted(_FACTORIES)})"
        )
    fn = factory(cmd, ns)

    # Flat actions (non multi-target) run their `requires` guards before the body.
    # Multi-target / apple-dev-loop handle per-target requires inside the closure.
    requires = cmd.meta.get("requires") or []
    if requires and cmd.kind not in ("multi-target", "apple-dev-loop"):
        inner = fn

        def _guarded(args):
            from admin_lib.guards import run_requires
            rc = run_requires(requires)
            if rc != 0:
                sys.exit(rc)
            return inner(args)
        return _guarded
    return fn


# ---------------------------------------------------------------------------
# Registration + dispatch
# ---------------------------------------------------------------------------

def register_all(resolved, project_name, inline_body=None):
    """Register every env var, url, guard, action, and command from a resolved
    manifest into the live admin_lib.core registry. Returns the flat namespace
    used for kind="python" / [inline] code.
    """
    from gen.manifest import SPACER

    m = resolved.manifest
    ns = build_namespace(resolved)

    # --- env vars (for the `env` command) -----------------------------------
    _register_env_vars(resolved, m)

    # --- urls ----------------------------------------------------------------
    if resolved.urls:
        set_urls([(label, url) for label, url in resolved.urls.items()])

    # --- config-derived runtime setup ---------------------------------------
    if m.logging:
        configure_logging(m.logging)
    if m.log_bridge:
        lb_cfg = {**m.log_bridge, "project": m.project_name}
        if isinstance(lb_cfg.get("hosts"), list):
            lb_cfg["hosts"] = [resolve_env(h) for h in lb_cfg["hosts"]]
        from admin_lib.log_bridge import configure_log_bridge
        configure_log_bridge(lb_cfg)

    # --- guards --------------------------------------------------------------
    if m.guards:
        from admin_lib.guards import register_shell_guard
        for gname, gdef in m.guards.items():
            register_shell_guard(gname, gdef["run"], gdef["fail"])

    # --- actions (must register before commands that call them) -------------
    for action in resolved.actions.values():
        register_action(action.name, _build_action_fn(action, ns))

    # --- commands (display order, with spacers) -----------------------------
    for entry in resolved.display_order:
        if entry == SPACER:
            add_spacer()
            continue
        cmd = resolved.commands[entry]
        command(cmd.name, cmd.desc)(_make_step_runner(cmd))

    # --- logs command --------------------------------------------------------
    emit_builtin_logs = "logs" not in resolved.commands and "logs" not in m.hide_commands
    if m.logs:
        from admin_lib.logtail import register_logs_command
        register_logs_command(m.logs)
    elif emit_builtin_logs:
        register_builtin_logs_command()

    # --- inline escape file --------------------------------------------------
    if inline_body is not None:
        inline_path = m.inline_file or "<inline>"
        code = compile(inline_body, inline_path, "exec")
        exec(code, ns)

    # Expose registered callables under their bundled cmd_/_act_ names so any
    # kind="python"/[inline] body using the historical `"cmd_kill" in globals()`
    # pattern still resolves. Done after registration so all names exist.
    from gen.fragments import _safe_ident
    for cname, cmd in resolved.commands.items():
        entry = core._commands.get(cname)
        if entry and "func" in entry:
            ns["cmd_" + _safe_ident(cname)] = entry["func"]
    for aname, fn in core._actions.items():
        ns["_act_" + _safe_ident(aname)] = fn

    return ns


def _register_env_vars(resolved, m):
    """Scan the manifest for ${VAR} references and register each live so the
    `env` command can list them. Shares the scanner with the manifest layer.
    """
    from gen.fragments import scan_env_vars

    command_list = list(resolved.commands.values()) + list(resolved.actions.values())
    config_tables = {
        "deploy": m.deploy, "vite_dev": m.vite_dev, "docker_dev": m.docker_dev,
        "docker_run": m.docker_run, "dev_kill": m.dev_kill, "clean_dev": m.clean_dev,
        "install_template": m.install_template, "fixtures": m.fixtures,
    }
    seen = scan_env_vars(command_list, resolved.urls, m.logs, config_tables)
    for vname, entry in seen.items():
        register_env_var(vname, default=entry["default"], used_by=entry["used_by"])


def interpret(resolved, project_root, argv, inline_body=None):
    """Register the manifest and dispatch argv (the project command + args).

    project_root is the directory containing admin.toml; argv is sys.argv-style
    (argv[0] ignored, argv[1] = command). Mirrors core.boot but with an explicit
    root instead of inferring it from the caller's __file__.
    """
    m = resolved.manifest
    # Load .env BEFORE register_all: register_all eagerly resolves env-bearing
    # config (e.g. [log_bridge].hosts via resolve_env), so .env must already be in
    # os.environ or those ${VAR:-default} expansions fall back to their defaults.
    # boot() calls _load_dotenv again, but it uses setdefault so this is idempotent.
    core._load_dotenv(project_root)
    register_all(resolved, m.project_name, inline_body=inline_body)
    core.boot(m.project_name, project_root=project_root, argv=argv)
