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

from gen.fragments import KNOWN_KINDS, CONFIGLESS_KINDS, KIND_MODULES, _ACTION_RENDERERS
from gen.manifest import Command, Manifest, SPACER, VALID_COMMAND_KEYS, VALID_ACTION_KEYS


@dataclass
class Archetype:
    name: str
    modules: list[str] = field(default_factory=lambda: ["core"])
    urls: dict[str, str] = field(default_factory=dict)
    commands: dict[str, dict] = field(default_factory=dict)
    actions: dict[str, dict] = field(default_factory=dict)
    order: list[str] = field(default_factory=list)


class ArchetypeError(Exception):
    pass


_GP_SUBTABLE_HINT = (
    " 'group' and 'priority' belong on inline target subtables "
    "(e.g. [actions.build.mac]), not on the action itself."
)


def _validate_gp_ints(data: dict, error_prefix: str) -> None:
    """Raise ArchetypeError if 'group' or 'priority' in data is not a plain int."""
    for key in ("group", "priority"):
        val = data.get(key, 0)
        # bool is a subclass of int — reject it explicitly so True/False isn't accepted.
        if isinstance(val, bool) or not isinstance(val, int):
            raise ArchetypeError(
                f"{error_prefix}: '{key}' must be an integer, "
                f"got {type(val).__name__} {val!r}"
            )


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
    for cmd_name, cmd_data in data.get("commands", {}).items():
        if not isinstance(cmd_data, dict):
            raise ArchetypeError(f"{path}: command '{cmd_name}' must be a dict")
        unknown = set(cmd_data) - VALID_COMMAND_KEYS
        if unknown:
            kind_hint = " Move kind/run/targets to [actions]." if unknown & {"kind", "run", "targets"} else ""
            raise ArchetypeError(
                f"{path}: command '{cmd_name}' has invalid keys {sorted(unknown)}. "
                f"Commands may only have 'desc', 'steps', and log overrides.{kind_hint}"
            )
    for action_name, action_data in data.get("actions", {}).items():
        if not isinstance(action_data, dict):
            raise ArchetypeError(f"{path}: action '{action_name}' must be a dict")
        inline_target_keys = {k for k, v in action_data.items() if isinstance(v, dict) and k not in VALID_ACTION_KEYS}
        unknown = set(action_data) - VALID_ACTION_KEYS - inline_target_keys
        if unknown:
            gp_hint = _GP_SUBTABLE_HINT if unknown & {"group", "priority"} else ""
            raise ArchetypeError(
                f"{path}: action '{action_name}' has unknown keys {sorted(unknown)}, "
                f"valid: {sorted(VALID_ACTION_KEYS)}{gp_hint}"
            )
    raw_order = list(data.get("order", []))
    normalized_order = ["" if e in ("", "---") else e for e in raw_order]
    return Archetype(
        name=data.get("name", name),
        modules=list(data.get("modules", ["core"])),
        urls=dict(data.get("urls", {})),
        commands=dict(data.get("commands", {})),
        actions=dict(data.get("actions", {})),
        order=normalized_order,
    )


@dataclass
class ResolvedManifest:
    """A manifest with all archetypes loaded and merged into a final command set."""
    manifest: Manifest
    commands: dict[str, Command]   # name -> Command, in merge order
    actions: dict[str, Command]    # name -> Command; registered but not shown in UI
    urls: dict[str, str]
    modules: list[str]             # required admin_lib modules
    merge_log: list[str]           # human-readable merge events
    display_order: list[str]       # final render order; "" entries mean spacer


