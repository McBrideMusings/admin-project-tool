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
cfg = apple_config_for(_rn, globals().get("_APPLE_CONFIG") or {})
set_log_context("dev", target)

ensure_metro_for(_rn)

if target == "ios":
    dev_ios(cfg, log_level=log_level, clean=force_clean)
elif target == "device":
    dev_device(cfg, log_level=log_level, clean=force_clean)
elif target == "android":
    dev_android(_rn, output_config=globals().get("_OUTPUT_CONFIG"))
