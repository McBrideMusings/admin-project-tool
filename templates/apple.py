#!/usr/bin/env python3
"""admin -- project task runner for {{PROJECT_NAME}}."""

import sys, os
sys.path.insert(0, os.path.expanduser("~/.admin"))
from admin_lib.core import core, command, run_cmd, boot, C, info, ok, warn, err
from admin_lib.apple import (
    setup_reload, poll_for_reload, cleanup_pid_file,
    find_app, log_predicate, xcodebuild_filtered, clean_derived_data,
    is_rebuild_requested, clear_rebuild_request, register_reload_command,
    resolve_simulator, boot_simulator, install_and_launch_sim,
    terminate_sim_app, is_sim_app_running,
    find_connected_device, install_on_device,
)

PROJECT_NAME = "{{PROJECT_NAME}}"
XCODEPROJ = "{{XCODEPROJ}}"
SCHEME = "{{SCHEME}}"
PROCESS = "{{PROCESS}}"
SUBSYSTEM = "{{SUBSYSTEM}}"
PLATFORM = "{{PLATFORM}}"

_ADMIN_PID_FILE = "/tmp/admin-run.pid"


@core("dev", "Dev build and run (--log LEVEL)")
def cmd_dev(args):
    import time

    log_level = "app"
    for i, arg in enumerate(args):
        if arg == "--log" and i + 1 < len(args):
            log_level = args[i + 1]

    predicate = log_predicate(PROCESS, SUBSYSTEM, log_level)
    setup_reload(_ADMIN_PID_FILE)

    if PLATFORM == "ios":
        _dev_ios(predicate)
    else:
        _dev_macos(predicate)


def _dev_macos(predicate):
    import subprocess, time
    pattern = f"~/Library/Developer/Xcode/DerivedData/{SCHEME}-*/Build/Products/Debug/{PROCESS}.app"

    while True:
        clear_rebuild_request()
        subprocess.run(["pkill", "-x", PROCESS], capture_output=True)

        info("Building...")
        rc = xcodebuild_filtered(XCODEPROJ, SCHEME, "Debug", "platform=macOS")
        print()

        if rc != 0:
            err("Build failed")
            if is_rebuild_requested():
                warn("Rebuild requested during failed build, retrying...")
                continue
            cleanup_pid_file(_ADMIN_PID_FILE)
            sys.exit(1)

        app_path = find_app(pattern)
        if not app_path:
            err("Could not find built app")
            cleanup_pid_file(_ADMIN_PID_FILE)
            sys.exit(1)

        info(f"Launching {app_path}")
        info("Press r to rebuild, Ctrl+C to quit")
        print()

        subprocess.run(["open", app_path], check=True)

        def _is_running():
            r = subprocess.run(["pgrep", "-x", PROCESS], capture_output=True)
            return r.returncode == 0

        for _ in range(10):
            if _is_running():
                break
            time.sleep(0.3)

        got_reload = poll_for_reload(_is_running)

        print()
        if got_reload:
            info("Reload signal received — rebuilding...")
        else:
            info(f"Stopping {PROCESS}...")
        subprocess.run(["pkill", "-x", PROCESS], capture_output=True)

        if not got_reload:
            cleanup_pid_file(_ADMIN_PID_FILE)
            return


