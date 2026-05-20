"""Unit tests for _build_run_args_from_inspect and related helpers in docker.py.

Loads admin_lib/docker.py via exec after stripping relative imports, then
injects stubs for the core helpers the function depends on.
"""
from __future__ import annotations

import re as _re
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
LIB_DIR = REPO / "admin_lib"


def _load_docker():
    source = (LIB_DIR / "docker.py").read_text()
    # Strip all relative imports so docker.py can be exec'd without a package context.
    # Two forms: multi-line `from .mod import (...)` and single-line `from .mod import X`.
    # The catch-all module pattern handles any new relative import added to docker.py.
    source = _re.sub(r"from \.[a-zA-Z_]+ import \([^)]*\)", "pass", source, flags=_re.DOTALL)
    source = _re.sub(r"from \.[a-zA-Z_]+ import [^\n]+", "pass", source)

    warnings = []
    ns: dict = {
        "C": type("C", (), {"DIM": "", "RESET": "", "GREEN": "", "RED": "", "YELLOW": ""})(),
        "LOG_DIR": "/tmp",
        "LOG_FILE": "/tmp/test.log",
        "_children": [],
        "_urls": {},
        "command": lambda *a, **k: (lambda f: f),
        "info": lambda *a, **k: None,
        "ok": lambda *a, **k: None,
        "err": lambda *a, **k: None,
        "warn": lambda *a: warnings.append(a[0] if a else ""),
        "register_cleanup": lambda *a, **k: None,
        "run_cmd": lambda *a, **k: 0,
        "check_url": lambda *a, **k: False,
        "format_url_status": lambda *a, **k: "",
        "pick_target": lambda *a, **k: None,
        "require_host": lambda *a, **k: None,
        "ssh_cmd": lambda *a, **k: 0,
    }
    exec(compile(source, "docker.py", "exec"), ns)
    ns["_warnings"] = warnings
    return ns


def _inspect(host_cfg=None, config=None, net_settings=None):
    return {
        "HostConfig": host_cfg or {},
        "Config": config or {},
        "NetworkSettings": net_settings or {},
    }


def test_basic_field_translation():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    inspect = _inspect(
        host_cfg={
            "Binds": ["/host/data:/container/data"],
            "PortBindings": {"8080/tcp": [{"HostIp": "", "HostPort": "9090"}]},
            "RestartPolicy": {"Name": "unless-stopped", "MaximumRetryCount": 0},
            "NetworkMode": "bridge",
            "ExtraHosts": ["myhost:1.2.3.4"],
            "Devices": [{"PathOnHost": "/dev/dri", "PathInContainer": "/dev/dri", "CgroupPermissions": "rwm"}],
            "CapAdd": ["NET_ADMIN"],
            "CapDrop": ["MKNOD"],
            "Privileged": True,
            "Memory": 536870912,
        },
        config={
            "Env": ["FOO=bar"],
            "Labels": {"com.example.app": "myapp"},
            "User": "1000",
            "Hostname": "mycontainer",
            "WorkingDir": "/app",
            "Tty": True,
            "OpenStdin": True,
        },
    )
    args = fn(inspect)
    assert "-v" in args and "/host/data:/container/data" in args
    assert "-p" in args and "9090:8080/tcp" in args
    assert "-e" in args and "FOO=bar" in args
    assert "--restart" in args and "unless-stopped" in args
    assert "--add-host" in args and "myhost:1.2.3.4" in args
    assert "--device" in args and "/dev/dri:/dev/dri" in args
    assert "--cap-add" in args and "NET_ADMIN" in args
    assert "--cap-drop" in args and "MKNOD" in args
    assert "--privileged" in args
    assert "--memory" in args and "536870912" in args
    assert "--label" in args and "com.example.app=myapp" in args
    assert "--user" in args and "1000" in args
    assert "--hostname" in args and "mycontainer" in args
    assert "--workdir" in args and "/app" in args
    assert "-t" in args
    assert "-i" in args
    print("OK: basic field translation")


