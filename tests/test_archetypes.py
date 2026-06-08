"""Sanity tests for the archetype catalog and generator pipeline.

Asserts that every archetype in archetypes/ loads, has the required shape,
that detectors behave, and that a multi-archetype composition round-trips
through resolve+render without crashing.
"""
from __future__ import annotations

import os
import subprocess
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


def _run_generated_script(script: str, argv: list[str], timeout: int = 10) -> subprocess.CompletedProcess:
    """Write `script` to a temp file, run it with `argv`, clean up, return the result."""
    with tempfile.NamedTemporaryFile(suffix=".py", mode="w", delete=False) as f:
        f.write(script)
        path = f.name
    try:
        return subprocess.run(
            [sys.executable, path, *argv],
            capture_output=True, text=True, timeout=timeout,
        )
    finally:
        os.unlink(path)


def test_all_archetypes_load_and_validate():
    for name in EXPECTED:
        arch = load_archetype(ARCH_DIR, name)
        assert arch.name, f"{name}: empty archetype name"
        assert arch.modules, f"{name}: empty modules list"
        assert arch.commands, f"{name}: no commands"
        for cmd_name, data in arch.commands.items():
            assert isinstance(data, dict), f"{name}.{cmd_name}: not a dict"
            assert data.get("steps"), f"{name}.{cmd_name}: commands must have 'steps'"
            assert data.get("desc"), f"{name}.{cmd_name}: missing desc"
        for action_name, data in arch.actions.items():
            assert isinstance(data, dict), f"{name}.{action_name}: action not a dict"
            assert data.get("kind") or data.get("run"), f"{name}.{action_name}: action needs kind or run"
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
    assert "run_with_reload" in script  # docker-reload-loop action fragment
    assert "pick_target" in script      # multi-target action fragment
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
    assert "hugo --minify" in script       # in action body
    assert "http://localhost:1313" in script
    assert "run_command" in script          # commands route to actions via run_command
    print("OK: hugo round-trips")


def test_unknown_command_key_rejected():
    try:
        _to_command("bad", {"desc": "test", "steps": ["x"], "desription": "typo"}, source="test", is_action=False)
        assert False, "should have raised ArchetypeError"
    except ArchetypeError as e:
        assert "desription" in str(e)
        assert "invalid keys" in str(e)
    print("OK: unknown command key rejected")


def test_command_with_run_rejected():
    try:
        _to_command("bad", {"run": "echo hi", "desc": "test"}, source="test", is_action=False)
        assert False, "should have raised ArchetypeError"
    except ArchetypeError as e:
        assert "run" in str(e)
        assert "Move kind/run/targets" in str(e)
    print("OK: command with run= rejected")


def test_invalid_kind_rejected():
    try:
        _to_command("bad", {"run": "echo hi", "desc": "test", "kind": "bogus"}, source="test", is_action=True)
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
    cmds = [Command(name="x", run="echo ${HOST}", desc="test", kind="shell")]
    urls = {"prod": "${APP_URL:-http://localhost}", "dev": "http://localhost:8080"}
    lines = collect_env_var_calls(cmds, urls)
    var_names = " ".join(lines)
    assert "HOST" in var_names, "should find HOST from action run string"
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
    from admin_lib.logtail import _seek_last_n_lines

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


def test_render_python_action_emits_verbatim_body():
    """kind=python actions emit the `run` string as a Python block.

    No ${VAR} substitution — the body is emitted verbatim,
    so any env access is explicit via os.environ.get.
    """
    from gen.fragments import render_action
    cmd = Command(
        name="dev",
        run="run_vite_dev(vite_port=int(os.environ.get('VITE_PORT', 5173)))",
        desc="Run backend + frontend",
        kind="python",
    )
    out = render_action(cmd)
    assert "def _act_dev(args):" in out, out
    assert "register_action" in out, out
    assert "run_vite_dev(vite_port=int(os.environ.get('VITE_PORT', 5173)))" in out, out
    assert "${" not in out, "python kind must not substitute ${VAR} placeholders"
    print("OK: kind=python action emits verbatim Python body")


