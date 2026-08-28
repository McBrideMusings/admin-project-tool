
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