def _sort_by_group_priority(commands: dict[str, Command]) -> list[str]:
    """Sort commands by (group, priority, name) and insert spacers between group boundaries."""
    sorted_cmds = sorted(commands.values(), key=lambda c: (c.group, c.priority, c.name))
    result: list[str] = []
    last_group: int | None = None
    for cmd in sorted_cmds:
        if last_group is not None and cmd.group != last_group:
            result.append(SPACER)
        result.append(cmd.name)
        last_group = cmd.group
    return result


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
    archetype_order: list[str] = []
    loaded_archetypes: list[Archetype] = []

    for arch_name in manifest.archetypes:
        arch = load_archetype(archetype_dir, arch_name)
        loaded_archetypes.append(arch)
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
            commands[cmd_name] = _to_command(cmd_name, cmd_data, source=arch_name, is_action=False)
        if arch.order:
            archetype_order = list(arch.order)

    # Manifest [commands] overrides — key validation already happened in manifest.parse()
    for cmd_name, override in manifest.overrides.items():
        if cmd_name in commands:
            base = commands[cmd_name]
            merged: dict = {
                "desc": override.get("desc", base.desc),
                "steps": override.get("steps", base.meta.get("steps", [])),
                "group": override.get("group", base.group),
                "priority": override.get("priority", base.priority),
            }
            base_log = base.meta.get("logging", {})
            for key in ("log_file", "log_retain", "log"):
                val = override.get(key, base_log.get(key))
                if val is not None:
                    merged[key] = val
            # Carry env through: override wins, then fall back to archetype base.
            base_env = base.meta.get("env")
            override_env = override.get("env")
            if override_env is not None:
                merged["env"] = override_env
            elif base_env is not None:
                merged["env"] = base_env
            log.append(f"command '{cmd_name}' overridden by manifest")
            commands[cmd_name] = _to_command(cmd_name, merged, source="manifest", is_action=False)
        else:
            if "steps" not in override:
                raise ArchetypeError(
                    f"manifest [commands.{cmd_name}] must include 'steps' (a list of action names). "
                    f"No archetype provides this command. Move run/kind/targets to [actions]."
                )
            commands[cmd_name] = _to_command(cmd_name, override, source="manifest", is_action=False)

    # Manifest [commands.custom] additions
    for cmd_name, custom in manifest.custom.items():
        if cmd_name in commands:
            log.append(f"custom '{cmd_name}' shadows archetype-provided command")
        commands[cmd_name] = _to_command(cmd_name, custom, source="custom", is_action=False)

    # Manifest URLs win — rebuild dict so manifest ordering takes priority
    if manifest.urls:
        merged = dict(manifest.urls)
        for label, url in urls.items():
            if label not in merged:
                merged[label] = url
        urls = merged


    # Compute display order.
    # Explicit order (manifest or archetype): use it as-is, auto-append any unlisted commands.
    # No explicit order: sort by (group, priority, name) and insert spacers between groups.
    if manifest.order or archetype_order:
        effective_order = manifest.order if manifest.order else archetype_order
        listed: set[str] = set()
        display: list[str] = []
        for entry in effective_order:
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
    else:
        display = _sort_by_group_priority(commands)

    # Process actions: archetype actions first, manifest actions win on conflict
    actions: dict[str, Command] = {}
    for arch in loaded_archetypes:
        for action_name, action_data in arch.actions.items():
            actions[action_name] = _to_command(action_name, action_data, source=arch.name, is_action=True)
    for action_name, action_data in manifest.actions.items():
        actions[action_name] = _to_command(action_name, action_data, source="action", is_action=True)

    # Validate that all command steps reference known actions.
    for cmd_name, cmd in commands.items():
        for step in cmd.meta.get("steps", []):
            if step not in actions:
                raise ArchetypeError(
                    f"command '{cmd_name}' has step '{step}' which is not a known action. "
                    f"Add [actions.{step}] to admin.toml."
                )

    # Validate @-ref targets in multi-target actions at resolve time.
    for action_name, action in actions.items():
        if action.kind in ("multi-target", "apple-dev-loop"):
            for target_name, target_val in action.targets.items():
                if target_val.startswith('@'):
                    ref = target_val[1:]
                    if ref not in actions:
                        raise ArchetypeError(
                            f"action '{action_name}': target '{target_name}' references unknown action '@{ref}'. "
                            f"Known actions: {sorted(actions)}"
                        )

    # Pull in modules required by the action kinds in use (e.g. nuke-dev → ports_nuke).
    for action in actions.values():
        modules.update(KIND_MODULES.get(action.kind, ()))

    # Apply exclusions and ensure core is always present
    modules -= set(manifest.exclude_modules)
    modules.add("core")

    return ResolvedManifest(
        manifest=manifest,
        commands=commands,
        actions=actions,
        urls=urls,
        modules=_order_modules(modules),
        merge_log=log,
        display_order=display,
    )