def test_render_python_action_rejects_empty_run():
    from gen.fragments import render_action
    cmd = Command(name="dev", run="", desc="...", kind="python")
    try:
        render_action(cmd)
    except Exception as e:
        assert "needs 'run'" in str(e), e
        print("OK: kind=python action rejects empty run")
        return
    raise AssertionError("expected FragmentError for empty python run")


def test_render_command_is_step_runner():
    """Commands render as step-runners that call run_command for each step."""
    cmd = Command(name="dev", run="", desc="Run dev", kind="steps",
                  meta={"steps": ["dev-action"]})
    out = render_command(cmd)
    assert '@command("dev"' in out, out
    assert "def cmd_dev(args):" in out, out
    assert 'set_log_context("dev")' in out, out
    assert 'run_command(_step, args)' in out, out
    assert '"dev-action"' in out, out
    print("OK: commands render as step-runners with set_log_context")


def test_collect_env_vars_scans_python_os_environ_get():
    """Python-kind actions that call os.environ.get('NAME') surface the env var."""
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


def test_render_python_action_multiline_body_indented():
    """A multi-line Python body is indented to fit inside the generated def."""
    from gen.fragments import render_action
    cmd = Command(
        name="boot",
        run="info('starting')\nrun_vite_dev(vite_port=5173)",
        desc="Boot it",
        kind="python",
    )
    out = render_action(cmd)
    assert "    info('starting')" in out, out
    assert "    run_vite_dev(vite_port=5173)" in out, out
    print("OK: kind=python indents multi-line body")


def test_log_override_keys_propagated_in_merge():
    """Per-command log_retain/log_file/log overrides must survive the archetype merge step.

    Regression for the bug where resolve() built a `merged` dict without log
    keys, so _to_command never saw them and cmd.meta['logging'] was empty.
    """
    m = Manifest(
        project_name="Log Override Test",
        python_min="3.11",
        archetypes=["simple"],
        modules=[],
        urls={},
        overrides={"build": {"log_retain": 7}},
        custom={},
        order=[],
    )
    resolved = resolve(m, ARCH_DIR)
    build_cmd = resolved.commands["build"]
    log_cfg = build_cmd.meta.get("logging", {})
    assert log_cfg.get("log_retain") == 7, (
        f"log_retain not propagated through resolve; meta={build_cmd.meta!r}"
    )
    print("OK: log_retain override survives archetype merge")


def test_log_false_override_propagated_in_merge():
    m = Manifest(
        project_name="Log Disable Test",
        python_min="3.11",
        archetypes=["simple"],
        modules=[],
        urls={},
        overrides={"build": {"log": False}},
        custom={},
        order=[],
    )
    resolved = resolve(m, ARCH_DIR)
    build_cmd = resolved.commands["build"]
    log_cfg = build_cmd.meta.get("logging", {})
    assert log_cfg.get("log") is False, (
        f"log=false not propagated through resolve; meta={build_cmd.meta!r}"
    )
    print("OK: log=false override survives archetype merge")


def test_render_multi_target_at_ref_args_forwarding():
    """render_multi_target generates run_command(shell[1:], rest_args) for @-ref targets.

    Regression guard for #295: the @-ref dispatch path must forward rest_args, not drop them.
    """
    from gen.fragments import render_action
    cmd = Command(
        name="build",
        run="",
        desc="Build",
        kind="multi-target",
        targets={"ios": "@build-ios", "mac": "xcodebuild -scheme Foo"},
    )
    out = render_action(cmd)
    assert "run_command(shell[1:], rest_args)" in out, (
        "@-ref targets must forward rest_args to run_command: " + out
    )
    assert "run_cmd(shell)" in out, "non-@-ref shell targets must use run_cmd"
    print("OK: render_multi_target @-ref path forwards rest_args")


