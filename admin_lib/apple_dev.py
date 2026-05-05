"""Higher-level Apple dev/deploy orchestration.

Parameterized wrappers around admin_lib.apple helpers for projects that
need the full dev loop with icon swap, build cache, rename+postprocess,
and log streaming. Each function takes project-specific config as kwargs
so the admin.toml python-kind run string is a single function call.

Assumes bundled scope with admin_lib.core, admin_lib.apple, admin_lib.cache
all in-scope.
"""

import os
import subprocess
import sys
import time

from .core import (
    LOG_DIR, LOG_FILE,
    info, ok, warn, err,
    command, set_log_context,
    register_cleanup,
)
from .apple import (
    setup_reload, cleanup_pid_file, clear_rebuild_request, is_rebuild_requested,
    poll_for_reload,
    find_app, xcodebuild_filtered, dev_macos_postprocess,
    find_simulator, boot_simulator,
    app_bundle_identifier, install_and_launch_sim, terminate_sim_app, is_sim_app_running,
    stream_os_log, stop_os_log,
    backup_icons, swap_dev_icons, restore_icons,
    log_predicate,
    clean_derived_data,
    register_reload_command,
    dev_loop_device,
    find_connected_device, install_on_device,
)
from .cache import (
    collect_sources, is_fresh, mark_fresh, check_build, invalidate,
)


def xcode_sources(project_name, *src_dirs, extra_paths=None):
    """Collect source files for Xcode build cache hashing.

    Includes the .xcodeproj/project.pbxproj, each src_dir's .swift files,
    and any extra_paths (relative to project root).
    """
    specs = []
    for d in src_dirs:
        specs.append((d, [".swift"]))
    specs.append(f"{project_name}.xcodeproj/project.pbxproj")
    for p in (extra_paths or []):
        specs.append(p)
    return collect_sources(*specs)


def dev_macos(
    project, scheme, config, destination,
    dev_bundle_id, dev_product_name, dev_display_name,
    subsystem, process,
    build_app_pattern, final_app_pattern, dev_process_match,
    icon_dirs, icon_backup_dir, icon_config,
    pid_file="/tmp/admin-run.pid",
    source_fn=None,
    cache_label="macos-debug",
    ensure_prereqs=None,
    extra_kill_patterns=None,
    log_predicate_str=None,
):
    """Full macOS dev loop: icon swap, build cache, rename to dev product, log stream.

    source_fn → sources for cache check. ensure_prereqs(): nonzero aborts.
    extra_kill_patterns: pgrep -f patterns killed each iter. log_predicate_str
    overrides the subsystem-based default predicate.
    """
    if ensure_prereqs:
        rc = ensure_prereqs()
        if rc != 0:
            sys.exit(1)

    setup_reload(pid_file)
    extra_settings = f"PRODUCT_BUNDLE_IDENTIFIER={dev_bundle_id}"

    backup_icons(icon_dirs, icon_backup_dir)
    swap_dev_icons(icon_dirs, icon_config)
    check_cache = True
    try:
        while True:
            clear_rebuild_request()
            subprocess.run(["pkill", "-f", dev_process_match], capture_output=True)
            for pat in (extra_kill_patterns or []):
                subprocess.run(["pkill", "-f", pat], capture_output=True)

            sources = source_fn() if source_fn else []
            if check_cache and sources:
                need_build = check_build(cache_label, sources, f"{scheme} (dev)",
                                         artifact=final_app_pattern)
                check_cache = False
            else:
                need_build = True

            if need_build:
                info(f"Building {scheme} (dev)...")
                rc = xcodebuild_filtered(project, scheme, config, destination,
                                         extra_settings=extra_settings)
                print()

                if rc != 0:
                    err("Build failed")
                    if is_rebuild_requested():
                        warn("Rebuild requested during failed build, retrying...")
                        continue
                    cleanup_pid_file(pid_file)
                    sys.exit(1)

                built = find_app(build_app_pattern)
                if not built:
                    err(f"Build succeeded but app not found at {build_app_pattern}")
                    cleanup_pid_file(pid_file)
                    sys.exit(1)
                dev_macos_postprocess(built, dev_product_name, dev_display_name)
                if sources:
                    mark_fresh(cache_label, sources)

            app_path = find_app(final_app_pattern)
            if not app_path:
                err("Could not find built app")
                cleanup_pid_file(pid_file)
                sys.exit(1)

            info(f"Launching {app_path}")
            info("Press r to rebuild, Ctrl+C to quit")
            print()

            subprocess.run(["open", "-n", app_path], check=True)

            def _is_running():
                r = subprocess.run(["pgrep", "-f", dev_process_match], capture_output=True)
                return r.returncode == 0

            for _ in range(10):
                if _is_running():
                    break
                time.sleep(0.3)

            pred = log_predicate_str or log_predicate(process, subsystem)
            log_stream = subprocess.Popen(
                ["log", "stream", "--level", "info", "--predicate", pred, "--style", "compact"],
                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            )
            log_proc = subprocess.Popen(
                ["tee", LOG_FILE],
                stdin=log_stream.stdout, stdout=sys.stdout, stderr=subprocess.DEVNULL,
            )
            log_stream.stdout.close()

            got_reload = poll_for_reload(_is_running)

            log_stream.terminate()
            log_stream.wait()
            log_proc.terminate()
            log_proc.wait()
            print()
            if got_reload:
                info("Reload signal received — rebuilding...")
            else:
                info(f"Stopping {dev_product_name}...")
            subprocess.run(["pkill", "-f", dev_process_match], capture_output=True)

            if not got_reload:
                cleanup_pid_file(pid_file)
                return
    finally:
        restore_icons(icon_dirs, icon_backup_dir)


