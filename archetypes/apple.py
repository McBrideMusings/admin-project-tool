"""'apple' archetype — Xcode project with macOS and/or iOS targets.

Commands (dev/deploy/test/clean/icons/reload) are all ``kind: "python"``
bodies that read a ``_APPLE_CONFIG`` dict (emitted by render.py from the
manifest's ``[apple]`` table) and delegate to the high-level wrappers in
``admin_lib.apple`` (``dev_mac``, ``dev_ios``, ``deploy_mac``,
``deploy_ios``, ``regenerate_icons``, ``build_icon_hooks``).

See ``admin_lib/apple.py`` for the wrapper signatures and
``docs/archetypes/apple.md`` (if present) for the full ``[apple]``
schema.
"""

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
    elif a in ("ios", "iphone"):
        target = "ios"
    elif a == "--log" and i + 1 < len(args):
        i += 1
        log_level = args[i]
    elif a == "--clean":
        force_clean = True
    elif a == "--install":
        install_mac = True
    elif a in ("-h", "--help"):
        info("usage: ./admin dev [mac|ios] [--log app|debug|system|verbose] [--clean] [--install]")
        return
    else:
        err("unknown arg: " + a); sys.exit(1)
    i += 1

if target is None:
    from collections import OrderedDict
    target = pick_target(OrderedDict([("mac", "mac"), ("ios", "ios")]), "dev — select target")
    if not target:
        return

cfg = globals().get("_APPLE_CONFIG") or {}
if target == "mac":
    dev_mac(cfg, log_level=log_level, clean=force_clean, install=install_mac)
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
    elif a == "--clean":
        force_clean = True
    elif a in ("-h", "--help"):
        info("usage: ./admin deploy <mac|ios> [--clean]")
        return
    else:
        err("unknown arg: " + a); sys.exit(1)

if target is None:
    from collections import OrderedDict
    target = pick_target(OrderedDict([("mac", "mac"), ("ios", "ios")]), "deploy — select target")
    if not target:
        return

cfg = globals().get("_APPLE_CONFIG") or {}
if target == "mac":
    deploy_mac(cfg, clean=force_clean)
else:
    deploy_ios(cfg, clean=force_clean)
'''

_TEST_PY = '''\
cfg = globals().get("_APPLE_CONFIG") or {}
test_cmd = cfg.get("test_cmd")
if not test_cmd:
    err("apple.test_cmd not set in admin.toml"); sys.exit(1)
info("running: " + test_cmd)
rc = run_cmd(test_cmd)
if rc != 0:
    err("tests failed"); sys.exit(rc)
ok("tests passed")
'''

_CLEAN_PY = '''\
cfg = globals().get("_APPLE_CONFIG") or {}
scheme = cfg.get("mac_scheme") or cfg.get("ios_scheme")
if not scheme:
    err("apple.mac_scheme or apple.ios_scheme required"); sys.exit(1)
pattern = os.path.expanduser("~/Library/Developer/Xcode/DerivedData/" + scheme + "-*")
info("rm -rf " + pattern)
rc = run_cmd("rm -rf " + pattern)
if rc != 0:
    err("clean failed"); sys.exit(rc)
ok("DerivedData cleaned")
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
    "modules": ["core", "icons", "apple", "log_columns"],
    "urls": {},
    "commands": {
        "build": {
            "kind": "multi-target",
            "desc": "Build for mac or ios (no run, no install)",
            "targets": {
                "mac": (
                    "xcodebuild -project ${APP_PROJECT} -scheme ${APP_SCHEME} "
                    "-configuration ${APP_CONFIG:-Debug} "
                    "-destination 'platform=macOS' build"
                ),
                "ios": (
                    "xcodebuild -project ${APP_PROJECT} -scheme ${APP_SCHEME} "
                    "-configuration ${APP_CONFIG:-Debug} "
                    "-destination '${IOS_DEST:-generic/platform=iOS Simulator}' build"
                ),
            },
        },
        "dev": {
            "kind": "python",
            "desc": "Dev build + run (mac|ios) [--log LVL] [--clean] [--install]",
            "run": _DEV_PY,
        },
        "deploy": {
            "kind": "python",
            "desc": "Build + install (mac → /Applications, ios → USB device) [--clean]",
            "run": _DEPLOY_PY,
        },
        "test": {
            "kind": "python",
            "desc": "Run tests (uses apple.test_cmd from admin.toml)",
            "run": _TEST_PY,
        },
        "clean": {
            "kind": "python",
            "desc": "Remove DerivedData for this project's scheme",
            "run": _CLEAN_PY,
        },
        "icons": {
            "kind": "python",
            "desc": "Icons: regenerate | swap-dev | restore",
            "run": _ICONS_PY,
        },
        "reload": {
            "kind": "python",
            "desc": "Signal a running './admin dev' to rebuild and relaunch",
            "run": _RELOAD_PY,
        },
    },
    "order": ["build", "dev", "deploy", "", "test", "clean", "icons", "reload"],
}
