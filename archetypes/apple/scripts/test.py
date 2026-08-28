_server = globals().get("_SERVER_CONFIG") or {}
_apple = globals().get("_APPLE_CONFIG") or {}
_choices = {}
if _server.get("test_cmd"):
    _choices["server"] = "server tests"
if _server.get("swift_test_cmd"):
    _choices["swift"] = "Swift integration tests"
if _apple.get("test_cmd"):
    _choices["apple"] = "apple tests"
if not _choices:
    err("no test_cmd configured in [server] or [apple]"); sys.exit(1)

target = args[0] if args else None
if target and target not in _choices:
    err("unknown test target: " + target); sys.exit(1)
if not target:
    target = only_target(_choices, "test")
if not target:
    target = pick_target(_choices, "test — select target")
    if not target:
        return

set_log_context("test", target)
if target == "server":
    test_server(_server)
elif target == "swift":
    test_swift(_server)
elif target == "apple":
    # Run apple.test_cmd here rather than borrowing rust.py's test_server: this
    # archetype does not pull the rust module, so on an apple-only project that
    # name is undefined and the action dies with a NameError before running a
    # single test.
    #
    # test_cmd is a raw shell string, usually its own `xcodebuild ... test`
    # invocation, so it goes through run_locked_xcode() rather than bare
    # run_cmd(): otherwise it would run alongside a build/dev/deploy
    # xcodebuild instead of taking the same per-checkout lock
    # (admin-project-tool#450).
    _cmd = _apple.get("test_cmd")
    info("running: " + _cmd)
    _rc = run_locked_xcode(_cmd, "apple test_cmd")
    if _rc != 0:
        err("tests failed"); sys.exit(_rc)
    ok("tests passed")
