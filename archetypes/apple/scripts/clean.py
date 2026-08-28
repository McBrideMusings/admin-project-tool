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
