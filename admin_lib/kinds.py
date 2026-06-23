"""Kind metadata and ${VAR} scanning for the manifest layer.

Historically this module rendered each action kind into Python source for the
generated ./admin. Under interpreter mode (ADR-0006) that rendering became live
closures in admin_lib/interp.py, so what remains here is:

  * the kind catalog — the `KINDS` registry of `Kind` records, the single source
    of truth for every action kind (name, configless flag, auto-pulled modules,
    and the runtime factory the interpreter binds in). KNOWN_KINDS,
    CONFIGLESS_KINDS, and KIND_MODULES are derived views the manifest and
    archetype validators consult, and
  * scan_env_vars() — the ${VAR} / os.environ.get() scanner that feeds the
    runtime `env` command with the set of referenced variables and origins.

The interpreter binds each kind's factory via register_factory() and checks at
import that every kind has exactly one — so the catalog and the factories can't
drift, with no separate sync test.
"""
from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Callable, Optional

_ENV_VAR_RE = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}")
_PY_ENV_RE = re.compile(
    r"""os\.environ\.get\(\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]"""
    r"""(?:\s*,\s*(?:['"]([^'"]*)['"]|[^)]+?))?\s*\)"""
)


class FragmentError(Exception):
    pass


@dataclass
class Kind:
    """One action kind — the single record for its name, validation metadata, and
    runtime factory.

    `name`, `configless`, and `modules` are validation metadata the manifest and
    archetype resolvers read **without importing the interpreter**. `factory` is
    bound later by admin_lib.interp via register_factory(); the interpreter
    dispatches on it. Because both sides key off this one record, the metadata
    and the factory can't drift.
    """
    name: str
    configless: bool = False           # body driven by a config table, not a `run` string
    modules: tuple[str, ...] = ()      # admin_lib modules auto-pulled when this kind is used
    factory: Optional[Callable] = field(default=None, repr=False, compare=False)


# The canonical action-kind catalog — the single source of truth. Adding a kind
# is one _kind(...) line here plus a register_factory(name, ...) in
# admin_lib/interp.py; the interpreter checks at import that every kind got
# exactly one factory.
KINDS: dict[str, Kind] = {}


def _kind(name: str, *, configless: bool = False, modules: tuple[str, ...] = ()) -> None:
    KINDS[name] = Kind(name, configless=configless, modules=modules)


_kind("shell")
_kind("shell-passthrough")
_kind("multi-target")
_kind("apple-dev-loop")
_kind("python")
_kind("python-script")
_kind("http")
_kind("npm")
_kind("sync")
_kind("apple")
_kind("clean-dev", configless=True)
_kind("vite-dev", configless=True)
_kind("print-urls", configless=True)
_kind("docker-reload-loop", modules=("docker",))
_kind("docker-dev", configless=True, modules=("docker",))
_kind("server-reload", configless=True, modules=("docker",))
_kind("kill-port", modules=("ports", "ports_nuke"))
_kind("nuke-dev", configless=True, modules=("ports", "ports_nuke"))
_kind("fixtures-generate", configless=True, modules=("cleanup", "fixtures"))
_kind("fixtures-reset", configless=True, modules=("cleanup", "fixtures"))
_kind("install-template", configless=True, modules=("remote", "unraid"))
_kind("interactive-shell", modules=("ports", "log_server", "log_bridge", "keybindings", "log_format"))


# Derived views over KINDS — one source, three lenses. Kept as module-level names
# so the manifest/archetype validators read them directly:
#   KNOWN_KINDS      — every valid kind name
#   CONFIGLESS_KINDS — kinds whose body is a config table, not a `run` string
#                      (validators skip the "needs run/targets" check for these)
#   KIND_MODULES     — admin_lib modules a kind auto-pulls into scope, so a
#                      project doesn't have to declare them (using the kind is enough)
KNOWN_KINDS = frozenset(KINDS)
CONFIGLESS_KINDS = frozenset(n for n, k in KINDS.items() if k.configless)
KIND_MODULES: dict[str, list[str]] = {n: list(k.modules) for n, k in KINDS.items() if k.modules}


