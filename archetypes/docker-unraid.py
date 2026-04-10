"""'docker-unraid' archetype — build a Docker image and deploy to an Unraid host.

Runtime env vars:
    UNRAID_HOST         hostname or IP of the Unraid server
    UNRAID_USER         SSH user (default: root)
    APP_IMAGE           docker image name (e.g. "my-app:latest")
    APP_RUN_ARGS        extra docker run args (ports, volumes, env)
    UNRAID_APP_URL      public URL for TUI url status (optional)
"""

ARCHETYPE = {
    "name": "docker-unraid",
    "modules": ["core", "docker", "remote", "unraid"],
    "urls": {
        "prod": "${UNRAID_APP_URL:-http://localhost}",
    },
    "commands": {
        "build": {
            "run": "docker build -t ${APP_IMAGE} .",
            "desc": "Build Docker image locally",
        },
        "deploy": {
            "run": (
                "docker save ${APP_IMAGE} | "
                "ssh ${UNRAID_USER:-root}@${UNRAID_HOST} 'docker load && "
                "docker rm -f ${APP_IMAGE%%:*} 2>/dev/null; "
                "docker run -d --name ${APP_IMAGE%%:*} --restart=unless-stopped ${APP_RUN_ARGS} ${APP_IMAGE}'"
            ),
            "desc": "Push image and restart container on Unraid",
        },
        "logs": {
            "run": "ssh ${UNRAID_USER:-root}@${UNRAID_HOST} 'docker logs -f --tail=200 ${APP_IMAGE%%:*}'",
            "desc": "Tail container logs over SSH",
        },
        "reload": {
            "kind": "docker-reload-loop",
            "run": "docker build -t ${APP_IMAGE} . && docker run --rm ${APP_RUN_ARGS} ${APP_IMAGE}",
            "desc": "Build+run with reload loop (press r or SIGUSR1 to rebuild)",
        },
        "open": {
            "run": "open ${UNRAID_APP_URL:-http://localhost}",
            "desc": "Open the deployed app URL",
        },
    },
}