def test_render_command_step_runner_dispatch_end_to_end():
    """Step-runner commands forward args to the dispatched action at runtime.

    Covers #296: end-to-end verification that args flow through run_command()
    when a generated step-runner command is invoked.
    """
    m = Manifest(
        project_name="StepTest",
        python_min="3.11",
        archetypes=[],
        modules=[],
        urls={},
        overrides={},
        custom={"runcmd": {"steps": ["echo-action"], "desc": "Echo args"}},
        order=[],
        actions={"echo-action": {"kind": "python", "desc": "Echo", "run": "print('got:', args)"}},
    )
    resolved = resolve(m, ARCH_DIR)
    script = render(resolved, LIB_DIR, generator_commit="test-sha")

    result = _run_generated_script(script, ["runcmd", "--flag", "value"])
    assert result.returncode == 0, f"script failed:\n{result.stderr}"
    assert "['--flag', 'value']" in result.stdout, (
        f"args not forwarded to action; stdout={result.stdout!r}"
    )
    print("OK: step-runner forwards args to dispatched action end-to-end")


def test_multi_target_at_ref_routing_end_to_end():
    """Multi-target @-ref dispatch routes to the action AND forwards trailing flags.

    Covers #298: './admin build ios --clean' must forward ['--clean'] to the do-ios action.
    """
    m = Manifest(
        project_name="RouteTest",
        python_min="3.11",
        archetypes=[],
        modules=[],
        urls={},
        overrides={},
        custom={"build": {"steps": ["build-action"], "desc": "Build"}},
        order=[],
        actions={
            "build-action": {
                "kind": "multi-target",
                "desc": "Build",
                "targets": {"ios": "@do-ios"},
            },
            "do-ios": {
                "kind": "python",
                "desc": "Build iOS",
                "run": "print('ios got:', args)",
            },
        },
    )
    resolved = resolve(m, ARCH_DIR)
    script = render(resolved, LIB_DIR, generator_commit="test-sha")

    result = _run_generated_script(script, ["build", "ios", "--clean"])
    assert result.returncode == 0, f"script failed:\n{result.stderr}"
    assert "['--clean']" in result.stdout, (
        f"trailing flags not forwarded via @-ref; stdout={result.stdout!r}"
    )
    print("OK: multi-target @-ref routing forwards trailing flags end-to-end")


def test_at_ref_validation_catches_unknown_action():
    """@-ref targets that reference non-existent actions raise ArchetypeError at resolve time.

    Covers #299: validation must happen at code-gen time, not at runtime.
    """
    m = Manifest(
        project_name="BadRef",
        python_min="3.11",
        archetypes=[],
        modules=[],
        urls={},
        overrides={},
        custom={"build": {"steps": ["build-action"], "desc": "Build"}},
        order=[],
        actions={
            "build-action": {
                "kind": "multi-target",
                "desc": "Build",
                "targets": {"ios": "@nonexistent"},
            },
        },
    )
    try:
        resolve(m, ARCH_DIR)
        assert False, "expected ArchetypeError for unknown @-ref"
    except ArchetypeError as e:
        assert "nonexistent" in str(e), str(e)
        assert "build-action" in str(e), str(e)
    print("OK: @-ref validation catches unknown action at resolve time")


def test_inline_target_action_key_adds_at_prefix():
    """Inline target subtables with 'action:' produce @-prefixed values in targets.

    Covers #218: the 'action' key must be prefixed with '@' so render_multi_target
    dispatches via run_command, not run_cmd (which would try to exec it as shell).
    """
    arch_data = {
        "kind": "multi-target",
        "desc": "Build",
        "ios": {"action": "build-ios", "desc": "Build iOS"},
        "mac": {"run": "xcodebuild -scheme Foo", "desc": "Build macOS"},
    }
    from gen.archetypes import _to_command
    cmd = _to_command("build", arch_data, source="test", is_action=True)
    assert cmd.targets["ios"] == "@build-ios", (
        f"'action' key must produce @-prefixed target; got {cmd.targets['ios']!r}"
    )
    assert cmd.targets["mac"] == "xcodebuild -scheme Foo", (
        f"'run' key must produce bare shell string; got {cmd.targets['mac']!r}"
    )
    print("OK: inline target 'action' key produces @-prefixed dispatch target")


