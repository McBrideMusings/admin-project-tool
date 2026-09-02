"""admin.toml manifest loader.

Parses admin.toml with stdlib tomllib (Python 3.11+), validates schema,
returns a normalized Manifest dataclass. Env var placeholders (${VAR},
${VAR:-default}) are preserved verbatim — they're resolved at runtime
inside the generated ./admin script, not here.
"""
from __future__ import annotations

import re
import sys
try:
    import tomllib
except ImportError:
    try:
        import tomli as tomllib  # type: ignore[no-redef]  # Python 3.9 backport
    except ImportError:
        sys.exit(
            "admin-project-tool requires Python 3.11+ (stdlib tomllib) "
            "or Python 3.9+ with tomli installed (pip install tomli)"
        )
from dataclasses import dataclass, field
from pathlib import Path

from admin_lib.kinds import LOG_KINDS, DEPLOY_KEYS
from admin_lib.guards import parse_guard_tables
from admin_lib.validate import ValidationError, validate_command

MANIFEST_FILENAME = "admin.toml"

_KNOWN_KEYS = {
    "project_name", "python_min", "archetypes", "modules", "urls",
    "commands", "actions", "order", "logs", "apple", "react_native", "server", "output",
    "exclude_modules", "hide_commands", "hide_urls", "inline", "logging",
    "dev_kill", "dev_precheck", "clean_dev", "vite_dev", "docker_dev", "docker_run", "docker_build", "deploy", "install_template",
    "fixtures", "log_bridge", "guards", "docs", "go", "go_dist", "raylib", "worktree",
    "launchd", "electron",
    # The six config.Config tables (Go's internal/config) — identity, where
    # worktrees are created, populate patterns, the Beads tab's columns, the
    # workflow dispatcher's default agent/ceiling, and the ADR-0020 terminal
    # launcher contract. admin.toml is their only home; the Python
    # interpreter never reads them, it just needs to not warn about a key it
    # doesn't otherwise use.
    "identity", "worktrees", "beads", "workflow", "populate", "terminal",
    # Obsolete under interpreter mode (ADR-0006); tolerated silently on
    # un-migrated manifests, never written back. `admin migrate` strips it.
    "generator_commit",
}



@dataclass
class Command:
    """A single admin command, post-merge."""
    name: str
    run: str                       # shell string (may contain ${VAR}); "" for sequence/http kinds
    desc: str
    kind: str = "shell"            # shell | shell-passthrough (appends shlex-quoted ./admin args) | multi-target | apple-dev-loop | docker-reload-loop | interactive-shell | python | python-script | http | sequence
    source: str = "manifest"       # which archetype contributed it (or "manifest")
    targets: dict = field(default_factory=dict)  # target name -> shell string (for multi-target kinds)
    meta: dict = field(default_factory=dict)     # kind-specific extras: {steps} for sequence, {method, body} for http
    group: int = 0                 # display group; commands with different groups get automatic spacers between them
    priority: int = 0              # sort order within a group; ties broken alphabetically
    keys: dict = field(default_factory=dict)     # interactive-shell: keychar -> {label?, run|action|signal+pid_file}


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
    logs: dict[str, dict] = field(default_factory=dict)  # name -> {kind, highlight, ...kind-specific fields}
    apple: dict = field(default_factory=dict)   # [apple] table — archetype-specific config, raw dict
    react_native: dict = field(default_factory=dict)  # [react_native] table — Metro/RN config, raw dict
    server: dict = field(default_factory=dict)  # [server] table — server build/dev/deploy config, raw dict
    output: dict = field(default_factory=dict)  # [output] table — log column config per source
    logging: dict = field(default_factory=dict) # [logging] table — file logging config (enabled, dir, retain)
    dev_kill: dict = field(default_factory=dict)  # [dev_kill] — config for kind="nuke-dev" commands
    dev_precheck: dict = field(default_factory=dict)  # [dev_precheck] — config for kind="dev-precheck" commands
    clean_dev: dict = field(default_factory=dict) # [clean_dev] — config for kind="clean-dev" commands
    vite_dev: dict = field(default_factory=dict)  # [vite_dev] — config for kind="vite-dev" commands
    docker_dev: dict = field(default_factory=dict)  # [docker_dev] — config for kind="docker-dev" commands
    deploy: dict = field(default_factory=dict)    # [deploy] — config for docker build/deploy commands
    docker_run: dict = field(default_factory=dict)  # [docker_run] — prod docker run config (target, image_tag, env, mounts, …)
    docker_build: dict = field(default_factory=dict)  # [docker_build] — where the image is built (on_host + context)
    install_template: dict = field(default_factory=dict)  # [install_template] — kind="install-template" config
    fixtures: dict = field(default_factory=dict)  # [fixtures] — config for kind="fixtures-generate"/"fixtures-reset"
    docs: dict = field(default_factory=dict)      # [docs] — config for kind="docs" (dir, script)
    go: dict = field(default_factory=dict)        # [go] — config for kind="go" (binary, main)
    go_dist: dict = field(default_factory=dict)   # [go_dist] — config for admin_lib.godist.build_go_dist (cross-compile matrix)
    raylib: dict = field(default_factory=dict)    # [raylib] — config for kind="raylib" (binary, build_dir, cmake_flags, jobs, smoke_env, clean_dirs)
    launchd: dict = field(default_factory=dict)   # [launchd] — macOS LaunchAgent config consumed by admin_lib.launchd
    electron: dict = field(default_factory=dict)  # [electron] table — Electron shell config, raw dict
    log_bridge: dict = field(default_factory=dict)            # [log_bridge] — browser-console HTTP bridge config
    worktree: dict = field(default_factory=dict)              # [worktree] — worktree verb config (root, setup, copy)
    guards: dict[str, dict] = field(default_factory=dict)     # [guards.<name>] — user-defined shell guards
    actions: dict[str, dict] = field(default_factory=dict)    # action name -> raw dict (not shown in UI)
    exclude_modules: list[str] = field(default_factory=list)  # modules to suppress even if archetype requires them
    hide_commands: list[str] = field(default_factory=list)    # archetype/manifest commands to drop from the listing (actions stay)
    hide_urls: list[str] = field(default_factory=list)        # [urls] entry names to omit everywhere admin renders urls (issue #438)
    inline_file: str | None = None           # path relative to project root
    raw: dict = field(default_factory=dict)  # original parsed TOML, for debugging


