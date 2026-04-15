"""Archetype loader and merger.

Archetypes live in archetypes/<name>.py at the admin-project-tool repo root.
Each archetype module defines a top-level ARCHETYPE dict:

    ARCHETYPE = {
        "name": "hugo",
        "modules": ["core"],                    # admin_lib modules required
        "urls": {"dev": "http://localhost:1313"},
        "commands": {
            "build": {"run": "hugo --minify", "desc": "Build site"},
            "dev":   {"run": "hugo server -D", "desc": "Run dev server"},
        },
    }

Multiple archetypes can be merged in order; later archetypes win on command
name conflicts. The manifest's [commands] table wins over all archetypes.
"""
from __future__ import annotations

import importlib.util
from dataclasses import dataclass, field
from pathlib import Path

from gen.fragments import KNOWN_KINDS
from gen.manifest import Command, Manifest, SPACER


@dataclass
class Archetype:
    name: str
    modules: list[str] = field(default_factory=lambda: ["core"])
    urls: dict[str, str] = field(default_factory=dict)
    commands: dict[str, dict] = field(default_factory=dict)


class ArchetypeError(Exception):
    pass


def load_archetype(archetype_dir: Path, name: str) -> Archetype:
    """Load archetypes/<name>.py and return its Archetype object."""
    path = archetype_dir / f"{name}.py"
    if not path.exists():
        raise ArchetypeError(f"unknown archetype: {name} (looked in {archetype_dir})")
    spec = importlib.util.spec_from_file_location(f"_arch_{name}", path)
    if spec is None or spec.loader is None:
        raise ArchetypeError(f"failed to load archetype module: {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    data = getattr(mod, "ARCHETYPE", None)
    if not isinstance(data, dict):
        raise ArchetypeError(f"{path}: missing top-level ARCHETYPE dict")
    return Archetype(
        name=data.get("name", name),
        modules=list(data.get("modules", ["core"])),
        urls=dict(data.get("urls", {})),
        commands=dict(data.get("commands", {})),
    )


@dataclass
class ResolvedManifest:
    """A manifest with all archetypes loaded and merged into a final command set."""
    manifest: Manifest
    commands: dict[str, Command]   # name -> Command, in merge order
    urls: dict[str, str]
    modules: list[str]             # required admin_lib modules
    merge_log: list[str]           # human-readable merge events
    display_order: list[str]       # final render order; "" entries mean spacer


def resolve(manifest: Manifest, archetype_dir: Path) -> ResolvedManifest:
    """Load all archetypes listed in the manifest and merge them with manifest overrides.

    Order of precedence (later wins):
        1. archetypes in listed order
        2. manifest [commands] overrides
        3. manifest [commands.custom] additions

    URL precedence: archetype urls merged in order, then manifest urls win.
    Module set: union of all archetype modules.
    """
    commands: dict[str, Command] = {}
    urls: dict[str, str] = {}
    modules: set[str] = {"core"}
    modules.update(manifest.modules)
    log: list[str] = []

    for arch_name in manifest.archetypes:
        arch = load_archetype(archetype_dir, arch_name)
        modules.update(arch.modules)
        for label, url in arch.urls.items():
            if label in urls:
                log.append(f"url '{label}' overridden by archetype '{arch_name}'")
            urls[label] = url
        for cmd_name, cmd_data in arch.commands.items():
            if cmd_name in commands:
                log.append(
                    f"command '{cmd_name}' from '{commands[cmd_name].source}' "
                    f"overridden by archetype '{arch_name}'"
                )
            commands[cmd_name] = _to_command(cmd_name, cmd_data, source=arch_name)

    # Manifest [commands] overrides
    for cmd_name, override in manifest.overrides.items():
        if cmd_name in commands:
            base = commands[cmd_name]
            merged = {
                "run": override.get("run", base.run),
                "desc": override.get("desc", base.desc),
                "kind": override.get("kind", base.kind),
                "targets": override.get("targets", base.targets),
            }
            log.append(f"command '{cmd_name}' overridden by manifest")
            commands[cmd_name] = _to_command(cmd_name, merged, source="manifest")
        else:
            if "run" not in override and "targets" not in override:
                raise ArchetypeError(
                    f"manifest [commands.{cmd_name}] must include 'run' or 'targets' "
                    f"(no archetype provides it)"
                )
            commands[cmd_name] = _to_command(cmd_name, override, source="manifest")

    # Manifest [commands.custom] additions
    for cmd_name, custom in manifest.custom.items():
        if cmd_name in commands:
            log.append(f"custom '{cmd_name}' shadows archetype-provided command")
        commands[cmd_name] = _to_command(cmd_name, custom, source="custom")

    # Manifest URLs win — rebuild dict so manifest ordering takes priority
    if manifest.urls:
        merged = dict(manifest.urls)
        for label, url in urls.items():
            if label not in merged:
                merged[label] = url
        urls = merged


    # Compute display order: entries from manifest.order (commands + spacers),
    # then any unlisted commands auto-appended in merge order.
    listed: set[str] = set()
    display: list[str] = []
    for entry in manifest.order:
        if entry == SPACER:
            display.append(SPACER)
            continue
        if entry not in commands:
            raise ArchetypeError(
                f"order references unknown command '{entry}' "
                f"(known: {sorted(commands)})"
            )
        if entry in listed:
            raise ArchetypeError(f"order lists command '{entry}' more than once")
        listed.add(entry)
        display.append(entry)
    for name in commands:
        if name not in listed:
            display.append(name)

    return ResolvedManifest(
        manifest=manifest,
        commands=commands,
        urls=urls,
        modules=_order_modules(modules),
        merge_log=log,
        display_order=display,
    )


_VALID_COMMAND_KEYS = {"run", "desc", "kind", "targets"}


def _to_command(name: str, data: dict, source: str) -> Command:
    unknown = set(data) - _VALID_COMMAND_KEYS
    if unknown:
        raise ArchetypeError(
            f"command '{name}' from {source}: unknown keys {sorted(unknown)}, "
            f"valid: {sorted(_VALID_COMMAND_KEYS)}"
        )
    kind = str(data.get("kind", "shell"))
    if kind not in KNOWN_KINDS:
        raise ArchetypeError(
            f"command '{name}' from {source}: unknown kind '{kind}', "
            f"known: {sorted(KNOWN_KINDS)}"
        )
    targets = data.get("targets") or {}
    if targets and not isinstance(targets, dict):
        raise ArchetypeError(
            f"command '{name}' from {source}: 'targets' must be a table of target->shell"
        )
    if "run" not in data and not targets:
        raise ArchetypeError(f"command '{name}' from {source}: missing 'run' or 'targets'")
    return Command(
        name=name,
        run=str(data.get("run", "")),
        desc=str(data.get("desc", name)),
        kind=kind,
        source=source,
        targets={str(k): str(v) for k, v in targets.items()},
    )


_MODULE_ORDER = ["core", "cache", "icons", "remote", "apple", "docker", "unraid", "log_format", "logtail"]


def _order_modules(modules: set[str]) -> list[str]:
    ordered = [m for m in _MODULE_ORDER if m in modules]
    extras = sorted(m for m in modules if m not in _MODULE_ORDER)
    return ordered + extras