def dev_ios(
    project, scheme, config,
    dev_bundle_id, subsystem, process,
    app_pattern,
    icon_dirs, icon_backup_dir, icon_config,
    sim_device=None,
    pid_file="/tmp/admin-run.pid",
    source_fn=None,
    cache_label="ios-debug",
    ensure_prereqs=None,
):
    """Full iOS simulator dev loop with icon swap, build cache, and log streaming."""
    if ensure_prereqs:
        rc = ensure_prereqs()
        if rc != 0:
            sys.exit(1)

    device_name = sim_device or os.environ.get("IOS_DEVICE", "iPhone 16")
    device_id = find_simulator(device_name)
    if not device_id:
        err(f"Could not find simulator for '{device_name}'")
        sys.exit(1)

    info("Booting simulator...")
    boot_simulator(device_id)
    setup_reload(pid_file)
    extra_settings = f"PRODUCT_BUNDLE_IDENTIFIER={dev_bundle_id}"

    import threading

    backup_icons(icon_dirs, icon_backup_dir)
    swap_dev_icons(icon_dirs, icon_config)
    check_cache = True
    try:
        while True:
            clear_rebuild_request()
            terminate_sim_app(device_id, dev_bundle_id)

            sources = source_fn() if source_fn else []
            if check_cache and sources:
                need_build = check_build(cache_label, sources, f"{scheme} (dev)")
                check_cache = False
            else:
                need_build = True

            if need_build:
                info(f"Building {scheme} for {device_name} (dev)...")
                rc = xcodebuild_filtered(
                    project, scheme, config, f"id={device_id}",
                    extra_settings=extra_settings,
                )
                print()

                if rc != 0:
                    err("Build failed")
                    if is_rebuild_requested():
                        warn("Rebuild requested during failed build, retrying...")
                        continue
                    cleanup_pid_file(pid_file)
                    sys.exit(1)
                if sources:
                    mark_fresh(cache_label, sources)

            app_path = find_app(app_pattern)
            if not app_path:
                err("Could not find built app")
                cleanup_pid_file(pid_file)
                sys.exit(1)

            info(f"Installing and launching on {device_name}...")
            install_and_launch_sim(device_id, app_path, dev_bundle_id)
            print()
            info("Press r to rebuild, Ctrl+C to quit")
            print()

            for _ in range(10):
                if is_sim_app_running(device_id, dev_bundle_id):
                    break
                time.sleep(0.3)

            predicate = log_predicate(process, subsystem)
            log_fh = open(LOG_FILE, "a")
            log_proc = subprocess.Popen(
                ["xcrun", "simctl", "spawn", device_id,
                 "log", "stream", "--predicate", predicate,
                 "--level", "debug", "--style", "compact"],
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                bufsize=1, universal_newlines=True,
            )

            def stream_logs():
                try:
                    for line in log_proc.stdout:
                        sys.stdout.write(line)
                        sys.stdout.flush()
                        log_fh.write(line)
                        log_fh.flush()
                except (IOError, ValueError):
                    pass

            log_thread = threading.Thread(target=stream_logs, daemon=True)
            log_thread.start()

            got_reload = poll_for_reload(lambda: is_sim_app_running(device_id, dev_bundle_id))

            log_proc.terminate()
            try:
                log_proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                log_proc.kill()
            log_fh.close()
            print()
            if got_reload:
                info("Reload signal received — rebuilding...")
            else:
                info(f"Stopping {process}...")
            terminate_sim_app(device_id, dev_bundle_id)

            if not got_reload:
                cleanup_pid_file(pid_file)
                return
    finally:
        restore_icons(icon_dirs, icon_backup_dir)


