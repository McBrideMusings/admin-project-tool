"""'apple' archetype — Xcode project with macOS and/or iOS targets.

Commands (build/dev/deploy/test/clean/icons/reload) are all ``kind: "python"``
bodies that read ``_APPLE_CONFIG`` and optionally ``_SERVER_CONFIG`` dicts
(emitted by render.py from the manifest's ``[apple]`` and ``[server]`` tables)
and delegate to the high-level wrappers in ``admin_lib.apple`` and
``admin_lib.rust``.

See ``admin_lib/apple.py`` for the wrapper signatures and
``docs/archetypes/apple.md`` for the full ``[apple]`` schema.
The reusable iOS-simulator helpers (``resolve_simulator``,
``ensure_booted_simulator``, ``install_and_launch_sim``, …) that any
``kind: "python"`` action may call by bare name are tabulated under
"Reusable library helpers in ``kind = "python"`` bodies" in
``docs/architecture/design.md``.
"""

_BUILD_PY = '''\
target = None
force = "--force" in args
rest = [a for a in args if a != "--force"]
if rest:
    a = rest[0].lower()
    if a in ("mac", "macos"):
        target = "mac"
    elif a in ("ios", "iphone"):
        target = "ios"
    elif a == "server":
        target = "server"
    else:
        err("unknown build target: " + rest[0]); sys.exit(1)

_server = globals().get("_SERVER_CONFIG") or {}
_choices = {"mac": "macOS client", "ios": "iOS client"}
if _server:
    _choices["server"] = "server (all platforms)"

if not target:
    target = pick_target(_choices, "build — select target")
    if not target:
        return

cfg = globals().get("_APPLE_CONFIG") or {}
set_log_context("build", target)
if target == "mac":
    build_mac(cfg, force=force)
elif target == "ios":
    build_ios(cfg, force=force)
elif target == "server":
    build_server(_server, force=force)
'''

_DEV_PY = '''\
target = None
log_level = "app"
force_clean = False
install_mac = False
i = 0
while i < len(args):
    a = args[i]
    if a in ("mac", "macos"):
        target = "mac"
    elif a in ("ios", "iphone", "simulator"):
        target = "ios"
    elif a == "device":
        target = "device"
    elif a == "server":
        target = "server"
    elif a == "--log" and i + 1 < len(args):
        i += 1
        log_level = args[i]
    elif a == "--clean":
        force_clean = True
    elif a == "--install":
        install_mac = True
    elif a in ("-h", "--help"):
        info("usage: ./admin dev [mac|ios|device|server] [--log app|debug|system|verbose] [--clean] [--install]")
        return
    else:
        err("unknown arg: " + a); sys.exit(1)
    i += 1

_server = globals().get("_SERVER_CONFIG") or {}
if target is None:
    _choices = {
        "mac": "mac",
        "ios": "ios simulator",
        "device": "iOS device (build + install + logs)",
    }
    if _server:
        _choices["server"] = "server with reload loop"
    target = pick_target(_choices, "dev — select target")
    if not target:
        return

cfg = globals().get("_APPLE_CONFIG") or {}
set_log_context("dev", target)
if target == "mac":
    dev_mac(cfg, log_level=log_level, clean=force_clean, install=install_mac)
elif target == "device":
    dev_device(cfg, log_level=log_level, clean=force_clean)
elif target == "server":
    dev_server(_server)
else:
    dev_ios(cfg, log_level=log_level, clean=force_clean)
'''