def test_empty_and_default_fields_omitted():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    inspect = _inspect(
        host_cfg={
            "Memory": 0,
            "NetworkMode": "default",
            "RestartPolicy": {"Name": "no", "MaximumRetryCount": 0},
            "Binds": [],
            "PortBindings": {},
            "CapAdd": None,
            "Privileged": False,
        },
        config={
            "Env": [],
            "Labels": {},
            "User": "",
            "Tty": False,
            "OpenStdin": False,
        },
    )
    args = fn(inspect)
    assert "--memory" not in args
    assert "--network" not in args
    assert "--restart" not in args
    assert "-v" not in args
    assert "-p" not in args
    assert "--privileged" not in args
    assert "-t" not in args
    assert "-i" not in args
    print("OK: empty/default fields omitted")


def test_tmpfs_with_opts():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={"Tmpfs": {"/tmp": "size=64m,mode=1777"}}))
    assert "--tmpfs" in args
    idx = args.index("--tmpfs")
    assert args[idx + 1] == "/tmp:size=64m,mode=1777"
    print("OK: tmpfs with opts")


def test_tmpfs_no_opts():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={"Tmpfs": {"/run": ""}}))
    assert "--tmpfs" in args
    idx = args.index("--tmpfs")
    assert args[idx + 1] == "/run"
    print("OK: tmpfs no opts (no trailing colon)")


def test_sysctls():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={"Sysctls": {"net.core.somaxconn": "1024"}}))
    assert "--sysctl" in args
    idx = args.index("--sysctl")
    assert args[idx + 1] == "net.core.somaxconn=1024"
    print("OK: sysctls")


def test_security_opt():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={"SecurityOpt": ["no-new-privileges:true"]}))
    assert "--security-opt" in args
    idx = args.index("--security-opt")
    assert args[idx + 1] == "no-new-privileges:true"
    print("OK: security-opt")


def test_group_add():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={"GroupAdd": ["video", "docker"]}))
    assert "--group-add" in args
    assert "video" in args
    assert "docker" in args
    print("OK: group-add")


def test_ulimits_long_form():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={"Ulimits": [{"Name": "nofile", "Soft": 1024, "Hard": 4096}]}))
    assert "--ulimit" in args
    idx = args.index("--ulimit")
    assert args[idx + 1] == "nofile=1024:4096"
    print("OK: ulimits long form (soft != hard)")


def test_ulimits_shorthand():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={"Ulimits": [{"Name": "nofile", "Soft": 1024, "Hard": 1024}]}))
    assert "--ulimit" in args
    idx = args.index("--ulimit")
    assert args[idx + 1] == "nofile=1024", f"expected shorthand, got {args[idx + 1]!r}"
    print("OK: ulimits shorthand (soft == hard)")


def test_unknown_field_triggers_warning():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    ns["_warnings"].clear()
    fn(_inspect(host_cfg={"UnknownFutureField": "some-value"}))
    assert len(ns["_warnings"]) >= 1, "expected at least one warning for unknown field"
    print("OK: unknown field triggers warning")


def test_clean_dict_no_warnings():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    ns["_warnings"].clear()
    fn(_inspect(
        host_cfg={
            "Binds": ["/a:/b"],
            "RestartPolicy": {"Name": "always", "MaximumRetryCount": 0},
        },
        config={"Env": ["X=1"]},
    ))
    assert ns["_warnings"] == [], f"unexpected warnings: {ns['_warnings']}"
    print("OK: clean dict produces no warnings")


def test_warn_prefix_in_message():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    ns["_warnings"].clear()
    fn(_inspect(host_cfg={"SomeFutureKey": "value"}), warn_prefix="current")
    assert any("(current)" in w for w in ns["_warnings"]), (
        f"warn_prefix not found in warnings: {ns['_warnings']}"
    )
    print("OK: warn_prefix included in warning text")


def test_tmpfs_none_opts():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={"Tmpfs": {"/tmp": None}}))
    assert "--tmpfs" in args
    idx = args.index("--tmpfs")
    assert args[idx + 1] == "/tmp", f"None opts should produce bare path, got {args[idx + 1]!r}"
    print("OK: tmpfs None opts treated as empty (no trailing colon)")