def test_group_priority_top_level_sort():
    """Commands sorted by (group, priority, name) with spacers between groups."""
    manifest = parse({
        "project_name": "test",
        "python_min": "3.11",
        "commands": {
            "deploy": {"desc": "Deploy", "steps": ["deploy-action"], "group": 2, "priority": 10},
            "build":  {"desc": "Build",  "steps": ["build-action"],  "group": 1, "priority": 20},
            "dev":    {"desc": "Dev",    "steps": ["dev-action"],    "group": 1, "priority": 10},
            "open":   {"desc": "Open",   "steps": ["open-action"],   "group": 2, "priority": 20},
        },
        "actions": {
            "build-action":  {"run": "build"},
            "dev-action":    {"run": "dev"},
            "deploy-action": {"run": "deploy"},
            "open-action":   {"run": "open"},
        },
    })
    resolved = resolve(manifest, ARCH_DIR)
    assert resolved.display_order == ["dev", "build", "", "deploy", "open"], (
        f"wrong order: {resolved.display_order}"
    )
    print("OK: group+priority produces sorted display order with spacers")


def test_group_priority_all_default_alphabetical():
    """When all commands use defaults (group=0, priority=0), order is alphabetical."""
    manifest = parse({
        "project_name": "test",
        "python_min": "3.11",
        "commands": {
            "zebra": {"desc": "Z", "steps": ["z"]},
            "alpha": {"desc": "A", "steps": ["a"]},
            "beta":  {"desc": "B", "steps": ["b"]},
        },
        "actions": {
            "z": {"run": "z"},
            "a": {"run": "a"},
            "b": {"run": "b"},
        },
    })
    resolved = resolve(manifest, ARCH_DIR)
    assert resolved.display_order == ["alpha", "beta", "zebra"], (
        f"wrong order: {resolved.display_order}"
    )
    print("OK: all-default group/priority gives alphabetical order with no spacers")


def test_group_priority_inline_targets_sorted():
    """Inline target subtables with group/priority produce meta['target_order']."""
    arch_data = {
        "kind": "multi-target",
        "desc": "Build",
        "clean": {"run": "rm -rf build/", "desc": "Clean",       "group": 2, "priority": 10},
        "ios":   {"run": "xcodebuild ios", "desc": "Build iOS",  "group": 1, "priority": 20},
        "mac":   {"run": "xcodebuild mac", "desc": "Build macOS","group": 1, "priority": 10},
    }
    cmd = _to_command("build", arch_data, source="test", is_action=True)
    assert "target_order" in cmd.meta, "target_order should be set when group/priority declared"
    assert cmd.meta["target_order"] == ["mac", "ios", "", "clean"], (
        f"wrong target_order: {cmd.meta['target_order']}"
    )
    print("OK: inline target group/priority produces sorted target_order with spacer")


def test_group_priority_inline_targets_no_order_when_all_default():
    """Inline targets with all-default group/priority don't produce target_order."""
    arch_data = {
        "kind": "multi-target",
        "desc": "Build",
        "mac": {"run": "xcodebuild mac", "desc": "Build macOS"},
        "ios": {"run": "xcodebuild ios", "desc": "Build iOS"},
    }
    cmd = _to_command("build", arch_data, source="test", is_action=True)
    assert "target_order" not in cmd.meta, (
        f"target_order should not be set when all defaults; got {cmd.meta.get('target_order')}"
    )
    print("OK: inline targets with all-default group/priority produce no target_order")


