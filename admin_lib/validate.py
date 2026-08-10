"""Schema validation for commands and actions — the single home for "is this
manifest entity well-formed".

Three pipeline sites delegate here so the schema rules live in one place:

  * admin_lib/manifest.py (parse phase) — validates `[commands]` / `commands.custom`.
  * admin_lib/archetypes.py::load_archetype (load phase) — light per-file key check
    on each archetype's commands + actions.
  * admin_lib/archetypes.py::_to_command (resolve phase) — the full action check;
    after it returns, `_to_command` only *builds* the Command (no schema raises).

Error type: validate_* raise the caller-supplied `exc` (ManifestError at parse,
ArchetypeError at resolve) — both subclasses of ValidationError, so a caller can
catch the one umbrella type. `lead` is a context prefix each site passes (e.g.
"action 'build' from hugo"); messages join it with ": ".

This module imports only admin_lib.kinds (the kind catalog), never the parser or
resolver, so those layers can call its hooks without pulling in the runtime.
"""
from __future__ import annotations

from dataclasses import dataclass

from admin_lib.kinds import KNOWN_KINDS, CONFIGLESS_KINDS


class ValidationError(Exception):
    """Umbrella for manifest/archetype schema errors. ManifestError and
    ArchetypeError subclass this; admin-run catches ValidationError."""


@dataclass
class ValidatedAction:
    """The shape `validate_action` derived while checking, returned so the
    builder (`_to_command`) consumes it instead of re-deriving the same facts —
    one derivation, no validator/builder divergence."""
    kind: str
    inline_targets: dict
    flat_targets: dict


# Keys a [commands.X] / archetype command table may carry (pure routing — ADR-0003).
VALID_COMMAND_KEYS = {"desc", "steps", "log_file", "log_retain", "log", "group", "priority", "env"}
# Keys an [actions.X] table may carry.
VALID_ACTION_KEYS = {"run", "desc", "kind", "targets", "method", "body", "src", "log_file",
                     "log_retain", "log", "interactive", "keys", "requires", "watch",
                     "excludes", "hosts"}

_GP_SUBTABLE_HINT = (
    " 'group' and 'priority' belong on inline target subtables "
    "(e.g. [actions.build.mac]), not on the action itself."
)

_VALID_KEY_BINDING_FIELDS = {"label", "run", "action", "signal", "pid_file"}

# Kinds whose factory actually reads `cmd.targets` — both are _make_multi_target
# in admin_lib/interp.py. Under any other kind the targets get built into the
# Command and then nothing ever runs them, which is always a manifest mistake.
_TARGET_CONSUMING_KINDS = {"multi-target", "apple-dev-loop"}


def _validate_gp_ints(data: dict, lead: str, exc: type[ValidationError]) -> None:
    """Raise if 'group' or 'priority' in data is not a plain int."""
    for key in ("group", "priority"):
        val = data.get(key, 0)
        # bool is a subclass of int — reject it explicitly so True/False isn't accepted.
        if isinstance(val, bool) or not isinstance(val, int):
            raise exc(
                f"{lead}: '{key}' must be an integer, "
                f"got {type(val).__name__} {val!r}"
            )


def parse_keys(raw, lead: str, exc: type[ValidationError]) -> dict[str, dict]:
    """Validate the `keys` table on an action and return a normalized copy.

    Each entry must be a single-char key mapped to a dict with exactly one of
    `run`, `action`, or `signal` (+ `pid_file`). `label` is optional.
    """
    if not isinstance(raw, dict):
        raise exc(f"{lead}: 'keys' must be a table")
    out: dict[str, dict] = {}
    for keychar, binding in raw.items():
        if not isinstance(keychar, str) or len(keychar) != 1:
            raise exc(f"{lead}: key '{keychar}' must be a single character")
        if keychar in ("\x03", "\x04"):
            raise exc(f"{lead}: key '{keychar!r}' is reserved (Ctrl-C / Ctrl-D)")
        if not isinstance(binding, dict):
            raise exc(f"{lead}: keys.{keychar} must be a table")
        unknown = set(binding) - _VALID_KEY_BINDING_FIELDS
        if unknown:
            raise exc(
                f"{lead}: keys.{keychar} has unknown fields {sorted(unknown)}, "
                f"valid: {sorted(_VALID_KEY_BINDING_FIELDS)}"
            )
        targets_set = {f for f in ("run", "action", "signal") if f in binding}
        if not targets_set:
            raise exc(f"{lead}: keys.{keychar} must have one of 'run', 'action', or 'signal'")
        if len(targets_set) > 1:
            raise exc(
                f"{lead}: keys.{keychar} has conflicting targets {sorted(targets_set)} — pick one"
            )
        if "signal" in binding and "pid_file" not in binding:
            raise exc(f"{lead}: keys.{keychar} has 'signal' but no 'pid_file'")
        if "pid_file" in binding and "signal" not in binding:
            raise exc(f"{lead}: keys.{keychar} has 'pid_file' but no 'signal'")
        for f in ("label", "run", "signal", "pid_file"):
            if f in binding and not isinstance(binding[f], str):
                raise exc(f"{lead}: keys.{keychar}.{f} must be a string")
        if "action" in binding:
            if not isinstance(binding["action"], str):
                raise exc(f"{lead}: keys.{keychar}.action must be a string")
        normalized = dict(binding)
        if "action" in normalized:
            ref = normalized["action"]
            normalized["action"] = ref[1:] if ref.startswith("@") else ref
        out[keychar] = normalized
    return out


def inline_targets(data: dict) -> dict[str, dict]:
    """Inline target subtables: keys whose value is a dict and not a reserved
    action key. Shared by validation and the _to_command builder so both agree
    on what counts as a target."""
    return {k: v for k, v in data.items() if k not in VALID_ACTION_KEYS and isinstance(v, dict)}


