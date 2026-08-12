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

sys.path.insert(0, str(REPO))

# Real, not stubbed. docker.py shares core's placeholder pattern so `${VAR}` and
# `${VAR:-default}` mean the same thing in a [docker_run] table as everywhere
# else in the manifest; substituting a fake here would let the two drift apart
# again without a test noticing.
from admin_lib.core import COMPUTED_VARS, _ENV_VAR_RE, MASK_VALUE, is_secret_env_name  # noqa: E402


def _load_docker():
    source = (LIB_DIR / "docker.py").read_text()
    # Strip all relative imports so docker.py can be exec'd without a package context.
    # Two forms: multi-line `from .mod import (...)` and single-line `from .mod import X`.
    # The catch-all module pattern handles any new relative import added to docker.py.
    source = _re.sub(r"from \.[a-zA-Z_]+ import \([^)]*\)", "pass", source, flags=_re.DOTALL)
    source = _re.sub(r"from \.[a-zA-Z_]+ import [^\n]+", "pass", source)

    warnings = []
    logged = []
    ns: dict = {
        "C": type("C", (), {"DIM": "", "RESET": "", "GREEN": "", "RED": "", "YELLOW": ""})(),
        "get_log_dir": lambda: "/tmp",
        "get_log_file": lambda: "/tmp/test.log",
        "get_urls": lambda: [],
        "_children": [],
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
        "COMPUTED_VARS": COMPUTED_VARS,
        "_ENV_VAR_RE": _ENV_VAR_RE,
        # Real, not stubbed — same rationale as COMPUTED_VARS/_ENV_VAR_RE above:
        # the secret-masking heuristic (issue #436) must be the one core.py
        # actually ships, or a test could pass against a fake that diverges.
        "MASK_VALUE": MASK_VALUE,
        "is_secret_env_name": is_secret_env_name,
        "_log_write": lambda text: logged.append(text),
    }
    exec(compile(source, "docker.py", "exec"), ns)
    ns["_warnings"] = warnings
    ns["_logged"] = logged
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


def test_device_requests_gpus_all():
    """`--gpus all` lives in HostConfig.DeviceRequests; inspect-recreate must preserve it.

    Regression guard: prior to this fix, _build_run_args_from_inspect silently
    dropped DeviceRequests, so any deploy that went through the inspect-and-
    recreate path stripped GPU access from the container — ffmpeg would then
    fail with "Cannot load libcuda.so.1 / Operation not permitted".
    """
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    ns["_warnings"].clear()
    args = fn(_inspect(host_cfg={
        "DeviceRequests": [
            {"Driver": "nvidia", "Count": -1, "DeviceIDs": None, "Capabilities": [["gpu"]]}
        ],
    }))
    assert "--gpus" in args, f"expected --gpus in args, got {args!r}"
    idx = args.index("--gpus")
    assert args[idx + 1] == "all", f"expected 'all', got {args[idx + 1]!r}"
    assert ns["_warnings"] == [], f"DeviceRequests should not warn, got: {ns['_warnings']}"
    print("OK: DeviceRequests Count=-1 → --gpus all")


def test_device_requests_gpus_count():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={
        "DeviceRequests": [{"Driver": "nvidia", "Count": 2, "Capabilities": [["gpu"]]}],
    }))
    idx = args.index("--gpus")
    assert args[idx + 1] == "2", f"expected '2', got {args[idx + 1]!r}"
    print("OK: DeviceRequests Count=2 → --gpus 2")


def test_device_requests_gpus_device_ids():
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={
        "DeviceRequests": [{
            "Driver": "nvidia", "Count": 0,
            "DeviceIDs": ["GPU-abc123", "GPU-def456"],
            "Capabilities": [["gpu"]],
        }],
    }))
    idx = args.index("--gpus")
    assert args[idx + 1] == "device=GPU-abc123,GPU-def456", f"got {args[idx + 1]!r}"
    print("OK: DeviceRequests DeviceIDs → --gpus device=... (no embedded quotes)")


def test_device_requests_non_nvidia_skipped():
    """Non-nvidia device requests aren't expressible via --gpus; skip rather than mistranslate."""
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    args = fn(_inspect(host_cfg={
        "DeviceRequests": [{"Driver": "amd", "Count": -1}],
    }))
    assert "--gpus" not in args, f"non-nvidia driver should not emit --gpus, got {args!r}"
    print("OK: non-nvidia DeviceRequests skipped")


def test_device_requests_null_no_warning():
    """A container with no GPU has DeviceRequests=null; must not trigger unconsumed-field warning."""
    ns = _load_docker()
    fn = ns["_build_run_args_from_inspect"]
    ns["_warnings"].clear()
    fn(_inspect(host_cfg={"DeviceRequests": None}))
    assert ns["_warnings"] == [], f"DeviceRequests=null should not warn, got: {ns['_warnings']}"
    print("OK: DeviceRequests=null does not warn")


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


# --- Drift detection (TOML-canonical deploy path) -------------------------

_MUXARR_CFG = {
    "image_tag": "muxarr:latest",
    "container_name": "muxarr",
    "gpu": "all",
    "restart": "unless-stopped",
    "ports": [{"host": "100.114.249.118:9091", "container": 9091}],
    "bind_mounts": [
        {"host": "/mnt/user/appdata/muxarr", "container": "/config"},
    ],
    "devices": [{"host": "/dev/dri", "container": "/dev/dri"}],
    "env": [
        {"name": "PORT", "value": "9091"},
        {"name": "NVIDIA_DRIVER_CAPABILITIES", "value": "all"},
    ],
}


def test_cfg_renderer_emits_gpu_and_device():
    ns = _load_docker()
    args = ns["_build_run_args_from_cfg"](_MUXARR_CFG, env_vars={})
    pair_set = ns["_argv_to_pair_set"](args)
    assert "--gpus\tall" in pair_set
    assert "--device\t/dev/dri:/dev/dri" in pair_set
    assert "--restart\tunless-stopped" in pair_set
    assert "-v\t/mnt/user/appdata/muxarr:/config" in pair_set
    assert "-p\t100.114.249.118:9091:9091/tcp" in pair_set
    assert "-e\tPORT=9091" in pair_set
    print("OK: cfg renderer emits expected pairs for muxarr-shape config")


def test_cfg_renderer_emits_user():
    ns = _load_docker()
    cfg = dict(_MUXARR_CFG, user="99:100")
    args = ns["_build_run_args_from_cfg"](cfg, env_vars={})
    pair_set = ns["_argv_to_pair_set"](args)
    assert "--user\t99:100" in pair_set
    # Absent user key → no --user flag (don't override the image default).
    args_none = ns["_build_run_args_from_cfg"](_MUXARR_CFG, env_vars={})
    assert "--user" not in args_none
    print("OK: cfg renderer emits --user when set, omits it when unset")


def test_cfg_renderer_expands_env_in_port_host():
    ns = _load_docker()
    cfg = dict(_MUXARR_CFG, ports=[{"host": "${TS_IP}:8080", "container": 8080}])
    args = ns["_build_run_args_from_cfg"](cfg, env_vars={"TS_IP": "100.64.0.1"})
    pair_set = ns["_argv_to_pair_set"](args)
    assert "-p\t100.64.0.1:8080:8080/tcp" in pair_set
    # Unset env → port skipped (so drift surfaces instead of a literal ${VAR}).
    args_unset = ns["_build_run_args_from_cfg"](cfg, env_vars={})
    assert not any("${TS_IP}" in a for a in args_unset)
    assert "-p" not in args_unset
    print("OK: cfg renderer expands ${VAR} in port host, skips on unset")


def test_cfg_renderer_optional_mount_gated_on_env():
    ns = _load_docker()
    cfg = dict(_MUXARR_CFG, optional_mounts=[
        {"env": "MOUNT_PLEX", "container": "/media/plex", "mode": "ro"},
    ])
    # env unset → mount skipped
    args_skipped = ns["_build_run_args_from_cfg"](cfg, env_vars={})
    assert not any("/media/plex" in a for a in args_skipped), \
        f"optional mount should be skipped when env unset: {args_skipped!r}"
    # env set → mount emitted
    args_set = ns["_build_run_args_from_cfg"](cfg, env_vars={"MOUNT_PLEX": "/mnt/user/media"})
    assert "-v" in args_set
    assert "/mnt/user/media:/media/plex:ro" in args_set
    print("OK: optional_mounts gated on host env var (TOML drift would surface if env changed)")