def test_group_priority_renders_target_order_in_pick_target():
    """render_multi_target emits _target_order and order= kwarg when meta['target_order'] set."""
    from gen.fragments import render_multi_target
    cmd = Command(
        name="build", run="", desc="Build", kind="multi-target",
        targets={"mac": "xcodebuild mac", "ios": "xcodebuild ios", "clean": "rm -rf build/"},
        meta={
            "target_descs": {"mac": "Build macOS", "ios": "Build iOS", "clean": "Clean"},
            "target_order": ["mac", "ios", "", "clean"],
        },
    )
    code = render_multi_target(cmd)
    assert "_target_order" in code, "should emit _target_order variable"
    assert "order=_target_order" in code, "should pass order= kwarg to pick_target"
    assert "''" in code or '""' in code, "spacer entry should appear in _target_order literal"
    print("OK: render_multi_target emits _target_order + order= kwarg")


def test_group_priority_within_group_tiebreaking():
    """Within the same group, lower priority number (higher rank) appears first.

    Regression guard for #322: _sort_by_group_priority must sort by (group, priority, name)
    so priority=10 precedes priority=20 within the same group.
    """
    manifest = parse({
        "project_name": "test",
        "python_min": "3.11",
        "commands": {
            "secondary": {"desc": "Secondary", "steps": ["b"], "group": 1, "priority": 20},
            "important":  {"desc": "Important", "steps": ["a"], "group": 1, "priority": 10},
        },
        "actions": {
            "a": {"run": "a"},
            "b": {"run": "b"},
        },
    })
    resolved = resolve(manifest, ARCH_DIR)
    assert resolved.display_order == ["important", "secondary"], (
        f"priority=10 should appear before priority=20 in same group; got {resolved.display_order}"
    )
    print("OK: within same group, lower priority number appears first")


def test_pick_target_spacer_order_end_to_end():
    """Generated multi-target script with spacers in target_order correctly dispatches by arg.

    Regression guard for #318: spacers in _target_order must not break explicit target
    selection. Tests that the generated pick_target call uses order=_target_order and
    that arg-based dispatch (bypassing the TUI) still works with spacers present.
    """
    m = Manifest(
        project_name="SpacerTest",
        python_min="3.11",
        archetypes=[],
        modules=[],
        urls={},
        overrides={},
        custom={"build": {"steps": ["build-action"], "desc": "Build"}},
        order=[],
        actions={
            "build-action": {
                "kind": "multi-target",
                "desc": "Build",
                "mac":   {"run": "echo mac-built",  "desc": "Build macOS", "group": 1, "priority": 10},
                "ios":   {"run": "echo ios-built",  "desc": "Build iOS",   "group": 1, "priority": 20},
                "clean": {"run": "echo cleaned",    "desc": "Clean",       "group": 2, "priority": 10},
            },
        },
    )
    resolved = resolve(m, ARCH_DIR)
    script = render(resolved, LIB_DIR, generator_commit="test-sha")

    # spacers in _target_order must not break explicit target dispatch
    result = _run_generated_script(script, ["build", "ios"])
    assert result.returncode == 0, f"explicit target with spacers in order failed:\n{result.stderr}"
    assert "ios-built" in result.stdout, f"wrong target executed; stdout={result.stdout!r}"

    # verify the generated code actually has a spacer in the order list
    assert "''" in script or '""' in script, "spacer should appear in generated _target_order"
    assert "order=_target_order" in script, "order= kwarg should be passed to pick_target"
    print("OK: multi-target with spacers in target_order dispatches correctly")


def test_group_priority_type_validation_command():
    """Commands reject non-integer group/priority with a clear ArchetypeError.

    Regression guard for #319: int('high') raises ValueError with no context;
    instead we should get a clear error at parse time.
    """
    try:
        _to_command("build", {"desc": "Build", "steps": ["a"], "group": "high", "priority": 10},
                    source="test", is_action=False)
        raise AssertionError("should have raised ArchetypeError for string group")
    except ArchetypeError as e:
        assert "'group' must be an integer" in str(e), f"unexpected error: {e}"

    try:
        _to_command("build", {"desc": "Build", "steps": ["a"], "group": 1, "priority": "low"},
                    source="test", is_action=False)
        raise AssertionError("should have raised ArchetypeError for string priority")
    except ArchetypeError as e:
        assert "'priority' must be an integer" in str(e), f"unexpected error: {e}"
    print("OK: non-integer group/priority on command raises clear ArchetypeError")


