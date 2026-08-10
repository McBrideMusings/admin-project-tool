"""'docker' archetype — build a Docker image and deploy to a remote (Unraid) host.

Runtime env vars:
    UNRAID_HOST         hostname or IP of the Unraid server
    UNRAID_USER         SSH user (default: root)
    APP_IMAGE           docker image name (e.g. "my-app:latest")
    PROD_URL            public URL for TUI url status (optional); formerly UNRAID_APP_URL

The container's run configuration (binds, ports, env, labels, network, etc.)
comes from the `[docker_run]` table when the project declares one — deploy
renders `docker run` from it and creates the container even when none exists,
so the Unraid GUI is never needed. Without `[docker_run]`, deploy falls back
to reading the live container with `docker inspect` and replaying its config
against the newly-loaded image; that path needs the container to exist already.

A project can also declare a `[deploy]` table naming a local directory that
mirrors one on the host (appdata, config, static assets). `admin deploy files`
rsyncs it without rebuilding the image; `admin deploy all` does both. Those
files land owned by `[deploy].chown = "UID:GID"`, which defaults to
`[docker_run].user` — without either, rsync stamps the local machine's
ownership onto the host directory and the container, running as a different
uid, can't write its own files.

`[docker_build] on_host = true, context = "<path on host>"` builds the image on
the deploy target instead of locally, and skips the `docker save | docker load`
transfer — the right shape for a cross-arch workstation (ARM Mac -> amd64 host)
or a project whose code is bind-mounted so the image rarely changes.
"""

ARCHETYPE = {
    "name": "docker",
    "modules": ["core", "docker", "remote", "log_format", "log_columns", "unraid"],
    "urls": {
        "prod": "${PROD_URL:-http://localhost}",
    },
    "order": ["build", "deploy", "---", "logs", "diff", "open"],
    "commands": {
        "build":            {"desc": "Build the Docker image — locally for linux/amd64, or on the target when [docker_build].on_host", "steps": ["build"]},
        "deploy":           {"desc": "Deploy to Unraid — `image` (default), `files` ([deploy] mirror dir), or `all`; --dry-run to preview",     "steps": ["deploy"],           "env": {"ADMIN_LOCAL": "${DEPLOY_LOCAL:-false}"}},
        "diff":             {"desc": "Show run-config drift between current container and last deploy; --format json for CI",          "steps": ["diff"],             "env": {"ADMIN_LOCAL": "${DEPLOY_LOCAL:-false}"}},
        "logs":             {"desc": "Tail logs — `dev` (newest local log) or `live` (container); --from DD:HH:MM:SS rolls back",                    "steps": ["logs"],             "env": {"ADMIN_LOCAL": "${DEPLOY_LOCAL:-false}"}},
        "reload":           {"desc": "Build+run with reload loop (press r or SIGUSR1 to rebuild)",                                     "steps": ["reload"]},
        "run-args":         {"desc": "Print [docker_run] rendered to shlex-quoted `docker run` args (for external native-build scripts)", "steps": ["run-args"], "log": False},
        "open":             {"desc": "Open the deployed app URL",                                                                       "steps": ["open"]},
    },
    "actions": {
        "build": {
            "kind": "python",
            "run": (
                "_target = (globals().get('_DOCKER_RUN_CONFIG') or {}).get('target')\n"
                "_bcfg = globals().get('_DOCKER_BUILD_CONFIG') or {}\n"
                "if _bcfg.get('on_host'):\n"
                "    _user = os.environ.get('UNRAID_USER', 'root')\n"
                "    _host = f\"{_user}@{os.environ.get('UNRAID_HOST')}\"\n"
                "    sys.exit(remote_docker_build(_host, os.environ.get('APP_IMAGE'),"
                " _build_context(_bcfg), target=_target))\n"
                "docker_build(os.environ.get('APP_IMAGE'), platform='linux/amd64', target=_target)"
            ),
            "desc": "Build the Docker image — locally for linux/amd64, or on the target when [docker_build].on_host",
        },
        "deploy": {
            "kind": "python",
            "run": (
                "_run_cfg = globals().get('_DOCKER_RUN_CONFIG') or {}\n"
                "sys.exit(deploy_dispatch(args,"
                " run_cfg=_run_cfg or None,"
                " deploy_cfg=deploy_cfg_with_chown_default("
                "globals().get('_DEPLOY_CONFIG'), _run_cfg.get('user')),"
                " template_cfg=globals().get('_INSTALL_TEMPLATE_CONFIG') or {},"
                " build_cfg=globals().get('_DOCKER_BUILD_CONFIG') or None))"
            ),
            "desc": "Deploy to Unraid — `image` (default, rebuild + recreate from [docker_run]), `files` ([deploy] mirror dir only), or `all`; --force to override drift, --dry-run to preview",
        },
        "diff": {
            "kind": "python",
            "run": (
                "user = os.environ.get('UNRAID_USER', 'root')\n"
                "host = f\"{user}@{os.environ.get('UNRAID_HOST')}\"\n"
                "output_format = 'json' if '--format' in args else 'text'\n"
                "sys.exit(remote_container_diff(host, os.environ.get('APP_IMAGE'), output_format=output_format))"
            ),
            "desc": "Show run-config drift between current container and last deploy; --format json for CI",
        },
        "logs": {
            "kind": "python",
            "run": (
                "sys.exit(logs_dispatch(args, image=os.environ.get('APP_IMAGE')))"
            ),
            "desc": "Tail logs — `dev` (newest local log) or `live` (container); --from DD:HH:MM:SS rolls back",
        },
        "reload": {
            "kind": "docker-reload-loop",
            "run": "docker build -t ${APP_IMAGE} . && docker run --rm ${APP_IMAGE}",
            "desc": "Build+run with reload loop (press r or SIGUSR1 to rebuild)",
        },
        "run-args": {
            "kind": "python",
            "run": (
                "_run_cfg = globals().get('_DOCKER_RUN_CONFIG') or {}\n"
                "print(render_run_args(_run_cfg))"
            ),
            "desc": "Print [docker_run] rendered to shlex-quoted `docker run` args (for external native-build scripts). Expands env_file into -e NAME=VALUE tokens, so any secrets appear in stdout — treat the output as sensitive.",
        },
        "open": {
            "kind": "shell",
            "run": "open ${PROD_URL:-http://localhost}",
            "desc": "Open the deployed app URL",
        },
        "print-urls": {
            "kind": "print-urls",
            "desc": "Print registered URLs and their health status",
        },
    },
}