def test_drift_detects_missing_gpu_in_live():
    """The regression we just fixed: cfg says --gpus all, live container lost it."""
    ns = _load_docker()
    target = ns["_build_run_args_from_cfg"](_MUXARR_CFG, env_vars={})
    # Simulate live container that lost DeviceRequests
    live_inspect = _inspect(
        host_cfg={
            "Binds": ["/mnt/user/appdata/muxarr:/config"],
            "PortBindings": {"9091/tcp": [{"HostIp": "100.114.249.118", "HostPort": "9091"}]},
            "RestartPolicy": {"Name": "unless-stopped", "MaximumRetryCount": 0},
            "Devices": [{"PathOnHost": "/dev/dri", "PathInContainer": "/dev/dri", "CgroupPermissions": "rwm"}],
            "DeviceRequests": None,  # <-- GPU lost
        },
        config={"Env": ["PORT=9091", "NVIDIA_DRIVER_CAPABILITIES=all"]},
    )
    live = ns["_live_argv_for_drift"](live_inspect)
    in_target_only, in_live_only = ns["_compute_drift"](target, live)
    assert any("--gpus" in p for p in in_target_only), \
        f"--gpus should appear in target-only: {in_target_only!r}"
    assert not any("--gpus" in p for p in in_live_only), \
        f"--gpus should NOT appear in live-only: {in_live_only!r}"
    print("OK: drift surfaces missing --gpus on live (the muxarr GPU bug)")


def test_memory_suffix_not_drift():
    """--memory 24g (TOML) vs 25769803776 bytes (docker inspect) is the same
    cap, so it must NOT register as drift."""
    ns = _load_docker()
    assert ns["_normalize_mem_bytes"]("24g") == "25769803776"
    assert ns["_normalize_mem_bytes"]("512m") == str(512 * 1024 ** 2)
    assert ns["_normalize_mem_bytes"]("25769803776") == "25769803776"
    assert ns["_normalize_mem_bytes"]("1.5g") == str(int(1.5 * 1024 ** 3))
    assert ns["_normalize_mem_bytes"]("weird") == "weird"
    target = ["--memory", "24g", "--cap-add", "PERFMON"]
    live = ["--memory", "25769803776", "--cap-add", "PERFMON"]
    in_target_only, in_live_only = ns["_compute_drift"](target, live)
    assert not in_target_only and not in_live_only, \
        f"memory unit notation should not drift: {in_target_only!r} {in_live_only!r}"
    print("OK: --memory 24g == 25769803776 bytes, no phantom drift")


def test_argv_to_pair_set_value_starting_with_dash():
    """A flag's value can legitimately start with "-" (e.g. a `--ulimit
    core=-1` unlimited form, or a future flag whose value is a bare negative
    number). The old startswith("-") heuristic read that value as the next
    flag, silently dropping the real flag from the pair set — issue #387."""
    ns = _load_docker()
    args = ["--ulimit", "-1", "--cap-add", "SYS_NICE"]
    pair_set = ns["_argv_to_pair_set"](args)
    assert "--ulimit\t-1" in pair_set, f"got {pair_set!r}"
    assert "--cap-add\tSYS_NICE" in pair_set, f"got {pair_set!r}"
    assert "-1\t" not in pair_set, f"value misread as a flag: {pair_set!r}"
    print("OK: dash-prefixed flag values are paired with their flag, not misread as a flag")


def test_live_argv_for_drift_strips_dash_prefixed_image_workdir():
    """_live_argv_for_drift had its own copy of the startswith("-") heuristic
    _argv_to_pair_set was fixed for in #387 — same bug, different function
    (issue #468). It decides whether to strip --workdir by comparing the
    live value against the image's baked WorkingDir; when that value starts
    with "-" (a legitimate path piece, e.g. a dir literally named "-app"),
    the old heuristic misread it as "no value" and left a bare, unpaired
    "--workdir" in the output instead of recognizing the match and
    stripping it."""
    ns = _load_docker()
    live_inspect = _inspect(config={"WorkingDir": "-app"})
    live = ns["_live_argv_for_drift"](live_inspect, image_workdir="-app")
    assert "--workdir" not in live, \
        f"--workdir should be stripped (matches image_workdir): {live!r}"
    assert "-app" not in live, f"dash-prefixed value leaked into output: {live!r}"
    print("OK: _live_argv_for_drift strips --workdir when its dash-prefixed value matches the image default")


def test_drift_detects_dash_value_flag_dropped_from_target():
    """The regression this issue describes: a target-only flag whose value
    starts with "-" must still surface as drift when missing from live —
    not silently vanish because its value was misread as a flag."""
    ns = _load_docker()
    target = ["--ulimit", "-1", "--cap-add", "SYS_NICE"]
    live = ["--cap-add", "SYS_NICE"]
    in_target_only, in_live_only = ns["_compute_drift"](target, live)
    assert in_target_only == ["--ulimit\t-1"], f"got {in_target_only!r}"
    assert in_live_only == [], f"got {in_live_only!r}"
    print("OK: --ulimit -1 missing from live is detected as drift, not silently dropped")


def test_remote_container_diff_pairs_dash_prefixed_value(monkeypatch, capsys):
    """remote_container_diff's own `_pair_args` closure carried a third copy
    of the startswith("-") heuristic `_argv_to_pair_set` was fixed for in
    #387 and `_live_argv_for_drift` was fixed for in #468 part 1 (issue
    #468 part 2). A flag whose value starts with "-" (e.g. a working dir
    literally named "-app") got split into two bare, unpaired tokens
    instead of staying "--workdir -app" — so a real drift (the workdir was
    removed on redeploy) rendered as two malformed diff lines instead of
    one clean paired line.

    Drives `remote_container_diff` end to end (mocked subprocess for the
    remote `docker inspect` and the snapshot `cat`) and pins its whole
    reported diff."""
    import json as _json

    ns = _load_docker()
    ns["_is_local_host"] = lambda host: False

    last_inspect = [{
        "HostConfig": {},
        "Config": {"WorkingDir": "-app"},
        "NetworkSettings": {},
    }]
    curr_inspect = [{
        "HostConfig": {},
        "Config": {},
        "NetworkSettings": {},
    }]

    class _Proc:
        def __init__(self, payload):
            self.returncode = 0
            self.stdout = _json.dumps(payload)

    def _fake_run(argv, capture_output=True, text=True):
        if argv[:3] == ["ssh", "remotehost", "docker"]:
            return _Proc(curr_inspect)
        if argv[:2] == ["ssh", "remotehost"] and argv[2].startswith("cat "):
            return _Proc(last_inspect)
        raise AssertionError(f"unexpected subprocess.run call: {argv!r}")

    monkeypatch.setattr(ns["subprocess"], "run", _fake_run)

    rc = ns["remote_container_diff"](
        "remotehost", "testimage", container="testcontainer", output_format="json",
    )
    assert rc == 1
    result = _json.loads(capsys.readouterr().out)

    assert result["diff"] == [
        "--- last-deploy", "+++ current", "@@ -1 +0,0 @@", "---workdir -app",
    ], (
        f"--workdir and its dash-prefixed value must appear as one paired "
        f"removal line, not two bare tokens: {result['diff']!r}"
    )
    print("OK: remote_container_diff pairs a dash-prefixed value with its flag instead of splitting it")


def test_drift_detects_undeclared_live_field_strict():
    """Strict drift: live has a port mapping TOML doesn't declare → drift."""
    ns = _load_docker()
    target = ns["_build_run_args_from_cfg"](_MUXARR_CFG, env_vars={})
    live_inspect = _inspect(
        host_cfg={
            "Binds": ["/mnt/user/appdata/muxarr:/config"],
            "PortBindings": {
                "9091/tcp": [{"HostIp": "100.114.249.118", "HostPort": "9091"}],
                "9999/tcp": [{"HostIp": "", "HostPort": "9999"}],  # <-- not in TOML
            },
            "RestartPolicy": {"Name": "unless-stopped", "MaximumRetryCount": 0},
            "Devices": [{"PathOnHost": "/dev/dri", "PathInContainer": "/dev/dri", "CgroupPermissions": "rwm"}],
            "DeviceRequests": [{"Driver": "nvidia", "Count": -1, "Capabilities": [["gpu"]]}],
        },
        config={"Env": ["PORT=9091", "NVIDIA_DRIVER_CAPABILITIES=all"]},
    )
    live = ns["_live_argv_for_drift"](live_inspect)
    in_target_only, in_live_only = ns["_compute_drift"](target, live)
    assert any("9999" in p for p in in_live_only), \
        f"undeclared port should appear in live-only: {in_live_only!r}"
    print("OK: strict drift flags live fields TOML doesn't declare")


