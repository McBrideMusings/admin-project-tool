target = None
force = "--force" in args
rest = [a for a in args if a != "--force"]

cfg = globals().get("_APPLE_CONFIG") or {}
_server = globals().get("_SERVER_CONFIG") or {}
# Platforms come from the manifest's scheme keys — never assumed. See
# APPLE_PLATFORMS in admin_lib/apple.py.
_labels = {"mac": "macOS client", "ios": "iOS client", "tv": "tvOS client"}
_choices = {p: _labels[p] for p in apple_platforms(cfg)}
if _server:
    _choices["server"] = "server (all platforms)"
if not _choices:
    err("no build target configured — set apple.mac_scheme, apple.ios_scheme or apple.tv_scheme")
    sys.exit(1)

if rest:
    a = rest[0].lower()
    # A fourth per-command copy of --help handling, matching dev and deploy.
    # admin-project-tool#456 is the structural fix (intercept it once in boot()
    # for every command) and is still an open decision; without this line
    # `admin build --help` reads "--help" as a target name and errors.
    if a in ("-h", "--help"):
        info("usage: ./admin build [" + "|".join(_choices) + "] [--force]")
        return
    target = "server" if a == "server" else resolve_apple_platform(a)
    if target is None:
        err("unknown build target: " + rest[0]); sys.exit(1)
    if target not in _choices:
        err("this project has no " + target + " build target (available: "
            + ", ".join(_choices) + ")")
        sys.exit(1)

if not target:
    target = only_target(_choices, "build")
if not target:
    target = pick_target(_choices, "build — select target")
    if not target:
        return

set_log_context("build", target)
if target == "mac":
    build_mac(cfg, force=force)
elif target == "ios":
    build_ios(cfg, force=force)
elif target == "tv":
    build_tv(cfg, force=force)
elif target == "server":
    build_server(_server, force=force)