def _pair_and_sort(flat):
    """Pair flag-value arguments and return sorted list of normalized pairs."""
    pairs = []
    i = 0
    while i < len(flat):
        flag = flat[i]
        if i + 1 < len(flat) and not flat[i + 1].startswith("-"):
            pairs.append(f"{flag} {flat[i + 1]}")
            i += 2
        else:
            pairs.append(flag)
            i += 1
    return sorted(pairs)


def test_tmpfs_sysctls_dict_order_stable():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]

    args_a = fn(_inspect(host_cfg={
        "Tmpfs": {"/tmp": "size=64m", "/run": ""},
        "Sysctls": {"net.core.somaxconn": "1024", "net.ipv4.tcp_tw_reuse": "1"},
    }))
    args_b = fn(_inspect(host_cfg={
        "Tmpfs": {"/run": "", "/tmp": "size=64m"},
        "Sysctls": {"net.ipv4.tcp_tw_reuse": "1", "net.core.somaxconn": "1024"},
    }))

    result_a = _pair_and_sort(args_a)
    result_b = _pair_and_sort(args_b)
    assert result_a == result_b, (
        "Tmpfs/Sysctls dict order caused drift false positive:\n"
        f"  a: {result_a}\n  b: {result_b}"
    )
    print("OK: Tmpfs/Sysctls dict ordering doesn't cause false-positive diffs")


def test_restore_image_from_inspect():
    ns = _load_docker()
    fn = ns["_restore_image_from_inspect"]

    sha = "sha256:abc123def456789"
    assert fn({"Image": sha}, "my-app:latest") == sha, "should use SHA when present"
    assert fn({"Image": ""}, "my-app:latest") == "my-app:latest", "should fall back to tag on empty SHA"
    assert fn({}, "my-app:latest") == "my-app:latest", "should fall back to tag when Image key missing"
    assert fn({"Image": None}, "my-app:latest") == "my-app:latest", "should fall back to tag on None SHA"
    print("OK: _restore_image_from_inspect prefers SHA, falls back to tag")


def test_logconfig_no_warning():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    ns["_warnings"].clear()
    fn(_inspect(host_cfg={"LogConfig": {"Type": "json-file", "Config": {}}}))
    assert ns["_warnings"] == [], f"LogConfig should not warn, got: {ns['_warnings']}"
    print("OK: LogConfig does not trigger unconsumed-field warning")


def test_custom_network_mode():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={"NetworkMode": "mynet"}))
    assert "--network" in args
    idx = args.index("--network")
    assert args[idx + 1] == "mynet", f"expected mynet, got {args[idx + 1]!r}"
    print("OK: custom NetworkMode emits --network mynet")


def test_ulimits_zero_soft():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={"Ulimits": [{"Name": "nofile", "Soft": 0, "Hard": 4096}]}))
    assert "--ulimit" in args
    idx = args.index("--ulimit")
    assert args[idx + 1] == "nofile=0:4096", f"expected nofile=0:4096, got {args[idx + 1]!r}"
    print("OK: ulimits zero-soft / nonzero-hard edge case")


def test_devices_differing_paths():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={
        "Devices": [{"PathOnHost": "/dev/video0", "PathInContainer": "/dev/video1", "CgroupPermissions": "rwm"}]
    }))
    assert "--device" in args
    idx = args.index("--device")
    assert args[idx + 1] == "/dev/video0:/dev/video1", f"got {args[idx + 1]!r}"
    print("OK: Devices with differing host/container paths")


def _make_reload_driver(child_file: Path, log_suffix: str) -> str:
    """Return a Python -c script that runs run_with_reload with the given child file."""
    lines = [
        f"import sys; sys.path.insert(0, {str(REPO)!r})",
        "import admin_lib.docker as dm",
        f"dm.LOG_DIR = '/tmp'",
        f"dm.LOG_FILE = '/tmp/test-reload-{log_suffix}.log'",
        "try:",
        f"    dm.run_with_reload([sys.executable, {str(child_file)!r}], target='test', shell=False)",
        "except (KeyboardInterrupt, SystemExit): pass",
    ]
    return "\n".join(lines)