def test_drift_clean_when_target_matches_live():
    ns = _load_docker()
    target = ns["_build_run_args_from_cfg"](_MUXARR_CFG, env_vars={})
    live_inspect = _inspect(
        host_cfg={
            "Binds": ["/mnt/user/appdata/muxarr:/config"],
            "PortBindings": {"9091/tcp": [{"HostIp": "100.114.249.118", "HostPort": "9091"}]},
            "RestartPolicy": {"Name": "unless-stopped", "MaximumRetryCount": 0},
            "Devices": [{"PathOnHost": "/dev/dri", "PathInContainer": "/dev/dri", "CgroupPermissions": "rwm"}],
            "DeviceRequests": [{"Driver": "nvidia", "Count": -1, "Capabilities": [["gpu"]]}],
        },
        config={"Env": ["PORT=9091", "NVIDIA_DRIVER_CAPABILITIES=all"]},
    )
    live = ns["_live_argv_for_drift"](live_inspect)
    in_target_only, in_live_only = ns["_compute_drift"](target, live)
    assert in_target_only == [] and in_live_only == [], (
        f"clean match should produce no drift; got target_only={in_target_only!r} "
        f"live_only={in_live_only!r}"
    )
    print("OK: clean target/live match produces no drift")


def test_live_argv_strips_image_env():
    """Image-baked env vars (NV_CUDA_*, NVIDIA_REQUIRE_CUDA, etc.) must be
    filtered from live before comparison, else every deploy would be drifty."""
    ns = _load_docker()
    inspect = _inspect(config={"Env": ["NV_CUDA_LIB_VERSION=12.6.3-1", "PORT=9091"]})
    image_env = ["NV_CUDA_LIB_VERSION=12.6.3-1"]
    args = ns["_live_argv_for_drift"](inspect, image_env=image_env)
    pair_set = ns["_argv_to_pair_set"](args)
    assert "-e\tPORT=9091" in pair_set
    assert "-e\tNV_CUDA_LIB_VERSION=12.6.3-1" not in pair_set, \
        f"image env should be stripped: {pair_set!r}"
    print("OK: live_argv_for_drift strips image-baked env vars")


def test_format_drift_report_renders_both_sections():
    ns = _load_docker()
    report = ns["_format_drift_report"](["--gpus\tall"], ["-p\t:9999:9999/tcp"])
    assert "target has, live missing" in report
    assert "live has, target missing" in report
    assert "--gpus" in report and "all" in report
    assert "9999" in report
    assert "--force" in report
    print("OK: drift report renders both target-only and live-only sections")


def test_format_drift_report_empty_when_no_drift():
    ns = _load_docker()
    assert ns["_format_drift_report"]([], []) == ""
    print("OK: empty drift produces empty report (caller doesn't print)")


def test_env_substitution_dollar_brace():
    """${VAR} in env value gets substituted from process env at render time
    so drift compares actual values, not literal ${VAR} strings."""
    ns = _load_docker()
    cfg = {"env": [{"name": "RADARR_API_KEY", "value": "${RADARR_API_KEY}"}]}
    args = ns["_build_run_args_from_cfg"](cfg, env_vars={"RADARR_API_KEY": "abc123"})
    assert args == ["-e", "RADARR_API_KEY=abc123"], f"got {args!r}"
    print("OK: ${VAR} substituted from process env")


def test_env_substitution_omitted_value_inherits():
    """`name = X` with no value behaves like value = ${X} — inherits from process env."""
    ns = _load_docker()
    cfg = {"env": [{"name": "TZ"}]}
    args = ns["_build_run_args_from_cfg"](cfg, env_vars={"TZ": "America/New_York"})
    assert args == ["-e", "TZ=America/New_York"]
    print("OK: omitted value inherits from process env")


def test_env_substitution_missing_var_skipped():
    """If a ${VAR} reference is unset, drop the entry (warn) rather than emit
    a literal ${VAR} that would falsely flag drift on every deploy."""
    ns = _load_docker()
    ns["_warnings"].clear()
    cfg = {"env": [
        {"name": "PRESENT", "value": "${PRESENT}"},
        {"name": "ABSENT", "value": "${ABSENT}"},
    ]}
    args = ns["_build_run_args_from_cfg"](cfg, env_vars={"PRESENT": "yes"})
    assert "-e" in args
    assert "PRESENT=yes" in args
    assert not any("ABSENT" in a for a in args), f"missing var should be skipped: {args!r}"
    assert any("ABSENT" in w for w in ns["_warnings"]), \
        f"should warn about missing env var: {ns['_warnings']!r}"
    print("OK: missing ${VAR} skipped with warning, not emitted as literal")


def test_labels_emitted_with_substitution():
    """[[docker_run.labels]] emits --label key=value with ${VAR} substitution."""
    ns = _load_docker()
    cfg = {"labels": [
        {"key": "net.unraid.docker.managed", "value": "dockerman"},
        {"key": "net.unraid.docker.webui", "value": "${PROD_URL}"},
    ]}
    args = ns["_build_run_args_from_cfg"](cfg, env_vars={"PROD_URL": "https://muxarr.example.com"})
    assert "--label" in args
    assert "net.unraid.docker.managed=dockerman" in args
    assert "net.unraid.docker.webui=https://muxarr.example.com" in args
    print("OK: labels emitted with ${VAR} substitution")


def test_labels_missing_ref_skipped():
    ns = _load_docker()
    ns["_warnings"].clear()
    cfg = {"labels": [{"key": "foo", "value": "${MISSING}"}]}
    args = ns["_build_run_args_from_cfg"](cfg, env_vars={})
    assert not any("foo" in a for a in args)
    assert any("MISSING" in w for w in ns["_warnings"])
    print("OK: label with unresolved ${VAR} skipped + warned")


def test_cfg_mount_mode_rw_normalized():
    """mode = "rw" matches Docker's default; cfg must NOT emit :rw or
    drift would falsely fire vs the live-side `:rw` strip on every deploy."""
    ns = _load_docker()
    cfg = {"bind_mounts": [
        {"host": "/a", "container": "/b"},                  # no mode
        {"host": "/c", "container": "/d", "mode": "rw"},    # explicit rw
        {"host": "/e", "container": "/f", "mode": "ro"},    # ro preserved
    ]}
    args = ns["_build_run_args_from_cfg"](cfg, env_vars={})
    binds = [args[i+1] for i in range(len(args)) if args[i] == "-v"]
    assert binds == ["/a:/b", "/c:/d", "/e:/f:ro"], f"got {binds!r}"
    print("OK: cfg mode='rw' and unset both render without :rw suffix")


def test_live_strips_explicit_hostname():
    """--hostname is always stripped — Unraid GUI seeds hostname=<container_name>
    (not a 12-hex autogen) and there's no TOML knob to declare it."""
    ns = _load_docker()
    insp = _inspect(config={"Hostname": "muxarr"})
    args = ns["_live_argv_for_drift"](insp)
    assert "--hostname" not in args, f"hostname should be stripped: {args!r}"
    print("OK: explicit --hostname stripped from live (no TOML to compete with)")


def test_live_strips_unraid_host_env():
    """HOST_CONTAINERNAME / HOST_HOSTNAME / HOST_OS are Unraid-injected
    diagnostics; flagging drift on them just clutters every deploy."""
    ns = _load_docker()
    inspect = _inspect(config={"Env": [
        "PORT=9091", "HOST_CONTAINERNAME=muxarr",
        "HOST_HOSTNAME=PierceTower", "HOST_OS=Unraid",
    ]})
    args = ns["_live_argv_for_drift"](inspect)
    pair_set = ns["_argv_to_pair_set"](args)
    assert "-e\tPORT=9091" in pair_set
    assert not any("HOST_" in p for p in pair_set), \
        f"HOST_* should be stripped: {pair_set!r}"
    print("OK: Unraid HOST_* env vars stripped from live")