def test_group_priority_type_validation_inline_target():
    """Inline target subtables reject non-integer group/priority with a clear ArchetypeError.

    Regression guard for #319.
    """
    try:
        _to_command("build", {
            "kind": "multi-target", "desc": "Build",
            "mac": {"run": "xcodebuild mac", "group": "high", "priority": 10},
        }, source="test", is_action=True)
        raise AssertionError("should have raised ArchetypeError for string group on inline target")
    except ArchetypeError as e:
        assert "'group' must be an integer" in str(e), f"unexpected error: {e}"

    try:
        _to_command("build", {
            "kind": "multi-target", "desc": "Build",
            "ios": {"run": "xcodebuild ios", "group": 1, "priority": "last"},
        }, source="test", is_action=True)
        raise AssertionError("should have raised ArchetypeError for string priority on inline target")
    except ArchetypeError as e:
        assert "'priority' must be an integer" in str(e), f"unexpected error: {e}"
    print("OK: non-integer group/priority on inline target raises clear ArchetypeError")


def test_group_priority_on_action_level_gives_hint():
    """group/priority at the [actions.X] level (not on a subtable) gets a helpful error.

    Regression guard for #316: the error must name 'group'/'priority' and hint that
    they belong on inline target subtables, not on the action itself.
    """
    try:
        _to_command("build", {
            "kind": "multi-target", "desc": "Build",
            "group": 1,
            "targets": {"mac": "xcodebuild mac"},
        }, source="test", is_action=True)
        raise AssertionError("should have raised ArchetypeError for group on action level")
    except ArchetypeError as e:
        assert "inline target subtables" in str(e), f"expected hint about subtables, got: {e}"
    print("OK: group/priority on action level gives subtable hint")


def test_group_priority_on_action_level_gives_hint_via_load_archetype():
    """load_archetype also emits the inline-target-subtable hint for group/priority on actions.

    Regression guard for #316: both the load_archetype validation path and the _to_command
    action path must emit the hint — _to_command alone is already covered above.
    """
    import shutil
    import tempfile

    arch_code = (
        'ARCHETYPE = {\n'
        '    "name": "test-gp-hint",\n'
        '    "modules": ["core"],\n'
        '    "commands": {"build": {"desc": "Build", "steps": ["build"]}},\n'
        '    "actions": {"build": {"kind": "shell", "run": "make", "group": 1}},\n'
        '}\n'
    )
    tmp_dir = Path(tempfile.mkdtemp())
    arch_file = tmp_dir / "test-gp-hint.py"
    arch_file.write_text(arch_code)
    try:
        try:
            load_archetype(tmp_dir, "test-gp-hint")
            raise AssertionError("should have raised ArchetypeError for group on action level")
        except ArchetypeError as e:
            assert "inline target subtables" in str(e), f"expected subtable hint, got: {e}"
    finally:
        shutil.rmtree(tmp_dir)
    print("OK: load_archetype also gives subtable hint for group/priority on action level")


def test_manifest_override_propagates_group_and_priority():
    """manifest [commands.X] overrides must carry group/priority into the resolved Command.

    Regression guard for #323: the merge dict previously dropped these fields, so
    overridden commands inherited the archetype's group/priority (often 0) regardless
    of what the manifest specified.
    """
    m = Manifest(
        project_name="GPOverrideTest",
        python_min="3.11",
        archetypes=["docker-unraid"],
        modules=[],
        urls={},
        overrides={"logs": {"group": 5, "priority": 99}},
        custom={},
        order=[],
    )
    resolved = resolve(m, ARCH_DIR)
    logs_cmd = resolved.commands["logs"]
    assert logs_cmd.group == 5, f"expected group=5 after override, got {logs_cmd.group}"
    assert logs_cmd.priority == 99, f"expected priority=99 after override, got {logs_cmd.priority}"
    print("OK: manifest override propagates group/priority to resolved Command")


