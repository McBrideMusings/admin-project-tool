"""Rust build helpers — multi-platform compilation with Docker cross-compile."""

import hashlib
import os
import subprocess
import sys

from .core import info, ok, warn, err, run_cmd


def compute_source_hash(base_dir, src_dirs=None, extra_files=None):
    """SHA256 of all source files in a Rust project.

    Scans src_dirs for .rs files, plus Cargo.toml and Cargo.lock. Returns hex
    digest. extra_files is a list of additional paths (relative to base_dir).
    """
    paths = []
    for d in (src_dirs or [os.path.join(base_dir, "src")]):
        for root, _dirs, files in os.walk(d):
            for f in files:
                if f.endswith(".rs"):
                    paths.append(os.path.join(root, f))
    for name in ("Cargo.toml", "Cargo.lock"):
        p = os.path.join(base_dir, name)
        if os.path.exists(p):
            paths.append(p)
    for f in (extra_files or []):
        p = os.path.join(base_dir, f) if not os.path.isabs(f) else f
        if os.path.exists(p):
            paths.append(p)
    paths.sort()
    h = hashlib.sha256()
    for p in paths:
        with open(p, "rb") as fh:
            h.update(fh.read())
    return h.hexdigest()


def get_version_id(server_dir):
    """Git short hash + dirty suffix for server versioning."""
    try:
        git_hash = subprocess.check_output(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=server_dir, text=True, stderr=subprocess.DEVNULL,
        ).strip()
    except Exception:
        git_hash = "unknown"
    is_dirty = subprocess.run(
        ["git", "diff", "--quiet", "--", "src/", "Cargo.toml", "Cargo.lock"],
        cwd=server_dir, capture_output=True,
    ).returncode != 0
    if is_dirty:
        source_hash = compute_source_hash(server_dir)
        return f"{git_hash}-dirty-{source_hash[:8]}"
    return git_hash


def build_multiplatform(server_dir, targets, binary_name="server"):
    """Build a Rust binary for multiple platforms.

    targets is a list of (platform_name, cargo_cmd_or_None, binary_rel_path,
    docker_platform_or_None) tuples. cargo_cmd is a shell string to run in
    server_dir (for native builds). If None, Docker cross-compile is used with
    the given docker_platform.

    Copies each built binary to server_dir/bundled-binaries/<binary_name>-<platform>.
    Strips each binary (native `strip` for darwin, Docker strip for linux).
    Returns (exit_code, version_id).
    """
    out_dir = os.path.join(server_dir, "bundled-binaries")
    os.makedirs(out_dir, exist_ok=True)
    version_id = get_version_id(server_dir)

    abs_server_dir = os.path.abspath(server_dir)
    abs_out_dir = os.path.abspath(out_dir)

    for name, cargo_cmd, binary_path, docker_platform in targets:
        info(f"Building {binary_name} for {name}...")
        full_binary = os.path.join(server_dir, binary_path)

        if cargo_cmd:
            rc = run_cmd(f"cd {server_dir} && {cargo_cmd}")
        else:
            target_dir = os.path.basename(os.path.dirname(os.path.dirname(binary_path)))
            rc = run_cmd(
                f"docker run --rm --platform {docker_platform} "
                f"-v {abs_server_dir}:/src -w /src "
                f"-e CARGO_TARGET_DIR=/src/{target_dir} "
                f"rust:latest cargo build --release"
            )

        if rc != 0:
            err(f"Build failed for {name}")
            return rc, version_id

        dest = os.path.join(out_dir, f"{binary_name}-{name}")
        subprocess.run(["cp", full_binary, dest])

        if "darwin" in name:
            subprocess.run(["strip", dest])
        else:
            subprocess.run([
                "docker", "run", "--rm", "--platform", docker_platform,
                "-v", f"{abs_out_dir}:/bins", "rust:latest",
                "strip", f"/bins/{binary_name}-{name}",
            ])

        size_mb = os.path.getsize(dest) / (1024 * 1024)
        ok(f"  {name}: {size_mb:.1f} MB")

    with open(os.path.join(out_dir, f"{binary_name.split('-')[-1]}-version.txt"), "w") as f:
        f.write(version_id + "\n")

    source_hash = compute_source_hash(server_dir)
    with open(os.path.join(out_dir, ".source-hash"), "w") as f:
        f.write(source_hash + "\n")

    ok(f"All {binary_name} binaries built ({version_id})")
    return 0, version_id


def build_server(cfg, *, force=False):
    """Build server binary for all 4 platforms.

    Skips the build if bundled binaries are fresh (source hash unchanged),
    unless force=True. Uses build_multiplatform for the actual build.
    """
    server_dir = cfg.get("dir")
    if not server_dir:
        err("server.dir not set"); sys.exit(1)
    binary = cfg.get("binary", os.path.basename(server_dir))
    targets = [
        ("darwin-arm64",  "cargo build --release",
         "target/release/" + binary, None),
        ("darwin-x86_64", "cargo build --release --target x86_64-apple-darwin",
         "target/x86_64-apple-darwin/release/" + binary, None),
        ("linux-x86_64",  None,
         "target-linux-x86/release/" + binary, "linux/amd64"),
        ("linux-arm64",   None,
         "target-linux-arm64/release/" + binary, "linux/arm64"),
    ]
    if force:
        rc, _ = build_multiplatform(server_dir, targets, binary_name=binary)
        sys.exit(rc)
    platforms = [t[0] for t in targets]
    def _rebuild():
        rc, _ = build_multiplatform(server_dir, targets, binary_name=binary)
        return rc
    rc = ensure_binaries(server_dir, platforms, binary_name=binary, rebuild_fn=_rebuild)
    sys.exit(rc)


