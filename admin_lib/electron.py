"""Generic Electron shell build/dev/deploy helper.

Any project shipping an Electron desktop client configures a ``[electron]``
table and gets three modes, all driven by config — no project-specific paths
live here:

  dev_electron(cfg)     run Electron from source, unpackaged, restarting the
                         main process when a watched file changes.
  build_electron(cfg)   stage web/server resources, then `electron-builder
                         --mac dir` — an artifact only, nothing installed.
  deploy_electron(cfg)  build_electron(cfg), then atomically install into
                         [electron].install_dir (default /Applications).

cfg keys (all paths relative to the repo root except where noted):
  desktop_dir       required — the Electron project dir (package.json, main
                     entry point).
  app_name          required — release bundle name; installs as
                     <install_dir>/<app_name>.app.
  install_dir       default "/Applications".
  out_dir           electron-builder output dir, relative to desktop_dir,
                     default "dist".
  web_source_dir    optional — built via `npm run build`; its dist/ is
                     staged into <desktop_dir>/web-dist.
  web_dist_dirname  default "web-dist" — staged web build's dirname under
                     desktop_dir.
  server_dist_dir   optional — repo-root-relative dir (e.g. cross-compiled
                     server binaries) copied into
                     <desktop_dir>/<server_dist_dirname>.
  server_dist_dirname  default "server-dist".
  watch_files       dev only — files relative to desktop_dir that trigger a
                     main-process restart on change (default []).
  dev_env           dev only — extra env vars for the unpackaged Electron
                     process, ``${VAR}`` resolved (default {}).
  dev_dist_dir_var  dev only — env var name to receive the ABSOLUTE
                     server_dist_dir path in dev mode (optional).
"""

import os
import shlex
import shutil
import signal
import subprocess
import sys
import threading
import time

from .core import err, get_project_root, info, ok, resolve_env, run_cmd, warn
from .reload import run_with_reload


def _desktop_dir(cfg):
    d = cfg.get("desktop_dir")
    if not d:
        err("[electron].desktop_dir not set"); sys.exit(1)
    return os.path.join(get_project_root(), d)


def _app_name(cfg):
    name = cfg.get("app_name")
    if not name:
        err("[electron].app_name not set"); sys.exit(1)
    return name


def _electron_bin(desktop_dir):
    electron_bin = os.path.join(desktop_dir, "node_modules/.bin/electron")
    if not os.path.exists(electron_bin):
        err(f"electron binary not found at {electron_bin} — run `npm install` in {desktop_dir}")
        sys.exit(1)
    return electron_bin


def _built_app_path(desktop_dir, out_dir, app_name):
    # electron-builder's `dir` target names the output folder `mac` on x64
    # but `mac-<arch>` on everything else (e.g. `mac-arm64`) — find it
    # instead of hardcoding, so this works on either Mac architecture.
    base = os.path.join(desktop_dir, out_dir)
    mac_dir = "mac"
    if os.path.isdir(base):
        for d in os.listdir(base):
            if d == "mac" or d.startswith("mac-"):
                mac_dir = d
                break
    return os.path.join(base, mac_dir, f"{app_name}.app")


def stage_electron_resources(cfg):
    """Build the web client (if configured) and copy the repo's server dist
    (if configured) into the desktop project, ready for electron-builder.
    """
    desktop_dir = _desktop_dir(cfg)
    root = get_project_root()

    web_source_dir = cfg.get("web_source_dir")
    if web_source_dir:
        info("building web client...")
        web_dir = os.path.join(root, web_source_dir)
        rc = run_cmd("cd " + shlex.quote(web_dir) + " && npm run build")
        if rc != 0:
            err("web client build failed"); sys.exit(rc)
        web_dist_name = cfg.get("web_dist_dirname", "web-dist")
        src = os.path.join(root, web_source_dir, "dist")
        dst = os.path.join(desktop_dir, web_dist_name)
        shutil.rmtree(dst, ignore_errors=True)
        shutil.copytree(src, dst)

    server_dist_dir = cfg.get("server_dist_dir")
    if server_dist_dir:
        src = os.path.join(root, server_dist_dir)
        if not os.path.isdir(src):
            err(f"{src} not found — build the server dist matrix first"); sys.exit(1)
        server_dist_name = cfg.get("server_dist_dirname", "server-dist")
        dst = os.path.join(desktop_dir, server_dist_name)
        shutil.rmtree(dst, ignore_errors=True)
        shutil.copytree(src, dst)


def build_electron(cfg):
    """stage_electron_resources(cfg) + `electron-builder --mac dir`. Returns
    the built .app path (release identity, self-contained artifact — nothing
    installed).
    """
    desktop_dir = _desktop_dir(cfg)
    app_name = _app_name(cfg)
    out_dir = cfg.get("out_dir", "dist")

    stage_electron_resources(cfg)

    info("building (release identity, self-contained)...")
    rc = run_cmd("cd " + shlex.quote(desktop_dir) + " && npx electron-builder --mac dir")
    if rc != 0:
        err("electron-builder failed"); sys.exit(rc)

    app_path = _built_app_path(desktop_dir, out_dir, app_name)
    if not os.path.exists(app_path):
        err(f"built app not found at {app_path}"); sys.exit(1)
    return app_path