class ManifestError(ValidationError):
    pass


# Manifest fields that are archetype/module config tables — raw dicts whose string
# values are handed to core.resolve_env when the owning kind runs. Command and
# action `run` strings are deliberately absent: an `interactive-shell` body goes
# through resolve_computed instead (interp.py:746), which passes an unresolvable
# ${VAR} to the shell on purpose, so a shell-local there is legal and must not be
# reported as a broken reference.
CONFIG_TABLE_FIELDS = (
    "apple", "react_native", "server", "electron", "output", "logging",
    "dev_kill", "dev_precheck", "clean_dev", "vite_dev", "docker_dev",
    "deploy", "docker_run", "docker_build", "install_template", "fixtures",
    "docs", "go", "go_dist", "raylib", "launchd", "log_bridge", "worktree",
)


def config_tables(m: "Manifest") -> dict:
    """The manifest's non-empty config tables, keyed by table name.

    One list, read by both the runtime registrar (interp._register_env_vars) and
    `admin check`'s env pass, so a new table becomes visible to both at once.
    """
    return {name: getattr(m, name) for name in CONFIG_TABLE_FIELDS if getattr(m, name)}


def unresolvable_env_refs(m: "Manifest") -> list[tuple[str, str, str]]:
    """Return (var, origin, state) for every config-table ${VAR} that would abort a run.

    A reference with no default whose variable is unset, or set to the empty
    string, makes core.resolve_env exit(2) the moment the owning command runs.
    Reporting it from `admin check` moves that failure off the build and onto the
    one command whose job is to say whether the manifest is usable — which matters
    most for a key the consumer would otherwise skip silently rather than abort on.

    `origin` is the dotted config path (e.g. "apple.development_team").
    """
    import os

    from admin_lib.core import COMPUTED_VARS
    from admin_lib.kinds import scan_env_vars

    bad = []
    for var, entry in scan_env_vars([], config_tables=config_tables(m)).items():
        if entry["default"] is not None or var in COMPUTED_VARS:
            continue
        val = os.environ.get(var)
        if val:
            continue
        state = "set but empty" if val == "" else "not set"
        for origin in entry["used_by"] or ["(unknown)"]:
            bad.append((var, origin, state))
    return sorted(bad)


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
        validate_command(f"{source}: commands.{name}", val, ManifestError, require_steps=False)
        overrides[name] = val

    custom_normalized: dict[str, dict] = {}
    for name, val in custom.items():
        if not isinstance(val, dict):
            raise ManifestError(
                f"{source}: commands.custom.{name} must be a table with 'steps' (a list of action names). "
                f"Move run/kind/targets to [actions]."
            )
        validate_command(f"{source}: commands.custom.{name}", val, ManifestError, require_steps=True)
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

    react_native = data.get("react_native", {}) or {}
    if not isinstance(react_native, dict):
        raise ManifestError(f"{source}: '[react_native]' must be a table")

    electron = data.get("electron", {}) or {}
    if not isinstance(electron, dict):
        raise ManifestError(f"{source}: '[electron]' must be a table")

    server = data.get("server", {}) or {}
    if not isinstance(server, dict):
        raise ManifestError(f"{source}: '[server]' must be a table")

    output = data.get("output", {}) or {}
    if not isinstance(output, dict):
        raise ManifestError(f"{source}: '[output]' must be a table")

    dev_kill = data.get("dev_kill", {}) or {}
    if not isinstance(dev_kill, dict):
        raise ManifestError(f"{source}: '[dev_kill]' must be a table")
    for _p in dev_kill.get("patterns", []) or []:
        if isinstance(_p, str):
            continue
        if not isinstance(_p, dict):
            raise ManifestError(
                f"{source}: '[dev_kill].patterns' entries must be a string or a "
                f"table with 'match', got {type(_p).__name__}"
            )
        if not isinstance(_p.get("match"), str) or not _p.get("match"):
            raise ManifestError(f"{source}: '[dev_kill].patterns' entry needs a non-empty 'match' string")
        _unknown = set(_p) - {"match", "regex", "global"}
        if _unknown:
            raise ManifestError(
                f"{source}: '[dev_kill].patterns' unknown keys: {sorted(_unknown)}, "
                f"valid: ['global', 'match', 'regex']"
            )
        for _flag in ("regex", "global"):
            if _flag in _p and not isinstance(_p[_flag], bool):
                raise ManifestError(f"{source}: '[dev_kill].patterns' '{_flag}' must be a boolean")

    dev_precheck = data.get("dev_precheck", {}) or {}
    if not isinstance(dev_precheck, dict):
        raise ManifestError(f"{source}: '[dev_precheck]' must be a table")
    _dp_unknown = set(dev_precheck) - {"containers", "runtimes", "accept_runtimes", "reap_pidfiles", "label"}
    if _dp_unknown:
        raise ManifestError(
            f"{source}: '[dev_precheck]' unknown keys: {sorted(_dp_unknown)}, "
            "valid: ['accept_runtimes', 'containers', 'label', 'reap_pidfiles', 'runtimes']"
        )
    for _lk in ("containers", "runtimes", "accept_runtimes", "reap_pidfiles"):
        _lv = dev_precheck.get(_lk)
        if _lv is not None and (not isinstance(_lv, list) or any(not isinstance(x, str) for x in _lv)):
            raise ManifestError(f"{source}: '[dev_precheck].{_lk}' must be a list of strings")
    if "label" in dev_precheck and not isinstance(dev_precheck["label"], str):
        raise ManifestError(f"{source}: '[dev_precheck].label' must be a string")

    clean_dev = data.get("clean_dev", {}) or {}
    if not isinstance(clean_dev, dict):
        raise ManifestError(f"{source}: '[clean_dev]' must be a table")

    vite_dev = data.get("vite_dev", {}) or {}
    if not isinstance(vite_dev, dict):
        raise ManifestError(f"{source}: '[vite_dev]' must be a table")

    docker_dev = data.get("docker_dev", {}) or {}
    if not isinstance(docker_dev, dict):
        raise ManifestError(f"{source}: '[docker_dev]' must be a table")

    deploy = data.get("deploy", {}) or {}
    if not isinstance(deploy, dict):
        raise ManifestError(f"{source}: '[deploy]' must be a table")
    if deploy:
        unknown = set(deploy) - DEPLOY_KEYS
        if unknown:
            raise ManifestError(
                f"{source}: '[deploy]' has unknown key(s): {', '.join(sorted(unknown))}"
                f" (accepted: {', '.join(sorted(DEPLOY_KEYS))})"
            )
        for required in ("src", "dest"):
            if not deploy.get(required):
                raise ManifestError(f"{source}: '[deploy].{required}' is required")
        if not isinstance(deploy.get("exclude", []), list):
            raise ManifestError(f"{source}: '[deploy].exclude' must be a list")
        if not isinstance(deploy.get("delete", False), bool):
            raise ManifestError(f"{source}: '[deploy].delete' must be true or false")

    docker_run = data.get("docker_run", {}) or {}
    if not isinstance(docker_run, dict):
        raise ManifestError(f"{source}: '[docker_run]' must be a table")

    docker_build = data.get("docker_build", {}) or {}
    if not isinstance(docker_build, dict):
        raise ManifestError(f"{source}: '[docker_build]' must be a table")
    if docker_build.get("on_host") and not docker_build.get("context"):
        raise ManifestError(
            f"{source}: '[docker_build].on_host' requires 'context' "
            "(the build-context path ON the target host)"
        )

    install_template = data.get("install_template", {}) or {}
    if not isinstance(install_template, dict):
        raise ManifestError(f"{source}: '[install_template]' must be a table")

    fixtures = data.get("fixtures", {}) or {}
    if not isinstance(fixtures, dict):
        raise ManifestError(f"{source}: '[fixtures]' must be a table")

    docs = data.get("docs", {}) or {}
    if not isinstance(docs, dict):
        raise ManifestError(f"{source}: '[docs]' must be a table")
    _DOCS_KNOWN = {"dir", "script"}
    unknown_docs = set(docs) - _DOCS_KNOWN
    if unknown_docs:
        raise ManifestError(
            f"{source}: '[docs]' unknown keys: {sorted(unknown_docs)}, "
            f"valid: {sorted(_DOCS_KNOWN)}"
        )
    for _k in ("dir", "script"):
        if _k in docs and not isinstance(docs[_k], str):
            raise ManifestError(f"{source}: '[docs].{_k}' must be a string")

    go_cfg = data.get("go", {}) or {}
    if not isinstance(go_cfg, dict):
        raise ManifestError(f"{source}: '[go]' must be a table")
    _GO_KNOWN = {"binary", "main"}
    unknown_go = set(go_cfg) - _GO_KNOWN
    if unknown_go:
        raise ManifestError(
            f"{source}: '[go]' unknown keys: {sorted(unknown_go)}, "
            f"valid: {sorted(_GO_KNOWN)}"
        )
    for _k in ("binary", "main"):
        if _k in go_cfg and not isinstance(go_cfg[_k], str):
            raise ManifestError(f"{source}: '[go].{_k}' must be a string")

    go_dist_cfg = data.get("go_dist", {}) or {}
    if not isinstance(go_dist_cfg, dict):
        raise ManifestError(f"{source}: '[go_dist]' must be a table")
    _GO_DIST_KNOWN = {
        "source_dir", "out_dir", "binary", "targets", "stamp",
        "version_flag", "extra_ldflags", "trimpath", "cgo_enabled",
    }
    unknown_go_dist = set(go_dist_cfg) - _GO_DIST_KNOWN
    if unknown_go_dist:
        raise ManifestError(
            f"{source}: '[go_dist]' unknown keys: {sorted(unknown_go_dist)}, "
            f"valid: {sorted(_GO_DIST_KNOWN)}"
        )
    for _k in ("source_dir", "out_dir", "binary", "version_flag", "extra_ldflags"):
        if _k in go_dist_cfg and not isinstance(go_dist_cfg[_k], str):
            raise ManifestError(f"{source}: '[go_dist].{_k}' must be a string")
    for _k in ("stamp", "trimpath"):
        if _k in go_dist_cfg and not isinstance(go_dist_cfg[_k], bool):
            raise ManifestError(f"{source}: '[go_dist].{_k}' must be a boolean")
    if "targets" in go_dist_cfg:
        _targets = go_dist_cfg["targets"]
        if not isinstance(_targets, list) or not all(isinstance(t, str) for t in _targets):
            raise ManifestError(f"{source}: '[go_dist].targets' must be a list of strings")

    raylib_cfg = data.get("raylib", {}) or {}
    if not isinstance(raylib_cfg, dict):
        raise ManifestError(f"{source}: '[raylib]' must be a table")
    _RAYLIB_KNOWN = {"binary", "build_dir", "cmake_flags", "jobs", "smoke_env", "clean_dirs"}
    unknown_raylib = set(raylib_cfg) - _RAYLIB_KNOWN
    if unknown_raylib:
        raise ManifestError(
            f"{source}: '[raylib]' unknown keys: {sorted(unknown_raylib)}, "
            f"valid: {sorted(_RAYLIB_KNOWN)}"
        )
    for _k in ("binary", "build_dir", "smoke_env"):
        if _k in raylib_cfg and not isinstance(raylib_cfg[_k], str):
            raise ManifestError(f"{source}: '[raylib].{_k}' must be a string")
    for _k in ("cmake_flags", "clean_dirs"):
        if _k in raylib_cfg and not (
            isinstance(raylib_cfg[_k], list)
            and all(isinstance(x, str) for x in raylib_cfg[_k])
        ):
            raise ManifestError(f"{source}: '[raylib].{_k}' must be a list of strings")
    if "jobs" in raylib_cfg:
        _j = raylib_cfg["jobs"]
        _ok = (isinstance(_j, int) and not isinstance(_j, bool) and _j >= 0) or _j == "auto"
        if not _ok:
            raise ManifestError(
                f"{source}: '[raylib].jobs' must be a non-negative integer or "
                f'"auto" (bare -j, one job per core), got {_j!r}'
            )

    launchd_cfg = data.get("launchd", {}) or {}
    if not isinstance(launchd_cfg, dict):
        raise ManifestError(f"{source}: '[launchd]' must be a table")
    if launchd_cfg:
        _LAUNCHD_KNOWN = {
            "label", "program", "source", "args", "env", "inherit_path", "inherit_home",
            "stdout", "stderr", "working_dir", "keep_alive", "run_at_load",
            "throttle", "process_type", "nice", "start_interval",
            "env_file", "env_file_keys",
        }
        unknown_launchd = set(launchd_cfg) - _LAUNCHD_KNOWN
        if unknown_launchd:
            raise ManifestError(
                f"{source}: '[launchd]' unknown keys: {sorted(unknown_launchd)}, "
                f"valid: {sorted(_LAUNCHD_KNOWN)}"
            )
        for _k in ("label", "program"):
            if not isinstance(launchd_cfg.get(_k), str) or not launchd_cfg.get(_k):
                raise ManifestError(f"{source}: '[launchd].{_k}' is required and must be a non-empty string")
        for _k in ("source", "stdout", "stderr", "working_dir", "process_type"):
            if _k in launchd_cfg and not isinstance(launchd_cfg[_k], str):
                raise ManifestError(f"{source}: '[launchd].{_k}' must be a string")
        if "args" in launchd_cfg and not (
            isinstance(launchd_cfg["args"], list)
            and all(isinstance(x, str) for x in launchd_cfg["args"])
        ):
            raise ManifestError(f"{source}: '[launchd].args' must be a list of strings")
        if "env" in launchd_cfg and not isinstance(launchd_cfg["env"], dict):
            raise ManifestError(f"{source}: '[launchd].env' must be a table")
        if "env_file" in launchd_cfg and not isinstance(launchd_cfg["env_file"], str):
            raise ManifestError(f"{source}: '[launchd].env_file' must be a string")
        if "env_file_keys" in launchd_cfg and not (
            isinstance(launchd_cfg["env_file_keys"], list)
            and all(isinstance(x, str) for x in launchd_cfg["env_file_keys"])
        ):
            raise ManifestError(f"{source}: '[launchd].env_file_keys' must be a list of strings")
        for _k in ("inherit_path", "inherit_home", "keep_alive", "run_at_load"):
            if _k in launchd_cfg and not isinstance(launchd_cfg[_k], bool):
                raise ManifestError(f"{source}: '[launchd].{_k}' must be a boolean")
        for _k in ("throttle", "nice", "start_interval"):
            if _k in launchd_cfg and (
                not isinstance(launchd_cfg[_k], int) or isinstance(launchd_cfg[_k], bool)
            ):
                raise ManifestError(f"{source}: '[launchd].{_k}' must be an integer")

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

    worktree_cfg = data.get("worktree", {}) or {}
    _validate_worktree(worktree_cfg)

    log_bridge = data.get("log_bridge", {}) or {}
    if not isinstance(log_bridge, dict):
        raise ManifestError(f"{source}: '[log_bridge]' must be a table")
    _LOG_BRIDGE_KNOWN = {"hosts", "port_range"}
    unknown_log_bridge = set(log_bridge) - _LOG_BRIDGE_KNOWN
    if unknown_log_bridge:
        raise ManifestError(
            f"{source}: '[log_bridge]' unknown keys: {sorted(unknown_log_bridge)}, "
            f"valid: {sorted(_LOG_BRIDGE_KNOWN)}"
        )
    if log_bridge:
        hosts = log_bridge.get("hosts")
        if not isinstance(hosts, list) or not hosts or not all(isinstance(h, str) for h in hosts):
            raise ManifestError(
                f"{source}: '[log_bridge].hosts' must be a non-empty list of strings"
            )
        port_range = log_bridge.get("port_range")
        if port_range is not None:
            if (
                not isinstance(port_range, list)
                or len(port_range) != 2
                or not all(isinstance(p, int) for p in port_range)
                or port_range[0] >= port_range[1]
            ):
                raise ManifestError(
                    f"{source}: '[log_bridge].port_range' must be [start, end] with start < end"
                )
            if port_range[0] < 1 or port_range[1] > 65535:
                raise ManifestError(
                    f"{source}: '[log_bridge].port_range' must fall within 1-65535 "
                    f"(got {port_range})"
                )

    # [guards.<name>] table validation lives in the guard module (the single
    # home for guard logic) and raises the injected ManifestError.
    guards = parse_guard_tables(data, source, ManifestError)

    exclude_modules = data.get("exclude_modules", [])
    if not isinstance(exclude_modules, list) or not all(isinstance(m, str) for m in exclude_modules):
        raise ManifestError(f"{source}: 'exclude_modules' must be a list of strings")

    hide_commands = data.get("hide_commands", [])
    if not isinstance(hide_commands, list) or not all(isinstance(c, str) for c in hide_commands):
        raise ManifestError(f"{source}: 'hide_commands' must be a list of strings")

    hide_urls = data.get("hide_urls", [])
    if not isinstance(hide_urls, list) or not all(isinstance(u, str) for u in hide_urls):
        raise ManifestError(f"{source}: 'hide_urls' must be a list of strings")

    unknown = set(data.keys()) - _KNOWN_KEYS
    if unknown:
        print(
            f"warning: {source}: unknown keys are ignored: "
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
        react_native=react_native,
        electron=electron,
        server=server,
        output=output,
        logging=logging_cfg,
        dev_kill=dev_kill,
        dev_precheck=dev_precheck,
        clean_dev=clean_dev,
        vite_dev=vite_dev,
        docker_dev=docker_dev,
        deploy=deploy,
        docker_run=docker_run,
        docker_build=docker_build,
        install_template=install_template,
        fixtures=fixtures,
        docs=docs,
        go=go_cfg,
        go_dist=go_dist_cfg,
        raylib=raylib_cfg,
        launchd=launchd_cfg,
        log_bridge=log_bridge,
        worktree=worktree_cfg,
        guards=guards,
        exclude_modules=exclude_modules,
        hide_commands=hide_commands,
        hide_urls=hide_urls,
        inline_file=inline_file,
        raw=data,
    )


