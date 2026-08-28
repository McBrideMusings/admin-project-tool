_server = globals().get("_SERVER_CONFIG") or {}
cfg = globals().get("_APPLE_CONFIG") or {}
# Platforms come from the manifest's scheme keys — never assumed. Resolved
# before the args are read so --help advertises this project's targets.
_choices = {p: p for p in apple_platforms(cfg, only=("mac", "ios", "tv"))}
if _server:
    _choices["server"] = "Docker → remote host"
    if _server.get("bazzite_host_var") or _server.get("bazzite_cross_target"):
        _choices["bazzite"] = "cross-compile → Bazzite"
if not _choices:
    err("no deploy target configured — set apple.mac_scheme, apple.ios_scheme or apple.tv_scheme")
    sys.exit(1)

target = None
force_clean = False
for a in args:
    if a in ("server", "docker"):
        target = "server"
    elif a == "bazzite":
        target = "bazzite"
    elif a == "--clean":
        force_clean = True
    elif a in ("-h", "--help"):
        info("usage: ./admin deploy <" + "|".join(_choices) + "> [--clean]")
        return
    else:
        _p = resolve_apple_platform(a)
        if _p is None:
            err("unknown arg: " + a); sys.exit(1)
        target = _p

if target is not None and target not in _choices:
    err("this project has no " + target + " deploy target (available: "
        + ", ".join(_choices) + ")")
    sys.exit(1)

if target is None:
    target = only_target(_choices, "deploy")
if target is None:
    target = pick_target(_choices, "deploy — select target")
    if not target:
        return

set_log_context("deploy", target)
if target == "mac":
    deploy_mac(cfg, clean=force_clean)
elif target == "ios":
    deploy_ios(cfg, clean=force_clean)
elif target == "tv":
    deploy_tv(cfg, clean=force_clean)
elif target == "server":
    deploy_server_docker(_server)
elif target == "bazzite":
    deploy_bazzite_server(_server)
else:
    err("unknown deploy target: " + target); sys.exit(1)
