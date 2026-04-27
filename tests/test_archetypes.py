"""Sanity tests for the archetype catalog and generator pipeline.

Asserts that every archetype in archetypes/ loads, has the required shape,
that detectors behave, and that a multi-archetype composition round-trips
through resolve+render without crashing.
"""
from __future__ import annotations

import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from gen.archetypes import load_archetype, resolve, ArchetypeError, _to_command  # noqa: E402
from gen.detectors import detect  # noqa: E402
from gen.fragments import collect_env_var_calls, render_command  # noqa: E402
from gen.manifest import Command, Manifest, ManifestError, parse  # noqa: E402
from gen.render import render  # noqa: E402

ARCH_DIR = REPO / "archetypes"
DET_DIR = REPO / "detectors"
LIB_DIR = REPO / "admin_lib"

EXPECTED = [
    "hugo", "simple", "go-cli", "firefox-extension",
    "stash-plugin", "unraid-plugin", "docker-unraid", "apple",
]


def test_all_archetypes_load_and_validate():
    for name in EXPECTED:
        arch = load_archetype(ARCH_DIR, name)
        assert arch.name, f"{name}: empty archetype name"
        assert arch.modules, f"{name}: empty modules list"
        assert arch.commands, f"{name}: no commands"
        for cmd_name, data in arch.commands.items():
            assert isinstance(data, dict), f"{name}.{cmd_name}: not a dict"
            has_run = bool(data.get("run"))
            has_targets = bool(data.get("targets"))
            assert has_run or has_targets, (
                f"{name}.{cmd_name}: needs 'run' or 'targets'"
            )
            assert data.get("desc"), f"{name}.{cmd_name}: missing desc"
    print(f"OK: {len(EXPECTED)} archetypes load and validate")


def test_composition_docker_unraid_plus_apple():
    """docker-unraid + apple is the canonical composition case from the design doc."""
    m = Manifest(
        project_name="Test Composition",
        python_min="3.11",
        archetypes=["docker-unraid", "apple"],
        modules=[],
        urls={},
        overrides={},
        custom={},
        order=[],
    )
    resolved = resolve(m, ARCH_DIR)
    # Commands from both should be present. Apple is later in the list, so
    # commands it declares (build, deploy, reload) override docker-unraid's.
    for name in ("build", "dev", "clean", "deploy", "test", "icons",
                 "logs", "reload", "open"):
        assert name in resolved.commands, f"missing command after merge: {name}"
    assert resolved.commands["build"].source == "apple"
    assert resolved.commands["deploy"].source == "apple"
    assert resolved.commands["reload"].source == "apple"
    # docker-unraid still contributes commands that apple doesn't declare.
    assert resolved.commands["logs"].source == "docker-unraid"
    assert resolved.commands["open"].source == "docker-unraid"
    # Render must not crash.
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    assert "def cmd_build" in script
    assert "def cmd_deploy" in script
    assert "run_with_reload" in script  # docker-reload-loop fragment
    assert "pick_target" in script      # multi-target fragment
    print("OK: docker-unraid + apple composition renders")


def test_detector_composition():
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp)
        (d / "Dockerfile").write_text("FROM scratch\n")
        (d / "Foo.xcodeproj").mkdir()
        result = detect(d, DET_DIR)
        assert "docker-unraid" in result["archetypes"]
        assert "apple" in result["archetypes"]
    print("OK: detector returns unioned archetypes")


def test_hugo_roundtrip():
    m = Manifest(
        project_name="Hugo Site",
        python_min="3.11",
        archetypes=["hugo"],
        modules=[],
        urls={},
        overrides={},
        custom={},
        order=[],
    )
    resolved = resolve(m, ARCH_DIR)
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    assert "hugo --minify" in script
    assert "http://localhost:1313" in script
    print("OK: hugo round-trips")


def test_unknown_command_key_rejected():
    try:
        _to_command("bad", {"run": "echo hi", "desc": "test", "desription": "typo"}, source="test")
        assert False, "should have raised ArchetypeError"
    except ArchetypeError as e:
        assert "desription" in str(e)
        assert "unknown keys" in str(e)
    print("OK: unknown command key rejected")


def test_invalid_kind_rejected():
    try:
        _to_command("bad", {"run": "echo hi", "desc": "test", "kind": "bogus"}, source="test")
        assert False, "should have raised ArchetypeError"
    except ArchetypeError as e:
        assert "bogus" in str(e)
        assert "unknown kind" in str(e)
    print("OK: invalid kind rejected")