def validate_command(lead: str, data: dict, exc: type[ValidationError], *, require_steps: bool) -> None:
    """Pure-routing rule (ADR-0003): a command may only carry desc/steps/log
    overrides (+ group/priority/env). `require_steps` is True at the resolve and
    custom-command sites (a final command needs steps) and False for archetype
    files / manifest overrides (steps may come from the merge)."""
    unknown = set(data) - VALID_COMMAND_KEYS
    if unknown:
        kind_hint = " Move kind/run/targets to [actions]." if unknown & {"kind", "run", "targets"} else ""
        raise exc(
            f"{lead}: invalid keys {sorted(unknown)}. "
            f"Commands may only have 'desc', 'steps', and log overrides.{kind_hint}"
        )
    if require_steps:
        steps = data.get("steps")
        if not steps or not isinstance(steps, list):
            raise exc(f"{lead}: commands require 'steps' (a list of action names)")
    if "env" in data and not isinstance(data["env"], dict):
        raise exc(f"{lead}: 'env' must be a table")
    _validate_gp_ints(data, lead, exc)


def validate_action_keys(lead: str, data: dict, exc: type[ValidationError]) -> None:
    """Light check: an action carries only known keys (inline target subtables
    excepted). Shared by load_archetype (early per-file check) and the full
    validate_action."""
    unknown = set(data) - VALID_ACTION_KEYS - set(inline_targets(data))
    if unknown:
        gp_hint = _GP_SUBTABLE_HINT if unknown & {"group", "priority"} else ""
        raise exc(
            f"{lead}: unknown keys {sorted(unknown)}, "
            f"valid: {sorted(VALID_ACTION_KEYS)}{gp_hint}"
        )


def validate_action(lead: str, data: dict, exc: type[ValidationError]) -> ValidatedAction:
    """Full action schema check. Returns the derived shape (kind + raw targets)
    so the builder consumes it instead of re-deriving; after this returns clean,
    _to_command builds the Command with no further raises."""
    validate_action_keys(lead, data, exc)

    inline_targets_raw = inline_targets(data)

    # Reserved keys used as inline target names (dict value where a string is
    # expected → the user wrote [actions.X.run] intending "run" as a target).
    reserved_as_targets = {
        k for k in (VALID_ACTION_KEYS - {"targets", "keys"})
        if isinstance(data.get(k), dict)
    }
    if reserved_as_targets:
        raise exc(
            f"{lead}: reserved key(s) {sorted(reserved_as_targets)} "
            f"have table values — they look like inline target names but collide with "
            f"reserved action keys; rename the target or use the flat "
            f"'targets' table form"
        )

    kind = str(data.get("kind", "shell"))
    if kind not in KNOWN_KINDS:
        raise exc(f"{lead}: unknown kind '{kind}', known: {sorted(KNOWN_KINDS)}")

    flat_targets = data.get("targets") or {}
    if flat_targets and not isinstance(flat_targets, dict):
        raise exc(f"{lead}: 'targets' must be a table of target->shell")
    if inline_targets_raw and flat_targets:
        raise exc(f"{lead}: cannot mix targets with inline target subtables")

    if "requires" in data:
        parent_requires = data["requires"]
        if not isinstance(parent_requires, list) or not all(isinstance(r, str) for r in parent_requires):
            raise exc(f"{lead}: 'requires' must be a list of guard-name strings")

    if inline_targets_raw:
        for tname, tdata in inline_targets_raw.items():
            if "action" not in tdata and "run" not in tdata:
                raise exc(f"{lead}: target '{tname}' must have 'action' or 'run'")
            if "action" in tdata and "run" in tdata:
                raise exc(f"{lead}: target '{tname}' has both 'action' and 'run' — pick one")
            _validate_gp_ints(tdata, f"{lead}: target '{tname}'", exc)
            if "requires" in tdata:
                treq = tdata["requires"]
                if not isinstance(treq, list) or not all(isinstance(r, str) for r in treq):
                    raise exc(
                        f"{lead}: target '{tname}' 'requires' must be a list of guard-name strings"
                    )
            if "env" in tdata and not isinstance(tdata["env"], dict):
                raise exc(f"{lead}: target '{tname}' 'env' must be a table of NAME = value")
        targets = inline_targets_raw
    else:
        targets = flat_targets

    if targets and kind not in _TARGET_CONSUMING_KINDS:
        raise exc(
            f"{lead}: has target(s) {sorted(targets)} but kind is '{kind}' — "
            'targets only run under kind = "multi-target" (or "apple-dev-loop"); '
            'add that kind, or remove the target(s) if they were a mistake'
        )

    if kind == "http":
        if "run" not in data:
            raise exc(f"{lead}: http kind requires 'run' (the URL)")
    elif kind == "sync":
        if "run" not in data:
            raise exc(f"{lead}: sync kind requires 'run' (destination path)")
    elif kind == "npm":
        if "run" not in data:
            raise exc(f"{lead}: npm kind requires 'run' (script name)")
    elif kind == "interactive-shell":
        if "run" not in data:
            raise exc(f"{lead}: interactive-shell requires 'run'")
    elif kind in CONFIGLESS_KINDS:
        pass
    elif "run" not in data and not targets:
        raise exc(f"{lead}: missing 'run' or 'targets'")

    if "keys" in data:
        if kind != "interactive-shell":
            raise exc(f"{lead}: 'keys' is only valid on kind = \"interactive-shell\"")
        parse_keys(data["keys"], lead, exc)

    return ValidatedAction(kind, inline_targets_raw, flat_targets)