def test_inline_target_action_run_mutual_exclusion():
    """Inline target subtable with both 'action' and 'run' raises ArchetypeError.

    Regression guard for #307: the validation guard in _to_command could be silently
    removed and all other tests would still pass.
    """
    try:
        _to_command("build", {
            "kind": "multi-target",
            "desc": "Build",
            "ios": {"action": "build-ios", "run": "xcodebuild ios", "desc": "Build iOS"},
        }, source="test", is_action=True)
        raise AssertionError("expected ArchetypeError for action+run on same target")
    except ArchetypeError as e:
        assert "both 'action' and 'run'" in str(e), f"unexpected error: {e}"
        assert "ios" in str(e), f"target name missing from error: {e}"
    print("OK: inline target with both 'action' and 'run' raises ArchetypeError")


def test_inline_target_action_ref_dispatch_end_to_end():
    """Inline subtable 'action:' @-ref dispatches correctly and forwards args at runtime.

    Regression guard for #302: the _to_command unit test covers the @-prefix logic,
    but a regression in the generated dispatch code could pass unit tests while
    silently breaking actual runtime behavior.
    """
    m = Manifest(
        project_name="ActionRefTest",
        python_min="3.11",
        archetypes=[],
        modules=[],
        urls={},
        overrides={},
        custom={"build": {"steps": ["build-action"], "desc": "Build"}},
        order=[],
        actions={
            "build-action": {
                "kind": "multi-target",
                "desc": "Build",
                "ios": {"action": "do-ios", "desc": "Build iOS"},
                "mac": {"run": "echo mac-built", "desc": "Build macOS"},
            },
            "do-ios": {
                "kind": "python",
                "desc": "Build iOS",
                "run": "print('ios got:', args)",
            },
        },
    )
    resolved = resolve(m, ARCH_DIR)
    script = render(resolved, LIB_DIR, generator_commit="test-sha")

    result = _run_generated_script(script, ["build", "ios", "--verbose"])
    assert result.returncode == 0, f"script failed:\n{result.stderr}"
    assert "['--verbose']" in result.stdout, (
        f"args not forwarded through inline action: ref dispatch; stdout={result.stdout!r}"
    )
    print("OK: inline target subtable action: @-ref dispatches and forwards args end-to-end")


def test_render_apple_dev_loop_at_ref_args_forwarding():
    """render_apple_dev_loop generates run_command(shell[1:], rest_args) for @-ref targets.

    Regression guard for #304: apple-dev-loop delegates to render_multi_target, but the
    kind must appear in the @-ref validation and its render path must forward args.
    This unit test covers the render side; the e2e test below covers runtime dispatch.
    """
    from gen.fragments import render_action
    cmd = Command(
        name="dev",
        run="",
        desc="Dev loop",
        kind="apple-dev-loop",
        targets={"device": "@dev-device", "sim": "echo sim-built"},
    )
    out = render_action(cmd)
    assert "run_command(shell[1:], rest_args)" in out, (
        "apple-dev-loop @-ref targets must forward rest_args: " + out
    )
    assert "run_cmd(shell)" in out, "non-@-ref shell targets must use run_cmd"
    print("OK: render_apple_dev_loop @-ref path forwards rest_args")