def test_env_substitution_mixed_literal_and_ref():
    """Values can mix literals and ${VAR} references — common for URLs."""
    ns = _load_docker()
    cfg = {"env": [{"name": "RADARR_URL", "value": "http://${RADARR_HOST}:7878"}]}
    args = ns["_build_run_args_from_cfg"](
        cfg, env_vars={"RADARR_HOST": "100.114.249.118"}
    )
    assert args == ["-e", "RADARR_URL=http://100.114.249.118:7878"]
    print("OK: mixed literal + ${VAR} substitution")


def test_parse_env_file_basic():
    ns = _load_docker()
    fn = ns["_parse_env_file"]
    assert fn(None) == {}
    assert fn("/nonexistent/path/.env") == {}
    # Write a temp file
    import tempfile, os as _os
    fd, path = tempfile.mkstemp(suffix=".env")
    try:
        _os.write(fd, b"# comment\nFOO=bar\nBAZ=\"quoted value\"\nSING='q'\n\nEMPTY=\n")
        _os.close(fd)
        result = fn(path)
        # Docker's --env-file keeps surrounding quotes verbatim (they become
        # literal chars in the value), so the parser must NOT strip them.
        assert result == {"FOO": "bar", "BAZ": '"quoted value"', "SING": "'q'", "EMPTY": ""}
    finally:
        _os.unlink(path)
    print("OK: env file parser handles comments, blank lines, verbatim quotes")


def _make_reload_driver(child_file: Path, log_suffix: str) -> str:
    """Return a Python -c script that runs run_with_reload with the given child file."""
    lines = [
        f"import sys; sys.path.insert(0, {str(REPO)!r})",
        "import admin_lib.docker as dm",
        "import admin_lib.core as _core",
        "_core.LOG_DIR = '/tmp'",
        f"_core.LOG_FILE = '/tmp/test-reload-{log_suffix}.log'",
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


# ---------------------------------------------------------------------------
# logs dispatch: dev (file tail) vs live (container logs) + --from parsing
# ---------------------------------------------------------------------------

def _load_docker_logs(tmp_dir=None, chosen="dev"):
    """docker.py namespace wired for logs_dispatch/remote_logs/tail_dev_log testing.

    Captures the shell command remote_logs builds, the argv tail_dev_log spawns,
    and the menu pick_target is shown. Forces remote so the SSH path is exercised.
    """
    ns = _load_docker()
    calls = {"run_cmd": [], "subprocess": [], "pick": []}
    ns["run_cmd"] = lambda cmd, *a, **k: calls["run_cmd"].append(cmd) or 0
    sp = type("SP", (), {})()
    sp.call = lambda argv, *a, **k: calls["subprocess"].append(list(argv)) or 0
    ns["subprocess"] = sp
    ns["_is_local_host"] = lambda host: False
    ns["pick_target"] = lambda menu, prompt=None: (calls["pick"].append(list(menu.keys())) or chosen)
    if tmp_dir is not None:
        ns["get_log_dir"] = lambda: tmp_dir
    ns["_calls"] = calls
    return ns


def test_parse_since_offset_full_and_partial():
    fn = _load_docker()["_parse_since_offset"]
    assert fn("00:02:00:00") == 7200          # 2 hours
    assert fn("01:00:00:00") == 86400         # 1 day
    assert fn("02:00:00") == 7200             # HH:MM:SS right-aligned
    assert fn("90") == 90                     # bare seconds
    assert fn("05:30") == 330                 # MM:SS


def test_parse_since_offset_rejects_bad_input():
    fn = _load_docker()["_parse_since_offset"]
    for bad in ("1:2:3:4:5", "abc", "1:x", "-5"):
        try:
            fn(bad)
        except ValueError:
            continue
        raise AssertionError(f"expected ValueError for {bad!r}")


# --- timestamp filtering (dev --from) -------------------------------------

def test_ts_epoch_parses_ansi_wrapped_rfc3339():
    fn = _load_docker()["_ts_epoch"]
    wrapped = "\x1b[2m2026-06-03T03:16:11.686512Z\x1b[0m \x1b[32m INFO\x1b[0m hi\n"
    plain = "2026-06-03T03:16:11Z INFO hi"
    assert fn(wrapped) is not None
    assert fn(plain) is not None
    assert abs(fn(wrapped) - fn(plain)) < 2          # same second, ignoring frac
    assert fn("Starting dev...\n") is None           # banner line, no ts
    assert fn("[Running 'cargo run']\n") is None
    # Nanosecond precision (>6 frac digits) must not blow up strptime.
    assert fn("2026-06-03T03:16:11.686512123Z hi") is not None


def test_lines_since_includes_from_first_match():
    ns = _load_docker()
    fn, ts = ns["_lines_since"], ns["_ts_epoch"]
    lines = [
        "banner no timestamp\n",
        "\x1b[2m2026-06-03T03:00:00Z\x1b[0m old\n",
        "\x1b[2m2026-06-03T04:00:00Z\x1b[0m new\n",
        "    untimestamped continuation\n",
    ]
    cutoff = ts("2026-06-03T03:30:00Z x")
    out, matched = fn(lines, cutoff)
    assert matched is True
    assert out == lines[2:]                           # from 04:00 onward, incl. continuation


def test_lines_since_no_match():
    ns = _load_docker()
    fn, ts = ns["_lines_since"], ns["_ts_epoch"]
    lines = ["\x1b[2m2026-06-03T01:00:00Z\x1b[0m old\n"]
    out, matched = fn(lines, ts("2026-06-03T05:00:00Z x"))
    assert matched is False and out == []


# --- dev file tailing ------------------------------------------------------

def test_newest_log_and_tail_no_since():
    import os
    with tempfile.TemporaryDirectory() as d:
        old = os.path.join(d, "dev.2026-01-01_000000.log")
        new = os.path.join(d, "dev.log")
        open(old, "w").write("old\n")
        open(new, "w").write("new\n")
        os.utime(old, (1, 1))                         # force old mtime
        ns = _load_docker_logs(tmp_dir=d)
        ns["tail_dev_log"]()
        argv = ns["_calls"]["subprocess"][-1]
        assert argv[:3] == ["tail", "-n", "100"] and argv[-1] == new


def test_tail_dev_log_since_no_match_warns_and_follows():
    import os
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "dev.log")
        open(path, "w").write("2020-01-01T00:00:00Z ancient\n")
        ns = _load_docker_logs(tmp_dir=d)
        ns["tail_dev_log"](since=60)
        assert any("trailing new output only" in w for w in ns["_warnings"])
        assert ns["_calls"]["subprocess"][-1][:3] == ["tail", "-n", "0"]


def test_tail_dev_log_no_files_errors():
    with tempfile.TemporaryDirectory() as d:
        ns = _load_docker_logs(tmp_dir=d)
        assert ns["tail_dev_log"]() == 1
        assert ns["_calls"]["subprocess"] == []


# --- dispatch --------------------------------------------------------------

def test_remote_logs_default_follows():
    ns = _load_docker_logs()
    ns["remote_logs"]("root@box", "app:latest")
    cmd = ns["_calls"]["run_cmd"][-1]
    assert cmd.startswith("ssh ")
    assert "docker logs" in cmd and "-f" in cmd and "--tail=200" in cmd
    assert "--since" not in cmd


def test_logs_dispatch_live_from_since_and_follows():
    ns = _load_docker_logs()
    ns["logs_dispatch"](["live", "--from", "00:02:00:00"], image="app:latest", host="root@box")
    cmd = ns["_calls"]["run_cmd"][-1]
    assert "--since 7200s" in cmd and "-f" in cmd      # --from always trails too


def test_logs_dispatch_live_bare_follows():
    ns = _load_docker_logs()
    ns["logs_dispatch"](["live"], image="app:latest", host="root@box")
    cmd = ns["_calls"]["run_cmd"][-1]
    assert "-f" in cmd and "--since" not in cmd


def test_logs_dispatch_bad_from_errors_before_ssh():
    ns = _load_docker_logs()
    rc = ns["logs_dispatch"](["live", "--from", "nope"], image="app:latest", host="root@box")
    assert rc == 1 and ns["_calls"]["run_cmd"] == []