_WORKTREE_KNOWN = {"root", "setup", "copy"}


def _validate_worktree(cfg: dict) -> None:
    """Shape-check the [worktree] table: root (str), setup (str), copy (list[str])."""
    if not cfg:
        return
    if not isinstance(cfg, dict):
        raise ManifestError("[worktree] must be a table")
    unknown = set(cfg) - _WORKTREE_KNOWN
    if unknown:
        raise ManifestError(f"[worktree]: unknown key(s) {sorted(unknown)} (known: {sorted(_WORKTREE_KNOWN)})")
    for key in ("root", "setup"):
        if key in cfg and not isinstance(cfg[key], str):
            raise ManifestError(f"[worktree].{key} must be a string")
    copy = cfg.get("copy")
    if copy is not None:
        if not isinstance(copy, list) or not all(isinstance(x, str) for x in copy):
            raise ManifestError("[worktree].copy must be a list of strings")


# Keys that carry meaning for every kind and so are never treated as file envs.
_LOG_COMMON_KEYS = ("kind", "highlight")


def _log_opt_str(target, key, source, name):
    val = target.get(key)
    if val is not None and not isinstance(val, str):
        raise ManifestError(f"{source}: logs.{name}.{key} must be a string")
    return val


def _log_req_str(target, key, source, name):
    val = target.get(key)
    if not isinstance(val, str) or not val:
        raise ManifestError(f"{source}: logs.{name}.{key} must be a non-empty string")
    return val