def test_apple_dev_loop_at_ref_dispatch_end_to_end():
    """apple-dev-loop @-ref target dispatches and forwards args at runtime.

    Regression guard for #304: apple-dev-loop is validated in resolve() alongside
    multi-target; this test ensures the kind's inclusion in that block is exercised
    and that args flow through to the referenced action at runtime.
    """
    m = Manifest(
        project_name="AppleDevLoopTest",
        python_min="3.11",
        archetypes=[],
        modules=[],
        urls={},
        overrides={},
        custom={"dev": {"steps": ["dev-action"], "desc": "Dev"}},
        order=[],
        actions={
            "dev-action": {
                "kind": "apple-dev-loop",
                "desc": "Dev loop",
                "targets": {"device": "@dev-device", "sim": "echo sim-built"},
            },
            "dev-device": {
                "kind": "python",
                "desc": "Dev on device",
                "run": "print('device got:', args)",
            },
        },
    )
    resolved = resolve(m, ARCH_DIR)
    script = render(resolved, LIB_DIR, generator_commit="test-sha")

    result = _run_generated_script(script, ["dev", "device", "--no-clean"])
    assert result.returncode == 0, f"script failed:\n{result.stderr}"
    assert "['--no-clean']" in result.stdout, (
        f"args not forwarded through apple-dev-loop @-ref; stdout={result.stdout!r}"
    )
    print("OK: apple-dev-loop @-ref dispatches correctly and forwards args end-to-end")




# ---------------------------------------------------------------------------
# hide_commands: drop a command from the listing while keeping its action
# ---------------------------------------------------------------------------

def _docker_manifest(**kw):
    base = dict(
        project_name="Hide Test",
        python_min="3.11",
        archetypes=["docker-unraid"],
        modules=[],
        urls={},
        overrides={},
        custom={},
        order=[],
    )
    base.update(kw)
    return Manifest(**base)


def test_hide_commands_drops_command_keeps_action():
    m = _docker_manifest(hide_commands=["reload", "open", "diff"])
    resolved = resolve(m, ARCH_DIR)
    for hidden in ("reload", "open", "diff"):
        assert hidden not in resolved.commands, f"{hidden} should be hidden"
        assert hidden not in resolved.display_order, f"{hidden} still in display order"
    # Actions survive so other commands' steps still resolve and they stay callable.
    for act in ("reload", "open", "diff"):
        assert act in resolved.actions, f"action {act} should remain"
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    assert "def cmd_reload" not in script
    assert "def cmd_open" not in script
    assert "def cmd_diff" not in script


def test_hide_commands_unknown_raises():
    m = _docker_manifest(hide_commands=["nonesuch"])
    try:
        resolve(m, ARCH_DIR)
    except ArchetypeError as e:
        assert "nonesuch" in str(e)
        return
    raise AssertionError("expected ArchetypeError for unknown hide_commands entry")


def test_archetype_logs_command_not_shadowed_by_builtin():
    """docker-unraid provides a `logs` command, so the builtin file-tailer
    registration must NOT be emitted (it would overwrite the real one)."""
    m = _docker_manifest()
    resolved = resolve(m, ARCH_DIR)
    assert "logs" in resolved.commands
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    import re as _re
    # The *call* must not be emitted (the def is hard-kept; match a bare call line).
    assert not _re.search(r"(?m)^register_builtin_logs_command\(\)$", script)
    assert "logs_dispatch(" in script  # the dispatcher is wired in


def test_hiding_logs_does_not_readd_builtin():
    """Hiding `logs` must NOT let the builtin file-tailer re-register a `logs`
    command — that would be the opposite of what hide_commands asked for."""
    m = _docker_manifest(hide_commands=["logs"])
    resolved = resolve(m, ARCH_DIR)
    assert "logs" not in resolved.commands
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    import re as _re
    assert not _re.search(r"(?m)^register_builtin_logs_command\(\)$", script)


def test_builtin_logs_emitted_when_no_logs_command():
    """An archetype without a `logs` command keeps the builtin file-tailer."""
    m = Manifest(
        project_name="No Logs Cmd",
        python_min="3.11",
        archetypes=["hugo"],
        modules=[],
        urls={},
        overrides={},
        custom={},
        order=[],
    )
    resolved = resolve(m, ARCH_DIR)
    assert "logs" not in resolved.commands
    script = render(resolved, LIB_DIR, generator_commit="test-sha")
    import re as _re
    assert _re.search(r"(?m)^register_builtin_logs_command\(\)$", script)
