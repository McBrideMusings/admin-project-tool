#!/usr/bin/env python3
"""admin -- project task runner for {{PROJECT_NAME}}."""

import sys, os
sys.path.insert(0, os.path.expanduser("~/.admin"))
from admin_lib.core import core, command, run_cmd, boot, C, info, ok, warn, err, set_urls
from admin_lib.docker import (
    run_with_reload, docker_build, docker_deploy, ensure_deps,
    register_server_reload_command, register_open_command,
)

PROJECT_NAME = "{{PROJECT_NAME}}"
DOCKER_IMAGE = "{{DOCKER_IMAGE}}"

# TODO: update URLs for this project
URLS = [
    ("Dev", "http://localhost:9090"),
    ("Prod", "https://{{DOCKER_IMAGE}}.example.com"),
]
set_urls(URLS)


@core("build", "Build Docker image")
def cmd_build(args):
    rc = docker_build(DOCKER_IMAGE, ".")
    if rc == 0:
        ok(f"Image built: {DOCKER_IMAGE}")
    return rc


@command("run", "Run local dev server")
def cmd_run(args):
    ensure_deps("node_modules", "npm install")
    return run_with_reload("node server.js", DOCKER_IMAGE)


@core("deploy", "Build and deploy to remote host")
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


register_open_command()
register_server_reload_command()


if __name__ == "__main__":
    boot(PROJECT_NAME, load_dotenv=True)
