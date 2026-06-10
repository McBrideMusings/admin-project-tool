"""Kind metadata and ${VAR} scanning for the manifest layer.

Historically this module rendered each action kind into Python source for the
generated ./admin. Under interpreter mode (ADR-0006) that rendering became live
closures in admin_lib/interp.py, so what remains here is:

  * the kind catalog — KNOWN_KINDS, CONFIGLESS_KINDS, KIND_MODULES — that the
    manifest and archetype validators consult, and
  * scan_env_vars() — the ${VAR} / os.environ.get() scanner that feeds the
    runtime `env` command with the set of referenced variables and origins.

KNOWN_KINDS is kept in sync with admin_lib.interp._FACTORIES (a test asserts the
two never drift).
"""
from __future__ import annotations

import re

_ENV_VAR_RE = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}")
_PY_ENV_RE = re.compile(
    r"""os\.environ\.get\(\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]"""
    r"""(?:\s*,\s*(?:['"]([^'"]*)['"]|[^)]+?))?\s*\)"""
)


class FragmentError(Exception):
    pass


# Canonical set of action kinds. The runtime closure factory for each lives in
# admin_lib.interp._FACTORIES; keep the two in sync (tests/test_interp.py guards).
KNOWN_KINDS = frozenset({
    "shell", "shell-passthrough", "multi-target", "apple-dev-loop",
    "docker-reload-loop", "docker-dev", "interactive-shell", "python",
    "python-script", "http", "npm", "sync", "apple", "kill-port", "nuke-dev",
    "clean-dev", "fixtures-generate", "fixtures-reset", "vite-dev",
    "server-reload", "install-template", "print-urls",
})

# Kinds whose body is driven by a named manifest config table rather than a
# `run` string. The manifest/archetype validators skip the "needs run/targets"
# check for these.
CONFIGLESS_KINDS = frozenset({
    "nuke-dev", "clean-dev", "vite-dev", "docker-dev",
    "fixtures-generate", "fixtures-reset",
    "server-reload", "install-template", "print-urls",
})

# Action kinds that pull additional admin_lib modules into scope. The resolver
# consults this so a project doesn't have to declare these modules explicitly —
# using a `kind` is enough.
KIND_MODULES: dict[str, list[str]] = {
    "kill-port": ["ports", "ports_nuke"],
    "nuke-dev": ["ports", "ports_nuke"],
    "docker-reload-loop": ["docker"],
    "docker-dev": ["docker"],
    "server-reload": ["docker"],
    "install-template": ["remote", "unraid"],
    "fixtures-generate": ["cleanup", "fixtures"],
    "fixtures-reset": ["cleanup", "fixtures"],
    "interactive-shell": ["ports", "log_server", "log_bridge", "keybindings", "log_format"],
}


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
        for env_name, env_spec in target.get("envs", {}).items():
            origin = f"logs.{target_name}.{env_name}"
            for field in ("path", "host", "user"):
                _scan_for_env_vars(env_spec.get(field), _ENV_VAR_RE, seen, origin)

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
