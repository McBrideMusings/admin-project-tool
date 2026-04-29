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
samples_root = os.path.join(os.getcwd(), "sample-games")
_games = []  # list of (folder_name, display_name)
if os.path.isdir(samples_root):
    for _name in sorted(os.listdir(samples_root)):
        if os.path.isfile(os.path.join(samples_root, _name, "main.lua")):
            _m = _re.match(r"^\\d+-(.+)$", _name)
            _display = _m.group(1) if _m else _name
            _games.append((_name, _display))

def _pick_sample(action):
    if not _games:
        err("no sample games found (sample-games/*/main.lua)")
        info("create one, e.g. sample-games/mygame/main.lua")
        sys.exit(1)
    if args:
        _match = next((f for f, d in _games if d == args[0] or f == args[0]), None)
        if _match is None:
            err("unknown sample: " + args[0])
            info("available: " + ", ".join(d for _, d in _games))
            sys.exit(1)
        return _match
    if len(_games) == 1:
        return _games[0][0]
    from collections import OrderedDict
    choice = pick_target(OrderedDict((f, d) for f, d in _games), action + " — select sample game")
    if not choice:
        sys.exit(0)
    return choice
'''

_DEV_PY = _DISCOVER_PY + '''
import shlex

game = _pick_sample("dev")

binary = "engine/zig-out/bin/forge"
if not os.path.isfile(binary):
    info("engine binary missing, building first")
    rc = run_cmd("cd engine && ./zig build")
    if rc != 0:
        err("build failed")
        sys.exit(rc)

project_dir = os.path.join("sample-games", game)
# Quote in case a sample directory name contains shell metacharacters.
cmd = shlex.quote(binary) + " " + shlex.quote(project_dir)
info("running: " + cmd)
rc = run_cmd(cmd)
sys.exit(rc)
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
        "build": {
            "kind": "python",
            "desc": "Build the engine for a target OS (mac | windows | linux)",
            "run": _BUILD_PY,
        },
        "dev": {
            "kind": "python",
            "desc": "Run a sample game (auto-picks if one, picker if many)",
            "run": _DEV_PY,
        },
        "test": {
            "kind": "python",
            "desc": "Run unit tests (none wired up yet)",
            "run": _TEST_PY,
        },
        "clean": {
            "kind": "python",
            "desc": "Remove engine/zig-cache and engine/zig-out",
            "run": _CLEAN_PY,
        },
    },
    "order": ["build", "dev", "", "test", "clean"],
}
