"""Tests for manifest parse/dump round-trips and edge cases."""
from __future__ import annotations

import io
import sys
try:
    import tomllib
except ImportError:
    import tomli as tomllib  # type: ignore[no-redef]
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.manifest import Manifest, ManifestError, dump, parse  # noqa: E402


def _base_data(**overrides) -> dict:
    data = {"project_name": "test-proj", "python_min": "3.11"}
    data.update(overrides)
    return data


def _base_manifest(**overrides) -> Manifest:
    m = Manifest(
        project_name="test-proj",
        python_min="3.11",
        archetypes=[],
        modules=[],
        urls={},
        overrides={},
        custom={},
        order=[],
    )
    for k, v in overrides.items():
        object.__setattr__(m, k, v)
    return m


def _capture_stderr(fn) -> str:
    """Execute function and capture stderr output."""
    buf = io.StringIO()
    old_stderr = sys.stderr
    sys.stderr = buf
    try:
        fn()
    finally:
        sys.stderr = old_stderr
    return buf.getvalue()


# ---------------------------------------------------------------------------
# exclude_modules round-trip
# ---------------------------------------------------------------------------

def test_exclude_modules_survives_dump():
    m = _base_manifest(exclude_modules=["log_columns", "logtail"])
    toml = dump(m)
    assert 'exclude_modules = ["log_columns", "logtail"]' in toml, (
        f"exclude_modules not emitted in dump output:\n{toml}"
    )


def test_exclude_modules_round_trip():
    m = _base_manifest(exclude_modules=["log_columns"])
    toml = dump(m)
    m2 = parse(tomllib.loads(toml))
    assert m2.exclude_modules == ["log_columns"], (
        f"exclude_modules lost after round-trip; got {m2.exclude_modules!r}"
    )


def test_exclude_modules_empty_not_emitted():
    m = _base_manifest(exclude_modules=[])
    toml = dump(m)
    assert "exclude_modules" not in toml, "empty exclude_modules should not be emitted"


# ---------------------------------------------------------------------------
# _inline_table / _inline_list — nested types
# ---------------------------------------------------------------------------

def test_inline_table_nested_dict_round_trips():
    """A command override with a steps list must survive dump+parse."""
    m = _base_manifest(overrides={"build": {
        "desc": "Build all",
        "steps": ["build-mac", "build-ios"],
    }})
    toml = dump(m)
    m2 = parse(tomllib.loads(toml))
    steps = m2.overrides["build"].get("steps")
    assert steps == ["build-mac", "build-ios"], (
        f"steps list not preserved after round-trip; got {steps!r}"
    )


def test_inline_table_list_value_round_trips():
    """A command with a list-valued field (steps) must survive dump+parse."""
    m = _base_manifest(custom={"deploy": {
        "desc": "Deploy",
        "steps": ["build", "push"],
    }})
    toml = dump(m)
    m2 = parse(tomllib.loads(toml))
    steps = m2.custom["deploy"].get("steps")
    assert steps == ["build", "push"], (
        f"list-valued field not preserved after round-trip; got {steps!r}"
    )


# ---------------------------------------------------------------------------
# Unknown-key warning
# ---------------------------------------------------------------------------

def test_unknown_key_prints_warning():
    data = _base_data(exlude_modules=["typo"])  # intentional typo
    warning = _capture_stderr(lambda: parse(data))
    assert "exlude_modules" in warning, (
        f"Expected warning mentioning 'exlude_modules', got: {warning!r}"
    )
    assert "warning" in warning.lower(), "Warning message should say 'warning'"


def test_known_keys_produce_no_warning():
    data = _base_data(archetypes=["docker"], exclude_modules=["log_columns"])
    warning = _capture_stderr(lambda: parse(data))
    assert warning == "", f"Unexpected warning for known keys: {warning!r}"


# ---------------------------------------------------------------------------
# hide_urls parsing — dropping the matching [urls] entries happens in
# resolve(), so that half is covered in test_archetypes.py
# ---------------------------------------------------------------------------

def test_hide_urls_key_produces_no_warning():
    warning = _capture_stderr(lambda: parse(_base_data(hide_urls=["prod"])))
    assert warning == "", f"hide_urls should be a known key, got warning: {warning!r}"


def test_hide_urls_parses_into_manifest():
    m = parse(_base_data(hide_urls=["prod"]))
    assert m.hide_urls == ["prod"]