def test_python_min_validation():
    for bad in ["3", "3.11.0", "abc", "3.11+", ""]:
        try:
            parse({"project_name": "T", "python_min": bad})
            assert False, f"should have rejected python_min={bad!r}"
        except ManifestError as e:
            assert "MAJOR.MINOR" in str(e)
    parse({"project_name": "T", "python_min": "3.11"})
    parse({"project_name": "T", "python_min": "3.12"})
    print("OK: python_min validation works")


def test_env_vars_from_urls():
    cmds = [Command(name="x", run="echo ${HOST}", desc="test")]
    urls = {"prod": "${APP_URL:-http://localhost}", "dev": "http://localhost:8080"}
    lines = collect_env_var_calls(cmds, urls)
    var_names = " ".join(lines)
    assert "HOST" in var_names, "should find HOST from command"
    assert "APP_URL" in var_names, "should find APP_URL from url"
    assert "http://localhost" in var_names, "should preserve default"
    lines_no_urls = collect_env_var_calls(cmds)
    assert not any("APP_URL" in l for l in lines_no_urls), "without urls param, APP_URL should not appear"
    print("OK: env vars from urls detected")


def test_logs_section_parsed():
    data = {
        "project_name": "t",
        "python_min": "3.11",
        "logs": {
            "app": {
                "dev": "./tmp/app.log",
                "prod": {
                    "path": "/var/log/app.log",
                    "host": "${APP_HOST}",
                    "user": "${APP_USER:-root}",
                },
                "default_env": "dev",
            },
            "worker": {"local": "./tmp/worker.log"},
        },
    }
    m = parse(data)
    assert "app" in m.logs
    assert m.logs["app"]["default_env"] == "dev"
    assert m.logs["app"]["envs"]["dev"]["path"] == "./tmp/app.log"
    assert m.logs["app"]["envs"]["dev"]["host"] is None
    assert m.logs["app"]["envs"]["prod"]["host"] == "${APP_HOST}"
    assert m.logs["app"]["envs"]["prod"]["user"] == "${APP_USER:-root}"
    assert m.logs["worker"]["default_env"] is None
    assert m.logs["worker"]["envs"]["local"]["path"] == "./tmp/worker.log"
    print("OK: [logs] section parsed and normalized")


def test_logs_rejects_unknown_default_env():
    data = {
        "project_name": "t",
        "python_min": "3.11",
        "logs": {"app": {"dev": "./x.log", "default_env": "staging"}},
    }
    try:
        parse(data)
        assert False, "expected ManifestError"
    except ManifestError as e:
        assert "default_env" in str(e) and "staging" in str(e)
    print("OK: unknown default_env rejected")


def test_logs_env_vars_scanned():
    logs = {
        "app": {
            "default_env": None,
            "envs": {
                "prod": {
                    "path": "${LOG_ROOT:-/var/log}/app.log",
                    "host": "${APP_HOST}",
                    "user": None,
                },
            },
        }
    }
    lines = collect_env_var_calls([], urls=None, logs=logs)
    joined = " ".join(lines)
    assert "LOG_ROOT" in joined
    assert "/var/log" in joined
    assert "APP_HOST" in joined
    print("OK: env vars from logs section detected")


def test_seek_last_n_lines():
    import io
    import re as _re
    # Load the helper without triggering logtail's relative imports (it imports
    # from .core, which fails outside a package context). Strip the relative
    # import line and exec the file.
    source = (LIB_DIR / "logtail.py").read_text()
    source = _re.sub(r"from \.core import \([^)]*\)", "pass", source, count=1)
    ns: dict = {}
    exec(compile(source, "logtail.py", "exec"), ns)
    _seek_last_n_lines = ns["_seek_last_n_lines"]

    def take(data: bytes, n: int) -> bytes:
        buf = io.BytesIO(data)
        _seek_last_n_lines(buf, n)
        return buf.read()

    assert take(b"a\nb\nc\n", 2) == b"b\nc\n"
    assert take(b"a\nb\nc\n", 5) == b"a\nb\nc\n"
    assert take(b"a\nb", 1) == b"b", "unterminated last line must still be selectable"
    assert take(b"a\nb\nc", 2) == b"b\nc", "unterminated tail counts as a line"
    assert take(b"", 3) == b""
    assert take(b"only\n", 1) == b"only\n"
    print("OK: _seek_last_n_lines handles terminated and unterminated files")


