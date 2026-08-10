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

from admin_lib.kinds import KIND_MODULES
from admin_lib.manifest import Command, Manifest, SPACER
from admin_lib.guards import validate_requires
from admin_lib.validate import (
    ValidationError,
    validate_command,
    validate_action,
    validate_action_keys,
    parse_keys,
)


@dataclass
class Archetype:
    name: str
    modules: list[str] = field(default_factory=lambda: ["core"])
    urls: dict[str, str] = field(default_factory=dict)
    commands: dict[str, dict] = field(default_factory=dict)
    actions: dict[str, dict] = field(default_factory=dict)
    order: list[str] = field(default_factory=list)


class ArchetypeError(ValidationError):
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
    # Light per-file key check; full action validation happens at resolve time
    # in _to_command. Both delegate to admin_lib.validate.
    for cmd_name, cmd_data in data.get("commands", {}).items():
        if not isinstance(cmd_data, dict):
            raise ArchetypeError(f"{path}: command '{cmd_name}' must be a dict")
        validate_command(f"{path}: command '{cmd_name}'", cmd_data, ArchetypeError, require_steps=False)
    for action_name, action_data in data.get("actions", {}).items():
        if not isinstance(action_data, dict):
            raise ArchetypeError(f"{path}: action '{action_name}' must be a dict")
        validate_action_keys(f"{path}: action '{action_name}'", action_data, ArchetypeError)
    raw_order = list(data.get("order", []))
    normalized_order = ["" if e in ("", "---") else e for e in raw_order]
    declared_commands = data.get("commands", {})
    for entry in normalized_order:
        if entry != SPACER and entry not in declared_commands:
            raise ArchetypeError(
                f"{path}: order references unknown command '{entry}' "
                f"(known: {sorted(declared_commands)})"
            )
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

    # hide_commands: drop named commands from the listing entirely. The underlying
    # actions (and any [actions] they reference) are untouched, so other commands'
    # steps still resolve and the action remains callable via run_command.
    for cmd_name in manifest.hide_commands:
        if cmd_name not in commands:
            raise ArchetypeError(
                f"hide_commands references unknown command '{cmd_name}' "
                f"(known: {sorted(commands)})"
            )
        del commands[cmd_name]
        log.append(f"command '{cmd_name}' hidden by manifest hide_commands")

    # Manifest URLs win — rebuild dict so manifest ordering takes priority
    if manifest.urls:
        merged = dict(manifest.urls)
        for label, url in urls.items():
            if label not in merged:
                merged[label] = url
        urls = merged

    # hide_urls: drop named urls from the final set entirely. Every renderer
    # (header, health check, URL table, `open`) reads this merged dict, so
    # dropping an entry here omits it everywhere at once.
    for label in manifest.hide_urls:
        if label not in urls:
            raise ArchetypeError(
                f"hide_urls references unknown url '{label}' "
                f"(known: {sorted(urls)})"
            )
        del urls[label]
        log.append(f"url '{label}' hidden by manifest hide_urls")

    # Compute display order.
    # Explicit order (manifest or archetype): use it as-is, auto-append any unlisted commands.
    # No explicit order: sort by (group, priority, name) and insert spacers between groups.
    if manifest.order or archetype_order:
        effective_order = manifest.order if manifest.order else archetype_order
        hidden_set = set(manifest.hide_commands)
        listed: set[str] = set()
        display: list[str] = []
        for entry in effective_order:
            if entry == SPACER:
                display.append(SPACER)
                continue
            if entry not in commands:
                # A hidden command may still appear in an archetype's (or the
                # project's) order list — skip it rather than treating it as a typo.
                if entry in hidden_set:
                    continue
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

    # Every `requires` name (action- or target-level) must resolve to a known
    # guard. Delegated to the guard module, which owns guard-name knowledge and
    # raises the injected ArchetypeError.
    validate_requires(actions, manifest.guards, ArchetypeError)

    # Validate `action = "@name"` keybindings reference known actions.
    for action_name, action in actions.items():
        for keychar, binding in action.keys.items():
            ref = binding.get("action")
            if ref and ref not in actions:
                raise ArchetypeError(
                    f"action '{action_name}': keys.{keychar} references unknown action '{ref}'. "
                    f"Known actions: {sorted(actions)}"
                )

    # Pull in modules required by the action kinds in use (e.g. nuke-dev → ports_nuke).
    for action in actions.values():
        modules.update(KIND_MODULES.get(action.kind, ()))
        if action.meta.get("watch") or action.meta.get("target_watch"):
            modules.add("watch")

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
        # Schema validation lives in admin_lib.validate; here we only build.
        validate_command(f"command '{name}' from {source}", data, ArchetypeError, require_steps=True)
        meta: dict = {"steps": [str(s) for s in data["steps"]]}
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
            meta["env"] = {str(k): str(v) for k, v in data["env"].items()}
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

    # Actions: validate the full schema (returns the derived shape), then build
    # the Command from it with no raises below.
    va = validate_action(f"action '{name}' from {source}", data, ArchetypeError)
    inline_targets_raw = va.inline_targets
    kind = va.kind
    flat_targets = va.flat_targets

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
    # Parent-level `requires` (default for multi-target children, or the
    # action-level guard list for flat actions).
    if "requires" in data:
        meta["requires"] = [str(r) for r in data["requires"]]
    if "watch" in data:
        meta["watch"] = str(data["watch"])
    if "excludes" in data:
        meta["excludes"] = [str(e) for e in data["excludes"]]
    if "hosts" in data:
        meta["hosts"] = str(data["hosts"])
    if inline_targets_raw:
        targets = {k: ("@" + v["action"] if "action" in v else v.get("run", "")) for k, v in inline_targets_raw.items()}
        meta["target_descs"] = {k: v.get("desc", k) for k, v in inline_targets_raw.items()}
        target_pty = {k: True for k, v in inline_targets_raw.items() if v.get("pty")}
        if target_pty:
            meta["target_pty"] = target_pty
        target_watch = {k: str(v["watch"]) for k, v in inline_targets_raw.items() if v.get("watch")}
        if target_watch:
            meta["target_watch"] = target_watch
        # Per-target env. The reason this exists: several projects wrapped an
        # otherwise-declarative multi-target in a python dispatcher purely to set
        # one variable per target before handing off (stash-reels and muxarr both
        # scope ADMIN_LOG_BRIDGE_HOSTS to dev vs live this way).
        target_env = {
            k: {str(ek): str(ev) for ek, ev in v["env"].items()}
            for k, v in inline_targets_raw.items() if v.get("env")
        }
        if target_env:
            meta["target_env"] = target_env
        # Per-target `requires` overrides. None = inherit parent default;
        # an explicit list (including []) replaces it. Only emitted when at least
        # one child overrides — otherwise the parent default covers every target.
        target_requires: dict[str, list[str] | None] = {}
        any_override = False
        for tname, tdata in inline_targets_raw.items():
            if "requires" in tdata:
                target_requires[tname] = [str(r) for r in tdata["requires"]]
                any_override = True
            else:
                target_requires[tname] = None
        if any_override:
            meta["target_requires"] = target_requires
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
        meta["method"] = str(data.get("method", "POST"))
        if "body" in data:
            meta["body"] = str(data["body"])
    elif kind == "sync":
        if "src" in data:
            meta["src"] = str(data["src"])

    parsed_keys: dict[str, dict] = {}
    if "keys" in data:
        parsed_keys = parse_keys(data["keys"], f"action '{name}' from {source}", ArchetypeError)

    return Command(
        name=name,
        run=str(data.get("run", "")),
        desc=str(data.get("desc", name)),
        kind=kind,
        source=source,
        targets={str(k): str(v) for k, v in targets.items()},
        meta=meta,
        keys=parsed_keys,
    )


# Canonical bundle order for admin_lib modules. Add new modules here when they
# are created in admin_lib/ — unlisted modules fall back to alphabetical order
# at the end, which may cause import-ordering issues in the flat bundle namespace.
_MODULE_ORDER = [
    "core", "cache", "icons", "remote", "ports", "ports_nuke", "cleanup",
    "reload", "streams", "apple", "android", "metro", "react_native", "rust",
    "docker", "unraid", "web_dev", "keybindings",
    "log_format", "log_columns", "logtail",
    "install", "watch",
]


def _order_modules(modules: set[str]) -> list[str]:
    ordered = [m for m in _MODULE_ORDER if m in modules]
    extras = sorted(m for m in modules if m not in _MODULE_ORDER)
    return ordered + extras
