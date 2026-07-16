"""'react-native' archetype — bare React Native app (iOS + Android).

A mixin, not a monolith: it owns the targets that need Metro (the JS bundler)
running, and delegates the Xcode half to the same admin_lib.apple wrappers a
native project uses. Compose it with other archetypes for the rest of a repo
(a Go server, a web client) — this one only claims `dev`.

Config lives in two tables. [react_native] carries app_dir and metro_port;
[apple] configures the iOS build exactly as for a native project (an RN app is
a normal Xcode workspace). See admin_lib/react_native.py for the schema.

Commands are kind: "python" bodies reading _REACT_NATIVE_CONFIG and
_APPLE_CONFIG, delegating to admin_lib.react_native / admin_lib.apple.
"""

_DEV_PY = '''\
target = None
log_level = "app"
force_clean = False
i = 0
while i < len(args):
    a = args[i]
    if a in ("ios", "iphone", "simulator"):
        target = "ios"
    elif a in ("device", "ios-device"):
        target = "device"
    elif a == "android":
        target = "android"
    elif a == "--log" and i + 1 < len(args):
        i += 1
        log_level = args[i]
    elif a == "--clean":
        force_clean = True
    elif a in ("-h", "--help"):
        info("usage: ./admin dev [ios|device|android] [--log app|debug|system|verbose] [--clean]")
        return
    else:
        err("unknown arg: " + a); sys.exit(1)
    i += 1

if target is None:
    target = pick_target({
        "ios": "iOS simulator (Metro + build + install + logs)",
        "device": "iOS device (Metro + build + install + logs)",
        "android": "Android emulator/device (Metro + build + install)",
    }, "dev — select target")
    if not target:
        return

_rn = globals().get("_REACT_NATIVE_CONFIG") or {}
cfg = globals().get("_APPLE_CONFIG") or {}
set_log_context("dev", target)

ensure_metro_for(_rn)

if target == "ios":
    dev_ios(cfg, log_level=log_level, clean=force_clean)
elif target == "device":
    dev_device(cfg, log_level=log_level, clean=force_clean)
elif target == "android":
    dev_android(_rn, output_config=globals().get("_OUTPUT_CONFIG"))
'''

_METRO_PY = '''\
_rn = globals().get("_REACT_NATIVE_CONFIG") or {}
if args and args[0] == "stop":
    if stop_metro_for(_rn):
        ok("Metro stopped")
    else:
        info("no Metro running")
else:
    ensure_metro_for(_rn)
'''

ARCHETYPE = {
    "name": "react-native",
    "modules": ["core", "reload", "apple", "android", "metro", "react_native", "log_columns"],
    "urls": {},
    "commands": {
        "dev": {"desc": "Run a React Native dev target (ios | device | android)", "steps": ["dev"]},
        "metro": {"desc": "Start the Metro bundler, or `metro stop` to stop it", "steps": ["metro"]},
    },
    "actions": {
        "dev": {"kind": "python", "run": _DEV_PY,
                "desc": "Run a React Native dev target (ios | device | android)"},
        "metro": {"kind": "python", "run": _METRO_PY,
                  "desc": "Start the Metro bundler, or `metro stop` to stop it"},
    },
}
