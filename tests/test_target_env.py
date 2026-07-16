"""Per-target env on multi-target actions (#418).

stash-reels and muxarr each wrapped an otherwise-declarative multi-target in a
python dispatcher for one reason: setting a single variable per target before
handing off (ADMIN_LOG_BRIDGE_HOSTS scoped to dev vs live). With env on the
target subtable the dispatcher has nothing left to do.
"""
from __future__ import annotations

import subprocess
import sys
import tempfile
import tomllib
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.archetypes import resolve  # noqa: E402
from admin_lib.manifest import ManifestError, parse  # noqa: E402

ARCH_DIR = REPO / "archetypes"


def _parse(toml: str):
    return parse(tomllib.loads(toml), source="t")


def _run(toml: str, argv: list[str]):
    with tempfile.TemporaryDirectory() as d:
        (Path(d) / "admin.toml").write_text(toml)
        return subprocess.run(
            [sys.executable, str(REPO / "admin-run"), "--project-dir", d, *argv],
            capture_output=True, text=True, timeout=20,
        )


_MANIFEST = '''
project_name = "X"

[commands.logs]
desc  = "Tail logs"
steps = ["logs"]

[actions.logs]
kind = "multi-target"
desc = "Tail logs"

[actions.logs.dev]
desc = "dev"
run  = "echo scope=$ADMIN_LOG_BRIDGE_HOSTS"
env  = { ADMIN_LOG_BRIDGE_HOSTS = "localhost" }

[actions.logs.live]
desc = "live"
run  = "echo scope=$ADMIN_LOG_BRIDGE_HOSTS"
env  = { ADMIN_LOG_BRIDGE_HOSTS = "prod.example.com" }

[actions.logs.plain]
desc = "plain"
run  = "echo scope=${ADMIN_LOG_BRIDGE_HOSTS:-unset}"
'''


def test_target_env_lands_in_meta():
    m = _parse(_MANIFEST)
    resolved = resolve(m, ARCH_DIR)
    env = resolved.actions["logs"].meta["target_env"]
    assert env["dev"] == {"ADMIN_LOG_BRIDGE_HOSTS": "localhost"}
    assert env["live"] == {"ADMIN_LOG_BRIDGE_HOSTS": "prod.example.com"}
    assert "plain" not in env, "a target without env should not appear"


def test_env_is_applied_for_the_picked_target():
    r = _run(_MANIFEST, ["logs", "dev"])
    assert r.returncode == 0, r.stderr
    assert "scope=localhost" in r.stdout, r.stdout


def test_each_target_gets_its_own_env():
    r = _run(_MANIFEST, ["logs", "live"])
    assert r.returncode == 0, r.stderr
    assert "scope=prod.example.com" in r.stdout, r.stdout


def test_target_without_env_is_unaffected():
    r = _run(_MANIFEST, ["logs", "plain"])
    assert r.returncode == 0, r.stderr
    assert "scope=unset" in r.stdout, r.stdout


def test_target_env_supports_var_expansion():
    toml = '''
project_name = "X"

[commands.go]
desc  = "go"
steps = ["go"]

[actions.go]
kind = "multi-target"
desc = "go"

[actions.go.one]
desc = "one"
run  = "echo host=$SCOPE"
env  = { SCOPE = "${LOG_HOSTS:-fallback.example.com}" }
'''
    r = _run(toml, ["go", "one"])
    assert r.returncode == 0, r.stderr
    assert "host=fallback.example.com" in r.stdout, r.stdout


def test_non_table_target_env_is_rejected():
    toml = '''
project_name = "X"

[commands.go]
desc  = "go"
steps = ["go"]

[actions.go]
kind = "multi-target"
desc = "go"

[actions.go.one]
desc = "one"
run  = "true"
env  = "SCOPE=x"
'''
    try:
        resolve(_parse(toml), ARCH_DIR)
    except Exception as e:
        assert "'env' must be a table" in str(e), e
    else:
        raise AssertionError("expected a validation error for non-table target env")


def test_env_does_not_turn_a_target_into_something_else():
    """`env` is a table, and inline targets are detected as 'dict-valued keys' —
    so it must not be mistaken for a nested target named 'env'."""
    m = _parse(_MANIFEST)
    resolved = resolve(m, ARCH_DIR)
    assert set(resolved.actions["logs"].targets) == {"dev", "live", "plain"}
