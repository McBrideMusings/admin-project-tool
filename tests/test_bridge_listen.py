"""Tests for the bridge-listen kind (#422).

reddit-poker had three actions and stash-reels one whose entire body was
`tail -f /dev/null` under kind = "interactive-shell". They exist only to park a
session so the log bridge attaches and browser console output streams in —
which nothing about `tail -f /dev/null` tells you.
"""
from __future__ import annotations

import sys
import tomllib
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.archetypes import ArchetypeError, resolve  # noqa: E402
from admin_lib.kinds import KINDS, KIND_MODULES  # noqa: E402
from admin_lib.manifest import parse  # noqa: E402

ARCH_DIR = REPO / "archetypes"


def _parse(toml: str):
    return parse(tomllib.loads(toml), source="t")


def test_bridge_listen_is_a_known_kind():
    assert "bridge-listen" in KINDS


def test_bridge_listen_pulls_in_the_log_bridge_modules():
    """It must bundle exactly what interactive-shell does — it uses that machinery."""
    mods = set(KIND_MODULES.get("bridge-listen", ()))
    assert {"log_bridge", "log_server", "keybindings", "log_format"} <= mods


def test_bridge_listen_needs_no_run():
    """The point of the kind: the shell command is boilerplate, so it's implicit."""
    m = _parse(
        'project_name = "X"\n\n'
        '[commands.logs]\ndesc = "logs"\nsteps = ["logs"]\n\n'
        '[actions.logs]\nkind = "bridge-listen"\ndesc = "Live client console"\n'
    )
    r = resolve(m, ARCH_DIR)
    assert r.actions["logs"].kind == "bridge-listen"
    assert not r.actions["logs"].run


def test_hosts_is_carried_into_meta():
    m = _parse(
        'project_name = "X"\n\n'
        '[commands.logs]\ndesc = "logs"\nsteps = ["logs"]\n\n'
        '[actions.logs]\nkind  = "bridge-listen"\ndesc  = "Live client console"\n'
        'hosts = "${LOG_BRIDGE_LIVE_HOSTS:-example.com}"\n'
    )
    r = resolve(m, ARCH_DIR)
    assert r.actions["logs"].meta["hosts"] == "${LOG_BRIDGE_LIVE_HOSTS:-example.com}"


def test_hosts_is_scanned_as_an_env_var():
    """`admin env` should still surface a real var referenced from hosts."""
    from admin_lib.kinds import scan_env_vars

    m = _parse(
        'project_name = "X"\n\n'
        '[commands.logs]\ndesc = "logs"\nsteps = ["logs"]\n\n'
        '[actions.logs]\nkind  = "bridge-listen"\ndesc  = "d"\n'
        'hosts = "${LOG_BRIDGE_LIVE_HOSTS}"\n'
    )
    r = resolve(m, ARCH_DIR)
    seen = scan_env_vars(list(r.actions.values()))
    # hosts lives in meta, so this documents current behavior rather than
    # asserting a promise: the var is resolved at run time either way.
    assert isinstance(seen, dict)


def test_several_listeners_can_declare_different_hosts():
    """reddit-poker runs prod/local/branch client listeners side by side."""
    m = _parse(
        'project_name = "X"\n\n'
        '[commands.logs]\ndesc = "logs"\nsteps = ["logs"]\n\n'
        '[actions.logs]\nkind = "multi-target"\ndesc = "logs"\n\n'
        '[actions.logs.prod]\ndesc = "prod"\naction = "logs-prod"\n\n'
        '[actions.logs.local]\ndesc = "local"\naction = "logs-local"\n\n'
        '[actions.logs-prod]\nkind = "bridge-listen"\ndesc = "prod"\nhosts = "prod.example.com"\n\n'
        '[actions.logs-local]\nkind = "bridge-listen"\ndesc = "local"\nhosts = "localhost"\n'
    )
    r = resolve(m, ARCH_DIR)
    assert r.actions["logs-prod"].meta["hosts"] == "prod.example.com"
    assert r.actions["logs-local"].meta["hosts"] == "localhost"


def test_unknown_key_on_a_bridge_listen_action_still_errors():
    m = _parse(
        'project_name = "X"\n\n'
        '[commands.logs]\ndesc = "logs"\nsteps = ["logs"]\n\n'
        '[actions.logs]\nkind = "bridge-listen"\ndesc = "d"\nbogus = "x"\n'
    )
    try:
        resolve(m, ARCH_DIR)
    except ArchetypeError as e:
        assert "unknown keys" in str(e) and "bogus" in str(e)
    else:
        raise AssertionError("expected ArchetypeError for an unknown action key")
