#!/usr/bin/env python3
"""admin -- project task runner for {{PROJECT_NAME}}."""

import sys, os
sys.path.insert(0, os.path.expanduser("~/.admin"))
from admin_lib.core import command, run_cmd, boot, C, info, ok, warn, err
from admin_lib.server import (
    run_with_reload, docker_build, docker_deploy, ensure_deps,
    register_server_reload_command,
)

PROJECT_NAME = "{{PROJECT_NAME}}"
DOCKER_IMAGE = "{{DOCKER_IMAGE}}"


@command("build", "Build Docker image")
def cmd_build(args):
    rc = docker_build(DOCKER_IMAGE, ".")
    if rc == 0:
        ok(f"Image built: {DOCKER_IMAGE}")
    return rc


@command("run", "Run local dev server")
def cmd_run(args):
    ensure_deps("node_modules", "npm install")
    return run_with_reload("node server.js", DOCKER_IMAGE)


@command("deploy", "Build and deploy to remote host")
def cmd_deploy(args):
    remote_host = os.environ.get("DEPLOY_HOST", "")
    if not remote_host:
        err("DEPLOY_HOST not set")
        return 1

    rc = docker_build(DOCKER_IMAGE)
    if rc != 0:
        return rc

    return docker_deploy(DOCKER_IMAGE, remote_host,
                         "-p 9090:9090")


@command("clean", "Remove build artifacts")
def cmd_clean(args):
    info("Stopping containers and removing images...")
    run_cmd("docker compose down --rmi local --volumes")
    ok("Clean complete")


register_server_reload_command()


if __name__ == "__main__":
    boot(PROJECT_NAME, load_dotenv=True)
