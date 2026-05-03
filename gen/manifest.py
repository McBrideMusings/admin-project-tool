"""admin.toml manifest loader.

Parses admin.toml with stdlib tomllib (Python 3.11+), validates schema,
returns a normalized Manifest dataclass. Env var placeholders (${VAR},
${VAR:-default}) are preserved verbatim — they're resolved at runtime
inside the generated ./admin script, not here.
"""
from __future__ import annotations

import re
import sys
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

MANIFEST_FILENAME = "admin.toml"

_KNOWN_KEYS = {
    "project_name", "python_min", "archetypes", "modules", "urls",
    "commands", "actions", "order", "logs", "apple", "server", "output",
    "exclude_modules", "generator_commit", "inline", "logging",
    "dev_kill", "clean_dev", "vite_dev", "deploy",
}

VALID_COMMAND_KEYS = {"desc", "steps", "log_file", "log_retain", "log", "group", "priority"}
VALID_ACTION_KEYS = {"run", "desc", "kind", "targets", "method", "body", "src", "log_file", "log_retain", "log"}


@dataclass
class Command:
    """A single admin command, post-merge."""
    name: str
    run: str                       # shell string (may contain ${VAR}); "" for sequence/http kinds
    desc: str
    kind: str = "shell"            # shell | multi-target | apple-dev-loop | docker-reload-loop | python | http | sequence
    source: str = "manifest"       # which archetype contributed it (or "manifest")
    targets: dict = field(default_factory=dict)  # target name -> shell string (for multi-target kinds)
    meta: dict = field(default_factory=dict)     # kind-specific extras: {steps} for sequence, {method, body} for http
    group: int = 0                 # display group; commands with different groups get automatic spacers between them
    priority: int = 0              # sort order within a group; ties broken alphabetically


SPACER = ""  # sentinel used inside Manifest.order to denote a spacer row


@dataclass
class Manifest:
    project_name: str
    python_min: str
    archetypes: list[str]
    modules: list[str]                       # extra admin_lib modules to bundle (beyond archetype deps)
    urls: dict[str, str]                     # label -> url (may contain ${VAR})
    overrides: dict[str, dict]               # command name -> partial command dict
    custom: dict[str, dict]                  # command name -> full command dict
    order: list[str]                         # explicit display order; "" = spacer
    logs: dict[str, dict] = field(default_factory=dict)  # target -> {default_env, envs: {env: {path, host, user}}}
    apple: dict = field(default_factory=dict)   # [apple] table — archetype-specific config, raw dict
    server: dict = field(default_factory=dict)  # [server] table — server build/dev/deploy config, raw dict
    output: dict = field(default_factory=dict)  # [output] table — log column config per source
    logging: dict = field(default_factory=dict) # [logging] table — file logging config (enabled, dir, retain)
    dev_kill: dict = field(default_factory=dict)  # [dev_kill] — config for kind="nuke-dev" commands
    clean_dev: dict = field(default_factory=dict) # [clean_dev] — config for kind="clean-dev" commands
    vite_dev: dict = field(default_factory=dict)  # [vite_dev] — config for kind="vite-dev" commands
    deploy: dict = field(default_factory=dict)    # [deploy] — config for docker-unraid build/deploy commands
    actions: dict[str, dict] = field(default_factory=dict)    # action name -> raw dict (not shown in UI)
    exclude_modules: list[str] = field(default_factory=list)  # modules to suppress even if archetype requires them
    generator_commit: str | None = None
    inline_file: str | None = None           # path relative to project root
    raw: dict = field(default_factory=dict)  # original parsed TOML, for debugging


class ManifestError(Exception):
    pass


def load(project_dir: Path) -> Manifest:
    """Load and validate admin.toml from project_dir. Raises ManifestError."""
    path = project_dir / MANIFEST_FILENAME
    if not path.exists():
        raise ManifestError(f"no {MANIFEST_FILENAME} at {project_dir}")
    try:
        data = tomllib.loads(path.read_text())
    except tomllib.TOMLDecodeError as e:
        raise ManifestError(f"{path}: invalid TOML: {e}") from e
    return parse(data, source=str(path))