def _parse_highlight(target, source, name):
    raw = target.get("highlight")
    if raw is None:
        return []
    if not isinstance(raw, list) or not all(isinstance(x, str) for x in raw):
        raise ManifestError(f"{source}: logs.{name}.highlight must be a list of strings")
    return raw


def _parse_logs(raw, source: str) -> dict[str, dict]:
    """Normalize [logs.<name>] sections into per-source specs.

    Every spec carries "kind" (one of file/command/docker/pick) and a "highlight"
    list of regex strings. ${VAR} placeholders are preserved verbatim. Per kind:

      file (default)  {"default_env", "envs": {env: {"path","host","user"}}}
      command         {"run", "host", "user"}      — stream a command's stdout
      docker          {"container", "host", "user"} — docker logs -f <container>
      pick            {"glob"}                       — pick a file, then tail it

    `host`/`user` on command/docker run the source over SSH when set.
    """
    if raw is None:
        return {}
    if not isinstance(raw, dict):
        raise ManifestError(f"{source}: 'logs' must be a table")

    result: dict[str, dict] = {}
    for name, target in raw.items():
        if not isinstance(target, dict):
            raise ManifestError(f"{source}: logs.{name} must be a table")

        kind = target.get("kind", "file")
        if kind not in LOG_KINDS:
            raise ManifestError(
                f"{source}: logs.{name}.kind must be one of "
                f"{tuple(LOG_KINDS)}, got '{kind}'"
            )

        if kind == "file":
            spec = _parse_file_log(target, source, name)
        else:
            # Non-file kinds are flat string fields driven by LOG_KINDS metadata.
            lk = LOG_KINDS[kind]
            spec = {f: _log_req_str(target, f, source, name)
                    for f in lk.required}
            spec.update({f: _log_opt_str(target, f, source, name)
                         for f in lk.optional})

        spec["kind"] = kind
        spec["highlight"] = _parse_highlight(target, source, name)
        result[name] = spec

    return result