def deploy_mac(
    project, scheme, install_path, process,
    app_pattern,
    source_fn=None,
    cache_label="macos-release",
    ensure_prereqs=None,
):
    """Build Release and install to /Applications (or custom path)."""
    subprocess.run(["pkill", "-x", process], capture_output=True)

    if ensure_prereqs:
        rc = ensure_prereqs()
        if rc != 0:
            sys.exit(1)

    sources = source_fn() if source_fn else []
    if sources and check_build(cache_label, sources, f"{scheme} (Release)"):
        info(f"Building {scheme} (Release)...")
        rc = xcodebuild_filtered(project, scheme, "Release", "platform=macOS")
        print()
        if rc != 0:
            err("Build failed")
            sys.exit(1)
        mark_fresh(cache_label, sources)

    app_path = find_app(app_pattern)
    if not app_path:
        err("Could not find built app")
        sys.exit(1)

    info(f"Installing to {install_path}...")
    subprocess.run(["rm", "-rf", install_path])
    rc = subprocess.run(["cp", "-R", app_path, install_path]).returncode
    if rc != 0:
        err("Failed to copy app to destination")
        sys.exit(1)

    ok(f"Installed to {install_path}")
    info("Launching...")
    subprocess.run(["open", install_path])


def deploy_ios(
    project, scheme,
    app_pattern,
    source_fn=None,
    cache_label="ios-device",
    ensure_prereqs=None,
):
    """Build and install on a connected iOS device."""
    device_id, device_name, _udid = find_connected_device()

    if ensure_prereqs:
        rc = ensure_prereqs()
        if rc != 0:
            sys.exit(1)

    sources = source_fn() if source_fn else []
    if sources and check_build(cache_label, sources, f"{scheme} (device)"):
        info(f"Building {scheme} for device...")
        rc = xcodebuild_filtered(project, scheme, "Debug", "generic/platform=iOS")
        print()
        if rc != 0:
            err("Build failed")
            sys.exit(1)
        mark_fresh(cache_label, sources)

    app_path = find_app(app_pattern)
    if not app_path:
        err("Could not find built app")
        sys.exit(1)

    info(f"Installing on {device_name}...")
    rc = install_on_device(device_id, app_path)
    if rc != 0:
        err("Install failed")
        sys.exit(1)

    ok(f"Deployed to {device_name}")
