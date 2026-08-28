cfg = globals().get("_APPLE_CONFIG") or {}
_server = globals().get("_SERVER_CONFIG") or {}
# Platforms come from the manifest's scheme keys — never assumed. See
# APPLE_PLATFORMS in admin_lib/apple.py. Resolved before the args are read so
# --help advertises this project's targets rather than every target that exists.
_labels = {"mac": "mac", "ios": "ios simulator", "tv": "tvOS simulator"}
_choices = {p: _labels[p] for p in apple_platforms(cfg)}
# `device` is the iOS-device path, so it exists only where an iOS scheme does.
if "ios" in _choices:
    _choices["device"] = "iOS device (build + install + logs)"
if _server:
    _choices["server"] = "server with reload loop"
if not _choices:
    err("no dev target configured — set apple.mac_scheme, apple.ios_scheme or apple.tv_scheme")
    sys.exit(1)

target = None
log_level = "app"
force_clean = False
install_mac = False
i = 0
while i < len(args):
    a = args[i]
    if a == "--log" and i + 1 < len(args):
        i += 1
        log_level = args[i]
    elif a == "--clean":
        force_clean = True
    elif a == "--install":
        install_mac = True
    elif a in ("-h", "--help"):
        info("usage: ./admin dev [" + "|".join(_choices)
             + "] [--log app|debug|system|verbose] [--clean] [--install]")
        return
    else:
        _p = a if a in ("device", "server") else resolve_apple_platform(a)
        if _p is None:
            err("unknown arg: " + a); sys.exit(1)
        target = _p
    i += 1

if target is not None and target not in _choices:
    err("this project has no " + target + " dev target (available: "
        + ", ".join(_choices) + ")")
    sys.exit(1)

if target is None:
    target = only_target(_choices, "dev")
if target is None:
    target = pick_target(_choices, "dev — select target")
    if not target:
        return

set_log_context("dev", target)
if target == "mac":
    dev_mac(cfg, log_level=log_level, clean=force_clean, install=install_mac)
elif target == "tv":
    dev_tv(cfg, log_level=log_level, clean=force_clean)
elif target == "device":
    dev_device(cfg, log_level=log_level, clean=force_clean)
elif target == "server":
    dev_server(_server)
else:
    dev_ios(cfg, log_level=log_level, clean=force_clean)
