
import re as _re
try:
    import tomllib as _tomllib
except ImportError:
    _tomllib = None
import glob as _glob

samples_root = os.path.join(os.getcwd(), "sample-games")
# Each entry: (folder_name, display_name, kind)  kind = "game" | "workspace"
_games = []
if os.path.isdir(samples_root):
    for _name in sorted(os.listdir(samples_root)):
        _sample_path = os.path.join(samples_root, _name)
        if os.path.isfile(os.path.join(_sample_path, "main.lua")):
            _kind = "game"
        elif os.path.isfile(os.path.join(_sample_path, "forge.workspace.toml")):
            _kind = "workspace"
        else:
            continue
        _m = _re.match(r"^\d+-(.+)$", _name)
        _games.append((_name, _m.group(1) if _m else _name, _kind))

def _pick_sample(action):
    if not _games:
        err("no sample games found (sample-games/*/main.lua or forge.workspace.toml)")
        info("create one, e.g. sample-games/mygame/main.lua")
        sys.exit(1)
    _kinds = {f: k for f, _, k in _games}
    if args:
        _match = next((f for f, d, _k in _games if d == args[0] or f == args[0]), None)
        if _match is None:
            err("unknown sample: " + args[0])
            info("available: " + ", ".join(d for _, d, _k in _games))
            sys.exit(1)
        return _match, _kinds[_match]
    if len(_games) == 1:
        return _games[0][0], _games[0][2]
    from collections import OrderedDict
    choice = pick_target(
        OrderedDict((f, d + ("  [workspace]" if k == "workspace" else "")) for f, d, k in _games),
        action + " — select sample game",
    )
    if not choice:
        sys.exit(0)
    return choice, _kinds[choice]

def _workspace_projects(folder_name):
    """Return list of project dirs (relative to repo root) from forge.workspace.toml."""
    if _tomllib is None:
        warn("tomllib unavailable (Python < 3.11, no tomli) — skipping workspace.toml")
        return []
    ws_path = os.path.join(samples_root, folder_name, "forge.workspace.toml")
    try:
        with open(ws_path, "rb") as _f:
            _ws = _tomllib.load(_f)
        _proj_dirs = []
        for _pg in _ws.get("projects", []):
            _base = os.path.join(samples_root, folder_name, _pg)
            _matches = sorted(_glob.glob(_base))
            if not _matches:
                warn("workspace project pattern matched nothing: " + _pg)
                continue
            _found = 0
            for _match in _matches:
                if os.path.isfile(os.path.join(_match, "main.lua")):
                    _proj_dirs.append(os.path.relpath(_match, os.getcwd()))
                    _found += 1
            if _found == 0:
                warn("workspace project pattern matched but no main.lua found: " + _pg)
        return _proj_dirs
    except Exception as _e:
        err("could not parse forge.workspace.toml: " + str(_e))
        return []

import shlex
import subprocess as _subprocess
import threading as _threading
import time as _time

game, kind = _pick_sample("dev")

binary = "engine/zig-out/bin/forge"
if not os.path.isfile(binary):
    info("engine binary missing, building first")
    rc = run_cmd("cd engine && ./zig build")
    if rc != 0:
        err("build failed")
        sys.exit(rc)

if kind != "workspace":
    project_dir = os.path.join("sample-games", game)
    cmd = shlex.quote(binary) + " " + shlex.quote(project_dir)
    info("running: " + cmd)
    sys.exit(run_cmd(cmd))

project_dirs = _workspace_projects(game)
if not project_dirs:
    err("workspace has no launchable projects (need main.lua in each project dir)")
    sys.exit(1)
info("launching workspace \"" + game + "\" — " + str(len(project_dirs)) + " projects")
for _pd in project_dirs:
    info("  " + _pd)
_log_fh = open(LOG_FILE, "a") if LOG_FILE and LOG_CONFIG.get("enabled") is not False else None
_log_lock = _threading.Lock()
def _pump(proc, tag):
    for line in proc.stdout:
        out = "[" + tag + "] " + line
        sys.stdout.write(out)
        sys.stdout.flush()
        if _log_fh:
            with _log_lock:
                _log_fh.write(out)
                _log_fh.flush()
procs = [
    _subprocess.Popen([binary, _pd], stdout=_subprocess.PIPE, stderr=_subprocess.STDOUT,
                      bufsize=1, universal_newlines=True)
    for _pd in project_dirs
]
threads = [_threading.Thread(target=_pump, args=(p, os.path.basename(d)), daemon=True)
           for p, d in zip(procs, project_dirs)]
for _t in threads:
    _t.start()
try:
    rcs = [p.wait() for p in procs]
except KeyboardInterrupt:
    info("interrupt — terminating workspace projects")
    # SIGTERM all, then bounded wait, then SIGKILL any survivors. A second
    # Ctrl+C during cleanup must not orphan children, so swallow it here.
    for _p in procs:
        if _p.poll() is None:
            try:
                _p.terminate()
            except Exception:
                pass
    _deadline = _time.time() + 5.0
    for _p in procs:
        while _p.poll() is None:
            _remaining = _deadline - _time.time()
            if _remaining <= 0:
                break
            try:
                _p.wait(timeout=_remaining)
            except _subprocess.TimeoutExpired:
                break
            except KeyboardInterrupt:
                continue
    for _p in procs:
        if _p.poll() is None:
            warn("child ignored SIGTERM — sending SIGKILL")
            try:
                _p.kill()
            except Exception:
                pass
    rcs = []
    for _p in procs:
        while True:
            try:
                rcs.append(_p.wait())
                break
            except KeyboardInterrupt:
                continue
for _t in threads:
    _t.join()
if _log_fh:
    _log_fh.close()
sys.exit(max(rcs))