def register_factory(name: str, fn: Callable) -> Callable:
    """Bind a kind's runtime closure factory (called from admin_lib.interp).

    Raises if `name` isn't a declared kind — you can't register a factory for a
    kind absent from the catalog. That's one half of the can't-drift guarantee;
    the other is interp's import-time check that every kind got a factory.
    """
    spec = KINDS.get(name)
    if spec is None:
        raise FragmentError(
            f"register_factory: unknown kind {name!r} — declare it with _kind() "
            "in admin_lib/kinds.py first"
        )
    spec.factory = fn
    return fn


# Single source of truth for [logs.<name>] source kinds, shared by the manifest
# parser (validation + field parsing), the ${VAR} scanner below, and the logtail
# dispatch (admin_lib.logtail.LOG_KIND_HANDLERS — a test asserts the two cover
# the same kinds, mirroring how each action Kind binds its factory). "file" is special:
# it carries per-environment subtables (path/host/user) rather than flat string
# fields, so the parser handles it on its own; the rest are flat string fields.
LOG_KINDS: dict[str, dict[str, tuple]] = {
    "file":    {"required": (),            "optional": ()},
    "command": {"required": ("run",),      "optional": ("host", "user")},
    "docker":  {"required": ("container",), "optional": ("host", "user")},
    "pick":    {"required": ("glob",),      "optional": ()},
}

# Flat ${VAR}-bearing string fields across all non-file log kinds (what the
# scanner walks under logs.<name>); derived so a new kind needs no scanner edit.
LOG_FLAT_FIELDS: tuple = tuple(sorted({
    f for meta in LOG_KINDS.values()
    for f in (*meta["required"], *meta["optional"])
}))


def _safe_ident(name: str) -> str:
    return name.replace("-", "_").replace(":", "_").replace(".", "_")


# ---------------------------------------------------------------------------
# ${VAR} scanning
# ---------------------------------------------------------------------------

def _scan_for_env_vars(text, pattern, seen: dict, origin: str) -> None:
    """Extract env var names + defaults from a single string, tagging the origin."""
    if not isinstance(text, str):
        return
    for m in pattern.finditer(text):
        name = m.group(1)
        default = m.group(2)
        entry = seen.setdefault(name, {"default": default, "used_by": []})
        if entry["default"] is None and default is not None:
            entry["default"] = default
        if origin not in entry["used_by"]:
            entry["used_by"].append(origin)


def scan_env_vars(commands, urls=None, logs=None, config_tables=None) -> dict[str, dict]:
    """Return {var: {"default", "used_by": [origin, ...]}} for every ${VAR} (and
    os.environ.get in python bodies, and {env, default?} config sub-tables) the
    manifest references.

    The origin-string format ("cmd:<name>", "url:<key>", "logs.<target>.<env>",
    "<table>:…") is a stable contract shared with the runtime `env` command; see
    the block above register_env_var in admin_lib/core.py before changing it.
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
        # file kind: scan each env's path/host/user under logs.<target>.<env>
        for env_name, env_spec in target.get("envs", {}).items():
            origin = f"logs.{target_name}.{env_name}"
            for field in ("path", "host", "user"):
                _scan_for_env_vars(env_spec.get(field), _ENV_VAR_RE, seen, origin)
        # command/docker/pick kinds: scan flat string fields under logs.<target>
        origin = f"logs.{target_name}"
        for field in LOG_FLAT_FIELDS:
            _scan_for_env_vars(target.get(field), _ENV_VAR_RE, seen, origin)

    for table_name, table in (config_tables or {}).items():
        if table:
            _scan_config_table(table, _ENV_VAR_RE, seen, table_name)

    return seen


def _scan_config_table(value, pattern, seen, origin):
    """Recursively scan a manifest config table for ${VAR} placeholders and
    `{env, default?}` sub-tables, registering each found var into `seen`.
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
