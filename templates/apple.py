#!/usr/bin/env python3
"""admin -- project task runner for {{PROJECT_NAME}}."""

import sys, os
sys.path.insert(0, os.path.expanduser("~/.admin"))
from admin_lib.core import core, command, run_cmd, boot, C, info, ok, warn, err
from admin_lib.apple import (
    log_predicate, xcodebuild_filtered, clean_derived_data,
    find_app, register_reload_command,
    dev_loop_macos, dev_loop_ios, dev_loop_device, device_log_attach,
    resolve_simulator, find_connected_device, require_usb_device, install_on_device,
    backup_icons, swap_dev_icons, restore_icons,
)

PROJECT_NAME = "{{PROJECT_NAME}}"
XCODEPROJ = "{{XCODEPROJ}}"
SCHEME = "{{SCHEME}}"
PROCESS = "{{PROCESS}}"
SUBSYSTEM = "{{SUBSYSTEM}}"
PLATFORM = "{{PLATFORM}}"

_ADMIN_PID_FILE = "/tmp/admin-run.pid"

ICON_CONFIG = {
    "bg_color": "{{ICON_BG_COLOR}}",
    # Use glyph_svg for SVG icons, or glyph_text for a single character:
    # "glyph_svg": "path/to/icon.svg",
    "glyph_text": "{{ICON_GLYPH_TEXT}}",
    "glyph_color": "{{ICON_GLYPH_COLOR}}",
}

ICON_DIRS = {
    "mac": "{{MAC_ICON_DIR}}",
}

_ICON_BACKUP_DIR = os.path.join("tmp", "icon-backup")


@core("dev", "Dev build and run (--log LEVEL)")
def cmd_dev(args):
    log_level = "app"
    for i, arg in enumerate(args):
        if arg == "--log" and i + 1 < len(args):
            log_level = args[i + 1]

    predicate = log_predicate(PROCESS, SUBSYSTEM, log_level)

    def _pre_build():
        backup_icons(ICON_DIRS, _ICON_BACKUP_DIR)
        swap_dev_icons(ICON_DIRS, ICON_CONFIG)

    def _post_build():
        restore_icons(ICON_DIRS, _ICON_BACKUP_DIR)

    if PLATFORM == "ios":
        pattern = f"~/Library/Developer/Xcode/DerivedData/{SCHEME}-*/Build/Products/Debug-iphonesimulator/{PROCESS}.app"
        dev_loop_ios(XCODEPROJ, SCHEME, "Debug", SUBSYSTEM, PROCESS,
                     predicate, pattern, pid_file=_ADMIN_PID_FILE,
                     pre_build=_pre_build, post_build=_post_build)
    else:
        pattern = f"~/Library/Developer/Xcode/DerivedData/{SCHEME}-*/Build/Products/Debug/{PROCESS}.app"
        dev_loop_macos(XCODEPROJ, SCHEME, "Debug", "platform=macOS", PROCESS,
                       SUBSYSTEM, predicate, pattern, pid_file=_ADMIN_PID_FILE,
                       pre_build=_pre_build, post_build=_post_build)


@core("deploy", "Build and install on connected device")
def cmd_deploy(args):
    import subprocess

    if PLATFORM == "ios":
        device_id, device_name, _udid = find_connected_device()
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