def test_logs_render_emits_register_call():
    m = Manifest(
        project_name="Logs Test",
        python_min="3.11",
        archetypes=["simple"],
        modules=[],
        urls={},
        overrides={},
        custom={},
        order=[],
        logs={
            "app": {
                "default_env": "dev",
                "envs": {
                    "dev": {"path": "./tmp/app.log", "host": None, "user": None},
                },
            }
        },
    )
    resolved = resolve(m, ARCH_DIR)
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    assert "register_logs_command(" in script
    assert '"app"' in script
    assert '"./tmp/app.log"' in script
    # logtail module must be inlined
    assert "# === admin_lib.logtail ===" in script
    assert "def tail_local" in script
    print("OK: [logs] renders register_logs_command and inlines logtail")


def test_render_python_kind_emits_verbatim_body():
    """kind=python emits the `run` string as a Python block inside cmd_<name>.

    Replaces the [inline] escape hatch for projects that need to call admin_lib
    helpers with project-specific kwargs. No ${VAR} substitution — the body is
    emitted verbatim, so any env access is explicit via os.environ.get.
    """
    cmd = Command(
        name="dev",
        run="run_vite_dev(vite_port=int(os.environ.get('VITE_PORT', 5173)))",
        desc="Run backend + frontend",
        kind="python",
    )
    out = render_command(cmd)
    assert '@command("dev", "Run backend + frontend")' in out, out
    assert "def cmd_dev(args):" in out, out
    assert "set_log_context(\"dev\")" in out, out
    assert "run_vite_dev(vite_port=int(os.environ.get('VITE_PORT', 5173)))" in out, out
    assert "${" not in out, "python kind must not substitute ${VAR} placeholders"
    print("OK: kind=python emits verbatim Python body")


def test_render_python_kind_rejects_empty_run():
    cmd = Command(name="dev", run="", desc="...", kind="python")
    try:
        render_command(cmd)
    except Exception as e:
        assert "needs 'run'" in str(e), e
        print("OK: kind=python rejects empty run")
        return
    raise AssertionError("expected FragmentError for empty python run")


def test_collect_env_vars_scans_python_os_environ_get():
    """Python-kind commands that call os.environ.get('NAME') should surface the
    env var in `./admin env` just like ${VAR} placeholders in shell commands do.
    """
    cmd = Command(
        name="dev",
        run=(
            "run_vite_dev("
            "vite_port=int(os.environ.get('VITE_PORT', '5173')), "
            "dev_mounts=os.environ.get('DEV_MOUNTS'))"
        ),
        desc="...",
        kind="python",
    )
    calls = collect_env_var_calls([cmd])
    joined = "\n".join(calls)
    assert '"VITE_PORT"' in joined, joined
    assert 'default="5173"' in joined, joined
    assert '"DEV_MOUNTS"' in joined, joined
    assert '"cmd:dev"' in joined, joined
    print("OK: collect_env_var_calls finds os.environ.get in python-kind run")


def test_render_python_kind_multiline_body_indented():
    """A multi-line Python body is indented to fit inside the generated def."""
    cmd = Command(
        name="boot",
        run="info('starting')\nrun_vite_dev(vite_port=5173)",
        desc="Boot it",
        kind="python",
    )
    out = render_command(cmd)
    assert "    info('starting')" in out, out
    assert "    run_vite_dev(vite_port=5173)" in out, out
    print("OK: kind=python indents multi-line body")


if __name__ == "__main__":
    test_all_archetypes_load_and_validate()
    test_composition_docker_unraid_plus_apple()
    test_detector_composition()
    test_hugo_roundtrip()
    test_unknown_command_key_rejected()
    test_invalid_kind_rejected()
    test_python_min_validation()
    test_env_vars_from_urls()
    test_logs_section_parsed()
    test_logs_rejects_unknown_default_env()
    test_logs_env_vars_scanned()
    test_seek_last_n_lines()
    test_logs_render_emits_register_call()
    test_render_python_kind_emits_verbatim_body()
    test_render_python_kind_rejects_empty_run()
    test_collect_env_vars_scans_python_os_environ_get()
    test_render_python_kind_multiline_body_indented()
    print("\nAll tests passed.")