def _parse_file_log(target, source, name) -> dict:
    """Parse a file-kind [logs.<name>] section into {default_env, envs}."""
    default_env = target.get("default_env")
    if default_env is not None and not isinstance(default_env, str):
        raise ManifestError(f"{source}: logs.{name}.default_env must be a string")

    envs: dict[str, dict] = {}
    for env_name, env_val in target.items():
        if env_name == "default_env" or env_name in _LOG_COMMON_KEYS:
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

    return {"default_env": default_env, "envs": envs}


def dump(manifest: Manifest) -> str:
    """Serialize a Manifest to TOML text. Used for bootstrap (writing starter manifests).

    Stdlib has no toml writer, so this is a hand-rolled minimal emitter that only
    handles the shapes we produce. Not a general-purpose TOML serializer.
    """
    lines: list[str] = []
    lines.append(f'project_name = "{_escape(manifest.project_name)}"')
    lines.append(f'python_min   = "{manifest.python_min}"')
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

    if manifest.guards:
        for name, guard in manifest.guards.items():
            lines.append(f"[guards.{name}]")
            for k, v in guard.items():
                lines.append(f'{k} = "{_escape(v)}"')
            lines.append("")

    if manifest.logs:
        for target_name, target in manifest.logs.items():
            lines.append(f"[logs.{target_name}]")
            kind = target.get("kind", "file")
            if kind == "file":
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
            else:
                # Non-file kinds: kind + flat string fields (LOG_KINDS metadata).
                lines.append(f'kind = "{_escape(kind)}"')
                for f in (*LOG_KINDS[kind].required, *LOG_KINDS[kind].optional):
                    val = target.get(f)
                    if val is not None:
                        lines.append(f'{f} = "{_escape(val)}"')
            highlight = target.get("highlight") or []
            if highlight:
                items = ", ".join(f'"{_escape(x)}"' for x in highlight)
                lines.append(f"highlight = [{items}]")
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


def require_python_min() -> None:
    """Preflight: Python 3.9+ with tomllib/tomli, or Python 3.11+ (stdlib tomllib)."""
    if sys.version_info < (3, 9):
        sys.exit(
            "admin-project-tool requires Python 3.9+ with tomli (pip install tomli) "
            "or Python 3.11+ (stdlib tomllib)"
        )
    if sys.version_info < (3, 11):
        try:
            import tomllib  # noqa: F401
        except ImportError:
            try:
                import tomli  # noqa: F401
            except ImportError:
                sys.exit(
                    f"Python {sys.version.split()[0]} found but tomllib is not available. "
                    "Install tomli: pip install tomli"
                )
