
# --- {{TARGET_NAME}} (basic) ---


def _build_{{TARGET_NAME}}(args):
    info("Building {{TARGET_LABEL}}...")
    rc = run_cmd("{{BUILD_CMD}}")
    if rc != 0:
        err("Build failed")
    else:
        ok("Build complete")
    return rc


def _dev_{{TARGET_NAME}}(args):
    return run_with_reload("{{DEV_CMD}}", "{{TARGET_NAME}}")


def _deploy_{{TARGET_NAME}}(args):
    info("Deploying {{TARGET_LABEL}}...")
    rc = _build_{{TARGET_NAME}}([])
    if rc != 0:
        return rc
    # TODO: add deploy logic for {{TARGET_NAME}}
    ok("Deploy complete")
    return 0
