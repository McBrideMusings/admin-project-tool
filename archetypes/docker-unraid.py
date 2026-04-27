"""'docker-unraid' archetype — build a Docker image and deploy to an Unraid host.

Runtime env vars:
    UNRAID_HOST         hostname or IP of the Unraid server
    UNRAID_USER         SSH user (default: root)
    APP_IMAGE           docker image name (e.g. "my-app:latest")
    PROD_URL            public URL for TUI url status (optional); formerly UNRAID_APP_URL

The container's run configuration (binds, ports, env, labels, network, etc.)
is whatever the Unraid Docker GUI currently has for that container — deploy
reads it off the live container with `docker inspect` and replays it against
the newly-loaded image. Create the container in the Unraid GUI first, then
use `./admin deploy` for every subsequent code push.
"""

ARCHETYPE = {
    "name": "docker-unraid",
    "modules": ["core", "docker", "remote", "unraid", "log_columns"],
    "urls": {
        "prod": "${PROD_URL:-http://localhost}",
    },
    "commands": {
        "build": {
            "kind": "python",
            "run": "docker_build(os.environ['APP_IMAGE'], platform='linux/amd64')",
            "desc": "Build Docker image locally for linux/amd64 (Unraid)",
        },
        "deploy": {
            "kind": "python",
            "run": (
                "rc = docker_build(os.environ['APP_IMAGE'], platform='linux/amd64')\n"
                "if rc != 0:\n"
                "    sys.exit(rc)\n"
                "host = f\"{os.environ.get('UNRAID_USER', 'root')}@{os.environ['UNRAID_HOST']}\"\n"
                "sys.exit(remote_recreate_container(host, os.environ['APP_IMAGE']))"
            ),
            "desc": "Build, push, and recreate container on Unraid (preserves run config); use `diff` to check drift",
        },
        "diff": {
            "kind": "python",
            "run": (
                "host = f\"{os.environ.get('UNRAID_USER', 'root')}@{os.environ['UNRAID_HOST']}\"\n"
                "sys.exit(remote_container_diff(host, os.environ['APP_IMAGE']))"
            ),
            "desc": "Show run-config drift between current container and last deploy",
        },
        "logs": {
            "kind": "python",
            "run": (
                "import shlex\n"
                "container = os.environ.get('APP_CONTAINER') or os.environ['APP_IMAGE'].split(':')[0].rsplit('/', 1)[-1]\n"
                "host = f\"{os.environ.get('UNRAID_USER', 'root')}@{os.environ['UNRAID_HOST']}\"\n"
                "sys.exit(run_cmd(f'ssh {shlex.quote(host)} docker logs -f --tail=200 {shlex.quote(container)}'))"
            ),
            "desc": "Tail container logs over SSH",
        },
        "reload": {
            "kind": "docker-reload-loop",
            "run": "docker build -t ${APP_IMAGE} . && docker run --rm ${APP_IMAGE}",
            "desc": "Build+run with reload loop (press r or SIGUSR1 to rebuild)",
        },
        "open": {
            "run": "open ${PROD_URL:-http://localhost}",
            "desc": "Open the deployed app URL",
        },
    },
}
