"""Rust build helpers — multi-platform compilation with Docker cross-compile."""

import hashlib
import os
import subprocess

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

    for name, cargo_cmd, binary_path, docker_platform in targets:
        info(f"Building {binary_name} for {name}...")
        full_binary = os.path.join(server_dir, binary_path)

        if cargo_cmd:
            rc = run_cmd(f"cd {server_dir} && {cargo_cmd}")
        else:
            target_dir = os.path.basename(os.path.dirname(os.path.dirname(binary_path)))
            rc = run_cmd(
                f"docker run --rm --platform {docker_platform} "
                f"-v {server_dir}:/src -w /src "
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
                "-v", f"{out_dir}:/bins", "rust:latest",
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