def dev_server(cfg):
    """Run server with SIGUSR1 reload loop using server.dev_cmd."""
    dev_cmd = cfg.get("dev_cmd")
    if not dev_cmd:
        err("server.dev_cmd not set"); sys.exit(1)
    run_with_reload(dev_cmd, "server")


def test_server(cfg):
    """Run server tests using cfg.test_cmd."""
    cmd = cfg.get("test_cmd")
    if not cmd:
        err("test_cmd not set"); sys.exit(1)
    info("running: " + cmd)
    rc = run_cmd(cmd)
    if rc != 0:
        err("tests failed"); sys.exit(rc)
    ok("tests passed")


def test_swift(cfg):
    """Run Swift integration tests using server.swift_test_cmd."""
    cmd = cfg.get("swift_test_cmd")
    if not cmd:
        err("server.swift_test_cmd not set"); sys.exit(1)
    info("running: " + cmd)
    rc = run_cmd(cmd)
    if rc != 0:
        err("tests failed"); sys.exit(rc)
    ok("tests passed")


def deploy_server_docker(cfg):
    """Build Docker image and deploy to DEPLOY_HOST via docker_deploy."""
    server_dir = cfg.get("dir")
    if not server_dir:
        err("server.dir not set"); sys.exit(1)
    docker_image = cfg.get("docker_image")
    if not docker_image:
        err("server.docker_image not set"); sys.exit(1)
    docker_run_args = cfg.get("docker_run_args", "")
    build_args = cfg.get("docker_build_args", "")
    deploy_host = os.environ.get(cfg.get("deploy_host_var", "DEPLOY_HOST"), "")

    build_arg_str = (" " + build_args) if build_args else ""
    info("Building Docker image: " + docker_image + " (linux/amd64)...")
    rc = run_cmd(
        "docker build --platform linux/amd64" + build_arg_str
        + " -t " + docker_image + " " + server_dir + "/"
    )
    if rc != 0:
        err("Docker build failed"); sys.exit(1)

    if deploy_host in ("", "localhost"):
        info("Running " + docker_image + " locally via Docker...")
        run_cmd(
            "docker stop " + docker_image + " 2>/dev/null; "
            "docker rm " + docker_image + " 2>/dev/null; "
            "docker run -d --name " + docker_image + " "
            + docker_run_args + " " + docker_image
        )
        ok("Running locally")
        return

    rc = docker_deploy(docker_image, deploy_host, docker_run_args)
    if rc != 0:
        sys.exit(1)


def deploy_bazzite_server(cfg):
    """Cross-compile server and deploy to Bazzite via SSH/systemd."""
    server_dir = cfg.get("dir")
    if not server_dir:
        err("server.dir not set"); sys.exit(1)
    binary = cfg.get("binary", os.path.basename(server_dir))
    host_var = cfg.get("bazzite_host_var", "BAZZITE_HOST")
    cross_target = cfg.get("bazzite_cross_target", "x86_64-unknown-linux-gnu")
    install_path = cfg.get("bazzite_install_path", "~/.local/bin/" + binary)
    service_name = cfg.get("bazzite_service_name", binary)
    service_desc = cfg.get("bazzite_service_desc", service_name)
    exec_start = cfg.get("bazzite_exec_start", install_path)

    host = require_host(host_var, "Bazzite")
    info("Cross-compiling " + binary + " for x86_64 Linux...")
    rc = run_cmd("cd " + server_dir + " && cross build --release --target " + cross_target)
    if rc != 0:
        err("Cross-compilation failed. Is cross installed? (cargo install cross)"); sys.exit(1)
    src = server_dir + "/target/" + cross_target + "/release/" + binary
    rc = scp_to(src, host, install_path)
    if rc != 0:
        sys.exit(1)
    unit = make_systemd_unit(description=service_desc, exec_start=exec_start)
    rc = systemd_user_install(host, service_name, unit)
    if rc != 0:
        sys.exit(1)
    systemd_user_status(host, service_name)


def clean_server(cfg):
    """Remove server build artifacts (target directory)."""
    server_dir = cfg.get("dir")
    if not server_dir:
        err("server.dir not set"); sys.exit(1)
    rc = run_cmd("rm -rf " + server_dir + "/target")
    if rc == 0:
        ok(server_dir + "/target removed.")


def ensure_binaries(server_dir, platforms, binary_name="server", rebuild_fn=None):
    """Rebuild bundled server binaries if source has changed.

    platforms: list of platform name strings (e.g. ["darwin-arm64", "linux-x86_64"]).
    rebuild_fn: callable that does the actual build. Defaults to build_multiplatform
    with the default target list for the given platforms. Returns exit code.
    """
    out_dir = os.path.join(server_dir, "bundled-binaries")
    hash_file = os.path.join(out_dir, ".source-hash")
    info("Checking server binaries...")
    current_hash = compute_source_hash(server_dir)
    stored_hash = ""
    if os.path.exists(hash_file):
        with open(hash_file) as f:
            stored_hash = f.read().strip()
    missing = [
        p for p in platforms
        if not os.path.exists(os.path.join(out_dir, f"{binary_name}-{p}"))
    ]
    if stored_hash == current_hash and not missing:
        ok("Server binaries up to date (skipping rebuild)")
        return 0
    if missing:
        warn(f"Server binary missing ({', '.join(missing)}) — rebuilding all platform binaries...")
    elif not stored_hash:
        warn("Server source hash not found — rebuilding all platform binaries...")
    else:
        warn("Server source changed — rebuilding all platform binaries...")
    if rebuild_fn:
        return rebuild_fn()
    return 0