_DEPLOY_PY = '''\
target = None
force_clean = False
for a in args:
    if a in ("mac", "macos"):
        target = "mac"
    elif a in ("ios", "iphone"):
        target = "ios"
    elif a in ("server", "docker"):
        target = "server"
    elif a == "bazzite":
        target = "bazzite"
    elif a == "--clean":
        force_clean = True
    elif a in ("-h", "--help"):
        info("usage: ./admin deploy <mac|ios|server|bazzite> [--clean]")
        return
    else:
        err("unknown arg: " + a); sys.exit(1)

_server = globals().get("_SERVER_CONFIG") or {}
if target is None:
    _choices = {"mac": "mac", "ios": "ios"}
    if _server:
        _choices["server"] = "Docker → remote host"
        if _server.get("bazzite_host_var") or _server.get("bazzite_cross_target"):
            _choices["bazzite"] = "cross-compile → Bazzite"
    target = pick_target(_choices, "deploy — select target")
    if not target:
        return

cfg = globals().get("_APPLE_CONFIG") or {}
set_log_context("deploy", target)
if target == "mac":
    deploy_mac(cfg, clean=force_clean)
elif target == "ios":
    deploy_ios(cfg, clean=force_clean)
elif target == "server":
    deploy_server_docker(_server)
elif target == "bazzite":
    deploy_bazzite_server(_server)
else:
    err("unknown deploy target: " + target); sys.exit(1)
'''

_TEST_PY = '''\
_server = globals().get("_SERVER_CONFIG") or {}
_apple = globals().get("_APPLE_CONFIG") or {}
_choices = {}
if _server.get("test_cmd"):
    _choices["server"] = "server tests"
if _server.get("swift_test_cmd"):
    _choices["swift"] = "Swift integration tests"
if _apple.get("test_cmd"):
    _choices["apple"] = "apple tests"
if not _choices:
    err("no test_cmd configured in [server] or [apple]"); sys.exit(1)

target = args[0] if args else None
if not target:
    if len(_choices) == 1:
        target = next(iter(_choices))
    else:
        target = pick_target(_choices, "test — select target")
        if not target:
            return
elif target not in _choices:
    err("unknown test target: " + target); sys.exit(1)

set_log_context("test", target)
if target == "server":
    test_server(_server)
elif target == "swift":
    test_swift(_server)
elif target == "apple":
    test_server(_apple)
'''

_CLEAN_PY = '''\
cfg = globals().get("_APPLE_CONFIG") or {}
_server = globals().get("_SERVER_CONFIG") or {}

# Build available targets list. Order matters — also defines `all` ordering.
_targets = ["xcode"]
if cfg.get("icons"):
    _targets.append("icons")
if _server:
    _targets.append("cargo")
    _server_dir = _server.get("dir")
    if _server_dir and os.path.isdir(os.path.join(_server_dir, "bundled-binaries")):
        _targets.append("bundled")

requested = args[0] if args else "all"

if requested in ("-h", "--help"):
    info("usage: ./admin clean [target|all]")
    info("targets: " + ", ".join(_targets) + ", all (default)")
    return

if requested == "all":
    _to_run = list(_targets)
elif requested in _targets:
    _to_run = [requested]
else:
    err("unknown clean target: " + requested
        + " (available: " + ", ".join(_targets + ["all"]) + ")")
    sys.exit(1)

set_log_context("clean", requested)

def _clean_xcode():
    scheme = cfg.get("mac_scheme") or cfg.get("ios_scheme")
    if not scheme:
        err("apple.mac_scheme or apple.ios_scheme required"); sys.exit(1)
    derived_name = _resolve_derived_data_name(cfg) or scheme
    pattern = os.path.expanduser("~/Library/Developer/Xcode/DerivedData/" + derived_name + "-*")
    info("rm -rf " + pattern)
    rc = run_cmd("rm -rf " + pattern)
    if rc != 0:
        err("clean xcode failed"); sys.exit(rc)
    ok("DerivedData cleaned")

_runners = {
    "xcode":   _clean_xcode,
    "icons":   lambda: clean_generated_icons(cfg),
    "cargo":   lambda: clean_server(_server),
    "bundled": lambda: clean_bundled_binaries(_server),
}

for _t in _to_run:
    _runners[_t]()
'''