def test_hide_urls_defaults_empty():
    m = parse(_base_data())
    assert m.hide_urls == []


def test_hide_urls_must_be_list_of_strings():
    with _pytest_raises(ManifestError, "hide_urls"):
        parse(_base_data(hide_urls="prod"))  # not a list


# ---------------------------------------------------------------------------
# [logging] round-trip
# ---------------------------------------------------------------------------

def test_logging_section_survives_dump():
    m = _base_manifest(logging={"enabled": True, "dir": "logs", "retain": 5})
    toml = dump(m)
    assert "[logging]" in toml, f"[logging] section not emitted:\n{toml}"
    assert "enabled = true" in toml
    assert 'dir = "logs"' in toml
    assert "retain = 5" in toml


def test_logging_section_round_trips():
    m = _base_manifest(logging={"enabled": False, "retain": 0})
    toml = dump(m)
    m2 = parse(tomllib.loads(toml))
    assert m2.logging["enabled"] is False
    assert m2.logging["retain"] == 0


def test_empty_logging_section_not_emitted():
    m = _base_manifest(logging={})
    toml = dump(m)
    assert "[logging]" not in toml, "empty [logging] should not be emitted"


# ---------------------------------------------------------------------------
# [logs] key order preservation
# ---------------------------------------------------------------------------

def test_logs_dump_preserves_raw_key_order():
    """dump() should write [logs] env entries in the original TOML order, not alphabetically."""
    toml_src = "\n".join([
        'project_name = "Ordering Test"',
        'python_min = "3.11"',
        "[logs.app]",
        'prod = "/var/log/app.log"',  # prod before default_env
        'default_env = "prod"',
        'dev = "/tmp/app.log"',       # dev after default_env
    ])
    m = parse(tomllib.loads(toml_src))
    out = dump(m)
    prod_pos = out.index("prod")
    default_pos = out.index("default_env")
    dev_pos = out.index("dev =")
    assert prod_pos < default_pos < dev_pos, (
        f"key order not preserved: prod={prod_pos}, default_env={default_pos}, dev={dev_pos}"
    )


# ---------------------------------------------------------------------------
# [logs] kinds: command / docker / pick
# ---------------------------------------------------------------------------

def _parse_logs_toml(*lines):
    toml = "\n".join((
        'project_name = "Kinds Test"',
        'python_min = "3.11"',
        *lines,
    ))
    return parse(tomllib.loads(toml)).logs


def test_logs_file_kind_is_default_backcompat():
    logs = _parse_logs_toml(
        "[logs.app]",
        'prod = "/var/log/app.log"',
    )
    assert logs["app"]["kind"] == "file"
    assert logs["app"]["highlight"] == []
    assert logs["app"]["envs"]["prod"]["path"] == "/var/log/app.log"


def test_logs_command_kind():
    logs = _parse_logs_toml(
        "[logs.plugin]",
        'kind = "command"',
        'run = "node scripts/logs.mjs --follow"',
    )
    spec = logs["plugin"]
    assert spec["kind"] == "command"
    assert spec["run"] == "node scripts/logs.mjs --follow"
    assert spec["host"] is None and spec["user"] is None


def test_logs_docker_kind_with_host_and_highlight():
    logs = _parse_logs_toml(
        "[logs.docker]",
        'kind = "docker"',
        'container = "stash"',
        'host = "${STASH_DEPLOY_TARGET}"',
        'highlight = ["stash-organize"]',
    )
    spec = logs["docker"]
    assert spec["kind"] == "docker"
    assert spec["container"] == "stash"
    assert spec["host"] == "${STASH_DEPLOY_TARGET}"
    assert spec["highlight"] == ["stash-organize"]


def test_logs_pick_kind():
    logs = _parse_logs_toml(
        "[logs.pick]",
        'kind = "pick"',
        'glob = "tmp/*.log"',
    )
    assert logs["pick"]["kind"] == "pick"
    assert logs["pick"]["glob"] == "tmp/*.log"