def test_logs_dispatch_dev_tails_file_not_container():
    import os
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "dev.log")
        open(path, "w").write("x\n")
        ns = _load_docker_logs(tmp_dir=d)
        ns["logs_dispatch"](["dev"], image="app:latest")
        assert ns["_calls"]["subprocess"][-1][-1] == path
        assert ns["_calls"]["run_cmd"] == []          # never hit docker logs


def test_logs_dispatch_live_no_follow_prints_and_exits():
    """`--no-follow` is what makes the log readable by anything that has to
    continue afterwards. Without it the only way to read a container log was a
    stream that never ends, so a script, a post-deploy check, or an agent hung
    instead of getting an answer."""
    ns = _load_docker_logs()
    ns["logs_dispatch"](["live", "--tail", "5", "--no-follow"], image="app:latest", host="root@box")
    cmd = ns["_calls"]["run_cmd"][-1]
    assert "--tail=5" in cmd
    assert " -f" not in cmd


def test_logs_dispatch_live_tail_equals_form():
    ns = _load_docker_logs()
    ns["logs_dispatch"](["live", "--tail=40"], image="app:latest", host="root@box")
    assert "--tail=40" in ns["_calls"]["run_cmd"][-1]


def test_logs_dispatch_unknown_flag_errors_before_ssh():
    """An unrecognised flag used to fall through unread and follow forever,
    which reads as a hang rather than as a rejected argument."""
    ns = _load_docker_logs()
    rc = ns["logs_dispatch"](["live", "--tale", "5"], image="app:latest", host="root@box")
    assert rc == 1 and ns["_calls"]["run_cmd"] == []


def test_logs_dispatch_dev_no_follow_omits_the_follow_flag():
    import os
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "dev.log")
        open(path, "w").write("x\n")
        ns = _load_docker_logs(tmp_dir=d)
        ns["logs_dispatch"](["dev", "--tail", "7", "--no-follow"], image="app:latest")
        argv = ns["_calls"]["subprocess"][-1]
        assert argv[-1] == path
        assert "-f" not in argv
        assert "7" in argv


def test_logs_dispatch_bare_offers_only_dev_and_live():
    ns = _load_docker_logs(chosen="live")
    ns["logs_dispatch"]([], image="app:latest", host="root@box")
    assert ns["_calls"]["pick"][-1] == ["dev", "live"]   # tiny 2-item menu, no files
    assert "docker logs" in ns["_calls"]["run_cmd"][-1]   # routed to live after pick


# ---------------------------------------------------------------------------
# Bind-mount provisioning (mkdir/chown) — the Unraid uid-99 trap
# ---------------------------------------------------------------------------

def test_provision_skips_mounts_that_did_not_opt_in():
    ns = _load_docker()
    cfg = {"bind_mounts": [{"host": "/mnt/a", "container": "/a"}]}
    assert ns["_provision_commands"](cfg) == []


def test_provision_mkdir_only():
    ns = _load_docker()
    cfg = {"bind_mounts": [{"host": "/mnt/a", "container": "/a", "mkdir": True}]}
    assert ns["_provision_commands"](cfg) == ["mkdir -p /mnt/a"]


def test_provision_chown_implies_mkdir():
    """chown without mkdir still creates the dir — you can't chown what isn't there."""
    ns = _load_docker()
    cfg = {"bind_mounts": [{"host": "/mnt/a", "container": "/a", "chown": "99:100"}]}
    assert ns["_provision_commands"](cfg) == ["mkdir -p /mnt/a && chown 99:100 /mnt/a"]


def test_provision_expands_env_in_host_path():
    ns = _load_docker()
    cfg = {"bind_mounts": [
        {"host": "${APPDATA}/data", "container": "/app/data", "mkdir": True, "chown": "99:100"},
    ]}
    out = ns["_provision_commands"](cfg, env_vars={"APPDATA": "/mnt/user/appdata/x"})
    assert out == ["mkdir -p /mnt/user/appdata/x/data && chown 99:100 /mnt/user/appdata/x/data"]


def test_provision_unresolved_env_skipped_with_warning():
    ns = _load_docker()
    cfg = {"bind_mounts": [{"host": "${NOPE}/data", "container": "/d", "mkdir": True}]}
    assert ns["_provision_commands"](cfg, env_vars={}) == []
    assert any("unset env var" in w for w in ns["_warnings"])


def test_provision_quotes_paths_with_spaces():
    ns = _load_docker()
    cfg = {"bind_mounts": [{"host": "/mnt/my data", "container": "/d", "mkdir": True}]}
    assert ns["_provision_commands"](cfg) == ["mkdir -p '/mnt/my data'"]


def test_provision_empty_cfg_is_empty():
    ns = _load_docker()
    assert ns["_provision_commands"]({}) == []
    assert ns["_provision_commands"](None) == []


# ---------------------------------------------------------------------------
# Post-deploy health check
# ---------------------------------------------------------------------------

def test_health_none_without_table_or_url():
    ns = _load_docker()
    assert ns["_health_check_shell"]({}) is None
    assert ns["_health_check_shell"]({"health": {}}) is None
    assert ns["_health_check_shell"](None) is None


def test_health_snippet_polls_expected_code():
    ns = _load_docker()
    cfg = {"health": {"url": "http://127.0.0.1:7860/", "expect": 200,
                      "timeout": 40, "interval": 2}}
    out = ns["_health_check_shell"](cfg)
    assert "seq 1 20" in out                 # timeout 40 / interval 2
    assert "http://127.0.0.1:7860/" in out
    assert '"$code" = "200"' in out
    assert "sleep 2" in out


def test_health_defaults_expect_200_and_30s():
    ns = _load_docker()
    out = ns["_health_check_shell"]({"health": {"url": "http://x/"}})
    assert '"$code" = "200"' in out
    assert "seq 1 15" in out                 # default timeout 30 / interval 2


def test_health_non_200_expectation_honored():
    ns = _load_docker()
    out = ns["_health_check_shell"]({"health": {"url": "http://x/", "expect": 401}})
    assert '"$code" = "401"' in out


def test_health_expands_env_in_url():
    ns = _load_docker()
    out = ns["_health_check_shell"]({"health": {"url": "http://127.0.0.1:${UI_PORT}/"}},
                                    env_vars={"UI_PORT": "7860"})
    assert "http://127.0.0.1:7860/" in out


def test_health_unresolved_env_returns_none_with_warning():
    ns = _load_docker()
    cfg = {"health": {"url": "http://127.0.0.1:${NOPE}/"}}
    assert ns["_health_check_shell"](cfg, env_vars={}) is None
    assert any("unset env var" in w for w in ns["_warnings"])


def test_health_interval_floor_is_one_second():
    """interval = 0 would divide-by-zero / busy-spin; floor it at 1."""
    ns = _load_docker()
    out = ns["_health_check_shell"]({"health": {"url": "http://x/", "interval": 0,
                                                "timeout": 5}})
    assert "sleep 1" in out
    assert "seq 1 5" in out


# ---------------------------------------------------------------------------
# Build on the deploy target ([docker_build].on_host) instead of locally
# ---------------------------------------------------------------------------

def _docker_ns_capturing(local=False):
    """docker.py namespace with run_cmd captured and _is_local_host forced.

    `_is_local_host` arrives via a relative import that _load_docker strips, so it
    has to be injected here or the build helper NameErrors.
    """
    ns = _load_docker()
    calls = []
    ns["run_cmd"] = lambda cmd, *a, **k: (calls.append(cmd), 0)[1]
    ns["_is_local_host"] = lambda h: local
    ns["_calls"] = calls
    return ns


def test_remote_docker_build_runs_over_ssh_when_remote():
    ns = _docker_ns_capturing(local=False)
    assert ns["remote_docker_build"]("root@box", "app:latest", "/mnt/user/appdata/app") == 0
    cmd = ns["_calls"][-1]
    assert cmd.startswith("ssh root@box ")
    assert "docker build" in cmd
    assert "app:latest" in cmd
    assert "/mnt/user/appdata/app" in cmd


def test_remote_docker_build_runs_directly_when_local():
    ns = _docker_ns_capturing(local=True)
    ns["remote_docker_build"]("root@box", "app:latest", "/srv/app")
    cmd = ns["_calls"][-1]
    assert not cmd.startswith("ssh ")
    assert cmd.startswith("cd /srv/app && docker build")