_ICONS_PY = '''\
cfg = globals().get("_APPLE_CONFIG") or {}
sub = args[0] if args else "regenerate"
if sub == "regenerate":
    regenerate_icons(cfg)
elif sub == "swap-dev":
    pre, _post = build_icon_hooks(cfg)
    if pre is None:
        err("no icons configured in [apple.icons]"); sys.exit(1)
    pre()
    ok("dev icons swapped in")
elif sub == "restore":
    _pre, post = build_icon_hooks(cfg)
    if post is None:
        err("no icons configured in [apple.icons]"); sys.exit(1)
    post()
    ok("icons restored from tmp/icon-backup")
elif sub in ("-h", "--help"):
    info("usage: ./admin icons [regenerate|swap-dev|restore]")
else:
    err("unknown subcommand: " + sub); sys.exit(1)
'''

_RELOAD_PY = '''\
pid_file = "/tmp/admin-run.pid"
if not os.path.exists(pid_file):
    err("no admin dev process found (no PID file at " + pid_file + ")"); sys.exit(1)
with open(pid_file) as f:
    pid = int(f.read().strip())
try:
    os.kill(pid, signal.SIGUSR1)
except ProcessLookupError:
    err("PID " + str(pid) + " is not running (stale PID file)")
    os.remove(pid_file); sys.exit(1)
ok("reload signal sent to PID " + str(pid))
'''


ARCHETYPE = {
    "name": "apple",
    "modules": ["core", "icons", "apple", "log_format", "log_columns"],
    "urls": {},
    "commands": {
        "build":  {"desc": "Build: mac | ios | server [--force]",                                    "steps": ["build"],  "group": 1, "priority": 10},
        "dev":    {"desc": "Dev build + run (mac|ios|device|server) [--log LVL] [--clean] [--install]", "steps": ["dev"],   "group": 1, "priority": 20},
        "deploy": {"desc": "Build + install (mac|ios|server|bazzite) [--clean]",                     "steps": ["deploy"], "group": 1, "priority": 30},
        "test":   {"desc": "Run tests (server|swift|apple)",                                          "steps": ["test"],   "group": 2, "priority": 10},
        "clean":  {"desc": "Remove build artifacts (xcode|cargo)",                                   "steps": ["clean"],  "group": 2, "priority": 20},
        "icons":  {"desc": "Icons: regenerate | swap-dev | restore",                                 "steps": ["icons"],  "group": 2, "priority": 30},
        "reload": {"desc": "Signal a running './admin dev' to rebuild and relaunch",                 "steps": ["reload"], "group": 2, "priority": 40},
    },
    "actions": {
        "build":      {"kind": "python", "desc": "Build: mac | ios | server [--force]",                                    "run": _BUILD_PY},
        "dev":        {"kind": "python", "desc": "Dev build + run (mac|ios|device|server) [--log LVL] [--clean] [--install]", "run": _DEV_PY},
        "deploy":     {"kind": "python", "desc": "Build + install (mac|ios|server|bazzite) [--clean]",                     "run": _DEPLOY_PY},
        "test":       {"kind": "python", "desc": "Run tests (server|swift|apple)",                                          "run": _TEST_PY},
        "clean":      {"kind": "python", "desc": "Remove build artifacts (xcode|cargo)",                                   "run": _CLEAN_PY},
        "icons":      {"kind": "python", "desc": "Icons: regenerate | swap-dev | restore",                                 "run": _ICONS_PY},
        "reload":     {"kind": "python", "desc": "Signal a running './admin dev' to rebuild and relaunch",                 "run": _RELOAD_PY},
        "build-ios":  {"kind": "apple",  "desc": "build iOS",               "run": "build_ios"},
        "dev-ios":    {"kind": "apple",  "desc": "dev iOS simulator",        "run": "dev_ios"},
        "dev-device": {"kind": "apple",  "desc": "dev iOS device",           "run": "dev_device"},
        "deploy-ios": {"kind": "apple",  "desc": "deploy iOS",               "run": "deploy_ios"},
        "clean-ios":  {"kind": "apple",  "desc": "clean Xcode DerivedData",  "run": "clean_ios"},
    },
}
