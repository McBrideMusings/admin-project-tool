
# --- {{TARGET_NAME}} (docker) ---

_{{TARGET_UPPER}}_IMAGE = "{{DOCKER_IMAGE}}"


def _build_{{TARGET_NAME}}(args):
    rc = docker_build(_{{TARGET_UPPER}}_IMAGE, "{{CONTEXT_DIR}}")
    if rc == 0:
        ok(f"Image built: {_{{TARGET_UPPER}}_IMAGE}")
    return rc


def _dev_{{TARGET_NAME}}(args):
    return run_with_reload("{{DEV_CMD}}", "{{TARGET_NAME}}")


def _deploy_{{TARGET_NAME}}(args):
    remote_host = os.environ.get("DEPLOY_HOST", "")
    if not remote_host:
        err("DEPLOY_HOST not set")
        return 1
    rc = docker_build(_{{TARGET_UPPER}}_IMAGE)
    if rc != 0:
        return rc
    return docker_deploy(_{{TARGET_UPPER}}_IMAGE, remote_host,
                         "{{DOCKER_RUN_ARGS}}")
