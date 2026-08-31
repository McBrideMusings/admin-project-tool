"""`admin check` refuses a config-table ${VAR} that no environment value satisfies.

The bug this closes (oa-jsy.2): admin.toml carried
`development_team = "${HERMES_DEV_TEAM}"` against a variable nothing exported.
Every deploy printed ** BUILD SUCCEEDED ** and installed an app signed ad-hoc,
with no iCloud entitlement, for weeks. admin.toml is untracked in every project,
so the next hand-written manifest gets the same silent downgrade unless the tool
itself objects — which is why this check lives here and not in documentation.
"""
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from admin_lib.manifest import (  # noqa: E402
    CONFIG_TABLE_FIELDS,
    config_tables,
    parse,
    unresolvable_env_refs,
)


def _manifest(**tables):
    data = {"project_name": "T", "archetypes": [], "commands": {}, "actions": {}}
    data.update(tables)
    return parse(data, source="<test>")


def test_unset_var_in_a_config_table_is_reported():
    m = _manifest(apple={"development_team": "${NOPE_UNSET_XYZ}"})
    os.environ.pop("NOPE_UNSET_XYZ", None)
    assert unresolvable_env_refs(m) == [
        ("NOPE_UNSET_XYZ", "apple.development_team", "not set")
    ]


def test_empty_var_in_a_config_table_is_reported(monkeypatch):
    """The exact incident shape — the variable exists, and says nothing."""
    monkeypatch.setenv("EMPTY_TEAM", "")
    m = _manifest(apple={"development_team": "${EMPTY_TEAM}"})
    assert unresolvable_env_refs(m) == [
        ("EMPTY_TEAM", "apple.development_team", "set but empty")
    ]


def test_a_satisfied_var_is_not_reported(monkeypatch):
    monkeypatch.setenv("REAL_TEAM", "ABCDE12345")
    m = _manifest(apple={"development_team": "${REAL_TEAM}"})
    assert unresolvable_env_refs(m) == []


def test_a_reference_with_a_default_is_not_reported(monkeypatch):
    monkeypatch.delenv("NOPE_UNSET_XYZ", raising=False)
    m = _manifest(apple={"development_team": "${NOPE_UNSET_XYZ:-fallback}"})
    assert unresolvable_env_refs(m) == []


def test_a_computed_var_is_not_reported(monkeypatch):
    """${LAN_IP} is answered at runtime; demanding the user export it is wrong."""
    monkeypatch.delenv("LAN_IP", raising=False)
    m = _manifest(log_bridge={"hosts": ["${LAN_IP}"]})
    assert unresolvable_env_refs(m) == []


def test_a_manifest_with_no_config_tables_is_clean():
    assert unresolvable_env_refs(_manifest()) == []


def test_command_run_strings_are_out_of_scope(monkeypatch):
    """An interactive-shell body resolves via resolve_computed (interp.py:746), which
    hands an unresolvable ${VAR} to the shell on purpose — a shell-local there is
    legal and must not be reported."""
    monkeypatch.delenv("NOPE_UNSET_XYZ", raising=False)
    m = _manifest(
        commands={"dev": {"desc": "d", "steps": ["dev"]}},
        actions={"dev": {"kind": "interactive-shell", "run": "echo ${NOPE_UNSET_XYZ}"}},
    )
    assert unresolvable_env_refs(m) == []


def test_config_tables_only_returns_populated_tables():
    m = _manifest(apple={"ios_scheme": "X"})
    assert config_tables(m) == {"apple": {"ios_scheme": "X"}}


def test_every_config_table_field_exists_on_the_manifest():
    """CONFIG_TABLE_FIELDS is read by both the runtime registrar and check; a
    renamed Manifest field must not leave either silently scanning nothing."""
    m = _manifest()
    for name in CONFIG_TABLE_FIELDS:
        assert isinstance(getattr(m, name), dict), name