def _child_script(ready_file: Path, sig_file: Path) -> str:
    """Return a child process script that records which signal it receives."""
    lines = [
        "import sys, signal, pathlib, time",
        f"ready = pathlib.Path({str(ready_file)!r})",
        f"sigf  = pathlib.Path({str(sig_file)!r})",
        "if sigf.exists(): raise SystemExit(0)",
        "def h(n, f):",
        "    sigf.write_text('SIGINT' if n == signal.SIGINT else 'SIGTERM')",
        "    raise SystemExit(0)",
        "signal.signal(signal.SIGINT, h)",
        "signal.signal(signal.SIGTERM, h)",
        "ready.write_text('ok')",
        "time.sleep(30)",
    ]
    return "\n".join(lines)


def _wait_for_file(path: Path, timeout: float = 10.0) -> None:
    deadline = time.monotonic() + timeout
    while not path.exists():
        assert time.monotonic() < deadline, f"timeout waiting for {path}"
        time.sleep(0.05)


def test_run_with_reload_sends_sigint_on_ctrl_c():
    """SIGINT to run_with_reload forwards SIGINT (not SIGTERM) to the child process.

    Regression guard for #317: the one-line fix in 29ecb56 changed the exit-signal
    from SIGTERM to SIGINT; a future refactor of the finally block could silently revert it.
    """
    ready_file = Path(tempfile.mktemp(suffix=".ready"))
    sig_file = Path(tempfile.mktemp(suffix=".sig"))
    child_file = Path(tempfile.mktemp(suffix=".py"))
    child_file.write_text(_child_script(ready_file, sig_file))
    driver_src = _make_reload_driver(child_file, "sigint")

    proc = subprocess.Popen(
        [sys.executable, "-c", driver_src],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    try:
        _wait_for_file(ready_file)
        proc.send_signal(signal.SIGINT)
        proc.wait(timeout=5)

        assert sig_file.exists(), "child never recorded a signal"
        received = sig_file.read_text()
        assert received == "SIGINT", f"expected SIGINT on Ctrl-C exit, child received {received}"
        print("OK: run_with_reload sends SIGINT to child when user Ctrl-Cs")
    finally:
        if proc.poll() is None:
            proc.kill()
        for f in (ready_file, sig_file, child_file):
            if f.exists():
                f.unlink()


def test_run_with_reload_sends_sigterm_on_reload():
    """SIGUSR1 (reload) to run_with_reload forwards SIGTERM (not SIGINT) to the child.

    Regression guard for #317: the reload path (triggered by SIGUSR1 or 'r' keypress)
    must always send SIGTERM so the child can distinguish a reload from a user exit.
    The child script exits immediately on its 2nd invocation (after reload starts a new
    child) so run_with_reload returns cleanly.
    """
    ready_file = Path(tempfile.mktemp(suffix=".ready"))
    sig_file = Path(tempfile.mktemp(suffix=".sig"))
    child_file = Path(tempfile.mktemp(suffix=".py"))
    child_file.write_text(_child_script(ready_file, sig_file))
    driver_src = _make_reload_driver(child_file, "sigterm")

    proc = subprocess.Popen(
        [sys.executable, "-c", driver_src],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    try:
        _wait_for_file(ready_file)
        proc.send_signal(signal.SIGUSR1)
        _wait_for_file(sig_file, timeout=5.0)

        received = sig_file.read_text()
        assert received == "SIGTERM", f"expected SIGTERM on reload, child received {received}"
        print("OK: run_with_reload sends SIGTERM to child on reload (SIGUSR1)")
    finally:
        if proc.poll() is None:
            proc.send_signal(signal.SIGINT)
            time.sleep(0.5)
        if proc.poll() is None:
            proc.kill()
        for f in (ready_file, sig_file, child_file):
            if f.exists():
                f.unlink()


