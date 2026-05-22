"""'forge-engine' archetype — Zig/Sokol game engine with Lua scripting.

Designed for a repo layout where the engine lives in `engine/` and one or
more sample projects live in `sample-games/<name>/main.lua`. Commands run
from the repo root and delegate into `engine/` via `cd engine && ./zig ...`.

Mental model:
- `build` is a submenu over desktop OS build targets (mac, windows, linux).
  The engine is a native binary — the build targets are the operating
  systems it runs on. Web is *not* a build target; a wasm bundle is a
  *deployed game*, which belongs under a future `pack`/`deploy` flow.
- `dev` runs a sample game through the engine. Discovers
  `sample-games/*/main.lua`; auto-selects if exactly one exists, picker
  otherwise. First positional arg picks directly.
"""

_DISCOVER_PY = '''
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
        _m = _re.match(r"^\\d+-(.+)$", _name)
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
'''

_DEV_PY = _DISCOVER_PY + '''
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
info("launching workspace \\"" + game + "\\" — " + str(len(project_dirs)) + " projects")
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
'''

_BUILD_PY = '''
_TARGETS = {
    "mac":     ("./zig build",                                 "native (host arch)"),
    "windows": ("./zig build -Dtarget=x86_64-windows",         "x86_64-windows / D3D11"),
    "linux":   ("./zig build -Dtarget=x86_64-linux-gnu",       "x86_64-linux-gnu / GL3.3 (needs Linux host)"),
}

target = None
if args:
    _alias = {"win": "windows", "macos": "mac", "osx": "mac"}
    target = _alias.get(args[0], args[0])
    if target not in _TARGETS:
        err("unknown build target: " + args[0])
        info("available: " + ", ".join(_TARGETS.keys()))
        sys.exit(1)
else:
    from collections import OrderedDict
    target = pick_target(
        OrderedDict((t, desc) for t, (_, desc) in _TARGETS.items()),
        "build — select target",
    )
    if not target:
        return

set_log_context("build", target)
cmd, _ = _TARGETS[target]
info("building for " + target + ": " + cmd)
rc = run_cmd("cd engine && " + cmd)
if rc != 0:
    err("build failed")
    sys.exit(rc)
ok("engine built for " + target)
'''

_TEST_PY = '''
info("no tests configured yet")
info("when tests exist, set [commands.test.run] in admin.toml or extend this archetype")
'''

_CLEAN_PY = '''
import shutil
_removed = 0
for _p in ("engine/zig-cache", "engine/zig-out"):
    if os.path.exists(_p):
        info("rm -rf " + _p)
        shutil.rmtree(_p)
        _removed += 1
if _removed:
    ok("clean done")
else:
    info("nothing to clean")
'''

ARCHETYPE = {
    "name": "forge-engine",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "build": {"desc": "Build the engine for a target OS (mac | windows | linux)", "steps": ["build"], "group": 1, "priority": 10},
        "dev":   {"desc": "Run a sample game (auto-picks if one, picker if many)",    "steps": ["dev"],   "group": 1, "priority": 20},
        "test":  {"desc": "Run unit tests (none wired up yet)",                       "steps": ["test"],  "group": 2, "priority": 10},
        "clean": {"desc": "Remove engine/zig-cache and engine/zig-out",               "steps": ["clean"], "group": 2, "priority": 20},
    },
    "actions": {
        "build": {"kind": "python", "desc": "Build the engine for a target OS (mac | windows | linux)", "run": _BUILD_PY},
        "dev":   {"kind": "python", "desc": "Run a sample game (auto-picks if one, picker if many)",    "run": _DEV_PY},
        "test":  {"kind": "python", "desc": "Run unit tests (none wired up yet)",                       "run": _TEST_PY},
        "clean": {"kind": "python", "desc": "Remove engine/zig-cache and engine/zig-out",               "run": _CLEAN_PY},
    },
}