def _kill_running(install_dir, app_name):
    exe_path = os.path.join(install_dir, f"{app_name}.app", "Contents/MacOS", app_name)
    found = subprocess.run(["pgrep", "-f", exe_path], capture_output=True)
    if found.returncode == 0:
        info(f"stopping running {app_name}...")
        subprocess.run(["pkill", "-f", exe_path])


def install_electron(built_path, cfg):
    """Atomically install a built .app into [electron].install_dir, killing
    any running copy first.
    """
    app_name = _app_name(cfg)
    install_dir = cfg.get("install_dir", "/Applications")
    target = os.path.join(install_dir, f"{app_name}.app")
    staged = os.path.join(install_dir, f".{app_name}.app.staged")

    shutil.rmtree(staged, ignore_errors=True)
    _kill_running(install_dir, app_name)

    info(f"staging into {install_dir}...")
    # `ditto`, not a generic recursive copy — a signed .app bundle's validity
    # depends on its exact symlink/resource-fork structure (e.g.
    # *.framework/Versions/Current), which a generic copy doesn't reliably
    # preserve. A plain-copy-staged bundle here failed `codesign --verify`
    # with "unsealed contents present in the root directory of an embedded
    # framework", and the hardened-runtime app then crashed on launch (V8
    # denied JIT memory). `ditto` is Apple's own bundle-copying tool and
    # preserves this correctly.
    rc = run_cmd(["ditto", built_path, staged], shell=False)
    if rc != 0:
        err("ditto failed"); sys.exit(rc)

    # rename(2) can only replace an EMPTY directory — a non-empty existing
    # target (i.e. every install after the first) fails with ENOTEMPTY. The
    # running instance is already dead (killed above), so it's safe to clear
    # the old bundle immediately before the rename swaps the new one in.
    shutil.rmtree(target, ignore_errors=True)
    os.rename(staged, target)
    ok(f"installed {target}")
    subprocess.run(["open", target])


def deploy_electron(cfg):
    """build_electron(cfg), then install_electron(...) into /Applications
    (or [electron].install_dir).
    """
    install_electron(build_electron(cfg), cfg)


def dev_electron(cfg, output_config=None):
    """Run Electron from source, unpackaged (no electron-builder step, so the
    main-process files are read live), restarting the child when a watched
    file changes. The renderer needs no restart handling here — it's served
    by the project's own dev server and hot-reloads on its own; only files
    the caller lists in ``watch_files`` (true main-process code) trigger a
    respawn.

    Reuses admin_lib.reload.run_with_reload's spawn/kill/signal-forward/PID-
    file machinery — the watcher thread below just sends itself the same
    SIGUSR1 that machinery already treats as "reload requested", so there is
    one restart-a-child implementation, not two.
    """
    desktop_dir = _desktop_dir(cfg)
    electron_bin = _electron_bin(desktop_dir)

    env = dict(cfg.get("dev_env") or {})
    for k, v in env.items():
        os.environ[k] = resolve_env(str(v))

    dist_var = cfg.get("dev_dist_dir_var")
    server_dist_dir = cfg.get("server_dist_dir")
    if dist_var and server_dist_dir:
        os.environ[dist_var] = os.path.join(get_project_root(), server_dist_dir)

    watch_files = cfg.get("watch_files") or []
    watch_paths = [os.path.join(desktop_dir, f) for f in watch_files]
    watch_paths = [p for p in watch_paths if os.path.exists(p)]

    stop_event = threading.Event()

    def _watch():
        # Poll mtimes (not an FS-event watch): an atomic-rename save (how
        # most editors write) makes inode-based watchers fire phantom events
        # forever on the dead inode. Re-stating the path each tick follows
        # an atomic replace and fires only when the mtime actually moved.
        mtimes = {p: os.path.getmtime(p) for p in watch_paths}
        last_change = 0
        while not stop_event.is_set():
            time.sleep(0.4)
            changed = False
            for p in watch_paths:
                try:
                    m = os.path.getmtime(p)
                except OSError:
                    continue
                if m != mtimes.get(p):
                    mtimes[p] = m
                    changed = True
            if changed:
                now = time.monotonic()
                if now - last_change < 0.15:
                    continue
                last_change = now
                info("main-process file changed, restarting...")
                os.kill(os.getpid(), signal.SIGUSR1)

    if watch_paths:
        t = threading.Thread(target=_watch, daemon=True)
        t.start()

    info("launching from source (unpackaged — main-process hot reload on file change)...")
    cmd = "cd " + shlex.quote(desktop_dir) + " && exec " + shlex.quote(electron_bin) + " ."
    try:
        run_with_reload(cmd, target="electron")
    finally:
        stop_event.set()
