_rn = globals().get("_REACT_NATIVE_CONFIG") or {}
if args and args[0] == "stop":
    if stop_metro_for(_rn):
        ok("Metro stopped")
    else:
        info("no Metro running")
else:
    ensure_metro_for(_rn)
