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


@dataclass
class Command:
    """A single admin command, post-merge."""
    name: str
    run: str                       # shell string (may contain ${VAR}); "" allowed when targets is set
    desc: str
    kind: str = "shell"            # shell | multi-target | apple-dev-loop | docker-reload-loop
    source: str = "manifest"       # which archetype contributed it (or "manifest")
    targets: dict = field(default_factory=dict)  # target name -> shell string (for multi-target kinds)


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
                f"{source}: commands.{name} must be a table with at least 'run'"
            )
        overrides[name] = val

    custom_normalized: dict[str, dict] = {}
    for name, val in custom.items():
        if not isinstance(val, dict) or "run" not in val:
            raise ManifestError(
                f"{source}: commands.custom.{name} must be a table with 'run'"
            )
        custom_normalized[name] = val

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

    return Manifest(
        project_name=project_name,
        python_min=python_min,
        archetypes=archetypes,
        modules=modules,
        urls=urls,
        overrides=overrides,
        custom=custom_normalized,
        order=order,
        logs=logs,
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

    if manifest.logs:
        for target_name, target in manifest.logs.items():
            lines.append(f"[logs.{target_name}]")
            if target.get("default_env"):
                lines.append(f'default_env = "{_escape(target["default_env"])}"')
            for env_name, env in target.get("envs", {}).items():
                host = env.get("host")
                user = env.get("user")
                path = env.get("path", "")
                if host is None and user is None:
                    lines.append(f'{env_name} = "{_escape(path)}"')
                else:
                    parts = [f'path = "{_escape(path)}"']
                    if host is not None:
                        parts.append(f'host = "{_escape(host)}"')
                    if user is not None:
                        parts.append(f'user = "{_escape(user)}"')
                    lines.append(f"{env_name} = {{ " + ", ".join(parts) + " }")
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
        else:
            parts.append(f"{k} = {v!r}")
    return "{ " + ", ".join(parts) + " }"


def require_python_311() -> None:
    if sys.version_info < (3, 11):
        sys.exit("admin-project-tool requires Python 3.11+ (for stdlib tomllib)")
