"""Unit tests for _build_run_args_from_inspect and related helpers in docker.py.

Loads admin_lib/docker.py via exec after stripping relative imports, then
injects stubs for the core helpers the function depends on.
"""
from __future__ import annotations

import re as _re
from pathlib import Path

LIB_DIR = Path(__file__).resolve().parent.parent / "admin_lib"


def _load_docker():
    source = (LIB_DIR / "docker.py").read_text()
    # Strip the multi-line from .core import (...) block
    source = _re.sub(r"from \.core import \([^)]*\)", "pass", source, flags=_re.DOTALL)
    # Strip the from .remote import ... inside try/except (leave try/except intact)
    source = _re.sub(r"from \.remote import [^\n]+", "pass", source)

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


def test_ulimits():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={"Ulimits": [{"Name": "nofile", "Soft": 1024, "Hard": 4096}]}))
    assert "--ulimit" in args
    idx = args.index("--ulimit")
    assert args[idx + 1] == "nofile=1024:4096"
    print("OK: ulimits")


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


if __name__ == "__main__":
    test_basic_field_translation()
    test_empty_and_default_fields_omitted()
    test_tmpfs_with_opts()
    test_tmpfs_no_opts()
    test_sysctls()
    test_security_opt()
    test_group_add()
    test_ulimits()
    test_unknown_field_triggers_warning()
    test_clean_dict_no_warnings()
    test_warn_prefix_in_message()
    print("\nAll docker tests passed.")
