#!/usr/bin/env python3
"""admin -- project task runner for {{PROJECT_NAME}}."""

import sys, os
sys.path.insert(0, os.path.expanduser("~/.admin"))
from admin_lib.core import core, command, run_cmd, boot, C, info, ok, warn, err

PROJECT_NAME = "{{PROJECT_NAME}}"


@core("build", "Build the project")
def cmd_build(args):
    info("Building...")
    rc = run_cmd("{{BUILD_CMD}}")
    if rc != 0:
        err("Build failed")
        sys.exit(rc)
    ok("Build complete")


@command("test", "Run tests")
def cmd_test(args):
    info("Running tests...")
    rc = run_cmd("{{TEST_CMD}}")
    sys.exit(rc)


@command("clean", "Remove build artifacts")
def cmd_clean(args):
    info("Cleaning...")
    # TODO: add project-specific clean logic
    ok("Clean complete")


if __name__ == "__main__":
    boot(PROJECT_NAME)
