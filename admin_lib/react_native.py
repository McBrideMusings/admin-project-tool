"""High-level React Native archetype wrappers — driven by the [react_native]
and [apple] tables in admin.toml.

React Native's own CLI (`npx react-native run-ios`) builds, installs, AND
launches in one step. That last part is why these wrappers drive xcodebuild
directly through admin_lib.apple instead: on a physical device the log stream
is only obtainable by owning the launch (`devicectl process launch --console`
attaches to the console and waits — there is no attach-to-running-process
subcommand, and `log stream` has no --device flag). Handing the launch to the
RN CLI therefore forfeits device logs entirely. Driving the build ourselves
keeps simulator and device on the same path, and a bare RN debug build needs
nothing from the RN CLI at build time — the .app fetches its JS from Metro at
runtime, so xcodebuild alone is sufficient.

The [react_native] table:

    app_dir          (required) path to the RN app — the dir holding
                     package.json and the ios/ project, relative to the root.
    metro_port       (default 8081) port Metro serves the JS bundle on.
    android_package  applicationId of the debug build — required for `dev
                     android`, which filters logcat to that app's pid.
    android_serial   pin a specific device/emulator; auto-resolved when there
                     is exactly one attached.

The iOS build itself is configured by [apple] exactly as for a native project
(project_path, ios_scheme, …); an RN app is a normal Xcode workspace.
"""
from __future__ import annotations

import os

from .android import dev_loop_android
from .core import err, get_log_dir, info
from .metro import DEFAULT_PORT, ensure_metro, stop_metro

_METRO_LOG = "metro.log"
_METRO_PID = "metro.pid"


def _rn_app_dir(rn_cfg: dict) -> str:
    app_dir = rn_cfg.get("app_dir")
    if not app_dir:
        err("react_native.app_dir required — path to the dir holding package.json")
        raise SystemExit(1)
    if not os.path.isdir(app_dir):
        err(f"react_native.app_dir does not exist: {app_dir}")
        raise SystemExit(1)
    return app_dir


def _metro_paths() -> tuple[str, str]:
    log_dir = get_log_dir() or "tmp"
    return os.path.join(log_dir, _METRO_LOG), os.path.join(log_dir, _METRO_PID)


def metro_port(rn_cfg: dict) -> int:
    return int(rn_cfg.get("metro_port", DEFAULT_PORT))


def ensure_metro_for(rn_cfg: dict) -> bool:
    """Start Metro for this project unless one is already serving its port.

    Called before any RN dev target builds. A build that succeeds without Metro
    still produces an app that only shows a red screen, so a failure here must
    stop the dev command rather than warn and continue.
    """
    app_dir = _rn_app_dir(rn_cfg)
    log_file, pid_file = _metro_paths()
    port = metro_port(rn_cfg)
    try:
        started = ensure_metro(app_dir, port=port, log_file=log_file, pid_file=pid_file)
    except RuntimeError as exc:
        err(str(exc))
        raise SystemExit(1)
    if started:
        info(f"Metro log → {log_file}")
    return started


def stop_metro_for(rn_cfg: dict) -> bool:
    """Stop this project's Metro. For a `down`/`kill` command."""
    log_file, pid_file = _metro_paths()
    return stop_metro(pid_file, port=metro_port(rn_cfg))


def dev_android(rn_cfg: dict, *, output_config=None):
    """Android dev loop driven by [react_native] config."""
    app_dir = _rn_app_dir(rn_cfg)
    package = rn_cfg.get("android_package")
    if not package:
        err("react_native.android_package required for `dev android`"
            " — the applicationId of the debug build (e.g. com.example.app.dev)")
        raise SystemExit(1)
    dev_loop_android(
        app_dir, package,
        serial=rn_cfg.get("android_serial"),
        output_config=output_config,
    )