def parse(data: dict, source: str = "<dict>") -> Manifest:
    """Parse a raw TOML dict into a Manifest. Validates required fields."""
    if not isinstance(data, dict):
        raise ManifestError(f"{source}: top-level must be a table")

    project_name = data.get("project_name")
    if not isinstance(project_name, str) or not project_name:
        raise ManifestError(f"{source}: 'project_name' is required (string)")

    python_min = data.get("python_min", "3.11")
    if not isinstance(python_min, str):
        raise ManifestError(f"{source}: 'python_min' must be a string")
    if not re.fullmatch(r"\d+\.\d+", python_min):
        raise ManifestError(f"{source}: 'python_min' must be MAJOR.MINOR (e.g. '3.11'), got '{python_min}'")

    archetypes = data.get("archetypes", [])
    if not isinstance(archetypes, list) or not all(isinstance(a, str) for a in archetypes):
        raise ManifestError(f"{source}: 'archetypes' must be a list of strings")

    modules = data.get("modules", [])
    if not isinstance(modules, list) or not all(isinstance(m, str) for m in modules):
        raise ManifestError(f"{source}: 'modules' must be a list of strings")

    urls = data.get("urls", {}) or {}
    if not isinstance(urls, dict):
        raise ManifestError(f"{source}: 'urls' must be a table")
    urls = {str(k): str(v) for k, v in urls.items()}

    raw_commands = data.get("commands", {}) or {}
    if not isinstance(raw_commands, dict):
        raise ManifestError(f"{source}: 'commands' must be a table")

    custom = raw_commands.pop("custom", {}) or {}
    if not isinstance(custom, dict):
        raise ManifestError(f"{source}: 'commands.custom' must be a table")

    overrides: dict[str, dict] = {}
    for name, val in raw_commands.items():
        if not isinstance(val, dict):
            raise ManifestError(
                f"{source}: commands.{name} must be a table with 'steps' (a list of action names)"
            )
        bad = set(val) - VALID_COMMAND_KEYS
        if bad:
            kind_hint = " Move kind/run/targets to [actions]." if bad & {"kind", "run", "targets"} else ""
            raise ManifestError(
                f"{source}: commands.{name} has invalid keys {sorted(bad)}. "
                f"Commands may only have 'desc', 'steps', and log overrides.{kind_hint}"
            )
        overrides[name] = val

    custom_normalized: dict[str, dict] = {}
    for name, val in custom.items():
        if not isinstance(val, dict):
            raise ManifestError(
                f"{source}: commands.custom.{name} must be a table with 'steps' (a list of action names). "
                f"Move run/kind/targets to [actions]."
            )
        bad = set(val) - VALID_COMMAND_KEYS
        if bad:
            kind_hint = " Move kind/run/targets to [actions]." if bad & {"kind", "run", "targets"} else ""
            raise ManifestError(
                f"{source}: commands.custom.{name} has invalid keys {sorted(bad)}. "
                f"Commands may only have 'desc', 'steps', and log overrides.{kind_hint}"
            )
        if "steps" not in val:
            raise ManifestError(
                f"{source}: commands.custom.{name} must have 'steps' (a list of action names). "
                f"Move run/kind/targets to [actions]."
            )
        custom_normalized[name] = val

    raw_actions = data.get("actions", {}) or {}
    if not isinstance(raw_actions, dict):
        raise ManifestError(f"{source}: 'actions' must be a table")
    actions: dict[str, dict] = {}
    for name, val in raw_actions.items():
        if not isinstance(val, dict):
            raise ManifestError(f"{source}: actions.{name} must be a table")
        actions[name] = val

    inline = data.get("inline", {}) or {}
    inline_file = inline.get("file") if isinstance(inline, dict) else None

    raw_order = data.get("order", [])
    if not isinstance(raw_order, list) or not all(isinstance(x, str) for x in raw_order):
        raise ManifestError(f"{source}: 'order' must be a list of strings")
    # Normalize spacer tokens ("" and "---") to the SPACER sentinel.
    order: list[str] = []
    for entry in raw_order:
        if entry == "" or entry == "---":
            order.append(SPACER)
        else:
            order.append(entry)

    logs = _parse_logs(data.get("logs"), source)

    apple = data.get("apple", {}) or {}
    if not isinstance(apple, dict):
        raise ManifestError(f"{source}: '[apple]' must be a table")

    server = data.get("server", {}) or {}
    if not isinstance(server, dict):
        raise ManifestError(f"{source}: '[server]' must be a table")

    output = data.get("output", {}) or {}
    if not isinstance(output, dict):
        raise ManifestError(f"{source}: '[output]' must be a table")

    dev_kill = data.get("dev_kill", {}) or {}
    if not isinstance(dev_kill, dict):
        raise ManifestError(f"{source}: '[dev_kill]' must be a table")

    clean_dev = data.get("clean_dev", {}) or {}
    if not isinstance(clean_dev, dict):
        raise ManifestError(f"{source}: '[clean_dev]' must be a table")

    vite_dev = data.get("vite_dev", {}) or {}
    if not isinstance(vite_dev, dict):
        raise ManifestError(f"{source}: '[vite_dev]' must be a table")

    deploy = data.get("deploy", {}) or {}
    if not isinstance(deploy, dict):
        raise ManifestError(f"{source}: '[deploy]' must be a table")

    logging_cfg = data.get("logging", {}) or {}
    if not isinstance(logging_cfg, dict):
        raise ManifestError(f"{source}: '[logging]' must be a table")
    _LOGGING_KNOWN = {"enabled", "dir", "retain"}
    unknown_logging = set(logging_cfg) - _LOGGING_KNOWN
    if unknown_logging:
        raise ManifestError(
            f"{source}: '[logging]' unknown keys: {sorted(unknown_logging)}, "
            f"valid: {sorted(_LOGGING_KNOWN)}"
        )
    if "enabled" in logging_cfg and not isinstance(logging_cfg["enabled"], bool):
        raise ManifestError(f"{source}: '[logging].enabled' must be a boolean")
    if "dir" in logging_cfg and not isinstance(logging_cfg["dir"], str):
        raise ManifestError(f"{source}: '[logging].dir' must be a string")
    if "retain" in logging_cfg and not isinstance(logging_cfg["retain"], int):
        raise ManifestError(f"{source}: '[logging].retain' must be an integer")

    exclude_modules = data.get("exclude_modules", [])
    if not isinstance(exclude_modules, list) or not all(isinstance(m, str) for m in exclude_modules):
        raise ManifestError(f"{source}: 'exclude_modules' must be a list of strings")

    unknown = set(data.keys()) - _KNOWN_KEYS
    if unknown:
        print(
            f"warning: {source}: unknown keys will be dropped on --regenerate: "
            + ", ".join(sorted(unknown)),
            file=sys.stderr,
        )

    return Manifest(
        project_name=project_name,
        python_min=python_min,
        archetypes=archetypes,
        modules=modules,
        urls=urls,
        overrides=overrides,
        custom=custom_normalized,
        actions=actions,
        order=order,
        logs=logs,
        apple=apple,
        server=server,
        output=output,
        logging=logging_cfg,
        dev_kill=dev_kill,
        clean_dev=clean_dev,
        vite_dev=vite_dev,
        deploy=deploy,
        exclude_modules=exclude_modules,
        generator_commit=data.get("generator_commit"),
        inline_file=inline_file,
        raw=data,
    )