def _dev_ios(predicate):
    import subprocess, time

    sim_id, sim_name = resolve_simulator()
    boot_simulator(sim_id)

    destination = f"platform=iOS Simulator,id={sim_id}"
    pattern = f"~/Library/Developer/Xcode/DerivedData/{SCHEME}-*/Build/Products/Debug-iphonesimulator/{PROCESS}.app"

    while True:
        clear_rebuild_request()
        terminate_sim_app(sim_id, SUBSYSTEM)

        info("Building for simulator...")
        rc = xcodebuild_filtered(XCODEPROJ, SCHEME, "Debug", destination)
        print()

        if rc != 0:
            err("Build failed")
            if is_rebuild_requested():
                warn("Rebuild requested during failed build, retrying...")
                continue
            cleanup_pid_file(_ADMIN_PID_FILE)
            sys.exit(1)

        app_path = find_app(pattern)
        if not app_path:
            err("Could not find built app")
            cleanup_pid_file(_ADMIN_PID_FILE)
            sys.exit(1)

        info(f"Installing on simulator...")
        install_and_launch_sim(sim_id, app_path, SUBSYSTEM)
        info("Press r to rebuild, Ctrl+C to quit")
        print()

        def _is_running():
            return is_sim_app_running(sim_id, SUBSYSTEM)

        for _ in range(10):
            if _is_running():
                break
            time.sleep(0.3)

        got_reload = poll_for_reload(_is_running)

        print()
        if got_reload:
            info("Reload signal received — rebuilding...")
        else:
            info(f"Stopping {PROCESS}...")
        terminate_sim_app(sim_id, SUBSYSTEM)

        if not got_reload:
            cleanup_pid_file(_ADMIN_PID_FILE)
            return


@core("deploy", "Build and install on connected device")
def cmd_deploy(args):
    import subprocess

    if PLATFORM == "ios":
        device_id, device_name = find_connected_device()
        destination = f"platform=iOS,id={device_id}"
        pattern = f"~/Library/Developer/Xcode/DerivedData/{SCHEME}-*/Build/Products/Debug-iphoneos/{PROCESS}.app"

        info(f"Building for {device_name}...")
        rc = xcodebuild_filtered(XCODEPROJ, SCHEME, "Debug", destination)
        print()
        if rc != 0:
            err("Build failed")
            sys.exit(1)

        app_path = find_app(pattern)
        if not app_path:
            err("Could not find built app")
            sys.exit(1)

        info(f"Installing on {device_name}...")
        rc = install_on_device(device_id, app_path)
        if rc != 0:
            err("Install failed")
            sys.exit(1)

        ok(f"Installed on {device_name}")
    else:
        info("Building release...")
        rc = xcodebuild_filtered(XCODEPROJ, SCHEME, "Release", "platform=macOS")
        print()
        if rc != 0:
            err("Build failed")
            sys.exit(1)

        pattern = f"~/Library/Developer/Xcode/DerivedData/{SCHEME}-*/Build/Products/Release/{PROCESS}.app"
        app_path = find_app(pattern)
        if not app_path:
            err("Could not find built app")
            sys.exit(1)

        dest = f"/Applications/{PROCESS}.app"
        info(f"Copying to {dest}")
        subprocess.run(["rm", "-rf", dest])
        subprocess.run(["cp", "-R", app_path, dest], check=True)
        ok(f"Installed to {dest}")


@command("test", "Run tests")
def cmd_test(args):
    info("Running tests...")
    if PLATFORM == "ios":
        sim_id, _ = resolve_simulator()
        dest = f"platform=iOS Simulator,id={sim_id}"
        rc = run_cmd(f'xcodebuild -project "{XCODEPROJ}" -scheme "{SCHEME}" -destination "{dest}" test 2>&1 | grep -E "Test|error:|FAIL|PASS" ; exit ${{PIPESTATUS[0]}}')
    else:
        rc = run_cmd(f'xcodebuild -project "{XCODEPROJ}" -scheme "{SCHEME}" -destination "platform=macOS" test 2>&1 | grep -E "Test|error:|FAIL|PASS" ; exit ${{PIPESTATUS[0]}}')
    sys.exit(rc)


@command("clean", "Nuke DerivedData for this project")
def cmd_clean(args):
    info(f"Cleaning DerivedData for {SCHEME}...")
    clean_derived_data(SCHEME)
    ok("Done.")


register_reload_command(_ADMIN_PID_FILE)


if __name__ == "__main__":
    boot(PROJECT_NAME)
