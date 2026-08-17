"""Go cross-compile dist helper — build a binary for an os/arch matrix, with
an optional git-revision/dirty ldflags stamp and a VERSION file derived from
running the binary's own `-version` flag.

Generic: any Go project configures source_dir/out_dir/binary/targets via a
[go_dist] config table. No project-specific paths live here.
"""

import glob
import os
import shlex
import shutil
import subprocess
import sys

from .core import info, ok, err, run_cmd, resolve_env

DEFAULT_TARGETS = ["darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64"]


def _git_short_rev(source_dir):
    try:
        return subprocess.check_output(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=source_dir, text=True, stderr=subprocess.DEVNULL,
        ).strip()
    except Exception:
        return ""


def _git_dirty(source_dir):
    return subprocess.run(
        ["git", "diff", "--quiet", "HEAD"],
        cwd=source_dir, capture_output=True,
    ).returncode != 0


def build_go_dist(cfg):
    """Cross-compile a Go binary for cfg["targets"] (default DEFAULT_TARGETS).

    cfg keys: source_dir (Go module dir, default "."), out_dir (default "dist"),
    binary (required, output filename prefix), targets (["os/arch", ...]),
    stamp (bool, default True — adds -s -w -X main.buildRevision/buildDirty),
    version_flag (e.g. "-version" — if set, `go run` the binary with that flag
    to derive a version string written to out_dir/VERSION), extra_ldflags,
    trimpath (bool, default True), cgo_enabled (default "0").
    """
    source_dir = resolve_env(cfg.get("source_dir", "."))
    out_dir = resolve_env(cfg.get("out_dir", "dist"))
    binary = cfg.get("binary")
    if not binary:
        err("go_dist.binary not set"); sys.exit(1)
    targets = cfg.get("targets") or DEFAULT_TARGETS
    stamp = cfg.get("stamp", True)
    version_flag = cfg.get("version_flag")
    trimpath = cfg.get("trimpath", True)
    cgo_enabled = str(cfg.get("cgo_enabled", "0"))
    extra_ldflags = cfg.get("extra_ldflags", "")

    os.makedirs(out_dir, exist_ok=True)
    abs_out_dir = os.path.abspath(out_dir)

    ldflags_parts = []
    if stamp:
        revision = _git_short_rev(source_dir)
        dirty = "true" if _git_dirty(source_dir) else ""
        ldflags_parts += ["-s", "-w",
                           "-X", "main.buildRevision=" + revision,
                           "-X", "main.buildDirty=" + dirty]
    if extra_ldflags:
        ldflags_parts.append(extra_ldflags)
    ldflags = " ".join(ldflags_parts)
    ldflags_arg = ("-ldflags " + shlex.quote(ldflags)) if ldflags else ""

    if version_flag:
        rc = subprocess.run(
            "go run " + ldflags_arg + " . " + version_flag,
            shell=True, cwd=source_dir, capture_output=True, text=True,
        )
        if rc.returncode != 0:
            err("version probe failed: " + rc.stderr.strip())
            sys.exit(rc.returncode)
        version = rc.stdout.strip()
        with open(os.path.join(abs_out_dir, "VERSION"), "w") as f:
            f.write(version + "\n")
        info("building " + binary + " " + version + " -> " + out_dir)

    trim_arg = "-trimpath " if trimpath else ""
    count = 0
    for target in targets:
        os_name, arch = target.split("/")
        bin_path = os.path.join(abs_out_dir, binary + "-" + os_name + "-" + arch)
        info("  " + os_name + "/" + arch + " -> " + os.path.basename(bin_path))
        build_cmd = (
            "GOOS=" + os_name + " GOARCH=" + arch + " CGO_ENABLED=" + cgo_enabled + " "
            "go build " + trim_arg + ldflags_arg + " -o " + shlex.quote(bin_path) + " ."
        )
        rc = run_cmd("cd " + shlex.quote(source_dir) + " && " + build_cmd)
        if rc != 0:
            err("build failed for " + os_name + "/" + arch)
            return rc
        count += 1

    ok("done — " + str(count) + " binaries" + (" + VERSION" if version_flag else ""))
    return 0


def stage_dist(cfg, dest_dir):
    """Copy a [go_dist]-built matrix (binary-* + VERSION) into `dest_dir`.

    For an app that bundles the cross-compiled binaries as a resource — e.g. an
    iOS app's folder reference so a real device (no access to the build
    machine's filesystem) still has something to connect through. Fails loudly
    when the source dist/ is missing rather than silently shipping a bundle
    with nothing to connect through.

    Returns the number of binaries staged.
    """
    out_dir = resolve_env(cfg.get("out_dir", "dist"))
    binary = cfg.get("binary")
    if not binary:
        err("go_dist.binary not set"); sys.exit(1)

    version_path = os.path.join(out_dir, "VERSION")
    if not os.path.isfile(version_path):
        err(f"no {version_path} — run: admin build dist"); sys.exit(1)

    os.makedirs(dest_dir, exist_ok=True)
    for stale in glob.glob(os.path.join(dest_dir, binary + "-*")):
        os.remove(stale)
    stale_version = os.path.join(dest_dir, "VERSION")
    if os.path.exists(stale_version):
        os.remove(stale_version)

    staged = 0
    for src in glob.glob(os.path.join(out_dir, binary + "-*")):
        shutil.copy2(src, dest_dir)
        staged += 1
    shutil.copy2(version_path, dest_dir)

    ok(f"staged {staged} {binary} binaries + VERSION -> {dest_dir}")
    return staged