def _parse_logs(raw, source: str) -> dict[str, dict]:
    """Normalize [logs.<name>] sections.

    Returns {target: {"default_env": str|None, "envs": {env: {"path", "host", "user"}}}}.
    Env values may be strings (local path shorthand) or tables with a required `path`
    and optional `host`/`user`. ${VAR} placeholders are preserved verbatim.
    """
    if raw is None:
        return {}
    if not isinstance(raw, dict):
        raise ManifestError(f"{source}: 'logs' must be a table")

    result: dict[str, dict] = {}
    for name, target in raw.items():
        if not isinstance(target, dict):
            raise ManifestError(f"{source}: logs.{name} must be a table")

        default_env = target.get("default_env")
        if default_env is not None and not isinstance(default_env, str):
            raise ManifestError(f"{source}: logs.{name}.default_env must be a string")

        envs: dict[str, dict] = {}
        for env_name, env_val in target.items():
            if env_name == "default_env":
                continue
            if isinstance(env_val, str):
                envs[env_name] = {"path": env_val, "host": None, "user": None}
            elif isinstance(env_val, dict):
                if "path" not in env_val or not isinstance(env_val["path"], str):
                    raise ManifestError(
                        f"{source}: logs.{name}.{env_name} must have a 'path' string"
                    )
                host = env_val.get("host")
                user = env_val.get("user")
                if host is not None and not isinstance(host, str):
                    raise ManifestError(
                        f"{source}: logs.{name}.{env_name}.host must be a string"
                    )
                if user is not None and not isinstance(user, str):
                    raise ManifestError(
                        f"{source}: logs.{name}.{env_name}.user must be a string"
                    )
                envs[env_name] = {
                    "path": env_val["path"],
                    "host": host,
                    "user": user,
                }
            else:
                raise ManifestError(
                    f"{source}: logs.{name}.{env_name} must be a string path or table"
                )

        if not envs:
            raise ManifestError(
                f"{source}: logs.{name} must declare at least one environment"
            )
        if default_env is not None and default_env not in envs:
            raise ManifestError(
                f"{source}: logs.{name}.default_env '{default_env}' not in envs "
                f"{sorted(envs)}"
            )

        result[name] = {"default_env": default_env, "envs": envs}

    return result


