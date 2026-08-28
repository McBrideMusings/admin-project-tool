cfg = globals().get("_APPLE_CONFIG") or {}
sub = args[0] if args else "regenerate"
if sub == "regenerate":
    regenerate_icons(cfg)
elif sub == "swap-dev":
    pre, _post = build_icon_hooks(cfg, restore_on_exit=False)
    if pre is None:
        err("no icons configured in [apple.icons]"); sys.exit(1)
    pre()
    ok("dev icons swapped in — `icons restore` puts the originals back")
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
