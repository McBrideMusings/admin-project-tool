#!/usr/bin/env python3
"""admin -- project task runner for {{PROJECT_NAME}}."""

import sys, os
sys.path.insert(0, os.path.expanduser("~/.admin"))
from admin_lib.core import core, command, run_cmd, boot, C, info, ok, warn, err
from admin_lib.apple import (
    setup_reload, poll_for_reload, cleanup_pid_file,
    find_app, log_predicate, xcodebuild_filtered, clean_derived_data,
    is_rebuild_requested, clear_rebuild_request, register_reload_command,
)

PROJECT_NAME = "{{PROJECT_NAME}}"
SCHEME = "{{SCHEME}}"
PROCESS = "{{PROCESS}}"
SUBSYSTEM = "{{SUBSYSTEM}}"

_ADMIN_PID_FILE = "/tmp/admin-run.pid"


@core("dev", "Dev build and run (--log LEVEL)")
def cmd_dev(args):
    import threading, time

    log_level = "app"
    for i, arg in enumerate(args):
        if arg == "--log" and i + 1 < len(args):
            log_level = args[i + 1]

    predicate = log_predicate(PROCESS, SUBSYSTEM, log_level)
    setup_reload(_ADMIN_PID_FILE)
    pattern = f"~/Library/Developer/Xcode/DerivedData/{PROJECT_NAME}-*/Build/Products/Debug/{PROCESS}.app"

    while True:
        clear_rebuild_request()
        import subprocess
        subprocess.run(["pkill", "-x", PROCESS], capture_output=True)

        info("Building...")
        rc = xcodebuild_filtered(
            f"{PROJECT_NAME}.xcodeproj", SCHEME, "Debug",
            "platform=macOS",
        )
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


@command("test", "Run tests")
def cmd_test(args):
    info("Running tests...")
    rc = run_cmd("swift test")
    sys.exit(rc)


@command("clean", "Nuke DerivedData for this project")
def cmd_clean(args):
    info(f"Cleaning DerivedData for {PROJECT_NAME}...")
    clean_derived_data(PROJECT_NAME)
    ok("Done.")


register_reload_command(_ADMIN_PID_FILE)


if __name__ == "__main__":
    boot(PROJECT_NAME)