def _to_command(name: str, data: dict, source: str, is_action: bool = False) -> Command:
    if not is_action:
        # Commands are pure routing: only desc, steps, and log overrides.
        unknown = set(data) - VALID_COMMAND_KEYS
        if unknown:
            kind_hint = ""
            if "kind" in unknown or "run" in unknown or "targets" in unknown:
                kind_hint = " Move kind/run/targets to [actions] instead."
            raise ArchetypeError(
                f"command '{name}' from {source}: invalid keys {sorted(unknown)}. "
                f"Commands may only have 'desc', 'steps', and log overrides.{kind_hint}"
            )
        steps = data.get("steps")
        if not steps or not isinstance(steps, list):
            raise ArchetypeError(
                f"command '{name}' from {source}: commands require 'steps' (a list of action names)"
            )
        meta: dict = {"steps": [str(s) for s in steps]}
        log_meta: dict = {}
        if "log_file" in data:
            log_meta["log_file"] = str(data["log_file"])
        if "log_retain" in data:
            log_meta["log_retain"] = int(data["log_retain"])
        if "log" in data:
            log_meta["log"] = bool(data["log"])
        if log_meta:
            meta["logging"] = log_meta
        if "env" in data:
            if not isinstance(data["env"], dict):
                raise ArchetypeError(f"command '{name}' from {source}: 'env' must be a table")
            meta["env"] = {str(k): str(v) for k, v in data["env"].items()}
        _validate_gp_ints(data, f"command '{name}' from {source}")
        return Command(
            name=name,
            run="",
            desc=str(data.get("desc", name)),
            kind="steps",
            source=source,
            targets={},
            meta=meta,
            group=data.get("group", 0),
            priority=data.get("priority", 0),
        )

    # Actions: full kind-based rendering.
    # Inline target subtables: keys whose value is a dict and not in VALID_ACTION_KEYS.
    inline_targets_raw = {
        k: v for k, v in data.items()
        if k not in VALID_ACTION_KEYS and isinstance(v, dict)
    }

    unknown = set(data) - VALID_ACTION_KEYS - set(inline_targets_raw)
    if unknown:
        gp_hint = _GP_SUBTABLE_HINT if unknown & {"group", "priority"} else ""
        raise ArchetypeError(
            f"action '{name}' from {source}: unknown keys {sorted(unknown)}, "
            f"valid: {sorted(VALID_ACTION_KEYS)}{gp_hint}"
        )

    # Detect reserved keys used as inline target names (dict value when a string
    # is expected means the user wrote [actions.X.run] intending "run" as a target).
    reserved_as_targets = {
        k for k in (VALID_ACTION_KEYS - {"targets"})
        if isinstance(data.get(k), dict)
    }
    if reserved_as_targets:
        raise ArchetypeError(
            f"action '{name}' from {source}: reserved key(s) {sorted(reserved_as_targets)} "
            f"have table values — they look like inline target names but collide with "
            f"reserved action keys; rename the target or use the flat "
            f"'[actions.{name}.targets]' table form"
        )

    kind = str(data.get("kind", "shell"))
    if kind not in KNOWN_KINDS:
        raise ArchetypeError(
            f"action '{name}' from {source}: unknown kind '{kind}', "
            f"known: {sorted(KNOWN_KINDS)}"
        )

    # Build targets: either from flat [actions.X.targets] table or inline subtables.
    flat_targets = data.get("targets") or {}
    if flat_targets and not isinstance(flat_targets, dict):
        raise ArchetypeError(
            f"action '{name}' from {source}: 'targets' must be a table of target->shell"
        )
    if inline_targets_raw and flat_targets:
        raise ArchetypeError(
            f"action '{name}' from {source}: cannot mix targets with inline target subtables"
        )

    meta = {}
    log_meta = {}
    if "log_file" in data:
        log_meta["log_file"] = str(data["log_file"])
    if "log_retain" in data:
        log_meta["log_retain"] = int(data["log_retain"])
    if "log" in data:
        log_meta["log"] = bool(data["log"])
    if log_meta:
        meta["logging"] = log_meta
    if data.get("interactive"):
        meta["interactive"] = True
    if inline_targets_raw:
        for tname, tdata in inline_targets_raw.items():
            if "action" not in tdata and "run" not in tdata:
                raise ArchetypeError(
                    f"action '{name}' from {source}: target '{tname}' must have 'action' or 'run'"
                )
            if "action" in tdata and "run" in tdata:
                raise ArchetypeError(
                    f"action '{name}' from {source}: target '{tname}' has both 'action' and 'run' — pick one"
                )
            _validate_gp_ints(tdata, f"action '{name}' from {source}: target '{tname}'")
        targets = {k: ("@" + v["action"] if "action" in v else v.get("run", "")) for k, v in inline_targets_raw.items()}
        meta["target_descs"] = {k: v.get("desc", k) for k, v in inline_targets_raw.items()}
        if any(tdata.get("group", 0) != 0 or tdata.get("priority", 0) != 0 for tdata in inline_targets_raw.values()):
            sorted_tgts = sorted(
                inline_targets_raw.items(),
                key=lambda item: (item[1].get("group", 0), item[1].get("priority", 0), item[0]),
            )
            target_order: list[str] = []
            last_grp: int | None = None
            for tname, tdata in sorted_tgts:
                g = tdata.get("group", 0)
                if last_grp is not None and g != last_grp:
                    target_order.append("")
                target_order.append(tname)
                last_grp = g
            meta["target_order"] = target_order
    else:
        targets = flat_targets
    if kind == "http":
        if "run" not in data:
            raise ArchetypeError(f"action '{name}' from {source}: http kind requires 'run' (the URL)")
        meta["method"] = str(data.get("method", "POST"))
        if "body" in data:
            meta["body"] = str(data["body"])
    elif kind == "sync":
        if "run" not in data:
            raise ArchetypeError(f"action '{name}' from {source}: sync kind requires 'run' (destination path)")
        if "src" in data:
            meta["src"] = str(data["src"])
    elif kind == "npm":
        if "run" not in data:
            raise ArchetypeError(f"action '{name}' from {source}: npm kind requires 'run' (script name)")
    elif kind in CONFIGLESS_KINDS:
        pass
    elif "run" not in data and not targets:
        raise ArchetypeError(f"action '{name}' from {source}: missing 'run' or 'targets'")

    return Command(
        name=name,
        run=str(data.get("run", "")),
        desc=str(data.get("desc", name)),
        kind=kind,
        source=source,
        targets={str(k): str(v) for k, v in targets.items()},
        meta=meta,
    )


# Canonical bundle order for admin_lib modules. Add new modules here when they
# are created in admin_lib/ — unlisted modules fall back to alphabetical order
# at the end, which may cause import-ordering issues in the flat bundle namespace.
_MODULE_ORDER = [
    "core", "cache", "icons", "remote", "ports", "ports_nuke", "cleanup",
    "apple", "rust",
    "docker", "unraid", "web_dev",
    "log_format", "log_columns", "logtail",
    "install",
]


def _order_modules(modules: set[str]) -> list[str]:
    ordered = [m for m in _MODULE_ORDER if m in modules]
    extras = sorted(m for m in modules if m not in _MODULE_ORDER)
    return ordered + extras