def test_logs_dump_roundtrips_docker_kind():
    # dump() used to only know the file shape, silently dropping non-file specs
    # to an empty [logs.name] block. It must now round-trip kind + fields.
    src = "\n".join((
        'project_name = "Roundtrip"',
        'python_min = "3.11"',
        "[logs.db]",
        'kind = "docker"',
        'container = "stash"',
        'host = "${DB_HOST}"',
        'highlight = ["err", "warn"]',
    ))
    dumped = dump(parse(tomllib.loads(src)))
    spec = parse(tomllib.loads(dumped)).logs["db"]
    assert spec["kind"] == "docker"
    assert spec["container"] == "stash"
    assert spec["host"] == "${DB_HOST}"
    assert spec["user"] is None
    assert spec["highlight"] == ["err", "warn"]


def test_logs_invalid_kind_raises():
    with _pytest_raises(ManifestError, "kind must be one of"):
        _parse_logs_toml("[logs.x]", 'kind = "nope"', 'run = "x"')


def test_logs_command_requires_run():
    with _pytest_raises(ManifestError, "run must be a non-empty string"):
        _parse_logs_toml("[logs.x]", 'kind = "command"')


def test_logs_docker_requires_container():
    with _pytest_raises(ManifestError, "container must be a non-empty string"):
        _parse_logs_toml("[logs.x]", 'kind = "docker"')


def test_logs_highlight_must_be_string_list():
    with _pytest_raises(ManifestError, "highlight must be a list of strings"):
        _parse_logs_toml("[logs.x]", 'kind = "pick"', 'glob = "*"', "highlight = 5")


# ---------------------------------------------------------------------------
# [log_bridge].port_range validation
# ---------------------------------------------------------------------------

def _parse_log_bridge(*lines):
    toml = "\n".join((
        'project_name = "Bridge Test"',
        'python_min = "3.11"',
        "[log_bridge]",
        'hosts = ["localhost"]',
        *lines,
    ))
    return parse(tomllib.loads(toml))


def test_log_bridge_valid_port_range_parses():
    _parse_log_bridge("port_range = [9988, 9999]")


def test_log_bridge_port_range_start_below_one_raises():
    with _pytest_raises(ManifestError, "within 1-65535"):
        _parse_log_bridge("port_range = [0, 100]")


def test_log_bridge_port_range_end_above_max_raises():
    with _pytest_raises(ManifestError, "within 1-65535"):
        _parse_log_bridge("port_range = [70000, 70010]")


def test_log_bridge_port_range_start_ge_end_still_raises():
    with _pytest_raises(ManifestError, "start < end"):
        _parse_log_bridge("port_range = [9999, 9988]")


# ---------------------------------------------------------------------------
# guards / requires survive dump round-trip
# ---------------------------------------------------------------------------

def test_guards_survive_dump_round_trip():
    # dump() had no [guards.<name>] emitter, silently dropping Manifest.guards
    # (action-level requires already round-tripped via the generic emitter).
    src = "\n".join((
        'project_name = "Guarded"',
        'python_min = "3.11"',
        "[guards.no-tag]",
        'run = "test -z x"',
        'fail = "already tagged"',
        "[actions.deploy]",
        'kind = "shell"',
        'run = "echo hi"',
        'requires = ["git-clean", "no-tag"]',
    ))
    m = parse(tomllib.loads(dump(parse(tomllib.loads(src)))))
    assert m.guards == {"no-tag": {"run": "test -z x", "fail": "already tagged"}}
    assert m.actions["deploy"]["requires"] == ["git-clean", "no-tag"]


def _pytest_raises(exc_type, needle):
    import contextlib

    @contextlib.contextmanager
    def _cm():
        try:
            yield
        except exc_type as e:
            assert needle in str(e), f"expected {needle!r} in {e!r}"
            return
        raise AssertionError(f"expected {exc_type.__name__} containing {needle!r}")
    return _cm()


# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------



def test_docker_build_on_host_requires_context():
    """on_host without a context has nowhere to build — fail loudly at parse time."""
    with _pytest_raises(ManifestError, "requires 'context'"):
        parse(_base_data(docker_build={"on_host": True}))


def test_docker_build_on_host_with_context_parses():
    m = parse(_base_data(docker_build={"on_host": True, "context": "${APPDATA_DIR}"}))
    assert m.docker_build["on_host"] is True
    assert m.docker_build["context"] == "${APPDATA_DIR}"


def test_docker_build_must_be_a_table():
    with _pytest_raises(ManifestError, "must be a table"):
        parse(_base_data(docker_build="nope"))


def test_docker_build_absent_defaults_empty():
    assert parse(_base_data()).docker_build == {}
