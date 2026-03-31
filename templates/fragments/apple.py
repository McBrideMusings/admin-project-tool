
# --- {{TARGET_NAME}} (apple/{{PLATFORM}}) ---

_{{TARGET_UPPER}}_XCODEPROJ = "{{XCODEPROJ}}"
_{{TARGET_UPPER}}_SCHEME = "{{SCHEME}}"
_{{TARGET_UPPER}}_PROCESS = "{{PROCESS}}"
_{{TARGET_UPPER}}_SUBSYSTEM = "{{SUBSYSTEM}}"
_{{TARGET_UPPER}}_PID_FILE = "/tmp/admin-run-{{TARGET_NAME}}.pid"


def _build_{{TARGET_NAME}}(args):
    info("Building {{TARGET_LABEL}}...")
    rc = xcodebuild_filtered(
        _{{TARGET_UPPER}}_XCODEPROJ, _{{TARGET_UPPER}}_SCHEME,
        "Debug", "{{DESTINATION}}")
    print()
    if rc != 0:
        err("Build failed")
    else:
        ok("{{TARGET_LABEL}} build succeeded")
    return rc


def _dev_{{TARGET_NAME}}(args):
    log_level = "app"
    for i, arg in enumerate(args):
        if arg == "--log" and i + 1 < len(args):
            log_level = args[i + 1]

    predicate = log_predicate(_{{TARGET_UPPER}}_PROCESS, _{{TARGET_UPPER}}_SUBSYSTEM, log_level)
    pattern = "{{DEV_APP_PATTERN}}"

{{DEV_BODY}}


def _deploy_{{TARGET_NAME}}(args):
{{DEPLOY_BODY}}