def dump(manifest: Manifest) -> str:
    """Serialize a Manifest to TOML text. Used for bootstrap (writing starter manifests).

    Stdlib has no toml writer, so this is a hand-rolled minimal emitter that only
    handles the shapes we produce. Not a general-purpose TOML serializer.
    """
    lines: list[str] = []
    lines.append(f'project_name = "{_escape(manifest.project_name)}"')
    lines.append(f'python_min   = "{manifest.python_min}"')
    if manifest.generator_commit:
        lines.append(f'generator_commit = "{manifest.generator_commit}"')
    arch_list = ", ".join(f'"{a}"' for a in manifest.archetypes)
    lines.append(f"archetypes = [{arch_list}]")
    if manifest.modules:
        mod_list = ", ".join(f'"{m}"' for m in manifest.modules)
        lines.append(f"modules = [{mod_list}]")
    if manifest.exclude_modules:
        excl_list = ", ".join(f'"{m}"' for m in manifest.exclude_modules)
        lines.append(f"exclude_modules = [{excl_list}]")
    if manifest.order:
        entries = ", ".join(f'"{"" if e == SPACER else _escape(e)}"' for e in manifest.order)
        lines.append(f"order = [{entries}]")
    lines.append("")

    if manifest.urls:
        lines.append("[urls]")
        for label, url in manifest.urls.items():
            lines.append(f'{label} = "{_escape(url)}"')
        lines.append("")

    if manifest.overrides:
        lines.append("[commands]")
        for name, val in manifest.overrides.items():
            lines.append(f"{name} = {_inline_table(val)}")
        lines.append("")

    if manifest.custom:
        lines.append("[commands.custom]")
        for name, val in manifest.custom.items():
            lines.append(f"{name} = {_inline_table(val)}")
        lines.append("")

    if manifest.actions:
        for action_name, action_data in manifest.actions.items():
            lines.append(f"[actions.{action_name}]")
            for k, v in action_data.items():
                if isinstance(v, str):
                    lines.append(f'{k} = "{_escape(v)}"')
                elif isinstance(v, bool):
                    lines.append(f"{k} = {'true' if v else 'false'}")
                elif isinstance(v, int):
                    lines.append(f"{k} = {v}")
                elif isinstance(v, dict):
                    lines.append(f"{k} = {_inline_table(v)}")
                elif isinstance(v, list):
                    lines.append(f"{k} = {_inline_list(v)}")
                else:
                    lines.append(f"{k} = {v!r}")
            lines.append("")

    if manifest.logs:
        for target_name, target in manifest.logs.items():
            lines.append(f"[logs.{target_name}]")
            envs = target.get("envs", {})
            # Preserve original key order from raw TOML if available.
            raw_keys = list(manifest.raw.get("logs", {}).get(target_name, {}).keys())
            ordered_keys = raw_keys if raw_keys else (
                (["default_env"] if target.get("default_env") else []) + list(envs)
            )
            for key in ordered_keys:
                if key == "default_env":
                    if target.get("default_env"):
                        lines.append(f'default_env = "{_escape(target["default_env"])}"')
                    continue
                env = envs.get(key)
                if env is None:
                    continue
                host = env.get("host")
                user = env.get("user")
                path = env.get("path", "")
                if host is None and user is None:
                    lines.append(f'{key} = "{_escape(path)}"')
                else:
                    parts = [f'path = "{_escape(path)}"']
                    if host is not None:
                        parts.append(f'host = "{_escape(host)}"')
                    if user is not None:
                        parts.append(f'user = "{_escape(user)}"')
                    lines.append(f"{key} = {{ " + ", ".join(parts) + " }")
            lines.append("")

    if manifest.server:
        lines.append("[server]")
        for k, v in manifest.server.items():
            if isinstance(v, str):
                lines.append(f'{k} = "{_escape(v)}"')
            elif isinstance(v, bool):
                lines.append(f"{k} = {'true' if v else 'false'}")
            elif isinstance(v, dict):
                lines.append(f"[server.{k}]")
                for sk, sv in v.items():
                    lines.append(f'{sk} = "{_escape(str(sv))}"')
            else:
                lines.append(f"{k} = {v!r}")
        lines.append("")

    if manifest.output:
        for source_name, source_cfg in manifest.output.items():
            lines.append(f"[output.{source_name}]")
            if isinstance(source_cfg, dict):
                for k, v in source_cfg.items():
                    if isinstance(v, list):
                        items = ", ".join(f'"{_escape(str(x))}"' for x in v)
                        lines.append(f"{k} = [{items}]")
                    elif isinstance(v, str):
                        lines.append(f'{k} = "{_escape(v)}"')
                    else:
                        lines.append(f"{k} = {v!r}")
            lines.append("")

    if manifest.logging:
        lines.append("[logging]")
        for k, v in manifest.logging.items():
            if isinstance(v, bool):
                lines.append(f"{k} = {'true' if v else 'false'}")
            elif isinstance(v, str):
                lines.append(f'{k} = "{_escape(v)}"')
            else:
                lines.append(f"{k} = {v!r}")
        lines.append("")

    if manifest.inline_file:
        lines.append("[inline]")
        lines.append(f'file = "{_escape(manifest.inline_file)}"')
        lines.append("")

    return "\n".join(lines).rstrip() + "\n"


def _escape(s: str) -> str:
    return s.replace("\\", "\\\\").replace('"', '\\"')


def _inline_table(val: dict) -> str:
    parts = []
    for k, v in val.items():
        if isinstance(v, str):
            parts.append(f'{k} = "{_escape(v)}"')
        elif isinstance(v, bool):
            parts.append(f"{k} = {'true' if v else 'false'}")
        elif isinstance(v, dict):
            parts.append(f"{k} = {_inline_table(v)}")
        elif isinstance(v, list):
            parts.append(f"{k} = {_inline_list(v)}")
        else:
            parts.append(f"{k} = {v!r}")
    return "{ " + ", ".join(parts) + " }"


def _inline_list(val: list) -> str:
    items = []
    for v in val:
        if isinstance(v, str):
            items.append(f'"{_escape(v)}"')
        elif isinstance(v, bool):
            items.append("true" if v else "false")
        elif isinstance(v, dict):
            items.append(_inline_table(v))
        elif isinstance(v, list):
            items.append(_inline_list(v))
        else:
            items.append(repr(v))
    return "[" + ", ".join(items) + "]"


def require_python_311() -> None:
    if sys.version_info < (3, 11):
        sys.exit("admin-project-tool requires Python 3.11+ (for stdlib tomllib)")
