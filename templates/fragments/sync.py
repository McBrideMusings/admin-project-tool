
# --- {{TARGET_NAME}} (sync) ---


def _build_{{TARGET_NAME}}(args):
    info("Building {{TARGET_LABEL}}...")
    rc = run_cmd("{{BUILD_CMD}}")
    if rc != 0:
        err("Build failed")
        return rc
    ok("Build complete")
    return 0


def _deploy_{{TARGET_NAME}}(args):
    rc = _build_{{TARGET_NAME}}([])
    if rc != 0:
        return rc
    dest = os.environ.get("{{DEPLOY_DEST_ENV}}", "")
    if not dest:
        err("{{DEPLOY_DEST_ENV}} not set")
        return 1
    return sync_deploy("{{DIST_DIR}}", os.path.join(dest, "{{DEPLOY_SUBDIR}}"),
                       excludes={{EXCLUDES}})
