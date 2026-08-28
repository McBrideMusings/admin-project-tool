cfg = globals().get("_APPLE_CONFIG") or {}
rc = appid(cfg, args)
if rc:
    sys.exit(rc)