def test_remote_docker_build_passes_target_stage():
    ns = _docker_ns_capturing(local=True)
    ns["remote_docker_build"]("h", "i:1", "/c", target="prod")
    assert "--target prod" in ns["_calls"][-1]


def test_build_context_expands_env():
    ns = _load_docker()
    got = ns["_build_context"]({"context": "${APPDATA}/x"}, env_vars={"APPDATA": "/mnt/a"})
    assert got == "/mnt/a/x"


def test_build_context_unresolved_env_returns_none_with_warning():
    ns = _load_docker()
    assert ns["_build_context"]({"context": "${NOPE}/x"}, env_vars={}) is None
    assert any("unset env var" in w for w in ns["_warnings"])


def test_build_context_absent_returns_none():
    ns = _load_docker()
    assert ns["_build_context"]({}) is None
    assert ns["_build_context"](None) is None


def test_provision_and_health_read_from_parsed_toml():
    """The documented TOML shape maps onto both helpers — [docker_run] is a raw
    dict passthrough in manifest.py, so the nested tables must survive as-is."""
    import tomllib
    ns = _load_docker()
    manifest = (
        "[docker_run]\n"
        "[[docker_run.bind_mounts]]\n"
        'host = "/mnt/user/appdata/app/data"\n'
        'container = "/app/data"\n'
        "mkdir = true\n"
        'chown = "99:100"\n'
        "\n"
        "[docker_run.health]\n"
        'url = "http://127.0.0.1:7860/"\n'
        "timeout = 40\n"
    )
    cfg = tomllib.loads(manifest)["docker_run"]
    assert ns["_provision_commands"](cfg) == [
        "mkdir -p /mnt/user/appdata/app/data && chown 99:100 /mnt/user/appdata/app/data"
    ]
    out = ns["_health_check_shell"](cfg)
    assert "http://127.0.0.1:7860/" in out
    assert "seq 1 20" in out




def test_drift_report_omits_refusal_tail_under_force():
    """--force already printed 'proceeding'; the refusal tail would contradict it."""
    ns = _load_docker()
    forced = ns["_format_drift_report"](["-v\t/a:/b"], [], refusing=False)
    assert "-v" in forced
    assert "Refusing to deploy" not in forced
    assert "--force" not in forced
    default = ns["_format_drift_report"](["-v\t/a:/b"], [])
    assert "Refusing to deploy" in default


# ---------------------------------------------------------------------------
# Secret masking (issue #436) — dry-run preview, drift report, log persistence
# ---------------------------------------------------------------------------

def test_mask_argv_for_display_masks_only_secret_named_env():
    ns = _load_docker()
    argv = [
        "docker", "run", "-d", "--name", "etv-station",
        "-e", "PLEX_TOKEN=abc123secret",
        "-e", "PLEX_URL=http://10.0.0.5:32400",
        "-e", "TAUTULLI_API_KEY=xyz789",
        "myimage",
    ]
    masked = ns["_mask_argv_for_display"](argv)
    assert f"PLEX_TOKEN={ns['MASK_VALUE']}" in masked
    assert f"TAUTULLI_API_KEY={ns['MASK_VALUE']}" in masked
    assert "PLEX_URL=http://10.0.0.5:32400" in masked
    assert "abc123secret" not in masked
    assert "xyz789" not in masked
    # original argv (used for the actually-executed docker run) is untouched
    assert "PLEX_TOKEN=abc123secret" in argv
    print("OK: only *_TOKEN/*_KEY/*_SECRET/*_PASSWORD env values are masked for display")


def test_format_drift_report_masks_secret_env_value():
    ns = _load_docker()
    report = ns["_format_drift_report"](
        ["-e\tPLEX_TOKEN=newsecret"],
        ["-e\tPLEX_TOKEN=oldsecret", "-e\tPLEX_URL=http://10.0.0.9:32400"],
    )
    assert "newsecret" not in report
    assert "oldsecret" not in report
    assert f"PLEX_TOKEN={ns['MASK_VALUE']}" in report
    # the variable name and the fact that it differs must still be visible
    assert "PLEX_TOKEN" in report
    # a non-secret-named var stays in the clear
    assert "PLEX_URL=http://10.0.0.9:32400" in report
    print("OK: drift report masks secret-named env values, keeps names + non-secret values visible")


def test_remote_recreate_container_dry_run_masks_secret_but_not_real_run(monkeypatch, capsys):
    """The dry-run preview must mask a *_TOKEN value on screen and in the log,
    while the real docker run (dry_run=False) still gets the real value."""
    ns = _load_docker()
    ns["_is_local_host"] = lambda host: True

    class _NotFoundProc:
        returncode = 1
        stdout = ""

    monkeypatch.setattr(ns["subprocess"], "run", lambda *a, **k: _NotFoundProc())

    cfg = {
        "env": [
            {"name": "PLEX_TOKEN", "value": "supersecretvalue123"},
            {"name": "PLEX_URL", "value": "http://10.0.0.5:32400"},
        ],
    }

    rc = ns["remote_recreate_container"](
        "localhost", "myimage", container="admin-test-436",
        target_cfg=cfg, dry_run=True,
    )
    assert rc == 0
    out = capsys.readouterr().out
    assert "supersecretvalue123" not in out
    assert f"PLEX_TOKEN={ns['MASK_VALUE']}" in out
    assert "PLEX_URL=http://10.0.0.5:32400" in out

    # The dry-run line must reach the log file (masked) — issue #436's third
    # complaint was that the leak persists in tmp/deploy.<timestamp>.log.
    logged_text = "\n".join(ns["_logged"])
    assert "supersecretvalue123" not in logged_text
    assert f"PLEX_TOKEN={ns['MASK_VALUE']}" in logged_text

    # A real (non-dry-run) run must NOT mask — the actually-executed docker
    # run still needs the real token. run_cmd is stubbed to just record what
    # it was asked to run.
    executed = []
    ns["run_cmd"] = lambda cmd, *a, **k: (executed.append(cmd), 0)[1]
    rc2 = ns["remote_recreate_container"](
        "localhost", "myimage", container="admin-test-436",
        target_cfg=cfg, dry_run=False,
    )
    assert rc2 == 0
    assert any("supersecretvalue123" in cmd for cmd in executed), (
        f"the real docker run must carry the real secret value, got: {executed!r}"
    )
    print("OK: dry-run preview + log are masked; the real docker run still gets the real value")


def test_remote_recreate_container_force_drift_masks_and_logs_secret(monkeypatch, capsys):
    """A --force redeploy that hits drift on a *_TOKEN var must mask it in the
    printed report AND in what reaches the log (issue #436 follow-up comment:
    this path 'matters more' since it fires unprompted on an ordinary deploy)."""
    import json as _json

    ns = _load_docker()
    ns["_is_local_host"] = lambda host: True

    inspect_json = _json.dumps([{
        "HostConfig": {},
        "Config": {"Env": ["PLEX_TOKEN=oldsecretvalue", "PLEX_URL=http://10.0.0.9:32400"]},
        "NetworkSettings": {},
    }])

    class _InspectProc:
        returncode = 0
        stdout = inspect_json

    class _ImageInspectProc:
        returncode = 1
        stdout = ""

    def _fake_run(argv, capture_output=True, text=True):
        if argv[:3] == ["docker", "image", "inspect"]:
            return _ImageInspectProc()
        return _InspectProc()

    monkeypatch.setattr(ns["subprocess"], "run", _fake_run)
    ns["run_cmd"] = lambda *a, **k: 0

    cfg = {"env": [{"name": "PLEX_TOKEN", "value": "newsecretvalue"}]}
    rc = ns["remote_recreate_container"](
        "localhost", "myimage", container="admin-test-436",
        target_cfg=cfg, force=True, dry_run=True,
    )
    assert rc == 0
    out = capsys.readouterr().out
    assert "oldsecretvalue" not in out
    assert "newsecretvalue" not in out
    assert f"PLEX_TOKEN={ns['MASK_VALUE']}" in out
    assert "http://10.0.0.9:32400" in out  # non-secret var stays visible

    logged_text = "\n".join(ns["_logged"])
    assert "oldsecretvalue" not in logged_text
    assert "newsecretvalue" not in logged_text


# ---------------------------------------------------------------------------
# remote_env_file — optional_mounts/bind_mounts host paths on a remote deploy
# must be read on the target, not on the machine running `admin deploy`
# (issue #371).
# ---------------------------------------------------------------------------

def _not_found_proc():
    class P:
        returncode = 1
        stdout = ""
    return P()


def test_mount_env_deps_collects_optional_mounts_and_bind_mount_refs():
    ns = _load_docker()
    cfg = {
        "optional_mounts": [{"env": "MOUNT_MEDIA", "container": "/media"}],
        "bind_mounts": [
            {"host": "${APPDATA_DIR}/x", "container": "/data"},
            {"host": "/plain/path", "container": "/plain"},
        ],
    }
    deps = ns["_mount_env_deps"](cfg)
    assert set(deps) == {"MOUNT_MEDIA", "APPDATA_DIR"}


def test_mount_env_deps_empty_when_nothing_references_env():
    ns = _load_docker()
    cfg = {"bind_mounts": [{"host": "/plain/path", "container": "/plain"}]}
    assert ns["_mount_env_deps"](cfg) == []


def test_read_remote_env_file_parses_ssh_output(monkeypatch):
    ns = _load_docker()

    class _Proc:
        returncode = 0
        stdout = "MOUNT_MEDIA=/mnt/user/media\n# comment\nOTHER=val\n"

    calls = []

    def _fake_run(argv, **kwargs):
        calls.append(argv)
        return _Proc()

    monkeypatch.setattr(ns["subprocess"], "run", _fake_run)
    result = ns["_read_remote_env_file"]("root@box", "/boot/config/admin.env")
    assert result == {"MOUNT_MEDIA": "/mnt/user/media", "OTHER": "val"}
    assert calls[0][:2] == ["ssh", "root@box"]
    assert "cat" in calls[0][2] and "/boot/config/admin.env" in calls[0][2]


def test_read_remote_env_file_missing_file_is_empty_not_fatal(monkeypatch):
    ns = _load_docker()
    monkeypatch.setattr(ns["subprocess"], "run", lambda *a, **k: _not_found_proc())
    assert ns["_read_remote_env_file"]("root@box", "/nope") == {}


def test_remote_deploy_optional_mounts_no_remote_env_file_errors(monkeypatch, capsys):
    """A remote deploy with optional_mounts and no remote_env_file must refuse
    before creating the container, naming the variable rather than silently
    reading it from the deploy machine's own environment."""
    ns = _load_docker()
    ns["_is_local_host"] = lambda host: False
    monkeypatch.setattr(ns["subprocess"], "run", lambda *a, **k: _not_found_proc())
    errors = []
    ns["err"] = lambda *a: errors.append(a[0] if a else "")

    cfg = {"optional_mounts": [{"env": "MOUNT_MEDIA", "container": "/media"}]}
    rc = ns["remote_recreate_container"](
        "root@box", "myimage", container="admin-test-371",
        target_cfg=cfg, dry_run=True,
    )
    assert rc == 1
    assert any("MOUNT_MEDIA" in e and "remote_env_file" in e for e in errors), errors


def test_remote_deploy_bind_mounts_env_ref_no_remote_env_file_errors(monkeypatch):
    ns = _load_docker()
    ns["_is_local_host"] = lambda host: False
    monkeypatch.setattr(ns["subprocess"], "run", lambda *a, **k: _not_found_proc())
    errors = []
    ns["err"] = lambda *a: errors.append(a[0] if a else "")

    cfg = {"bind_mounts": [{"host": "${APPDATA_DIR}", "container": "/data"}]}
    rc = ns["remote_recreate_container"](
        "root@box", "myimage", container="admin-test-371b",
        target_cfg=cfg, dry_run=True,
    )
    assert rc == 1
    assert any("APPDATA_DIR" in e for e in errors), errors


def test_remote_deploy_with_remote_env_file_renders_targets_value(monkeypatch, capsys):
    """With remote_env_file set, the mount is rendered from the value read on
    the target over ssh — never from this process's own environment."""
    ns = _load_docker()
    ns["_is_local_host"] = lambda host: False
    monkeypatch.setenv("MOUNT_MEDIA", "/Users/pierce/Media")  # deploy machine's own value

    def _fake_run(argv, **kwargs):
        if len(argv) >= 3 and isinstance(argv[2], str) and argv[2].startswith("cat "):
            class _EnvProc:
                returncode = 0
                stdout = "MOUNT_MEDIA=/mnt/user/media\n"
            return _EnvProc()
        return _not_found_proc()

    monkeypatch.setattr(ns["subprocess"], "run", _fake_run)

    cfg = {
        "remote_env_file": "/boot/config/admin.env",
        "optional_mounts": [{"env": "MOUNT_MEDIA", "container": "/media"}],
    }
    rc = ns["remote_recreate_container"](
        "root@box", "myimage", container="admin-test-371c",
        target_cfg=cfg, dry_run=True,
    )
    assert rc == 0
    out = capsys.readouterr().out
    # The target's value is used, never the deploy machine's own MOUNT_MEDIA.
    assert "/mnt/user/media:/media" in out
    assert "/Users/pierce/Media" not in out


def test_remote_deploy_provisions_from_the_targets_env_not_this_machines(monkeypatch, capsys):
    """The mkdir/chown must resolve `${VAR}` from the same place the `-v` does.

    They did not. The mount read `remote_env_file`; the provisioning read
    `os.environ` and, finding nothing on a workstation, warned "references unset
    env var — skipping provision" and created nothing — one line above a
    `docker run` carrying that very path. Harmless where the directory already
    exists, which is why it read as cosmetic; on a fresh host it is exactly the
    uid-99 permission trap mkdir/chown exist to prevent.
    """
    ns = _load_docker()
    ns["_is_local_host"] = lambda host: False
    monkeypatch.delenv("APPDATA_DIR", raising=False)  # unset here, as on a real workstation

    def _fake_run(argv, **kwargs):
        if len(argv) >= 3 and isinstance(argv[2], str) and argv[2].startswith("cat "):
            class _EnvProc:
                returncode = 0
                stdout = "APPDATA_DIR=/mnt/user/appdata/thing\n"
            return _EnvProc()
        return _not_found_proc()

    monkeypatch.setattr(ns["subprocess"], "run", _fake_run)

    cfg = {
        "remote_env_file": "/boot/config/admin.env",
        "bind_mounts": [
            {
                "host": "${APPDATA_DIR}",
                "container": "/data",
                "mkdir": True,
                "chown": "99:100",
            }
        ],
    }
    rc = ns["remote_recreate_container"](
        "root@box", "myimage", container="admin-test-provision",
        target_cfg=cfg, dry_run=True,
    )

    assert rc == 0
    out = capsys.readouterr().out
    assert "mkdir -p /mnt/user/appdata/thing" in out
    assert "chown 99:100 /mnt/user/appdata/thing" in out
    assert "skipping provision" not in out
    # And the mount it prepares is the one docker run actually mounts.
    assert "/mnt/user/appdata/thing:/data" in out


def test_remote_deploy_var_absent_from_remote_env_file_skips_mount_silently(monkeypatch, capsys):
    """'Optional' means optional once the file being consulted is the
    target's own — a var absent from remote_env_file must skip cleanly,
    not error."""
    ns = _load_docker()
    ns["_is_local_host"] = lambda host: False

    def _fake_run(argv, **kwargs):
        if len(argv) >= 3 and isinstance(argv[2], str) and argv[2].startswith("cat "):
            class _EnvProc:
                returncode = 0
                stdout = "OTHER_VAR=1\n"
            return _EnvProc()
        return _not_found_proc()

    monkeypatch.setattr(ns["subprocess"], "run", _fake_run)

    cfg = {
        "remote_env_file": "/boot/config/admin.env",
        "optional_mounts": [{"env": "MOUNT_MEDIA", "container": "/media"}],
    }
    rc = ns["remote_recreate_container"](
        "root@box", "myimage", container="admin-test-371d",
        target_cfg=cfg, dry_run=True,
    )
    assert rc == 0
    out = capsys.readouterr().out
    assert "-v" not in out or "/media" not in out


def test_local_deploy_with_optional_mounts_unchanged(monkeypatch, capsys):
    """A local deploy keeps resolving optional_mounts from os.environ exactly
    as before remote_env_file existed — no remote_env_file required."""
    ns = _load_docker()
    ns["_is_local_host"] = lambda host: True
    monkeypatch.setenv("MOUNT_MEDIA", "/Users/pierce/Media")
    monkeypatch.setattr(ns["subprocess"], "run", lambda *a, **k: _not_found_proc())

    cfg = {"optional_mounts": [{"env": "MOUNT_MEDIA", "container": "/media"}]}
    rc = ns["remote_recreate_container"](
        "localhost", "myimage", container="admin-test-371e",
        target_cfg=cfg, dry_run=True,
    )
    assert rc == 0
    out = capsys.readouterr().out
    assert "/Users/pierce/Media:/media" in out
    print("OK: forced-redeploy drift report masks secret env in print AND log")


def test_remote_deploy_without_mounts_still_resolves_env_values_locally(monkeypatch, capsys):
    """A remote deploy whose config has no env-dependent mount path still
    resolves `env` values from the deploy machine. Only *paths* have to come
    from the target; a value like an API key is the deploy machine's to
    supply, and resolving it against an empty dict would silently drop it
    (_build_run_args_from_cfg warns and skips an unresolved ${VAR}), so the
    container would come up missing its secret rather than failing."""
    ns = _load_docker()
    ns["_is_local_host"] = lambda host: False
    monkeypatch.setenv("API_KEY", "sk-real-value")
    monkeypatch.setattr(ns["subprocess"], "run", lambda *a, **k: _not_found_proc())

    cfg = {"env": [{"name": "API_KEY", "value": "${API_KEY}"}]}
    rc = ns["remote_recreate_container"](
        "root@box", "myimage", container="admin-test-371f",
        target_cfg=cfg, dry_run=True,
    )
    assert rc == 0
    out = capsys.readouterr().out
    # The printed value is masked (secrets never reach stdout), so assert the
    # flag is emitted at all — resolving against {} drops it entirely.
    assert "API_KEY=" in out


# --- issue #388: the field table's round-trip contract -----------------------
# These pin the property the table exists to guarantee: a value declared in
# [docker_run] and the same value read back out of `docker inspect` must render
# the identical flag, so the drift comparison reports nothing. Without them the
# two renderers can fall back out of agreement exactly as they did before, and
# nothing would say so until a deploy reported drift no one could silence.

_ROUNDTRIP_CFG = {
    "cap_add": ["SYS_NICE"],
    "cap_drop": ["MKNOD"],
    "privileged": True,
    "security_opt": ["no-new-privileges"],
    "sysctls": {"net.core.somaxconn": "1024"},
    "memory": "24g",
    "user": "99:100",
    "add_host": ["myhost:1.2.3.4"],
    "tmpfs": {"/scratch": "size=64m"},
    "group_add": ["users"],
    "ulimits": ["nofile=8192"],
    "tty": True,
    "open_stdin": True,
    "hostname": "muxarr",
    "workdir": "/app",
    "mac_address": "02:42:ac:11:00:02",
}

_ROUNDTRIP_INSPECT_HOST = {
    "CapAdd": ["SYS_NICE"],
    "CapDrop": ["MKNOD"],
    "Privileged": True,
    "SecurityOpt": ["no-new-privileges"],
    "Sysctls": {"net.core.somaxconn": "1024"},
    "Memory": 24 * 1024 * 1024 * 1024,
    "ExtraHosts": ["myhost:1.2.3.4"],
    "Tmpfs": {"/scratch": "size=64m"},
    "GroupAdd": ["users"],
    "Ulimits": [{"Name": "nofile", "Soft": 8192, "Hard": 8192}],
    "NetworkMode": "bridge",
}

_ROUNDTRIP_INSPECT_CONFIG = {
    "User": "99:100",
    "Tty": True,
    "OpenStdin": True,
    "Hostname": "muxarr",
    "WorkingDir": "/app",
    "MacAddress": "02:42:ac:11:00:02",
}


def test_field_table_every_row_round_trips_declared_to_live():
    """Every row in _DOCKER_FIELD_TABLE renders the same flag from a declared
    [docker_run] value as from the matching docker-inspect value, so a
    correctly-declared container reports no drift. This is the property the
    table exists to hold; it is checked row by row so a future row that only
    implements one side fails here rather than in a deploy."""
    ns = _load_docker()
    cfg_argv = ns["_build_run_args_from_cfg"](_ROUNDTRIP_CFG, env_vars={})
    live_argv = ns["_live_argv_for_drift"](
        _inspect(host_cfg=_ROUNDTRIP_INSPECT_HOST, config=_ROUNDTRIP_INSPECT_CONFIG),
        cfg=_ROUNDTRIP_CFG,
    )
    cfg_pairs = ns["_argv_to_pair_set"](cfg_argv)
    live_pairs = ns["_argv_to_pair_set"](live_argv)

    missing = []
    for row in ns["_DOCKER_FIELD_TABLE"]:
        flag = row["flag"]
        declared = {p for p in cfg_pairs if p.split("\t")[0] == flag}
        assert declared, f"{row['key']}: declared cfg rendered no {flag}: {cfg_argv!r}"
        if not declared <= live_pairs:
            missing.append((row["key"], sorted(declared), sorted(
                p for p in live_pairs if p.split("\t")[0] == flag)))
    assert not missing, f"declared-vs-live mismatch per field: {missing!r}"
    print("OK: all %d field-table rows round-trip declared -> live"
          % len(ns["_DOCKER_FIELD_TABLE"]))


def test_field_table_declared_value_matching_image_default_is_not_drift():
    """Declaring workdir equal to the image's own baked default must not report
    drift. The live side strips a value that matches the image, so before this
    was gated on the field being undeclared, the declared side kept --workdir
    while the live side dropped it and every deploy reported a difference that
    no edit could resolve."""
    ns = _load_docker()
    cfg = {"workdir": "/app"}
    cfg_argv = ns["_build_run_args_from_cfg"](cfg, env_vars={})
    live_argv = ns["_live_argv_for_drift"](
        _inspect(config={"WorkingDir": "/app"}),
        image_workdir="/app",
        cfg=cfg,
    )
    in_target, in_live = ns["_compute_drift"](cfg_argv, live_argv)
    assert not in_target and not in_live, (
        f"declaring workdir == image default reported drift: "
        f"target-only={in_target!r} live-only={in_live!r}")
    print("OK: workdir declared equal to the image default is not drift")


def test_field_table_undeclared_value_matching_image_default_still_strips():
    """The pre-existing behaviour the gate must not break: when the config does
    NOT declare workdir, a live value matching the image's default is still
    stripped, so an image's own WORKDIR never shows up as drift."""
    ns = _load_docker()
    live_argv = ns["_live_argv_for_drift"](
        _inspect(config={"WorkingDir": "/app"}),
        image_workdir="/app",
        cfg={},
    )
    assert "--workdir" not in live_argv, (
        f"undeclared workdir matching the image should strip: {live_argv!r}")
    print("OK: undeclared workdir matching the image default still strips")


def test_field_table_real_workdir_mismatch_still_reports_drift():
    """The gate must not silence a genuine difference: cfg declares one workdir,
    the container is running another."""
    ns = _load_docker()
    cfg = {"workdir": "/app"}
    cfg_argv = ns["_build_run_args_from_cfg"](cfg, env_vars={})
    live_argv = ns["_live_argv_for_drift"](
        _inspect(config={"WorkingDir": "/srv"}),
        image_workdir="/app",
        cfg=cfg,
    )
    in_target, in_live = ns["_compute_drift"](cfg_argv, live_argv)
    assert any(p.startswith("--workdir") for p in in_target), (
        f"a real workdir mismatch should surface as drift: "
        f"target-only={in_target!r} live-only={in_live!r}")
    print("OK: a genuine workdir mismatch still reports drift")


def test_boolean_flags_derive_from_the_field_table():
    """_BOOLEAN_FLAGS is derived from the table's boolean rows, not maintained
    beside it. A second hand-written copy is what produced #387 and #468."""
    ns = _load_docker()
    from_table = {row["flag"] for row in ns["_DOCKER_FIELD_TABLE"]
                  if row.get("shape") == "boolean"}
    assert from_table <= set(ns["_BOOLEAN_FLAGS"]), (
        f"boolean rows missing from _BOOLEAN_FLAGS: "
        f"{from_table - set(ns['_BOOLEAN_FLAGS'])!r}")
    print("OK: _BOOLEAN_FLAGS covers every boolean row in the field table")
